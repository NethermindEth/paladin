import { poseidon2, poseidon4 } from "poseidon-lite";
import { genEcdhSharedKey } from "maci-crypto";

// ---------------------------------------------------------------------------
// Poseidon decryption — ported from zeto/zkp/js/lib/util.js:86-128
//
// Matches the circom encryption scheme at zeto/zkp/circuits/lib/encrypt.circom.
// Uses poseidon4(state, 4) from poseidon-lite which returns the full 4-element
// permutation output (not just the hash) when the second argument is provided.
// ---------------------------------------------------------------------------

/** BN254 scalar field modulus */
const F = 21888242871839275222246405745257275088548364400416034343698204186575808495617n;
const TWO_128 = 2n ** 128n;

function addMod(a: bigint, b: bigint): bigint {
  let result = ((((a % F) + F) % F) + (((b % F) + F) % F)) % F;
  while (result < 0n) result += F;
  return result;
}

/**
 * Poseidon-based stream decryption.
 *
 * @param ciphertext - Encrypted field elements from the on-chain event
 * @param key - ECDH shared key [x, y]
 * @param nonce - Encryption nonce from the event
 * @param length - Number of plaintext elements to recover
 */
function poseidonDecrypt(
  ciphertext: bigint[],
  key: bigint[],
  nonce: bigint,
  length: number,
): bigint[] {
  if (key.length !== 2) throw new Error("Key must be [x, y]");
  if (length < 1) throw new Error("Length must be >= 1");

  // Initial state: S = (0, key[0], key[1], nonce + length * 2^128)
  let state: bigint[] = [0n, key[0], key[1], nonce + BigInt(length) * TWO_128];
  const message: bigint[] = [];

  const n = Math.floor(ciphertext.length / 3);
  for (let i = 0; i < n; i++) {
    // Full Poseidon permutation on 4-element state
    state = poseidon4(state, 4) as unknown as bigint[];

    // Recover 3 plaintext elements per round
    message.push(addMod(ciphertext[i * 3], -state[1]));
    message.push(addMod(ciphertext[i * 3 + 1], -state[2]));
    message.push(addMod(ciphertext[i * 3 + 2], -state[3]));

    // Feed ciphertext into state for the next round
    state[1] = ciphertext[i * 3];
    state[2] = ciphertext[i * 3 + 1];
    state[3] = ciphertext[i * 3 + 2];
  }

  // Verify padding zeros for non-multiple-of-3 lengths
  if (length > 3) {
    if (length % 3 === 2) {
      if (message[message.length - 1] !== 0n)
        throw new Error("Decryption failed: padding check (last element must be 0)");
    } else if (length % 3 === 1) {
      if (message[message.length - 1] !== 0n || message[message.length - 2] !== 0n)
        throw new Error("Decryption failed: padding check (last two elements must be 0)");
    }
  }

  // Final permutation and ciphertext integrity check
  state = poseidon4(state, 4) as unknown as bigint[];
  if (ciphertext[ciphertext.length - 1] !== state[1]) {
    throw new Error("Decryption failed: ciphertext integrity check");
  }

  return message.slice(0, length);
}

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export interface NoteInfo {
  commitment: bigint;
  value: bigint;
  salt: bigint;
  ownerPubKey: [string, string];
}

export interface DecryptedTransfer {
  senderPubKey: [string, string];
  inputs: Array<{ value: bigint; salt: bigint }>;
  outputs: Array<{ ownerPubKey: [string, string]; value: bigint; salt: bigint; commitment?: bigint }>;
}

// ---------------------------------------------------------------------------
// AuthorityNoteIndexer
// ---------------------------------------------------------------------------

