/*
 * DemoSession — in-memory state for one demo run.
 *
 * Wraps the Paladin SDK (trex.ts, helpers.ts, identity.ts, etc.) into a
 * single object the API routes call per-request. One session at a time.
 *
 * Key architectural choices:
 * - Arbiter private key lives here, never sent to the frontend — used to
 *   decrypt shielded transfers for the regulator dashboard.
 * - Compliance SMT (sparse Merkle tree) tracks KYC/frozen status per identity.
 *   Zeto circuits require an SMT inclusion proof; the root is posted on-chain.
 * - Shielded notes are auto-tracked in-session from transfer receipts so the
 *   regulator can discover them without querying the chain indexer.
 * - T-REX + Zeto contracts are deployed once and reused across demo runs.
 *   Only actors, KYC, and balances are reset per run (~1 min vs ~4 min).
 */

import PaladinClient, {
  ZetoFactory, ZetoInstance, PaladinVerifier, ITransactionReceipt,
  TransactionType as PaladinTxType,
} from "@lfdecentralizedtrust/paladin-sdk";
import { checkReceipt, getNodeConnections } from "paladin-example-common";
import { genKeypair } from "maci-crypto";
import * as fs from "fs";
import * as path from "path";
import * as trex from "../src/trex";
import { setArbiter, setEnforcer, setCodec, setTransferFacet, registerZetoKyc } from "../src/helpers";
import { resolveActorIdentity, postComplianceRoot, ActorIdentity } from "../src/identity";
import { ComplianceSmtManager } from "../src/complianceSmt";
import { AuthorityNoteIndexer } from "../src/authorityIndexer";
import { fundActors, fundDomainSubmitKey, defaultFundingList, ACTOR_FUNDING } from "../src/fund-actors";
import { IS_SEPOLIA, SEPOLIA_RPC_URL, POLL_TIMEOUT, LONG_POLL_TIMEOUT } from "../src/sepolia";
import enforcedAbi from "../src/zeto-abis/IZetoEnforced.json";
import { contracts } from "@erc3643org/erc-3643";

const TokenABI = contracts.Token.abi;

// Types matching private-trex-ui/src/types/index.ts
export type Visibility = "PUBLIC" | "PRIVATE";
export type TransactionType =
  | "PUBLIC_TRANSFER" | "SHIELDED_TRANSFER" | "DEPOSIT_TO_POOL"
  | "WITHDRAW_FROM_POOL" | "FREEZE" | "UNFREEZE" | "CLAWBACK"
  | "KYC_UPDATE" | "DISCLOSURE_DECRYPT" | "MINT";
export type TransactionStatus = "PENDING" | "CONFIRMED" | "FAILED";

export interface TransactionRecord {
  txId: string; type: TransactionType; timestamp: string; txHash: string | null;
  status: TransactionStatus; actorName: string; fromName: string | null;
  toName: string | null; amount: number | null; visibility: Visibility; uiSummary: string;
}

export interface ShieldedNote {
  noteId: string; ownerName: string; status: "UNSPENT" | "SPENT" | "LOCKED";
  createdTxHash: string; spentTxHash: string | null; decrypted: DecryptedNotePayload | null;
}

export interface DecryptedNotePayload {
  amount: number; ownerName: string; ownerAddress: string;
  counterpartyName: string | null; counterpartyAddress: string | null; createdAt: string;
}

export interface InvestorStatus { kyc: boolean; frozen: boolean; }

export interface PendingRequest {
  id: string; type: "KYC" | "TRANSFER"; actorName: string;
  toName?: string; amount?: number; transferMode?: Visibility; submittedAt: string;
}

type ActorName = "issuer" | "bank" | "regulator" | "alice" | "bob" | "charlie";
/** Identities that survive server restarts — tied to stable Paladin lookups. */
const PERSISTENT_NAMES: ActorName[] = ["issuer", "bank", "regulator"];
/** Identities regenerated on every demo run (fresh BIP32 keys per runId). */
const INVESTOR_NAMES: ActorName[] = ["alice", "bob", "charlie"];
const DISPLAY_NAMES: Record<ActorName, string> = {
  issuer: "Issuer", bank: "Bank", regulator: "Regulator",
  alice: "Alice", bob: "Bob", charlie: "Charlie",
};
const ROLE_MAP: Record<string, string> = {
  issuer: "bank", bank: "bank", regulator: "regulator",
  alice: "investor", bob: "investor", charlie: "investor",
};

