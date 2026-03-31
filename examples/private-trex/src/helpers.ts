import PaladinClient, {
  PaladinVerifier,
  TransactionType,
  algorithmZetoSnarkBJJ,
  IDEN3_PUBKEY_BABYJUBJUB_COMPRESSED_0X,
} from "@lfdecentralizedtrust/paladin-sdk";
import { checkReceipt, DEFAULT_POLL_TIMEOUT } from "paladin-example-common";
import { buildBabyjub } from "circomlibjs";
import kycAbi from "./zeto-abis/IZetoKyc.json";
import complianceRootAbi from "./zeto-abis/IZetoComplianceRoot.json";
import enforcedAbi from "./zeto-abis/IZetoEnforced.json";

// ---------------------------------------------------------------------------
// Internal: raw PUBLIC transaction helper (same pattern as trex.ts)
// ---------------------------------------------------------------------------

async function sendTx(
  paladin: PaladinClient,
  from: PaladinVerifier,
  to: string,
  abi: any[],
  fn: string,
  data: Record<string, any>,
): Promise<void> {
  const txId = await paladin.ptx.sendTransaction({
    type: TransactionType.PUBLIC,
    from: from.lookup,
    to,
    data,
    function: fn,
    abi,
  });
  const receipt = await paladin.pollForReceipt(txId, DEFAULT_POLL_TIMEOUT);
  if (!checkReceipt(receipt)) {
    throw new Error(`Transaction ${fn} failed (txId=${txId})`);
  }
}

async function queryTx(
  paladin: PaladinClient,
  from: PaladinVerifier,
  to: string,
  abi: any[],
  fn: string,
  data: Record<string, any> = {},
): Promise<any> {
  return paladin.ptx.call({
    type: TransactionType.PUBLIC,
    from: from.lookup,
    to,
    data,
    function: fn,
    abi,
  });
}

// ---------------------------------------------------------------------------
// BabyJub key resolution
// ---------------------------------------------------------------------------

/**
 * Resolves a Paladin verifier's BabyJubJub public key as [X, Y] decimal strings.
 *
 * The Paladin key store returns a 32-byte compressed point. We decompress it
 * using circomlibjs and return the two field element coordinates as decimal
 * strings, which is the format expected by Zeto's Solidity registration and
 * the Poseidon hash inputs in the compliance/KYC circuits.
 */
export async function getBabyjubPublicKey(
  verifier: PaladinVerifier,
): Promise<string[]> {
  const pubKeyStr = await verifier.resolve(
    algorithmZetoSnarkBJJ("zeto") as any,
    IDEN3_PUBKEY_BABYJUBJUB_COMPRESSED_0X as any,
  );

  if (!pubKeyStr || pubKeyStr === "0x0" || pubKeyStr === "0x") {
    throw new Error(`No babyjub key available for ${verifier.lookup}`);
  }

  const cleanHex = pubKeyStr.startsWith("0x") ? pubKeyStr.slice(2) : pubKeyStr;
  const compressedBytes = Buffer.from(cleanHex, "hex");

  if (compressedBytes.length !== 32) {
    throw new Error(
      `Invalid key length for ${verifier.lookup}: expected 32 bytes, got ${compressedBytes.length}`,
    );
  }

  const babyJub = await buildBabyjub();
  const publicKey = babyJub.unpackPoint(compressedBytes);

  if (!publicKey || publicKey.length < 2) {
    throw new Error(
      `Failed to unpack babyjub key for ${verifier.lookup}: invalid point`,
    );
  }

  return [babyJub.F.toString(publicKey[0]), babyJub.F.toString(publicKey[1])];
}

// ---------------------------------------------------------------------------
// Zeto KYC registration
// ---------------------------------------------------------------------------

/**
 * Registers a BabyJubJub public key in the Zeto KYC identity tree.
 *
 * This calls `register(uint256[2] publicKey, bytes data)` on the Zeto contract.
 * The domain's event handler adds the key to the off-chain identities SMT and
 * (for enforced tokens) the compliance SMT with status ACTIVE.
 */
export async function registerZetoKyc(
  paladin: PaladinClient,
  owner: PaladinVerifier,
  zetoAddr: string,
  pubKey: string[],
): Promise<void> {
  await sendTx(paladin, owner, zetoAddr, kycAbi.abi, "register", {
    publicKey: pubKey,
    data: "0x",
  });
}

