import PaladinClient, { ZetoFactory, ZetoInstance, PaladinVerifier, ITransactionReceipt } from "@lfdecentralizedtrust/paladin-sdk";
import { checkReceipt, nodeConnections } from "paladin-example-common";
import * as trex from "./trex";
import { setArbiter, setEnforcer, setCodec, setTransferFacet, registerZetoKyc } from "./helpers";
import * as fs from "fs";
import * as path from "path";
import { ComplianceSmtManager } from "./complianceSmt";
import { resolveActorIdentity, postComplianceRoot } from "./identity";
import { AuthorityNoteIndexer, DecryptedTransfer } from "./authorityIndexer";
import { genKeypair } from "maci-crypto";
import enforcedAbi from "./zeto-abis/IZetoEnforced.json";
import { fundActors, fundDomainSubmitKey, defaultFundingList } from "./fund-actors";
import { IS_SEPOLIA, SEPOLIA_RPC_URL, POLL_TIMEOUT, LONG_POLL_TIMEOUT, etherscanTx, etherscanAddr } from "./sepolia";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const log = console.log;
const header = (title: string, subtitle: string) => {
  log(`\n${"═".repeat(50)}`);
  log(`  ${title}`);
  log(`  ${subtitle}`);
  log(`${"═".repeat(50)}\n`);
};
const step = (n: number, msg: string) => log(`\n--- Step ${n}: ${msg} ---\n`);
const ok = (msg: string) => log(`  ✓ ${msg}`);
const info = (msg: string) => log(`  ${msg}`);

/** Log a contract address with optional Etherscan link (Sepolia only). */
const contract = (label: string, addr: string) => {
  ok(IS_SEPOLIA ? `${label}: ${addr} (${etherscanAddr(addr)})` : `${label}: ${addr}`);
};

/** Log a transaction hash with Etherscan link. */
const txLink = (label: string, hash: string) => {
  info(IS_SEPOLIA ? `${label}: ${hash} (${etherscanTx(hash)})` : `${label}: ${hash}`);
};

async function publicBalance(p: PaladinClient, from: PaladinVerifier, token: string, addr: string): Promise<string> {
  return (await trex.balanceOf(p, from, token, addr)).toLocaleString();
}

async function privateBalance(zeto: ZetoInstance, p: PaladinClient, v: PaladinVerifier): Promise<string> {
  return (await zeto.using(p).balanceOf(v, { account: v.lookup })).totalBalance;
}

function expectRejection(receipt: ITransactionReceipt | undefined, reason: string): boolean {
  if (receipt?.failureMessage) {
    ok(`REJECTED: ${receipt.failureMessage}`);
    return true;
  }
  log(`  ERROR: transfer should have been rejected (${reason})`);
  return false;
}

// ---------------------------------------------------------------------------
// Main demo
// ---------------------------------------------------------------------------

