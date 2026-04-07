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

import PaladinClient, { PaladinVerifier } from "@lfdecentralizedtrust/paladin-sdk";
import { getBabyjubPublicKey, setComplianceRoot } from "./helpers";
import { ComplianceSmtManager } from "./complianceSmt";

export interface ActorIdentity {
  name: string;
  verifier: PaladinVerifier;
  evmAddress: string;
  babyjubPubKey: string[];
}

export async function resolveActorIdentity(
  name: string,
  verifier: PaladinVerifier,
): Promise<ActorIdentity> {
  return {
    name,
    verifier,
    evmAddress: await verifier.address(),
    babyjubPubKey: await getBabyjubPublicKey(verifier),
  };
}

/** Posts the current compliance SMT root on-chain. Call after batch onboarding or freeze/unfreeze. */
export async function postComplianceRoot(
  paladin: PaladinClient,
  agent: PaladinVerifier,
  zetoAddr: string,
  complianceSmt: ComplianceSmtManager,
): Promise<void> {
  await setComplianceRoot(paladin, agent, zetoAddr, await complianceSmt.getRoot());
}
