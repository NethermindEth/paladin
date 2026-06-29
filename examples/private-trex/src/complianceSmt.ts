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

import { Merkletree, InMemoryDB, str2Bytes, ZERO_HASH } from "@iden3/js-merkletree";
import { poseidon2, poseidon3 } from "poseidon-lite";

export const STATUS_ACTIVE = 1n;
export const STATUS_FROZEN = 2n;

// Must match Go domain (SMT_HEIGHT_COMPLIANCE = 20) and circom nComplianceSMTLevels.
const SMT_HEIGHT = 20;

/**
 * Off-chain compliance SMT. Tracks ACTIVE/FROZEN status per identity.
 * Only the root is posted on-chain. Leaf encoding matches compliance_status.circom:
 *   key = Poseidon2(pubKeyX, pubKeyY), value = Poseidon3(pubKeyX, pubKeyY, STATUS)
 */
export class ComplianceSmtManager {
  private tree!: Merkletree;
  private knownKeys = new Set<string>();

  async init(): Promise<void> {
    const db = new InMemoryDB(str2Bytes("compliance"));
    this.tree = new Merkletree(db, true, SMT_HEIGHT);
  }

  /** Inserts or updates an identity's compliance status (ACTIVE or FROZEN). */
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

  /** Returns the current root as a decimal string for setComplianceRoot(). */
  async getRoot(): Promise<string> {
    const root = await this.tree.root();
    return root.bigInt().toString();
  }

  /** Generates a circom-compatible inclusion proof for an identity. */
  async getProof(pubKeyX: string, pubKeyY: string): Promise<{ root: bigint; siblings: bigint[] }> {
    const leafKey = poseidon2([BigInt(pubKeyX), BigInt(pubKeyY)]);
    const proof = await this.tree.generateCircomVerifierProof(leafKey, ZERO_HASH);
    return {
      root: proof.root.bigInt(),
      siblings: proof.siblings.map((s: any) => s.bigInt()),
    };
  }
}