const uid = () => Math.random().toString(36).substring(2, 10);

// No persistence. Every setup() deploys T-REX and Zeto fresh.
// See SUBMIT_KEY_POSTMORTEM.md for why cross-session contract reuse is
// currently deferred (Paladin's internal compliance SMT + missing batch
// circuit wasm + submit-key funding chicken-and-egg).

/**
 * Paladin returns verbose multi-line errors with circuit revert data
 * (e.g., "PD011814: Domain reverted... Error in template Kyc_256 line: 36").
 * Extract a concise, user-facing reason for the toast notification.
 */
function parseError(raw: string): string {
  if (raw.includes("insufficient funds for gas") || raw.includes("INSUFFICIENT_FUNDS"))
    return "Insufficient Sepolia ETH — top up the funder wallet (0xF9D5...aDaC6)";
  if (raw.includes("Insufficient funds") || raw.includes("available=0")) return "Insufficient private balance";
  if (raw.includes("Kyc") || raw.includes("KYC") || raw.includes("CheckSMTProof")) return "Receiver is not KYC verified";
  if (raw.includes("frozen") || raw.includes("revert data")) return "Account is frozen — transfers blocked";
  if (raw.includes("socket hang up")) return "Paladin connection lost — restart the node";
  if (raw.includes("timed out") || raw.includes("TIMEOUT")) return "Transaction timed out — try again";
  const first = raw.split(/[.\n]/)[0].replace(/^PD\d+:\s*/, "").trim();
  return first.length > 80 ? first.substring(0, 77) + "..." : first;
}

async function publicCallWithReceipt(
  paladin: PaladinClient, from: PaladinVerifier,
  to: string, abi: any[], fn: string, args: Record<string, any>,
): Promise<ITransactionReceipt> {
  const id = await paladin.ptx.sendTransaction({
    type: PaladinTxType.PUBLIC, from: from.lookup, to, data: args, function: fn, abi,
  });
  const receipt = await paladin.pollForReceipt(id, POLL_TIMEOUT);
  if (!checkReceipt(receipt)) throw new Error(`Call to ${fn} failed: ${(receipt as any)?.failureMessage}`);
  return receipt!;
}

// ---------------------------------------------------------------------------

export class DemoSession {
  private paladin: PaladinClient;
  private nodeId: string;
  private runId: string;
  private verifiers: Record<string, PaladinVerifier> = {};
  private identities: Record<string, ActorIdentity> = {};
  private trexSuite: trex.TREXSuite | null = null;
  private zeto: ZetoInstance | null = null;
  private deployData: { contracts?: Record<string, string> };
  private arbiterPrivKey = 0n;
  private arbiterPubKey: string[] = [];
  private pubkeyToName = new Map<string, string>();
  private complianceSmt: ComplianceSmtManager;
  private noteIndexer = new AuthorityNoteIndexer();

  private _transactions: TransactionRecord[] = [];
  private _shieldedNotes: Record<string, ShieldedNote[]> = {};
  private _investorStatuses: Record<string, InvestorStatus> = {
    alice: { kyc: false, frozen: false },
    bob: { kyc: false, frozen: false },
    charlie: { kyc: false, frozen: false },
  };
  private _pendingRequests: PendingRequest[] = [];
  setupComplete = false;

  get transactions() { return this._transactions; }
  get shieldedNotes() { return this._shieldedNotes; }
  get investorStatuses(): Readonly<Record<string, InvestorStatus>> { return this._investorStatuses; }
  get pendingRequests() { return this._pendingRequests; }

  constructor() {
    const nc = getNodeConnections(path.resolve(__dirname, "../config.json"));
    if (!nc.length) throw new Error("No Paladin nodes in config.json");
    this.paladin = new PaladinClient(nc[0].clientOptions);
    this.nodeId = nc[0].id;
    this.runId = uid();
    this.complianceSmt = new ComplianceSmtManager();
    const deployPath = path.resolve(__dirname, "../data/deploy.json");
    if (!fs.existsSync(deployPath)) throw new Error("data/deploy.json not found — run start-sepolia.sh first");
    this.deployData = JSON.parse(fs.readFileSync(deployPath, "utf-8"));
  }

