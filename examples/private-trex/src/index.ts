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

import PaladinClient from "@lfdecentralizedtrust/paladin-sdk";
import { nodeConnections } from "paladin-example-common";
import * as trex from "./trex";

async function main(): Promise<boolean> {
  if (nodeConnections.length < 3) {
    console.error("Need at least 3 nodes for this scenario.");
    return false;
  }

  const [n1, n2, n3] = nodeConnections;
  const [p1, p2, p3] = [n1, n2, n3].map((n) => new PaladinClient(n.clientOptions));
  const runId = Math.random().toString(36).substring(2, 8);

  // 6 actors across 3 nodes:
  //   Node 1: Issuer (deploys), Bank (operates)
  //   Node 2: Alice, Charlie (investors)
  //   Node 3: Bob, Regulator
  const v = (client: PaladinClient, name: string, nodeId: string) =>
    client.getVerifiers(`${name}-${runId}@${nodeId}`)[0];

  const issuer = v(p1, "issuer", n1.id);
  const bank = v(p1, "bank", n1.id);
  const alice = v(p2, "alice", n2.id);
  const charlie = v(p2, "charlie", n2.id);
  const bob = v(p3, "bob", n3.id);
  // Regulator verifier created here when Zeto flows are wired in:
  // const regulator = v(p3, "regulator", n3.id);

  const addrs = await Promise.all(
    [issuer, bank, alice, bob, charlie].map(async (v) => v.address()),
  );
  const [issuerAddr, bankAddr, aliceAddr, bobAddr, charlieAddr] = addrs;

  // Helper: query all balances at once
  const balances = async () => {
    const [b, a, bo, c] = await Promise.all(
      [bankAddr, aliceAddr, bobAddr, charlieAddr].map((addr) =>
        trex.balanceOf(p1, bank, suite.token, addr),
      ),
    );
    return { bank: b, alice: a, bob: bo, charlie: c };
  };

  // Helper: assert a call fails
  const expectFailure = async (fn: () => Promise<void>, reason: string) => {
    try {
      await fn();
      console.error(`   ERROR: should have failed (${reason})`);
      return false;
    } catch {
      console.log(`   Rejected: ${reason}`);
      return true;
    }
  };

  console.log(`\nRun ${runId} — Issuer=${issuerAddr}, Bank=${bankAddr}`);
  console.log(`  Alice=${aliceAddr}, Bob=${bobAddr}, Charlie=${charlieAddr}\n`);

  // ── Startup: Deploy & configure ───────────────────────────────────────────

  console.log("=== Startup ===\n");

  console.log("Deploying T-REX suite...");
  const suite = await trex.deployTREX(p1, issuer, "Demo Bond Token", "DBT", {
    token: [issuerAddr, bankAddr],
    ir: [issuerAddr, bankAddr],
  });

  // KYC: Alice ✅, Bob ✅, Charlie ❌
  console.log("Registering identities...");
  for (const [addr, label] of [[issuerAddr, "Issuer"], [bankAddr, "Bank"], [aliceAddr, "Alice"], [bobAddr, "Bob"]] as const) {
    await trex.registerIdentity(p1, issuer, suite.identityRegistry, suite.dummyIdentity, addr, 840);
    console.log(`  ${label} KYC'd`);
  }
  console.log("  Charlie NOT KYC'd (deliberate)");

  console.log("Unpausing token...");
  await trex.unpause(p1, issuer, suite.token);

  const TREASURY = 1_000_000;
  console.log(`Minting ${TREASURY} to Bank...`);
  await trex.mint(p1, issuer, suite.token, bankAddr, TREASURY);

  // Zeto integration: after deploying NM-Zeto, register its address in IR
  // and call trex.approve() before Zeto.deposit().

  // ── Minutes 1–3: Public transfers ─────────────────────────────────────────

  console.log("\n=== Public transfers (fully transparent on Etherscan) ===\n");

  const ALICE_BUY = 100_000;
  console.log(`Bank → Alice (${ALICE_BUY})...`);
  await trex.transfer(p1, bank, suite.token, aliceAddr, ALICE_BUY);

  const ALICE_TO_BOB = 25_000;
  console.log(`Alice → Bob (${ALICE_TO_BOB})...`);
  await trex.transfer(p2, alice, suite.token, bobAddr, ALICE_TO_BOB);

  let bal = await balances();
  console.log(`  Bank=${bal.bank}  Alice=${bal.alice}  Bob=${bal.bob}`);

  // Minutes 4–11: Shielded transfers and selective disclosure happen
  // entirely in the Zeto domain — T-REX is not involved.

  // ── Minutes 12–15: Compliance enforcement ─────────────────────────────────

  console.log("\n=== Compliance enforcement ===\n");

  // Scenario 1: KYC gate
  console.log("-- KYC enforcement --\n");

  console.log(`isVerified(Charlie) = ${await trex.isVerified(p1, bank, suite.identityRegistry, charlieAddr)}`);
  console.log("Alice → Charlie (10,000) — expect rejection...");
  if (!await expectFailure(
    () => trex.transfer(p2, alice, suite.token, charlieAddr, 10_000),
    "receiver not KYC'd",
  )) return false;

  console.log("\nBank approves Charlie's KYC...");
  await trex.registerIdentity(p1, bank, suite.identityRegistry, suite.dummyIdentity, charlieAddr, 840);
  console.log(`isVerified(Charlie) = ${await trex.isVerified(p1, bank, suite.identityRegistry, charlieAddr)}`);

  console.log("Alice → Charlie (10,000) — should succeed...");
  await trex.transfer(p2, alice, suite.token, charlieAddr, 10_000);
  bal = await balances();
  console.log(`  Alice=${bal.alice}  Charlie=${bal.charlie}`);

  // Scenario 2: Freeze
  console.log("\n-- Freeze --\n");

  console.log("Bank freezes Charlie...");
  await trex.setAddressFrozen(p1, bank, suite.token, charlieAddr, true);
  console.log(`isFrozen(Charlie) = ${await trex.isFrozen(p1, bank, suite.token, charlieAddr)}`);

  console.log("Charlie → Bob (5,000) — expect rejection...");
  if (!await expectFailure(
    () => trex.transfer(p2, charlie, suite.token, bobAddr, 5_000),
    "account is frozen",
  )) return false;

  // Scenario 3: Clawback
  console.log("\n-- Clawback --\n");

  const charlieBalance = await trex.balanceOf(p1, bank, suite.token, charlieAddr);
  console.log(`Bank clawback: reclaiming ${charlieBalance} from Charlie...`);
  await trex.forcedTransfer(p1, bank, suite.token, charlieAddr, bankAddr, charlieBalance);

  // ── Final state ───────────────────────────────────────────────────────────

  bal = await balances();
  const frozen = await trex.isFrozen(p1, bank, suite.token, charlieAddr);
  console.log("\n=== Final state ===\n");
  console.log(`Bank=${bal.bank}  Alice=${bal.alice}  Bob=${bal.bob}  Charlie=${bal.charlie} (frozen=${frozen})`);
  console.log("\nDemo complete.");
  return true;
}

export { main };

if (require.main === module) {
  main()
    .then((ok) => process.exit(ok ? 0 : 1))
    .catch((err) => { console.error("T-REX example crashed:", err); process.exit(1); });
}
