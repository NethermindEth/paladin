/*
 * Copyright © 2025 Kaleido, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file except in compliance with
 * the License. You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software distributed under the License is distributed on
 * an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the License for the
 * specific language governing permissions and limitations under the License.
 *
 * SPDX-License-Identifier: Apache-2.0
 */

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

/**
 * Poseidon-based stream encryption — inverse of poseidonDecrypt above.
 * Ported from zeto/zkp/js/lib/util.js:45-83.
 */
function poseidonEncrypt(msg: bigint[], key: bigint[], nonce: bigint): bigint[] {
  if (key.length !== 2) throw new Error("Key must be [x, y]");

  // Pad plaintext to a multiple of 3
  const message = [...msg];
  while (message.length % 3 > 0) message.push(0n);

  // Initial state: S = (0, key[0], key[1], nonce + length * 2^128)
  let state: bigint[] = [0n, key[0], key[1], nonce + BigInt(msg.length) * TWO_128];
  const ciphertext: bigint[] = [];

  const n = Math.floor(message.length / 3);
  for (let i = 0; i < n; i++) {
    state = poseidon4(state, 4) as unknown as bigint[];

    // Add plaintext to state (mod F) — the inverse of decryption's subtraction
    state[1] = addMod(message[i * 3], state[1]);
    state[2] = addMod(message[i * 3 + 1], state[2]);
    state[3] = addMod(message[i * 3 + 2], state[3]);

    ciphertext.push(state[1]);
    ciphertext.push(state[2]);
    ciphertext.push(state[3]);
  }

  // Final permutation — append integrity tag
  state = poseidon4(state, 4) as unknown as bigint[];
  ciphertext.push(state[1]);

  return ciphertext;
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

export interface ProofBundle {
  txHash: string;
  onChainCiphertext: string[];
  onChainOutputCommitments: string[];
  ecdhPublicKey: [string, string];
  encryptionNonce: string;
  ecdhSharedSecret: [string, string];
  decryptedPlaintext: string[];
  reEncryptedCiphertext: string[];
  computedCommitments: Array<{
    outputIndex: number;
    value: string; salt: string;
    ownerPubKeyX: string; ownerPubKeyY: string;
    hash: string;
  }>;
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
   * Decrypts + builds a verifiable proof bundle for independent client-side verification.
   *
   * The bundle contains the ECDH shared secret (safe to reveal per-tx — ephemeral,
   * doesn't leak the arbiter private key). Anyone with the bundle can re-encrypt
   * the plaintext and compare against on-chain ciphertext, or hash the plaintext
   * fields and compare against on-chain output commitments.
   */
  buildProofBundle(
    ciphertext: bigint[],
    authorityPrivKey: bigint,
    ecdhPublicKey: bigint[],
    encryptionNonce: bigint,
    onChainOutputCommitments: bigint[],
    txHash: string,
  ): { transfer: DecryptedTransfer; proof: ProofBundle } {
    const shared = genEcdhSharedKey(authorityPrivKey, ecdhPublicKey as [bigint, bigint]);
    const plaintext = poseidonDecrypt(ciphertext, shared, encryptionNonce, 14);

    const transfer: DecryptedTransfer = {
      senderPubKey: [plaintext[0].toString(), plaintext[1].toString()],
      inputs: [
        { value: plaintext[2], salt: plaintext[3] },
        { value: plaintext[4], salt: plaintext[5] },
      ],
      outputs: [
        { ownerPubKey: [plaintext[6].toString(), plaintext[7].toString()], value: plaintext[10], salt: plaintext[11] },
        { ownerPubKey: [plaintext[8].toString(), plaintext[9].toString()], value: plaintext[12], salt: plaintext[13] },
      ],
    };

    // Re-encrypt plaintext → should match on-chain ciphertext exactly
    const reEncrypted = poseidonEncrypt(plaintext, shared, encryptionNonce);

    // Compute output commitments: Poseidon4([value, salt, ownerPubKeyX, ownerPubKeyY])
    const computedCommitments = transfer.outputs
      .filter((o) => o.value > 0n)
      .map((o, i) => ({
        outputIndex: i,
        value: o.value.toString(),
        salt: o.salt.toString(),
        ownerPubKeyX: o.ownerPubKey[0],
        ownerPubKeyY: o.ownerPubKey[1],
        hash: poseidon4([o.value, o.salt, BigInt(o.ownerPubKey[0]), BigInt(o.ownerPubKey[1])]).toString(),
      }));

    return {
      transfer,
      proof: {
        txHash,
        onChainCiphertext: ciphertext.map(String),
        onChainOutputCommitments: onChainOutputCommitments.map(String),
        ecdhPublicKey: [ecdhPublicKey[0].toString(), ecdhPublicKey[1].toString()],
        encryptionNonce: encryptionNonce.toString(),
        ecdhSharedSecret: [shared[0].toString(), shared[1].toString()],
        decryptedPlaintext: plaintext.map(String),
        reEncryptedCiphertext: reEncrypted.map(String),
        computedCommitments,
      },
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

// ---------------------------------------------------------------------------
// AuthorityLedger — canonical per-UTXO view for the regulator
//
// Event-driven. Every shielded transfer decrypts to a 14-element plaintext
// that identifies the sender's inputs (as (value, salt) with senderPubKey as
// owner) and the recipient's outputs (with explicit ownerPubKey, value,
// salt). From those, both ends of each UTXO's life cycle — creation and
// spend — are derivable without any hand-maintained map.
// ---------------------------------------------------------------------------

export interface LedgerEntry {
  commitment: string;          // decimal string (matches event field encoding)
  value: bigint;
  salt: bigint;
  ownerPubKey: [string, string];
  ownerKeyHash: string;        // Poseidon2 of pubkey — stable owner identifier
  senderPubKey: [string, string] | null;
  createdTxHash: string;
  createdAt: string;           // ISO timestamp
  spentTxHash: string | null;
  spentAt: string | null;
}

function ownerHashOf(pubKeyX: string, pubKeyY: string): string {
  return poseidon2([BigInt(pubKeyX), BigInt(pubKeyY)]).toString();
}

function computeCommitment(value: bigint, salt: bigint, ownerX: string, ownerY: string): string {
  return poseidon4([value, salt, BigInt(ownerX), BigInt(ownerY)]).toString();
}

export class AuthorityLedger {
  private byCommitment = new Map<string, LedgerEntry>();

  size(): number { return this.byCommitment.size; }

  clear(): void { this.byCommitment.clear(); }

  get(commitment: string): LedgerEntry | undefined {
    return this.byCommitment.get(commitment);
  }

  upsertOutput(entry: Omit<LedgerEntry, "spentTxHash" | "spentAt">): void {
    const existing = this.byCommitment.get(entry.commitment);
    if (existing) return;      // idempotent — keep first-seen authoritative record
    this.byCommitment.set(entry.commitment, { ...entry, spentTxHash: null, spentAt: null });
  }

  markSpent(commitment: string, spentTxHash: string, spentAt: string): void {
    const entry = this.byCommitment.get(commitment);
    if (!entry || entry.spentTxHash) return;
    entry.spentTxHash = spentTxHash;
    entry.spentAt = spentAt;
  }

  getLiveForOwner(pubKeyX: string, pubKeyY: string): LedgerEntry[] {
    const keyHash = ownerHashOf(pubKeyX, pubKeyY);
    const out: LedgerEntry[] = [];
    for (const e of this.byCommitment.values()) {
      if (e.ownerKeyHash === keyHash && !e.spentTxHash) out.push(e);
    }
    return out;
  }

  getAllForOwner(pubKeyX: string, pubKeyY: string): LedgerEntry[] {
    const keyHash = ownerHashOf(pubKeyX, pubKeyY);
    const out: LedgerEntry[] = [];
    for (const e of this.byCommitment.values()) {
      if (e.ownerKeyHash === keyHash) out.push(e);
    }
    return out;
  }
}

/** Arguments for ingesting a UTXOTransferNonRepudiationEnforced event. */
export interface TransferEventPayload {
  encryptedValuesForArbiter: bigint[];
  ecdhPublicKey: bigint[];
  encryptionNonce: bigint;
  /** On-chain output commitments, in slot order; used to match decrypted outputs. */
  outputs: bigint[];
}

/** Arguments for ingesting a UTXOForcedTransferEnforced event. */
export interface ForcedTransferEventPayload extends TransferEventPayload {
  /** Commitment hashes of the seized UTXOs — from ZetoTransactionData_V0.SpentCommitments or a pre-call snapshot. */
  spentCommitments: string[];
}

/**
 * Ingest a normal shielded transfer event into the ledger.
 *
 * The arbiter ciphertext's 14-element plaintext includes the sender's pubkey
 * and each input's (value, salt). Because the sender is the owner of every
 * input in a normal transfer, we can reconstruct each input commitment as
 * Poseidon4(value, salt, senderX, senderY) and mark it spent without needing
 * the owner's secret key.
 */
export function ingestTransferEvent(
  ledger: AuthorityLedger,
  indexer: AuthorityNoteIndexer,
  event: TransferEventPayload,
  authorityPrivKey: bigint,
  txHash: string,
  timestamp: string,
): DecryptedTransfer {
  const decrypted = indexer.decryptTransfer(
    event.encryptedValuesForArbiter,
    authorityPrivKey,
    event.ecdhPublicKey,
    event.encryptionNonce,
  );

  // Mark inputs spent. Sender is the owner of every input in a normal transfer.
  const senderX = decrypted.senderPubKey[0];
  const senderY = decrypted.senderPubKey[1];
  for (const input of decrypted.inputs) {
    if (input.value === 0n) continue;              // padding slot
    const commitment = computeCommitment(input.value, input.salt, senderX, senderY);
    ledger.markSpent(commitment, txHash, timestamp);
  }

  // Add each non-zero output. Prefer the on-chain commitment if it matches
  // slot order; otherwise recompute to keep the ledger consistent.
  for (let i = 0; i < decrypted.outputs.length; i++) {
    const out = decrypted.outputs[i];
    if (out.value === 0n) continue;
    const onChain = i < event.outputs.length && event.outputs[i] !== 0n
      ? event.outputs[i].toString()
      : computeCommitment(out.value, out.salt, out.ownerPubKey[0], out.ownerPubKey[1]);
    ledger.upsertOutput({
      commitment: onChain,
      value: out.value,
      salt: out.salt,
      ownerPubKey: out.ownerPubKey,
      ownerKeyHash: ownerHashOf(out.ownerPubKey[0], out.ownerPubKey[1]),
      senderPubKey: decrypted.senderPubKey,
      createdTxHash: txHash,
      createdAt: timestamp,
    });
  }

  return decrypted;
}

/**
 * Ingest a forced-transfer (clawback) event. Unlike a normal transfer, the
 * seized owner's secret is unknown to the enforcer, so we cannot reconstruct
 * the seized commitments from the plaintext — they must be supplied by the
 * caller (either from a pre-call snapshot of the seized owner's live notes
 * or from the ABI-decoded bytes data field).
 */
export function ingestForcedTransferEvent(
  ledger: AuthorityLedger,
  indexer: AuthorityNoteIndexer,
  event: ForcedTransferEventPayload,
  authorityPrivKey: bigint,
  txHash: string,
  timestamp: string,
): DecryptedTransfer {
  const decrypted = indexer.decryptTransfer(
    event.encryptedValuesForArbiter,
    authorityPrivKey,
    event.ecdhPublicKey,
    event.encryptionNonce,
  );

  for (const commitment of event.spentCommitments) {
    if (!commitment || commitment === "0") continue;
    ledger.markSpent(commitment, txHash, timestamp);
  }

  for (let i = 0; i < decrypted.outputs.length; i++) {
    const out = decrypted.outputs[i];
    if (out.value === 0n) continue;
    const onChain = i < event.outputs.length && event.outputs[i] !== 0n
      ? event.outputs[i].toString()
      : computeCommitment(out.value, out.salt, out.ownerPubKey[0], out.ownerPubKey[1]);
    ledger.upsertOutput({
      commitment: onChain,
      value: out.value,
      salt: out.salt,
      ownerPubKey: out.ownerPubKey,
      ownerKeyHash: ownerHashOf(out.ownerPubKey[0], out.ownerPubKey[1]),
      senderPubKey: decrypted.senderPubKey,
      createdTxHash: txHash,
      createdAt: timestamp,
    });
  }

  return decrypted;
}