  // --- Lifecycle ---

  /**
   * Full fresh deploy. Used by both /setup and /restart (via restart()).
   *
   * Nothing is persisted across sessions — T-REX and Zeto are deployed
   * from scratch every time. See SUBMIT_KEY_POSTMORTEM.md for the full
   * history of why we tried and abandoned a persistence layer (short
   * version: Paladin's internal compliance SMT, the missing batch circuit
   * wasm, and the domain-submit-key funding race all conspired against us,
   * and we chose to defer rather than ship a fragile optimization).
   *
   * Runs in ~12-15 min on Sepolia. Not fast, but reliable.
   */
  async setup(): Promise<void> {
    console.log(`[session] Setup started (runId=${this.runId})`);
    const { issuer, bank } = await this.resolveActors();
    const issuerI = this.identities.issuer, bankI = this.identities.bank;

    if (IS_SEPOLIA && SEPOLIA_RPC_URL) {
      try {
        await fundActors(this.paladin, SEPOLIA_RPC_URL, defaultFundingList(this.nodeId, this.runId));
      } catch (e: any) {
        throw new Error(parseError(e.message));
      }
    }

    // --- T-REX fresh deploy ---
    this.trexSuite = await trex.deployTREX(this.paladin, issuer, "Demo Bond Token", "DBT", {
      token: [issuerI.evmAddress, bankI.evmAddress],
      ir: [issuerI.evmAddress, bankI.evmAddress],
    });
    console.log(`[session] T-REX deployed: ${this.trexSuite.token}`);
    await trex.unpause(this.paladin, issuer, this.trexSuite.token);

    // --- Zeto fresh deploy ---
    const zetoDeployed = await new ZetoFactory(this.paladin, "zeto")
      .newZeto(bank, { tokenName: "Zeto_AnonEncNullifierKycNonRepudiationEnforced" })
      .waitForDeploy(LONG_POLL_TIMEOUT);
    if (!zetoDeployed) throw new Error("Zeto deploy failed");
    this.zeto = zetoDeployed;
    console.log(`[session] Zeto deployed: ${this.zeto.address}`);

    // Wire codec + transfer facet (diamond-lite pattern for AENKNR-E).
    // Both are set-once on the Zeto contract — only safe because Zeto is fresh.
    if (this.deployData.contracts?.codec)
      await setCodec(this.paladin, bank, this.zeto.address, this.deployData.contracts.codec);
    if (this.deployData.contracts?.transferFacet)
      await setTransferFacet(this.paladin, bank, this.zeto.address, this.deployData.contracts.transferFacet);

    await this.zeto.setERC20(bank, { erc20: this.trexSuite.token }).waitForReceipt(POLL_TIMEOUT);

    // Zeto escrow address needs a T-REX IR entry so the pool's deposits and
    // forcedTransfers pass the token's transfer-allowed check.
    await trex.registerIdentity(
      this.paladin, issuer, this.trexSuite.identityRegistry,
      this.trexSuite.dummyIdentity, this.zeto.address, 756,
    );

    // Fresh arbiter keypair held only in memory. The regulator dashboard
    // uses it to decrypt shielded notes emitted during THIS session; there
    // are no cross-session transfers to decrypt, so no persistence needed.
    const kp = genKeypair();
    this.arbiterPrivKey = BigInt(kp.privKey.toString());
    this.arbiterPubKey = [kp.pubKey[0].toString(), kp.pubKey[1].toString()];
    await setArbiter(this.paladin, bank, this.zeto.address, this.arbiterPubKey);
    await setEnforcer(this.paladin, bank, this.zeto.address, bankI.babyjubPubKey);
    console.log("[session] Authorities set");

    // KYC onboarding: register all 5 identities on both T-REX IR and Zeto
    // KYC tree, then post the compliance root. Both are empty on this
    // freshly-deployed contract set.
    await this.complianceSmt.init();
    for (const name of ["issuer", "bank", "regulator", "alice", "bob"] as ActorName[]) {
      const a = this.identities[name];
      await trex.registerIdentity(
        this.paladin, issuer, this.trexSuite.identityRegistry,
        this.trexSuite.dummyIdentity, a.evmAddress, 756,
      );
      await registerZetoKyc(this.paladin, bank, this.zeto.address, a.babyjubPubKey);
      await this.complianceSmt.setStatus(a.babyjubPubKey[0], a.babyjubPubKey[1], 1n);
    }
    await postComplianceRoot(this.paladin, bank, this.zeto.address, this.complianceSmt);
    console.log("[session] KYC complete");

    this._investorStatuses = {
      alice: { kyc: true, frozen: false },
      bob: { kyc: true, frozen: false },
      charlie: { kyc: false, frozen: false },
    };

    // Mint 1,000,000 DBT to bank (public) and deposit 500,000 into the pool.
    const TREASURY = 1_000_000, DEPOSIT = TREASURY / 2;
    await trex.mint(this.paladin, issuer, this.trexSuite.token, bankI.evmAddress, TREASURY);
    this.addTx("MINT", "bank", null, null, TREASURY, "PUBLIC", `Minted ${TREASURY.toLocaleString()} DBT to Bank`);

    await trex.approve(this.paladin, bank, this.trexSuite.token, this.zeto.address, DEPOSIT);
    const dr = await this.zeto.deposit(bank, { amount: DEPOSIT }).waitForReceipt(LONG_POLL_TIMEOUT);
    if (!checkReceipt(dr)) throw new Error("Deposit failed");
    this.addTx("DEPOSIT_TO_POOL", "bank", "bank", null, DEPOSIT, "PRIVATE",
      `Deposited ${DEPOSIT.toLocaleString()} DBT to Zeto pool`, dr?.transactionHash);

    // Best-effort fund the per-Zeto domain submit key. The row may not
    // exist yet (see SUBMIT_KEY_POSTMORTEM.md §3.2). When it's missing,
    // we log a warning and let Paladin allocate it lazily on the first
    // private transfer. On machines with leftover ETH in the relevant
    // BIP32 slot from previous sessions, this just works.
    if (IS_SEPOLIA && SEPOLIA_RPC_URL) {
      try {
        await fundDomainSubmitKey(SEPOLIA_RPC_URL, this.zeto.address);
      } catch (e: any) {
        console.warn(`[session] Domain submit key not yet funded (${e.message}). First private transfer may need manual top-up.`);
      }
    }

    this.setupComplete = true;
    console.log("[session] Setup complete");
  }