async function main(): Promise<boolean> {
  if (nodeConnections.length < 1) {
    log("ERROR: Need at least 1 node for this scenario.");
    return false;
  }

  // Single-node mode: all actors on node1 with one PaladinClient
  const n1 = nodeConnections[0];
  const p1 = new PaladinClient(n1.clientOptions);
  // For multi-node setups, use separate clients; for single-node, alias them
  const p2 = nodeConnections.length >= 2 ? new PaladinClient(nodeConnections[1].clientOptions) : p1;
  const p3 = nodeConnections.length >= 3 ? new PaladinClient(nodeConnections[2].clientOptions) : p1;
  const runId = Math.random().toString(36).substring(2, 8);

  // All actors on node1 (single-node) or distributed across nodes
  const v = (client: PaladinClient, name: string, nodeId: string) =>
    client.getVerifiers(`${name}-${runId}@${nodeId}`)[0];

  const n2id = nodeConnections.length >= 2 ? nodeConnections[1].id : n1.id;
  const n3id = nodeConnections.length >= 3 ? nodeConnections[2].id : n1.id;

  const issuer    = v(p1, "issuer", n1.id);
  const bank      = v(p1, "bank", n1.id);
  const regulator = v(p1, "regulator", n1.id);
  const alice     = v(p2, "alice", n2id);
  const bob       = v(p3, "bob", n3id);
  const charlie   = v(p3, "charlie", n3id);

  log(`\nRun ${runId} — resolving actor identities...`);
  const [issuerI, bankI, regulatorI, aliceI, bobI, charlieI] = await Promise.all([
    resolveActorIdentity("Issuer", issuer),
    resolveActorIdentity("Bank", bank),
    resolveActorIdentity("Regulator", regulator),
    resolveActorIdentity("Alice", alice),
    resolveActorIdentity("Bob", bob),
    resolveActorIdentity("Charlie", charlie),
  ]);

  const actors = { issuer: issuerI, bank: bankI, regulator: regulatorI, alice: aliceI, bob: bobI, charlie: charlieI };
  for (const [name, a] of Object.entries(actors)) {
    info(`${name}: ${a.evmAddress}`);
  }

  // ══════════════════════════════════════════════════════════════════════
  // FUND ACTORS (Sepolia only — skipped if actors already have balance)
  // ══════════════════════════════════════════════════════════════════════

  if (IS_SEPOLIA && SEPOLIA_RPC_URL) {
    log("\n--- Funding actors on Sepolia ---\n");
    await fundActors(p1, SEPOLIA_RPC_URL, defaultFundingList(n1.id, runId));
  }

  // ══════════════════════════════════════════════════════════════════════
  // SETUP
  // ══════════════════════════════════════════════════════════════════════

  step(1, "Deploy T-REX (ERC-3643) token");

  const suite = await trex.deployTREX(p1, issuer, "Demo Bond Token", "DBT", {
    token: [issuerI.evmAddress, bankI.evmAddress],
    ir: [issuerI.evmAddress, bankI.evmAddress],
  });
  contract("Token", suite.token);
  contract("Identity Registry", suite.identityRegistry);

  step(2, "Deploy Zeto AENKNR-E (shielded pool)");

  // Bank deploys and owns the Zeto contract (matches Go integration test pattern).
  // This is required because forcedTransfer's on-chain submission must come from the
  // contract owner, and the ZK circuit signs with the from identity's BabyJub key.
  const zetoFactory = new ZetoFactory(p1, "zeto");
  const zeto = await zetoFactory
    .newZeto(bank, { tokenName: "Zeto_AnonEncNullifierKycNonRepudiationEnforced" })
    .waitForDeploy(LONG_POLL_TIMEOUT);
  if (!zeto) { log("  ERROR: Zeto deploy failed"); return false; }
  contract("Zeto", zeto.address);

  // Set codec and transfer facet (diamond-lite pattern for enforced tokens)
  const deployDataPath = path.resolve(__dirname, "../data/deploy.json");
  if (fs.existsSync(deployDataPath)) {
    const deployData = JSON.parse(fs.readFileSync(deployDataPath, "utf-8"));
    if (deployData.contracts?.codec) {
      await setCodec(p1, bank, zeto.address, deployData.contracts.codec);
      ok(`Codec set: ${deployData.contracts.codec}`);
    }
    if (deployData.contracts?.transferFacet) {
      await setTransferFacet(p1, bank, zeto.address, deployData.contracts.transferFacet);
      ok(`Transfer facet set: ${deployData.contracts.transferFacet}`);
    }
  }

  step(3, "Link T-REX <> Zeto, set authorities");

  const setErc20Receipt = await zeto
    .setERC20(bank, { erc20: suite.token })
    .waitForReceipt(POLL_TIMEOUT);
  if (!checkReceipt(setErc20Receipt)) return false;
  ok("T-REX linked as ERC-20 backing for Zeto deposits");

  await trex.registerIdentity(p1, issuer, suite.identityRegistry, suite.dummyIdentity, zeto.address, 756);
  ok("Zeto escrow registered in T-REX IR");

  // Arbiter key is generated EXTERNALLY — not managed by Paladin.
  // The private key stays in this process for decryption (Act 3).
  // In production, it would live in the UI backend's secure storage.
  const arbiterKeypair = genKeypair();
  const arbiterPrivKey = BigInt(arbiterKeypair.privKey.toString()); // raw key — genEcdhSharedKey formats internally
  const arbiterPubKey = [arbiterKeypair.pubKey[0].toString(), arbiterKeypair.pubKey[1].toString()];
  await setArbiter(p1, bank, zeto.address, arbiterPubKey);
  ok("Arbiter set (external keypair — private key held for decryption)");

  await setEnforcer(p1, bank, zeto.address, bankI.babyjubPubKey);
  ok("Enforcer set (Bank — set-once)");


  step(4, "KYC onboarding (Alice, Bob — NOT Charlie)");

  const complianceSmt = new ComplianceSmtManager();
  await complianceSmt.init();

  for (const actor of [issuerI, bankI, regulatorI, aliceI, bobI]) {
    // T-REX identity: issuer is agent. Zeto KYC: bank is owner.
    await trex.registerIdentity(p1, issuer, suite.identityRegistry, suite.dummyIdentity, actor.evmAddress, 756);
    await registerZetoKyc(p1, bank, zeto.address, actor.babyjubPubKey);
    await complianceSmt.setStatus(actor.babyjubPubKey[0], actor.babyjubPubKey[1], 1n);
    ok(`${actor.name}: T-REX identity + Zeto KYC + compliance ACTIVE`);
  }
  info("Charlie intentionally NOT onboarded");

  await postComplianceRoot(p1, bank, zeto.address, complianceSmt);
  ok("Compliance root posted on-chain");

  await trex.unpause(p1, issuer, suite.token);
  ok("T-REX token unpaused");

  step(5, "Issue treasury to Bank");

  const TREASURY = 1_000_000;
  await trex.mint(p1, issuer, suite.token, bankI.evmAddress, TREASURY);
  ok(`Minted ${TREASURY.toLocaleString()} DBT to Bank`);

  step(6, "Bank deposits 50% into Zeto privacy pool");

  const DEPOSIT = TREASURY / 2;
  await trex.approve(p1, bank, suite.token, zeto.address, DEPOSIT);
  const depositReceipt = await zeto
    .deposit(bank, { amount: DEPOSIT })
    .waitForReceipt(LONG_POLL_TIMEOUT);
  if (!checkReceipt(depositReceipt)) return false;

  info(`Bank public:  ${await publicBalance(p1, bank, suite.token, bankI.evmAddress)}`);
  info(`Bank private: ${await privateBalance(zeto, p1, bank)}`);

  // ══════════════════════════════════════════════════════════════════════
  // ACT 1 — PUBLIC TRANSFERS
  // ══════════════════════════════════════════════════════════════════════

  header("ACT 1: PUBLIC TRANSFERS", "Open block explorer — everything visible");

  step(7, "Alice buys 10,000 public tokens from Bank");

  await trex.transfer(p1, bank, suite.token, aliceI.evmAddress, 10_000);
  info(`Alice public: ${await publicBalance(p2, alice, suite.token, aliceI.evmAddress)}`);
  info(">> Block explorer: sender, receiver, amount ALL visible <<");

  step(8, "Alice sends 2,000 public tokens to Bob");

  await trex.transfer(p2, alice, suite.token, bobI.evmAddress, 2_000);
  info(`Alice public: ${await publicBalance(p2, alice, suite.token, aliceI.evmAddress)}`);
  info(`Bob public:   ${await publicBalance(p3, bob, suite.token, bobI.evmAddress)}`);
  info(">> Every observer can see WHO sent WHAT to WHOM <<");

  // ══════════════════════════════════════════════════════════════════════
  // ACT 2 — SHIELDED TRANSFERS
  // ══════════════════════════════════════════════════════════════════════

  header("ACT 2: SHIELDED TRANSFERS", "Same explorer — amounts and parties hidden");

  // Fund the domain submit key for this Zeto contract (Sepolia only).
  // The deposit above triggered Paladin to create this key in its key store.
  // It's a BIP32-derived key used to submit base-layer TXs for private transfers.
  if (IS_SEPOLIA && SEPOLIA_RPC_URL) {
    await fundDomainSubmitKey(SEPOLIA_RPC_URL, zeto.address);
  }

  step(9, "Alice buys 50,000 private tokens from Bank (Zeto transfer)");

  const privateBuyReceipt = await zeto
    .transfer(bank, { transfers: [{ to: alice, amount: 50_000, data: "0x" }] })
    .waitForReceipt(LONG_POLL_TIMEOUT);
  if (!checkReceipt(privateBuyReceipt)) return false;

  info(`Alice private: ${await privateBalance(zeto, p2, alice)}`);
  info(">> On-chain tx exists but CANNOT determine WHO received or HOW MUCH <<");

  step(10, "Alice sends 20,000 private tokens to Bob");

  const aliceToBobReceipt = await zeto.using(p2)
    .transfer(alice, { transfers: [{ to: bob, amount: 20_000, data: "0x" }] })
    .waitForReceipt(LONG_POLL_TIMEOUT);
  if (!checkReceipt(aliceToBobReceipt)) return false;

  info(`Alice private: ${await privateBalance(zeto, p2, alice)}`);
  info(`Bob private:   ${await privateBalance(zeto, p3, bob)}`);
  info(">> Public T-REX balances UNCHANGED — privacy preserved <<");

  // ══════════════════════════════════════════════════════════════════════
  // ACT 3 — REGULATORY DISCLOSURE
  // ══════════════════════════════════════════════════════════════════════

  header("ACT 3: REGULATORY DISCLOSURE", "Regulator can see what public observers cannot");

  step(11, "Regulator decrypts shielded transfers");

  info("Public observers see on-chain tx hashes but CANNOT determine who sent what to whom.");
  info("The regulator holds the arbiter private key and can decrypt every transfer.\n");

  const indexer = new AuthorityNoteIndexer();

  const decryptTransferEvent = async (
    label: string,
    txHash: string,
  ): Promise<DecryptedTransfer | undefined> => {
    const events = await p1.bidx.decodeTransactionEvents(txHash, enforcedAbi.abi, "");
    const evt = events?.find((e: { soliditySignature?: string }) =>
      e.soliditySignature?.includes("UTXOTransferNonRepudiationEnforced"),
    );
    if (!evt) {
      info(`  ${label}: event not found (tx may not be indexed yet)`);
      return undefined;
    }
    const data = evt.data as Record<string, unknown>;
    if (!data?.encryptedValuesForArbiter || !data?.ecdhPublicKey || !data?.encryptionNonce) {
      info(`  ${label}: missing decryption fields in event data`);
      return undefined;
    }
    const ciphertext = (data.encryptedValuesForArbiter as string[]).map(BigInt);
    const ecdhPubKey = (data.ecdhPublicKey as string[]).map(BigInt);
    const nonce = BigInt(data.encryptionNonce as string);

    const decrypted = indexer.decryptTransfer(ciphertext, arbiterPrivKey, ecdhPubKey, nonce);
    indexer.indexNotes(decrypted);
    return decrypted;
  };

  const formatDecrypted = (d: DecryptedTransfer) => {
    const inTotal = d.inputs.reduce((s, i) => s + i.value, 0n);
    const out0 = d.outputs[0];
    const out1 = d.outputs[1];
    info(`    Input total:  ${inTotal}`);
    if (out0.value > 0n) info(`    Output 1:     ${out0.value} → owner ${out0.ownerPubKey[0].substring(0, 12)}...`);
    if (out1.value > 0n) info(`    Output 2:     ${out1.value} → owner ${out1.ownerPubKey[0].substring(0, 12)}...`);
  };

  if (privateBuyReceipt?.transactionHash) {
    txLink("Tx (Bank→Alice)", privateBuyReceipt.transactionHash);
    const d = await decryptTransferEvent("Bank→Alice", privateBuyReceipt.transactionHash);
    if (d) {
      ok("Decrypted:");
      formatDecrypted(d);
    }
  }

  if (aliceToBobReceipt?.transactionHash) {
    txLink("\nTx (Alice→Bob)", aliceToBobReceipt.transactionHash);
    const d = await decryptTransferEvent("Alice→Bob", aliceToBobReceipt.transactionHash);
    if (d) {
      ok("Decrypted:");
      formatDecrypted(d);
    }
  }

  info("\n>> Public observers see NOTHING. Regulator decrypts EVERYTHING. <<");

  // ══════════════════════════════════════════════════════════════════════
  // ACT 4 — COMPLIANCE ENFORCEMENT
  // ══════════════════════════════════════════════════════════════════════

  header("ACT 4: COMPLIANCE — KYC, FREEZE, CLAWBACK", "Privacy does NOT break compliance");

  // -- Scenario 1: KYC enforcement --

  step(12, "Alice → Charlie (5,000 private) — Charlie NOT KYCed");

  const kycRejectReceipt = await zeto.using(p2)
    .transfer(alice, { transfers: [{ to: charlie, amount: 5_000, data: "0x" }] })
    .waitForReceipt(LONG_POLL_TIMEOUT);
  if (!expectRejection(kycRejectReceipt, "Charlie not KYCed")) return false;

  step(13, "Bank approves Charlie's KYC");

  await trex.registerIdentity(p1, issuer, suite.identityRegistry, suite.dummyIdentity, charlieI.evmAddress, 756);
  await registerZetoKyc(p1, bank, zeto.address, charlieI.babyjubPubKey);
  await complianceSmt.setStatus(charlieI.babyjubPubKey[0], charlieI.babyjubPubKey[1], 1n);
  await postComplianceRoot(p1, bank, zeto.address, complianceSmt);
  ok("Charlie KYCed on both T-REX and Zeto");

  step(14, "Alice → Charlie (5,000 private) — now succeeds");

  const aliceToCharlieReceipt = await zeto.using(p2)
    .transfer(alice, { transfers: [{ to: charlie, amount: 5_000, data: "0x" }] })
    .waitForReceipt(LONG_POLL_TIMEOUT);
  if (!checkReceipt(aliceToCharlieReceipt)) return false;

  info(`Charlie private: ${await privateBalance(zeto, p3, charlie)}`);

  // -- Scenario 2: Freeze --

  step(15, "Bank freezes Charlie (dual-domain)");

  await trex.setAddressFrozen(p1, bank, suite.token, charlieI.evmAddress, true);
  await complianceSmt.setStatus(charlieI.babyjubPubKey[0], charlieI.babyjubPubKey[1], 2n /* FROZEN */);
  await postComplianceRoot(p1, bank, zeto.address, complianceSmt);
  ok("Charlie frozen on T-REX + Zeto compliance");
  info(`isFrozen(Charlie) = ${await trex.isFrozen(p1, bank, suite.token, charlieI.evmAddress)}`);

  step(16, "Charlie → Bob (2,000 private) — FROZEN");

  const frozenRejectReceipt = await zeto.using(p3)
    .transfer(charlie, { transfers: [{ to: bob, amount: 2_000, data: "0x" }] })
    .waitForReceipt(LONG_POLL_TIMEOUT);
  if (!expectRejection(frozenRejectReceipt, "Charlie is frozen")) return false;

  // -- Scenario 3: Clawback --

  step(17, "Bank (enforcer) claws back Charlie's private funds");

  const charlieBalStr = await privateBalance(zeto, p3, charlie);
  const charlieBal = parseInt(charlieBalStr, 10);
  if (!Number.isSafeInteger(charlieBal) || charlieBal <= 0) {
    log(`  ERROR: unexpected Charlie balance: ${charlieBalStr}`);
    return false;
  }
  info(`Charlie private balance: ${charlieBal}`);

  const forcedReceipt = await zeto
    .forcedTransfer(bank, {
      seizedOwner: charlie.lookup,
      transfers: [{ to: bank, amount: charlieBal, data: "0x" }],
    })
    .waitForReceipt(LONG_POLL_TIMEOUT);
  if (!checkReceipt(forcedReceipt)) return false;

  info(`Charlie private: ${await privateBalance(zeto, p3, charlie)} (should be 0)`);
  info(`Bank private:    ${await privateBalance(zeto, p1, bank)}`);
  ok("Clawback complete — ALL frozen funds seized");

  // ══════════════════════════════════════════════════════════════════════
  // FINAL SUMMARY
  // ══════════════════════════════════════════════════════════════════════

  header("FINAL BALANCES", "Public vs Private — same asset, different visibility");

  const summary: Array<{ name: string; p: PaladinClient; v: PaladinVerifier; addr: string }> = [
    { name: "Bank",    p: p1, v: bank,    addr: bankI.evmAddress },
    { name: "Alice",   p: p2, v: alice,   addr: aliceI.evmAddress },
    { name: "Bob",     p: p3, v: bob,     addr: bobI.evmAddress },
    { name: "Charlie", p: p3, v: charlie, addr: charlieI.evmAddress },
  ];

  for (const { name, p, v: ver, addr } of summary) {
    const pub = await publicBalance(p1, bank, suite.token, addr);
    const priv = await privateBalance(zeto, p, ver);
    const frozen = name === "Charlie" ? ` (frozen=${await trex.isFrozen(p1, bank, suite.token, addr)})` : "";
    log(`  ${name.padEnd(10)} public: ${pub.padStart(10)}  private: ${priv.padStart(10)}${frozen}`);
  }

  log("\n=== Demo Complete ===\n");
  log("  1. Public transfers  — fully visible on block explorer");
  log("  2. Private transfers — on-chain but amounts/parties hidden");
  log("  3. Regulatory access — arbiter decrypts everything");
  log("  4. KYC enforcement  — unregistered receivers rejected");
  log("  5. Account freeze   — frozen users cannot transact");
  log("  6. Clawback         — enforcer seizes frozen user's private funds");
  log("");
  return true;
}

export { main };

if (require.main === module) {
  main()
    .then((success) => process.exit(success ? 0 : 1))
    .catch((err) => { log(`Private T-REX example crashed: ${err}`); process.exit(1); });
}