/**
 * Checks whether a BabyJubJub public key is registered in the KYC tree.
 */
export async function isRegistered(
  paladin: PaladinClient,
  from: PaladinVerifier,
  zetoAddr: string,
  pubKey: string[],
): Promise<boolean> {
  const result = await queryTx(paladin, from, zetoAddr, kycAbi.abi, "isRegistered", {
    publicKey: pubKey,
  });
  return Boolean(result[0]);
}

// ---------------------------------------------------------------------------
// Arbiter key management
// ---------------------------------------------------------------------------

/**
 * Sets the arbiter BabyJubJub public key on the Zeto enforced contract.
 *
 * The arbiter receives encrypted copies of every transfer's inputs/outputs
 * for audit/non-repudiation. This key is rotatable — each call emits an
 * ArbiterUpdated event with an incrementing keyId.
 */
export async function setArbiter(
  paladin: PaladinClient,
  owner: PaladinVerifier,
  zetoAddr: string,
  pubKey: string[],
): Promise<void> {
  await sendTx(paladin, owner, zetoAddr, enforcedAbi.abi, "setArbiter", {
    newKey: pubKey,
  });
}

/**
 * Reads the current arbiter public key from the contract.
 */
export async function getArbiter(
  paladin: PaladinClient,
  from: PaladinVerifier,
  zetoAddr: string,
): Promise<string[]> {
  const result = await queryTx(paladin, from, zetoAddr, enforcedAbi.abi, "getArbiter");
  return [result[0][0].toString(), result[0][1].toString()];
}

// ---------------------------------------------------------------------------
// Enforcer key management
// ---------------------------------------------------------------------------

/**
 * Sets the enforcer BabyJubJub public key on the Zeto enforced contract.
 *
 * The enforcer can freeze accounts and execute forced transfers (clawback).
 * This key is set-once — calling setEnforcer a second time will revert with
 * EnforcerAlreadySet. Deploy a fresh Zeto contract if the key must change.
 */
export async function setEnforcer(
  paladin: PaladinClient,
  owner: PaladinVerifier,
  zetoAddr: string,
  pubKey: string[],
): Promise<void> {
  await sendTx(paladin, owner, zetoAddr, enforcedAbi.abi, "setEnforcer", {
    newKey: pubKey,
  });
}

/**
 * Reads the current enforcer public key from the contract.
 */
export async function getEnforcer(
  paladin: PaladinClient,
  from: PaladinVerifier,
  zetoAddr: string,
): Promise<string[]> {
  const result = await queryTx(paladin, from, zetoAddr, enforcedAbi.abi, "getEnforcer");
  return [result[0][0].toString(), result[0][1].toString()];
}

// ---------------------------------------------------------------------------
// Compliance root management
// ---------------------------------------------------------------------------

/**
 * Posts a new compliance SMT root on-chain.
 *
 * The compliance root is the Poseidon hash at the top of an off-chain Sparse
 * Merkle Tree where each leaf encodes a user's compliance status:
 *   key   = Poseidon(pubKeyX, pubKeyY)
 *   value = Poseidon(pubKeyX, pubKeyY, STATUS)    STATUS: 1=ACTIVE, 2=FROZEN
 *
 * The root must be posted on-chain BEFORE any Zeto operation that requires
 * compliance proofs (transfer, deposit, withdraw, forcedTransfer). The Go
 * domain reads getComplianceRoot() during Prepare to construct the ZK proof's
 * public inputs. If the on-chain root doesn't match the off-chain tree used
 * for proof generation, the Groth16 verification will fail.
 *
 * @param data - Arbitrary bytes attached to the ComplianceRootUpdated event
 *               (pass "0x" if unused).
 */
export async function setComplianceRoot(
  paladin: PaladinClient,
  owner: PaladinVerifier,
  zetoAddr: string,
  root: string,
  data: string = "0x",
): Promise<void> {
  await sendTx(paladin, owner, zetoAddr, complianceRootAbi.abi, "setComplianceRoot", {
    newRoot: root,
    data,
  });
}

/**
 * Reads the current compliance root from the contract.
 */
export async function getComplianceRoot(
  paladin: PaladinClient,
  from: PaladinVerifier,
  zetoAddr: string,
): Promise<string> {
  const result = await queryTx(paladin, from, zetoAddr, complianceRootAbi.abi, "getComplianceRoot");
  return result[0].toString();
}