  /**
   * Restart the demo: redeploys T-REX and Zeto, swaps in fresh
   * alice/bob/charlie. Internally delegates to setup() after resetting
   * in-memory state, so there is exactly one on-chain setup code path.
   *
   * Takes ~12-15 min on Sepolia. Slow but reliable — see
   * SUBMIT_KEY_POSTMORTEM.md for why we don't currently try to reuse
   * contracts across sessions.
   */
  async restart(): Promise<void> {
    if (!this.setupComplete) {
      throw new Error("Cannot restart — no active session. Run /api/setup first.");
    }
    const prevRunId = this.runId;
    console.log(`[session] Restart started (prev runId=${prevRunId})`);

    // Reset everything the setup() path will rebuild. Notably:
    //  - runId → new investor identifiers
    //  - zeto / arbiter → fresh per-session deployment
    //  - compliance SMT → rebuilt from scratch against the new Zeto
    //  - UI state → cleared so the dashboard starts clean
    this.runId = uid();
    this.zeto = null;
    this.arbiterPrivKey = 0n;
    this.arbiterPubKey = [];
    this.complianceSmt = new ComplianceSmtManager();
    this._transactions = [];
    this._shieldedNotes = {};
    this._pendingRequests = [];
    this.noteIndexer = new AuthorityNoteIndexer();
    this._investorStatuses = {
      alice: { kyc: false, frozen: false },
      bob: { kyc: false, frozen: false },
      charlie: { kyc: false, frozen: false },
    };
    this.setupComplete = false;

    await this.setup();
    console.log(`[session] Restart complete (prev=${prevRunId}, new=${this.runId})`);
  }

