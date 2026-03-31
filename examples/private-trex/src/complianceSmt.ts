import { Merkletree, InMemoryDB, str2Bytes, ZERO_HASH } from "@iden3/js-merkletree";
import { poseidon2, poseidon3 } from "poseidon-lite";

/**
 * Compliance status constants matching the circom circuit template:
 *   compliance_status.circom → STATUS parameter
 */
export const STATUS_ACTIVE = 1n;
export const STATUS_FROZEN = 2n;

/**
 * SMT height for the compliance tree. Must match:
 *   - Go domain: smt.SMT_HEIGHT_COMPLIANCE = 64
 *   - Zeto TS test: SMT_HEIGHT = 64
 *   - circom circuit: nComplianceSMTLevels parameter
 */
const SMT_HEIGHT = 64;

/**
 * Off-chain compliance Sparse Merkle Tree manager.
 *
 * Tracks each registered identity's compliance status (ACTIVE or FROZEN).
 * Only the root hash is posted on-chain via setComplianceRoot().
 *
 * Leaf encoding (must match compliance_status.circom exactly):
 *   key   = Poseidon2(pubKeyX, pubKeyY)
 *   value = Poseidon3(pubKeyX, pubKeyY, STATUS)
 *
 * Uses the same libraries as the Zeto TS tests (@iden3/js-merkletree + poseidon-lite)
 * to guarantee root compatibility.
 */
export class ComplianceSmtManager {
  private tree!: Merkletree;
  private knownKeys = new Set<string>();

  async init(): Promise<void> {
    const db = new InMemoryDB(str2Bytes("compliance"));
    this.tree = new Merkletree(db, true, SMT_HEIGHT);
  }

  /**
   * Sets the compliance status for an identity.
   *
   * If the identity is new, inserts a leaf. If it already exists, updates the
   * leaf value (e.g. ACTIVE → FROZEN for a freeze, or FROZEN → ACTIVE for an
   * unfreeze). This uses @iden3/js-merkletree's native update() — no tree rebuild.
   */
  async setStatus(pubKeyX: string, pubKeyY: string, status: bigint): Promise<void> {
    const x = BigInt(pubKeyX);
    const y = BigInt(pubKeyY);
    const leafKey = poseidon2([x, y]);
    const leafValue = poseidon3([x, y, status]);

    const keyStr = leafKey.toString();
    if (this.knownKeys.has(keyStr)) {
      await this.tree.update(leafKey, leafValue);
    } else {
      await this.tree.add(leafKey, leafValue);
      this.knownKeys.add(keyStr);
    }
  }

  /**
   * Returns the current compliance root as a decimal string.
   *
   * This is the value passed to setComplianceRoot() on-chain.
   */
  async getRoot(): Promise<string> {
    const root = await this.tree.root();
    return root.bigInt().toString();
  }

  /**
   * Generates a circom-compatible inclusion proof for an identity.
   *
   * Returns { root, siblings } where siblings is an array of BigInt values
   * ready for use as circuit witness inputs.
   */
  async getProof(pubKeyX: string, pubKeyY: string): Promise<{ root: bigint; siblings: bigint[] }> {
    const leafKey = poseidon2([BigInt(pubKeyX), BigInt(pubKeyY)]);
    const proof = await this.tree.generateCircomVerifierProof(leafKey, ZERO_HASH);
    return {
      root: proof.root.bigInt(),
      siblings: proof.siblings.map((s: any) => s.bigInt()),
    };
  }
}