/**
 * Decrypts non-repudiation ciphertexts from AENKNR-E transfer/deposit events.
 *
 * Used by both:
 * - Regulator (arbiter key) — audits all transactions
 * - Bank/Enforcer (enforcer key) — finds notes for clawback
 *
 * Each AENKNR-E transfer encrypts a 14-element plaintext to the arbiter and
 * enforcer using Poseidon stream encryption with an ephemeral ECDH shared key:
 *
 *   [senderPubX, senderPubY,
 *    in1Value, in1Salt, in2Value, in2Salt,
 *    out1OwnerX, out1OwnerY, out2OwnerX, out2OwnerY,
 *    out1Value, out1Salt, out2Value, out2Salt]
 *
 * For deposit events, input fields (indices 0-5) are zeroed since there are
 * no input UTXOs.
 */
export class AuthorityNoteIndexer {
  private notes = new Map<string, NoteInfo[]>();

  /**
   * Decrypts an authority ciphertext from a transfer/deposit event.
   *
   * @param ciphertext - The encryptedValuesForArbiter or encryptedValuesForEnforcer
   *                     array from the on-chain event (as bigints)
   * @param authorityPrivKey - The authority's BabyJubJub private key (formatted for BabyJub)
   * @param ecdhPublicKey - The ephemeral ECDH public key [x, y] from the event
   * @param encryptionNonce - The encryption nonce from the event
   * @returns The decrypted 14-element transfer info
   */
  decryptTransfer(
    ciphertext: bigint[],
    authorityPrivKey: bigint,
    ecdhPublicKey: bigint[],
    encryptionNonce: bigint,
  ): DecryptedTransfer {
    // ECDH: shared = privKey * ecdhPubKey
    const shared = genEcdhSharedKey(authorityPrivKey, ecdhPublicKey as [bigint, bigint]);
    const plaintext = poseidonDecrypt(ciphertext, shared, encryptionNonce, 14);

    return {
      senderPubKey: [plaintext[0].toString(), plaintext[1].toString()],
      inputs: [
        { value: plaintext[2], salt: plaintext[3] },
        { value: plaintext[4], salt: plaintext[5] },
      ],
      outputs: [
        {
          ownerPubKey: [plaintext[6].toString(), plaintext[7].toString()],
          value: plaintext[10],
          salt: plaintext[11],
        },
        {
          ownerPubKey: [plaintext[8].toString(), plaintext[9].toString()],
          value: plaintext[12],
          salt: plaintext[13],
        },
      ],
    };
  }

  /**
   * Indexes decrypted output notes by owner public key hash.
   *
   * Call this after decryptTransfer() to build a searchable index of notes
   * that the enforcer can use for clawback (finding which notes a frozen
   * user owns and their commitment values/salts).
   */
  indexNotes(transfer: DecryptedTransfer): void {
    for (const out of transfer.outputs) {
      if (out.value === 0n) continue;
      const keyHash = poseidon2([BigInt(out.ownerPubKey[0]), BigInt(out.ownerPubKey[1])]).toString();
      const existing = this.notes.get(keyHash) ?? [];
      existing.push({
        commitment: 0n, // Commitment not available from plaintext; computed externally if needed
        value: out.value,
        salt: out.salt,
        ownerPubKey: out.ownerPubKey,
      });
      this.notes.set(keyHash, existing);
    }
  }

  /**
   * Returns all indexed notes for an identity.
   *
   * @param pubKeyX - BabyJubJub public key X coordinate (decimal string)
   * @param pubKeyY - BabyJubJub public key Y coordinate (decimal string)
   */
  getNotesForIdentity(pubKeyX: string, pubKeyY: string): NoteInfo[] {
    const keyHash = poseidon2([BigInt(pubKeyX), BigInt(pubKeyY)]).toString();
    return this.notes.get(keyHash) ?? [];
  }

  /**
   * Selects notes totaling at least the requested amount.
   *
   * Returns undefined if insufficient balance.
   */
  selectNotesForAmount(pubKeyX: string, pubKeyY: string, amount: bigint): NoteInfo[] | undefined {
    const notes = this.getNotesForIdentity(pubKeyX, pubKeyY);
    let total = 0n;
    const selected: NoteInfo[] = [];
    for (const note of notes) {
      selected.push(note);
      total += note.value;
      if (total >= amount) return selected;
    }
    return undefined;
  }
}