  getState() {
    return {
      setupComplete: this.setupComplete,
      runId: this.runId,
      actors: Object.fromEntries(Object.entries(this.identities).map(([name, id]) => [name, {
        name, displayName: DISPLAY_NAMES[name as ActorName],
        evmAddress: id.evmAddress, babyjubPubKey: id.babyjubPubKey,
        nodeId: 1, role: ROLE_MAP[name],
      }])),
      contracts: this.trexSuite && this.zeto ? { trex: { ...this.trexSuite }, zeto: { address: this.zeto.address } } : null,
      balances: {},
      investorStatuses: { ...this._investorStatuses },
      transactions: [...this._transactions],
      pendingRequests: [...this._pendingRequests],
      shieldedNotes: { ...this._shieldedNotes },
    };
  }

  async getBalances(): Promise<Record<string, { public: number; private: number }>> {
    if (!this.trexSuite || !this.zeto) return {};
    const result: Record<string, { public: number; private: number }> = {};
    for (const name of [...INVESTOR_NAMES, "bank"] as ActorName[]) {
      const v = this.verifiers[name], id = this.identities[name];
      if (!v || !id) continue;
      const pub = Number(await trex.balanceOf(this.paladin, v, this.trexSuite.token, id.evmAddress));
      const priv = Number((await this.zeto.using(this.paladin).balanceOf(v, { account: v.lookup })).totalBalance);
      result[name] = { public: pub, private: priv };
    }
    return result;
  }

  // --- Actions ---

  async transfer(from: string, to: string, amount: number, mode: Visibility): Promise<TransactionRecord> {
    this.ensureSetup();
    const fromV = this.verifiers[from], toV = this.verifiers[to];
    const toI = this.identities[to];
    if (!fromV || !toV || !toI) throw new Error(`Unknown actor: ${from} or ${to}`);
    const summary = (vis: string) =>
      `${DISPLAY_NAMES[from as ActorName]} sent ${amount.toLocaleString()} DBT to ${DISPLAY_NAMES[to as ActorName]} (${vis})`;

    if (mode === "PUBLIC") {
      const receipt = await publicCallWithReceipt(this.paladin, fromV, this.trexSuite!.token, TokenABI,
        "transfer", { _to: toI.evmAddress, _amount: amount.toString() });
      return this.addTx("PUBLIC_TRANSFER", from, from, to, amount, "PUBLIC", summary("public"), receipt.transactionHash);
    }

    // Pre-check private balance before dispatching to Paladin. Without this,
    // an account whose notes were previously consumed by a clawback can
    // trigger a "state not found" loop inside Paladin's sequencer assembler
    // (the stale commitment hash is still indexed for the owner even after
    // the underlying state has been marked spent). The loop eventually gives
    // up with a confusing message after several seconds. Failing fast here
    // gives the user a clear "Insufficient private balance" instead.
    const senderBal = Number(
      (await this.zeto!.using(this.paladin).balanceOf(fromV, { account: fromV.lookup })).totalBalance,
    );
    if (senderBal < amount) {
      const reason = `Insufficient private balance (have ${senderBal.toLocaleString()}, need ${amount.toLocaleString()})`;
      const tx = this.addTx("SHIELDED_TRANSFER", from, from, to, amount, "PRIVATE", reason, null, "FAILED");
      throw Object.assign(new Error(reason), { transaction: tx });
    }

    const receipt = await this.zeto!.using(this.paladin)
      .transfer(fromV, { transfers: [{ to: toV, amount, data: "0x" }] })
      .waitForReceipt(LONG_POLL_TIMEOUT);
    if (!checkReceipt(receipt)) {
      const reason = parseError((receipt as any)?.failureMessage ?? "Transfer failed");
      const tx = this.addTx("SHIELDED_TRANSFER", from, from, to, amount, "PRIVATE", reason, null, "FAILED");
      throw Object.assign(new Error(reason), { transaction: tx });
    }
    const tx = this.addTx("SHIELDED_TRANSFER", from, from, to, amount, "PRIVATE", summary("private"), receipt?.transactionHash);
    if (receipt?.transactionHash) this.trackNote(receipt.transactionHash, to);
    return tx;
  }

