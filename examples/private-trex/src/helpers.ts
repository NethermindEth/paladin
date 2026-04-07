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
// Internal helpers
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

// ---------------------------------------------------------------------------
// BabyJub key resolution
// ---------------------------------------------------------------------------

/** Resolves a Paladin verifier's BabyJubJub public key as [X, Y] decimal strings. */
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
// Zeto admin operations
// ---------------------------------------------------------------------------

/** Registers a BabyJubJub public key in the Zeto KYC identity tree. */
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

/** Sets the arbiter (audit) BabyJubJub key. Rotatable. */
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

/** Sets the enforcer (seizure) BabyJubJub key. Set-once — reverts on second call. */
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

// ---------------------------------------------------------------------------
// Codec and transfer facet (diamond-lite pattern for enforced tokens)
// ---------------------------------------------------------------------------

const codecFacetAbi = [
  { type: "function", name: "setCodec", inputs: [{ name: "codec", type: "address" }], outputs: [], stateMutability: "nonpayable" },
  { type: "function", name: "setTransferFacet", inputs: [{ name: "facet", type: "address" }], outputs: [], stateMutability: "nonpayable" },
];

export async function setCodec(
  paladin: PaladinClient,
  owner: PaladinVerifier,
  zetoAddr: string,
  codecAddr: string,
): Promise<void> {
  await sendTx(paladin, owner, zetoAddr, codecFacetAbi, "setCodec", { codec: codecAddr });
}

export async function setTransferFacet(
  paladin: PaladinClient,
  owner: PaladinVerifier,
  zetoAddr: string,
  facetAddr: string,
): Promise<void> {
  await sendTx(paladin, owner, zetoAddr, codecFacetAbi, "setTransferFacet", { facet: facetAddr });
}

// ---------------------------------------------------------------------------
// Compliance root
// ---------------------------------------------------------------------------

/** Posts the compliance SMT root on-chain. Must be called before any operation needing compliance proofs. */
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