  async approveKyc(actor: string): Promise<void> {
    this.ensureSetup();
    const id = this.requireIdentity(actor);
    if (this._investorStatuses[actor]?.kyc) throw new Error(`${actor} is already KYC'd`);

    await trex.registerIdentity(this.paladin, this.verifiers.issuer, this.trexSuite!.identityRegistry,
      this.trexSuite!.dummyIdentity, id.evmAddress, 756);
    await registerZetoKyc(this.paladin, this.verifiers.bank, this.zeto!.address, id.babyjubPubKey);
    await this.complianceSmt.setStatus(id.babyjubPubKey[0], id.babyjubPubKey[1], 1n);
    await postComplianceRoot(this.paladin, this.verifiers.bank, this.zeto!.address, this.complianceSmt);

    this._investorStatuses[actor] = { ...this._investorStatuses[actor], kyc: true };
    this.addTx("KYC_UPDATE", "bank", null, null, null, "PUBLIC", `KYC approved for ${DISPLAY_NAMES[actor as ActorName]}`);
  }

  async setFrozen(actor: string, frozen: boolean): Promise<void> {
    this.ensureSetup();
    const id = this.requireIdentity(actor);

    await trex.setAddressFrozen(this.paladin, this.verifiers.bank, this.trexSuite!.token, id.evmAddress, frozen);
    await this.complianceSmt.setStatus(id.babyjubPubKey[0], id.babyjubPubKey[1], frozen ? 2n : 1n);
    await postComplianceRoot(this.paladin, this.verifiers.bank, this.zeto!.address, this.complianceSmt);

    this._investorStatuses[actor] = { ...this._investorStatuses[actor], frozen };
    const label = frozen ? "frozen" : "unfrozen";
    this.addTx(frozen ? "FREEZE" : "UNFREEZE", "bank", null, actor, null, "PUBLIC",
      `${DISPLAY_NAMES[actor as ActorName]} account ${label}`);
  }

  async clawback(actor: string): Promise<TransactionRecord> {
    this.ensureSetup();
    this.requireIdentity(actor);
    if (!this._investorStatuses[actor]?.frozen) throw new Error(`${actor} is not frozen — freeze first`);

    const bal = Number((await this.zeto!.using(this.paladin).balanceOf(
      this.verifiers[actor], { account: this.verifiers[actor].lookup })).totalBalance);
    if (bal <= 0) throw new Error(`${actor} has no private balance to claw back`);

    const receipt = await this.zeto!.forcedTransfer(this.verifiers.bank, {
      seizedOwner: this.verifiers[actor].lookup,
      transfers: [{ to: this.verifiers.bank, amount: bal, data: "0x" }],
    }).waitForReceipt(LONG_POLL_TIMEOUT);
    if (!checkReceipt(receipt)) throw new Error(parseError((receipt as any)?.failureMessage ?? "Clawback failed"));

    return this.addTx("CLAWBACK", "bank", actor, "bank", bal, "PRIVATE",
      `Clawback: ${bal.toLocaleString()} DBT seized from ${DISPLAY_NAMES[actor as ActorName]}`, receipt?.transactionHash);
  }

  getNotes(investor: string): ShieldedNote[] { return this._shieldedNotes[investor] ?? []; }

  async decryptNotes(investor: string, noteIds: string[]): Promise<DecryptedNotePayload[]> {
    this.ensureSetup();
    const notes = this._shieldedNotes[investor] ?? [];
    const results: DecryptedNotePayload[] = [];

    for (const noteId of noteIds) {
      const note = notes.find(n => n.noteId === noteId);
      if (!note || note.decrypted) continue;

      const events = await this.paladin.bidx.decodeTransactionEvents(note.createdTxHash, enforcedAbi.abi, "");
      const evt = events?.find((e: { soliditySignature?: string }) =>
        e.soliditySignature?.includes("UTXOTransferNonRepudiationEnforced"));
      if (!evt) continue;

      const data = evt.data as Record<string, unknown>;
      if (!data?.encryptedValuesForArbiter || !data?.ecdhPublicKey || !data?.encryptionNonce) continue;

      const decrypted = this.noteIndexer.decryptTransfer(
        (data.encryptedValuesForArbiter as string[]).map(BigInt),
        this.arbiterPrivKey,
        (data.ecdhPublicKey as string[]).map(BigInt),
        BigInt(data.encryptionNonce as string),
      );

      for (const out of decrypted.outputs) {
        if (out.value === 0n) continue;
        const ownerName = this.pubkeyToName.get(out.ownerPubKey[0]) ?? "unknown";
        if (ownerName !== investor) continue;
        const senderName = this.pubkeyToName.get(decrypted.senderPubKey[0]) ?? "unknown";
        note.decrypted = {
          amount: Number(out.value), ownerName,
          ownerAddress: this.identities[ownerName]?.evmAddress ?? "",
          counterpartyName: senderName,
          counterpartyAddress: this.identities[senderName]?.evmAddress ?? null,
          createdAt: new Date().toISOString(),
        };
        results.push(note.decrypted);
      }
    }
    return results;
  }

  // --- Requests ---

  submitRequest(type: "KYC" | "TRANSFER", actor: string, details?: { to?: string; amount?: number; mode?: Visibility }): PendingRequest {
    const req: PendingRequest = {
      id: uid(), type, actorName: actor,
      toName: details?.to, amount: details?.amount, transferMode: details?.mode,
      submittedAt: new Date().toISOString(),
    };
    this._pendingRequests.push(req);
    return req;
  }

  async approveRequest(requestId: string): Promise<TransactionRecord | void> {
    const idx = this._pendingRequests.findIndex(r => r.id === requestId);
    if (idx === -1) throw new Error(`Request ${requestId} not found`);
    const req = this._pendingRequests.splice(idx, 1)[0];

    if (req.type === "KYC") return void await this.approveKyc(req.actorName);
    if (req.type === "TRANSFER" && req.amount && req.transferMode)
      return this.transfer("bank", req.toName ?? req.actorName, req.amount, req.transferMode);
  }

  rejectRequest(requestId: string): void {
    const idx = this._pendingRequests.findIndex(r => r.id === requestId);
    if (idx === -1) throw new Error(`Request ${requestId} not found`);
    this._pendingRequests.splice(idx, 1);
  }

  // --- Private ---

  private ensureSetup(): void {
    if (!this.setupComplete || !this.trexSuite || !this.zeto)
      throw new Error("Session not set up — call POST /api/setup first");
  }

  private requireIdentity(actor: string): ActorIdentity {
    const id = this.identities[actor];
    if (!id) throw new Error(`Unknown actor: ${actor}`);
    return id;
  }

  /**
   * Resolve all six actors.
   *
   * bank/issuer/regulator use stable identifiers (no runId suffix), so their
   * BIP32 slots — and therefore their EVM addresses and BabyJub keys —
   * survive server restarts. This is what lets Zeto's set-once enforcer stay
   * valid across runs: the enforcer is the bank's babyjub key, and the bank
   * is the same identity forever.
   *
   * alice/bob/charlie are scoped to the current runId, so every demo run
   * produces fresh investor identities.
   */
  private async resolveActors() {
    for (const name of PERSISTENT_NAMES) {
      const v = this.paladin.getVerifiers(`${name}@${this.nodeId}`)[0];
      this.verifiers[name] = v;
      this.identities[name] = await resolveActorIdentity(DISPLAY_NAMES[name], v);
    }
    for (const name of INVESTOR_NAMES) {
      const v = this.paladin.getVerifiers(`${name}-${this.runId}@${this.nodeId}`)[0];
      this.verifiers[name] = v;
      this.identities[name] = await resolveActorIdentity(DISPLAY_NAMES[name], v);
    }
    this.pubkeyToName.clear();
    for (const [name, id] of Object.entries(this.identities))
      this.pubkeyToName.set(id.babyjubPubKey[0], name);
    console.log("[session] Actors resolved");
    return { issuer: this.verifiers.issuer, bank: this.verifiers.bank };
  }

  private addTx(
    type: TransactionType, actor: string, from: string | null, to: string | null,
    amount: number | null, visibility: Visibility, summary: string,
    hash?: string | null, status?: TransactionStatus,
  ): TransactionRecord {
    const tx: TransactionRecord = {
      txId: uid(), type, timestamp: new Date().toISOString(),
      txHash: hash ?? null, status: status ?? "CONFIRMED",
      actorName: actor, fromName: from, toName: to,
      amount, visibility, uiSummary: summary,
    };
    this._transactions.unshift(tx);
    return tx;
  }

  private trackNote(txHash: string, recipient: string): void {
    const notes = this._shieldedNotes[recipient] ?? [];
    notes.push({
      noteId: txHash.substring(0, 18), ownerName: recipient, status: "UNSPENT",
      createdTxHash: txHash, spentTxHash: null, decrypted: null,
    });
    this._shieldedNotes[recipient] = notes;
  }
}
