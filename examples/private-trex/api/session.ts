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
import {
  AuthorityLedger, AuthorityNoteIndexer, ProofBundle,
  ingestTransferEvent, ingestForcedTransferEvent,
  TransferEventPayload, ForcedTransferEventPayload,
} from "../src/authorityIndexer";
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
  proofBundle: ProofBundle | null;
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
/** Names for dynamically-added investors (beyond alice/bob/charlie). */
const DYNAMIC_NAMES = ["dave", "eve", "frank", "grace", "heidi", "ivan", "judy", "karl", "liam", "mia"];

const uid = () => Math.random().toString(36).substring(2, 10);
const SESSION_FILE = path.resolve(__dirname, "../data/session.json");

function withTimeout<T>(promise: Promise<T>, ms: number, label: string): Promise<T> {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`${label} timed out after ${Math.round(ms / 1000)}s. Please try again.`)), ms);
    promise.then(
      (v) => { clearTimeout(timer); resolve(v); },
      (e) => { clearTimeout(timer); reject(e); },
    );
  });
}

const TRANSFER_TIMEOUT = 180_000;

/**
 * After posting a compliance root on-chain, Paladin's block indexer needs
 * to process the event before the updated SMT is available for proof
 * generation. On Sepolia (~12s blocks), we wait for 2 blocks to ensure
 * the indexer has caught up. Without this, transfers immediately after
 * KYC/freeze fail with "Failed to query the smt DB for leaf node".
 */
const INDEXER_SETTLE_MS = IS_SEPOLIA ? 30_000 : 2_000;
async function waitForIndexerSettle(): Promise<void> {
  if (INDEXER_SETTLE_MS > 0) {
    console.log(`[session] Waiting ${INDEXER_SETTLE_MS / 1000}s for block indexer to settle...`);
    await new Promise((r) => setTimeout(r, INDEXER_SETTLE_MS));
  }
}


// Session persistence — survives API server restarts without redeploying


interface PersistedSession {
  runId: string;
  trexSuite: trex.TREXSuite;
  zetoAddress: string;
  arbiterPrivKey: string;
  arbiterPubKey: string[];
  investorStatuses: Record<string, InvestorStatus>;
  dynamicInvestors: string[];
  complianceLeaves: Array<{ x: string; y: string; status: string }>;
}

function saveSessionFile(data: PersistedSession): void {
  const dir = path.dirname(SESSION_FILE);
  if (!fs.existsSync(dir)) fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(SESSION_FILE, JSON.stringify(data, null, 2));
  console.log("[session] State persisted to disk");
}

function loadSessionFile(): PersistedSession | null {
  if (!fs.existsSync(SESSION_FILE)) return null;
  try {
    return JSON.parse(fs.readFileSync(SESSION_FILE, "utf-8"));
  } catch {
    console.warn("[session] Corrupt session file, ignoring");
    return null;
  }
}


// Paladin health check — retry with backoff on connection failure


async function ensurePaladinReady(paladin: PaladinClient, maxAttempts = 5): Promise<void> {
  for (let i = 1; i <= maxAttempts; i++) {
    try {
      await paladin.ptx.getTransactionReceipt("00000000-0000-0000-0000-000000000000");
      return; // connected — the 404/null response is fine, we just need the RPC to respond
    } catch {
      if (i === maxAttempts) throw new Error("Paladin node not responding after multiple retries");
      const delay = Math.min(2000 * i, 10000);
      console.warn(`[session] Paladin not ready (attempt ${i}/${maxAttempts}), retrying in ${delay}ms...`);
      await new Promise((r) => setTimeout(r, delay));
    }
  }
}

/**
 * Paladin returns verbose multi-line errors with circuit revert data
 * (e.g., "PD011814: Domain reverted... Error in template Kyc_256 line: 36").
 * Extract a concise, user-facing reason for the toast notification.
 */
function parseError(raw: string): string {
  if (raw.includes("insufficient funds for gas") || raw.includes("INSUFFICIENT_FUNDS"))
    return "Insufficient gas funds. Contact the operator.";
  if (raw.includes("Insufficient funds") || raw.includes("available=0")) return "Insufficient private balance.";
  if (raw.includes("Kyc") || raw.includes("KYC") || raw.includes("CheckSMTProof")) return "Receiver is not KYC verified.";
  if (raw.includes("frozen")) return "Account is frozen. Transfers blocked.";
  if (raw.includes("Invalid proof")) return "Transaction reverted on-chain. Verify freeze statuses and retry.";
  if (raw.includes("Unable to decode revert data")) return "Transaction reverted on-chain.";
  if (raw.includes("socket hang up")) return "Paladin node connection lost. Please try again.";
  if (raw.includes("timed out") || raw.includes("TIMEOUT")) return "Transaction timed out. Please try again.";
  const first = raw.split(/[.\n]/)[0].replace(/^PD\d+:\s*/, "").trim();
  if (first.length === 0) return "Transaction failed. Please try again.";
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
  private ledger = new AuthorityLedger();
  // Memoised decryption results keyed by (investor, commitment) — populated
  // on demand by decryptNotes() and cleared on session restart.
  private _decryptedCache = new Map<string, DecryptedNotePayload>();
  private _dynamicInvestors: string[] = [];

  private _transactions: TransactionRecord[] = [];
  private _investorStatuses: Record<string, InvestorStatus> = {
    alice: { kyc: false, frozen: false },
    bob: { kyc: false, frozen: false },
    charlie: { kyc: false, frozen: false },
  };
  private _pendingRequests: PendingRequest[] = [];
  setupComplete = false;

  loadPersistedSession(): PersistedSession | null { return loadSessionFile(); }

  get transactions() { return this._transactions; }
  get shieldedNotes() {
    const out: Record<string, ShieldedNote[]> = {};
    for (const [name] of Object.entries(this.identities)) out[name] = this.getNotes(name);
    return out;
  }
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


  /**
   * Full fresh deploy (~12 min on Sepolia). Deploys T-REX + Zeto, registers
   * actors, posts compliance root, mints tokens, deposits to the pool.
   * Persists to data/session.json on completion so subsequent API server
   * restarts use the fast restore() path instead.
   */
  async setup(): Promise<void> {
    console.log(`[session] Setup started (runId=${this.runId})`);
    const { issuer, bank } = await this.resolveActors();
    const issuerI = this.identities.issuer, bankI = this.identities.bank;

    if (IS_SEPOLIA && SEPOLIA_RPC_URL) {
      try {
        await fundActors(this.paladin, SEPOLIA_RPC_URL, defaultFundingList(this.nodeId, this.runId));
      } catch (e: unknown) {
        throw new Error(parseError(e instanceof Error ? e.message : String(e)));
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
    await waitForIndexerSettle();
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
    const dr = await withTimeout(
      this.zeto.deposit(bank, { amount: DEPOSIT }).waitForReceipt(LONG_POLL_TIMEOUT),
      TRANSFER_TIMEOUT,
      "Deposit to pool",
    );
    if (!checkReceipt(dr)) throw new Error("Deposit failed");
    this.addTx("DEPOSIT_TO_POOL", "bank", "bank", null, DEPOSIT, "PRIVATE",
      `Deposited ${DEPOSIT.toLocaleString()} DBT to Zeto pool`, dr?.transactionHash);

    // Wait for the deposit's UTXO output to be indexed by Paladin.
    // Without this, the first private transfer after setup fails with
    // "Failed to query the smt DB for leaf node" because the bank's
    // coins haven't been added to the UTXO tree yet.
    await waitForIndexerSettle();

    // Best-effort fund the per-Zeto domain submit key. The row may not
    // exist yet (see SUBMIT_KEY_POSTMORTEM.md §3.2). When it's missing,
    // we log a warning and let Paladin allocate it lazily on the first
    // private transfer. On machines with leftover ETH in the relevant
    // BIP32 slot from previous sessions, this just works.
    if (IS_SEPOLIA && SEPOLIA_RPC_URL) {
      try {
        await fundDomainSubmitKey(SEPOLIA_RPC_URL, this.zeto.address);
      } catch (e: unknown) {
        console.warn(`[session] Domain submit key not yet funded (${e instanceof Error ? e.message : e}). First private transfer may need manual top-up.`);
      }
    }

    this.setupComplete = true;
    this.persist();
    console.log("[session] Setup complete");
  }

  /**
   * Restore a session from disk. Skips all contract deployment — only
   * resolves actors and rebuilds in-memory state from the persisted file.
   * Takes ~5 seconds instead of ~12 minutes.
   */
  async restore(saved: PersistedSession): Promise<void> {
    console.log(`[session] Restoring session (runId=${saved.runId})`);
    this.runId = saved.runId;

    await ensurePaladinReady(this.paladin);
    await this.resolveActors();

    // Restore dynamic investors
    this._dynamicInvestors = saved.dynamicInvestors;
    for (const name of saved.dynamicInvestors) {
      const identifier = `${name}-${this.runId}@${this.nodeId}`;
      const v = this.paladin.getVerifiers(identifier)[0];
      this.verifiers[name] = v;
      this.identities[name] = await resolveActorIdentity(this.getDisplayName(name), v);
      this.pubkeyToName.set(this.identities[name].babyjubPubKey[0], name);
    }

    // Restore T-REX suite
    this.trexSuite = saved.trexSuite;

    // Restore Zeto — reconnect to existing deployed contract
    this.zeto = new ZetoInstance(this.paladin, saved.zetoAddress);

    // Restore arbiter key
    this.arbiterPrivKey = BigInt(saved.arbiterPrivKey);
    this.arbiterPubKey = saved.arbiterPubKey;

    // Restore compliance SMT from saved leaves
    await this.complianceSmt.init();
    for (const leaf of saved.complianceLeaves) {
      await this.complianceSmt.setStatus(leaf.x, leaf.y, BigInt(leaf.status));
    }

    // Restore investor statuses
    this._investorStatuses = saved.investorStatuses;

    this.setupComplete = true;

    // Rebuild the UTXO ledger by replaying every confirmed shielded tx from
    // the persisted transaction log. Cheap (~one RPC per tx) and means a
    // restored session has a complete regulator view without extra calls.
    await this.rebuildLedgerFromHistory();

    console.log("[session] Session restored from disk");
  }

  /**
   * Walk every persisted SHIELDED_TRANSFER / CLAWBACK in _transactions and
   * feed its event to the ledger. Idempotent. Invoked only on restore — for
   * fresh sessions the ledger is built incrementally as txs execute.
   */
  private async rebuildLedgerFromHistory(): Promise<void> {
    // Persisted _transactions is not yet populated here (restore() doesn't
    // carry the log on disk — _transactions is cleared on every restart).
    // Nothing to replay unless future work persists the tx log explicitly.
    if (this._transactions.length === 0) {
      console.log("[session] No historical transactions to replay into ledger");
      return;
    }
    // Oldest first: the log is prepended (unshift), so iterate reversed.
    const ordered = [...this._transactions].reverse();
    for (const tx of ordered) {
      if (tx.status !== "CONFIRMED" || !tx.txHash) continue;
      try {
        if (tx.type === "SHIELDED_TRANSFER") {
          await this.indexTransferEvent(tx.txHash);
        } else if (tx.type === "CLAWBACK") {
          // We no longer have the pre-seizure snapshot post-restart. Best-
          // effort: leave seized commitments empty; outputs will still be
          // added. A follow-up audit scan can reconcile.
          await this.indexForcedTransferEvent(tx.txHash, []);
        }
      } catch (e) {
        console.warn(`[session] replay of ${tx.txHash} failed: ${errStr(e)}`);
      }
    }
    console.log(`[session] Ledger replay complete (entries=${this.ledger.size()})`);
  }

  /** Persist session state to disk so API server restarts don't require redeployment. */
  private persist(): void {
    if (!this.trexSuite || !this.zeto) return;
    try {
      saveSessionFile({
        runId: this.runId,
        trexSuite: this.trexSuite,
        zetoAddress: this.zeto.address,
        arbiterPrivKey: this.arbiterPrivKey.toString(),
        arbiterPubKey: this.arbiterPubKey,
        investorStatuses: { ...this._investorStatuses },
        dynamicInvestors: [...this._dynamicInvestors],
        complianceLeaves: this.getComplianceLeaves(),
      });
    } catch (e: unknown) {
      console.warn(`[session] Failed to persist: ${e instanceof Error ? e.message : e}`);
    }
  }

  private getComplianceLeaves(): Array<{ x: string; y: string; status: string }> {
    const leaves: Array<{ x: string; y: string; status: string }> = [];
    for (const [name, id] of Object.entries(this.identities)) {
      // Persistent actors (issuer, bank, regulator) are always KYC'd (status=1)
      // Investors derive status from _investorStatuses
      const investorStatus = this._investorStatuses[name];
      const smtStatus = investorStatus
        ? (investorStatus.frozen ? "2" : investorStatus.kyc ? "1" : "0")
        : "1"; // persistent actors default to KYC'd
      leaves.push({ x: id.babyjubPubKey[0], y: id.babyjubPubKey[1], status: smtStatus });
    }
    return leaves;
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
      throw new Error("Cannot restart. No active session. Run /api/setup first.");
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
    this._pendingRequests = [];
    this.noteIndexer = new AuthorityNoteIndexer();
    this.ledger = new AuthorityLedger();
    this._decryptedCache.clear();
    this._dynamicInvestors = [];
    this._investorStatuses = {
      alice: { kyc: false, frozen: false },
      bob: { kyc: false, frozen: false },
      charlie: { kyc: false, frozen: false },
    };
    this.setupComplete = false;

    // Wipe Paladin's state DB and restart the node to clear all ghost states.
    // Without this, coins from previous sessions pollute the new session's
    // balance queries and coin selection.
    await this.resetPaladinState();

    await this.setup();
    console.log(`[session] Restart complete (prev=${prevRunId}, new=${this.runId})`);
  }

  getActors(): Record<string, { name: string; displayName: string; evmAddress: string; babyjubPubKey: string[]; nodeId: number; role: string }> {
    return Object.fromEntries(Object.entries(this.identities).map(([name, id]) => [name, {
      name, displayName: this.getDisplayName(name),
      evmAddress: id.evmAddress, babyjubPubKey: id.babyjubPubKey,
      nodeId: 1, role: ROLE_MAP[name] ?? "investor",
    }]));
  }

  getState() {
    return {
      setupComplete: this.setupComplete,
      runId: this.runId,
      actors: this.getActors(),
      contracts: this.trexSuite && this.zeto ? { trex: { ...this.trexSuite }, zeto: { address: this.zeto.address } } : null,
      balances: {},
      investorStatuses: { ...this._investorStatuses },
      transactions: [...this._transactions],
      pendingRequests: [...this._pendingRequests],
      shieldedNotes: this.shieldedNotes,
    };
  }

  async getBalances(): Promise<Record<string, { public: number; private: number }>> {
    if (!this.trexSuite || !this.zeto) return {};
    const names = [...INVESTOR_NAMES, ...this._dynamicInvestors, "bank"]
      .filter((n) => this.verifiers[n] && this.identities[n]);
    const entries = await Promise.all(names.map(async (name) => {
      const v = this.verifiers[name], id = this.identities[name];
      const [pub, privResult] = await Promise.all([
        trex.balanceOf(this.paladin, v, this.trexSuite!.token, id.evmAddress),
        this.zeto!.using(this.paladin).balanceOf(v, { account: v.lookup }),
      ]);
      return [name, { public: Number(pub), private: Number(privResult.totalBalance) }] as const;
    }));
    return Object.fromEntries(entries);
  }


  async transfer(from: string, to: string, amount: number, mode: Visibility): Promise<TransactionRecord> {
    this.ensureSetup();
    await ensurePaladinReady(this.paladin);
    const fromV = this.verifiers[from], toV = this.verifiers[to];
    const toI = this.identities[to];
    if (!fromV || !toV || !toI) throw new Error(`Unknown actor: ${from} or ${to}`);

    // Pre-check compliance: recipient must be KYC'd and not frozen. Zeto's
    // enforced-circuit would otherwise reject with a generic "Invalid proof"
    // which is confusing in the UI.
    const toStatus = this._investorStatuses[to];
    if (!toStatus?.kyc) {
      const reason = `${this.getDisplayName(to)} is not KYC verified`;
      const tx = this.addTx("SHIELDED_TRANSFER", from, from, to, amount, mode, reason, null, "FAILED");
      throw Object.assign(new Error(reason), { transaction: tx });
    }
    if (toStatus?.frozen) {
      const reason = `${this.getDisplayName(to)}'s account is frozen. Transfers blocked.`;
      const tx = this.addTx("SHIELDED_TRANSFER", from, from, to, amount, mode, reason, null, "FAILED");
      throw Object.assign(new Error(reason), { transaction: tx });
    }
    const fromStatus = this._investorStatuses[from];
    if (fromStatus?.frozen) {
      const reason = `${this.getDisplayName(from)}'s account is frozen. Cannot send.`;
      const tx = this.addTx("SHIELDED_TRANSFER", from, from, to, amount, mode, reason, null, "FAILED");
      throw Object.assign(new Error(reason), { transaction: tx });
    }

    const summary = (vis: string) =>
      `${this.getDisplayName(from)} sent ${amount.toLocaleString()} DBT to ${this.getDisplayName(to)} (${vis})`;

    if (mode === "PUBLIC") {
      const receipt = await publicCallWithReceipt(this.paladin, fromV, this.trexSuite!.token, TokenABI,
        "transfer", { _to: toI.evmAddress, _amount: amount.toString() });
      return this.addTx("PUBLIC_TRANSFER", from, from, to, amount, "PUBLIC", summary("public"), receipt.transactionHash);
    }

    await this.ensureSubmitKeyFunded();

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

    const xferFuture = this.zeto!.using(this.paladin)
      .transfer(fromV, { transfers: [{ to: toV, amount, data: "0x" }] });
    const xferTxID = await xferFuture.id;
    const receipt = await withTimeout(
      xferFuture.waitForReceipt(LONG_POLL_TIMEOUT),
      TRANSFER_TIMEOUT,
      "Private transfer",
    );
    if (!checkReceipt(receipt)) {
      const reason = parseError((receipt as any)?.failureMessage ?? "Transfer failed");
      const tx = this.addTx("SHIELDED_TRANSFER", from, from, to, amount, "PRIVATE", reason, null, "FAILED");
      throw Object.assign(new Error(reason), { transaction: tx });
    }
    // Same indexer-lag guard as clawback(). Without this, a fast shielded
    // transfer can return stale balances to the UI.
    await this.waitForStateReceipt(xferTxID, "Private transfer");

    const tx = this.addTx("SHIELDED_TRANSFER", from, from, to, amount, "PRIVATE", summary("private"), receipt?.transactionHash);
    if (receipt?.transactionHash) {
      try { await this.indexTransferEvent(receipt.transactionHash); }
      catch (e) { console.warn(`[session] failed to index transfer event ${receipt.transactionHash}: ${errStr(e)}`); }
    }
    this.ensureSubmitKeyFunded().catch(() => {});
    return tx;
  }

  async approveKyc(actor: string): Promise<TransactionRecord> {
    this.ensureSetup();
    await ensurePaladinReady(this.paladin);
    const id = this.requireIdentity(actor);
    if (this._investorStatuses[actor]?.kyc) throw new Error(`${this.getDisplayName(actor)} is already KYC verified.`);

    await trex.registerIdentity(this.paladin, this.verifiers.issuer, this.trexSuite!.identityRegistry,
      this.trexSuite!.dummyIdentity, id.evmAddress, 756);
    await registerZetoKyc(this.paladin, this.verifiers.bank, this.zeto!.address, id.babyjubPubKey);
    await this.complianceSmt.setStatus(id.babyjubPubKey[0], id.babyjubPubKey[1], 1n);
    const receipt = await postComplianceRoot(this.paladin, this.verifiers.bank, this.zeto!.address, this.complianceSmt);
    await waitForIndexerSettle();

    this._investorStatuses[actor] = { ...this._investorStatuses[actor], kyc: true };
    const tx = this.addTx("KYC_UPDATE", "bank", null, null, null, "PUBLIC",
      `KYC approved for ${this.getDisplayName(actor)}`, receipt.transactionHash);
    this.persist();
    return tx;
  }

  async setFrozen(actor: string, frozen: boolean): Promise<TransactionRecord> {
    this.ensureSetup();
    await ensurePaladinReady(this.paladin);
    const id = this.requireIdentity(actor);

    await trex.setAddressFrozen(this.paladin, this.verifiers.bank, this.trexSuite!.token, id.evmAddress, frozen);
    await this.complianceSmt.setStatus(id.babyjubPubKey[0], id.babyjubPubKey[1], frozen ? 2n : 1n);
    const receipt = await postComplianceRoot(this.paladin, this.verifiers.bank, this.zeto!.address, this.complianceSmt);
    await waitForIndexerSettle();

    this._investorStatuses[actor] = { ...this._investorStatuses[actor], frozen };
    const label = frozen ? "frozen" : "unfrozen";
    const tx = this.addTx(frozen ? "FREEZE" : "UNFREEZE", "bank", null, actor, null, "PUBLIC",
      `${this.getDisplayName(actor)}'s account ${label}`, receipt.transactionHash);
    this.persist();
    return tx;
  }

  async clawback(actor: string): Promise<TransactionRecord> {
    this.ensureSetup();
    await ensurePaladinReady(this.paladin);
    await this.ensureSubmitKeyFunded();
    this.requireIdentity(actor);
    if (!this._investorStatuses[actor]?.frozen) throw new Error(`${this.getDisplayName(actor)} is not frozen. Freeze the account first.`);

    const bal = Number((await this.zeto!.using(this.paladin).balanceOf(
      this.verifiers[actor], { account: this.verifiers[actor].lookup })).totalBalance);
    if (bal <= 0) throw new Error(`${this.getDisplayName(actor)} has no private balance to claw back.`);

    // Snapshot the seized owner's live UTXOs before dispatch — the enforcer
    // can't derive owner nullifiers from the event plaintext, so we rely on
    // this pre-call snapshot to mark them spent post-receipt.
    const actorId = this.identities[actor];
    const seizedCommitments = actorId
      ? this.ledger.getLiveForOwner(actorId.babyjubPubKey[0], actorId.babyjubPubKey[1]).map(e => e.commitment)
      : [];

    // Every currently-frozen identity other than the seized owner must be
    // declared so Paladin's prover applies FROZEN status to each of them when
    // reconstructing the compliance tree. Without this the root diverges from
    // the on-chain compliance root and the circuit rejects with "Invalid
    // proof" — see smtProofForForcedTransferCompliance.
    const frozenAccounts = Object.entries(this._investorStatuses)
      .filter(([name, status]) => status.frozen && name !== actor && !!this.verifiers[name])
      .map(([name]) => this.verifiers[name].lookup);

    const txFuture = this.zeto!.forcedTransfer(this.verifiers.bank, {
      seizedOwner: this.verifiers[actor].lookup,
      transfers: [{ to: this.verifiers.bank, amount: bal, data: "0x" }],
      frozenAccounts,
    });
    const txID = await txFuture.id;
    const receipt = await withTimeout(
      txFuture.waitForReceipt(LONG_POLL_TIMEOUT),
      TRANSFER_TIMEOUT,
      "Clawback",
    );
    if (!checkReceipt(receipt)) throw new Error(parseError((receipt as any)?.failureMessage ?? "Clawback failed"));

    // waitForReceipt returns as soon as the chain confirms the tx, but the
    // Zeto domain plugin's HandleEventBatch (which applies SpentStates /
    // ConfirmedStates to Paladin's state store) runs asynchronously off the
    // block indexer. Without waiting for that to land, getBalances() below
    // returns the pre-seizure balance and the UI leaves the Clawback button
    // clickable. Poll ptx_getStateReceipt for THIS transaction until the
    // domain reports the Spent/Confirmed transitions.
    await this.waitForStateReceipt(txID, "Clawback");

    if (receipt?.transactionHash) {
      try { await this.indexForcedTransferEvent(receipt.transactionHash, seizedCommitments); }
      catch (e) { console.warn(`[session] failed to index forced transfer event ${receipt.transactionHash}: ${errStr(e)}`); }
    }

    return this.addTx("CLAWBACK", "bank", actor, "bank", bal, "PRIVATE",
      `Clawback: ${bal.toLocaleString()} DBT seized from ${this.getDisplayName(actor)}`, receipt?.transactionHash);
  }

  /**
   * Poll ptx_getStateReceipt for a specific Paladin transaction until the
   * domain plugin's HandleEventBatch has applied its Spent/Confirmed state
   * transitions. Needed after waitForReceipt because waitForReceipt only
   * guarantees chain confirmation, not that the domain state store has
   * caught up — and getBalances() reads that state store.
   */
  private async waitForStateReceipt(txID: string, label: string): Promise<void> {
    const deadline = Date.now() + 30_000;
    const start = Date.now();
    let attempts = 0;
    while (Date.now() < deadline) {
      attempts++;
      try {
        const states = await this.paladin.getStateReceipt(txID);
        // `none: true` means the state manager has not yet seen this tx.
        // Otherwise, any Spent/Confirmed entries mean the domain processed it.
        if (states && states.none !== true &&
            ((states.spent && states.spent.length > 0) ||
             (states.confirmed && states.confirmed.length > 0))) {
          const elapsed = Date.now() - start;
          console.log(`[session] ${label} state receipt indexed for tx ${txID.slice(0, 10)} in ${elapsed}ms (${attempts} poll(s))`);
          return;
        }
      } catch (e: unknown) {
        // getStateReceipt can transiently 404 before the tx is registered.
        // Keep polling.
      }
      await new Promise(r => setTimeout(r, 500));
    }
    console.warn(`[session] ${label} state receipt never landed for tx ${txID.slice(0, 10)} within 30s — indexer may be stuck, response will show stale balances`);
  }

  private getDisplayName(actor: string): string {
    if (actor in DISPLAY_NAMES) return DISPLAY_NAMES[actor as ActorName];
    return actor.charAt(0).toUpperCase() + actor.slice(1);
  }

  /**
   * Add a fresh un-KYC'd investor on demand. The new investor is:
   *  - Resolved as a Paladin identity (fresh BIP32 key)
   *  - Funded on Sepolia for gas
   *  - NOT registered on T-REX IR or Zeto KYC tree (approveKyc handles both)
   *
   * This lets the presenter demonstrate the KYC approval flow multiple times
   * without a full redeploy.
   */
  async addInvestor(): Promise<{ name: string; displayName: string }> {
    this.ensureSetup();

    const idx = this._dynamicInvestors.length;
    const name = idx < DYNAMIC_NAMES.length
      ? DYNAMIC_NAMES[idx]
      : `investor${idx - DYNAMIC_NAMES.length + 1}`;
    const identifier = `${name}-${this.runId}@${this.nodeId}`;

    const v = this.paladin.getVerifiers(identifier)[0];
    this.verifiers[name] = v;
    this.identities[name] = await resolveActorIdentity(this.getDisplayName(name), v);
    this.pubkeyToName.set(this.identities[name].babyjubPubKey[0], name);

    if (IS_SEPOLIA && SEPOLIA_RPC_URL) {
      await fundActors(this.paladin, SEPOLIA_RPC_URL, [
        { label: name, identifier, amountEth: "0.1" },
      ]);
    }

    this._investorStatuses[name] = { kyc: false, frozen: false };
    this._dynamicInvestors.push(name);
    const displayName = this.getDisplayName(name);
    console.log(`[session] Added investor ${displayName} (${name})`);
    this.persist();
    return { name, displayName };
  }

  /**
   * Remove an investor from the dashboard. Purely cosmetic: on-chain state
   * (T-REX IR registration, Zeto KYC, any held tokens) is immutable and
   * stays forever. This just hides the actor from the UI so the presenter
   * can clean up after demonstrating a flow.
   *
   * Static investors (alice, bob, charlie) cannot be removed.
   */
  removeInvestor(name: string): void {
    this.ensureSetup();
    if ((INVESTOR_NAMES as string[]).includes(name)) {
      throw new Error(`Cannot remove ${name}: static investors cannot be removed`);
    }
    if (!this._dynamicInvestors.includes(name)) {
      throw new Error(`Unknown dynamic investor: ${name}`);
    }

    this._dynamicInvestors = this._dynamicInvestors.filter((n) => n !== name);
    delete this.verifiers[name];
    delete this.identities[name];
    delete this._investorStatuses[name];
    // Ledger entries keyed by pubkey remain in memory but become unreachable
    // via shieldedNotes (no identities[name] to look up); left in place to
    // keep historical audit trail if the name is re-added.
    for (const [key, val] of this.pubkeyToName) {
      if (val === name) { this.pubkeyToName.delete(key); break; }
    }
    console.log(`[session] Removed investor ${this.getDisplayName(name)} (${name})`);
  }

  /** Convert a LedgerEntry to the wire-format ShieldedNote the UI expects. */
  private toShieldedNote(entry: { commitment: string; createdTxHash: string; spentTxHash: string | null }, ownerName: string): ShieldedNote {
    const noteId = entry.commitment.length > 18 ? entry.commitment.substring(0, 18) : entry.commitment;
    const cached = this._decryptedCache.get(`${ownerName}:${entry.commitment}`) ?? null;
    return {
      noteId,
      ownerName,
      status: entry.spentTxHash ? "SPENT" : "UNSPENT",
      createdTxHash: entry.createdTxHash,
      spentTxHash: entry.spentTxHash,
      decrypted: cached,
    };
  }

  getNotes(investor: string): ShieldedNote[] {
    const id = this.identities[investor];
    if (!id) return [];
    const entries = this.ledger.getAllForOwner(id.babyjubPubKey[0], id.babyjubPubKey[1]);
    return entries.map(e => this.toShieldedNote(e, investor));
  }

  async decryptNotes(investor: string, noteIds: string[]): Promise<DecryptedNotePayload[]> {
    this.ensureSetup();
    const id = this.identities[investor];
    if (!id) return [];
    const entries = this.ledger.getAllForOwner(id.babyjubPubKey[0], id.babyjubPubKey[1]);
    const results: DecryptedNotePayload[] = [];

    for (const requested of noteIds) {
      const entry = entries.find(e =>
        (e.commitment.length > 18 ? e.commitment.substring(0, 18) : e.commitment) === requested
        || e.commitment === requested);
      if (!entry) continue;
      const cacheKey = `${investor}:${entry.commitment}`;
      const cached = this._decryptedCache.get(cacheKey);
      if (cached) { results.push(cached); continue; }

      // We already know value/salt/owner from ingestion; build the proof bundle
      // off the on-chain event so the regulator can independently verify.
      const events = await this.paladin.bidx.decodeTransactionEvents(entry.createdTxHash, enforcedAbi.abi, "");
      const evt = events?.find((e: { soliditySignature?: string }) =>
        e.soliditySignature?.includes("UTXOTransferNonRepudiationEnforced")
        || e.soliditySignature?.includes("UTXOForcedTransferEnforced"));
      if (!evt) continue;
      const data = evt.data as Record<string, unknown>;
      if (!data?.encryptedValuesForArbiter || !data?.ecdhPublicKey || !data?.encryptionNonce) continue;

      const ciphertext = (data.encryptedValuesForArbiter as string[]).map(BigInt);
      const ecdhPubKey = (data.ecdhPublicKey as string[]).map(BigInt);
      const nonce = BigInt(data.encryptionNonce as string);
      const onChainOutputs = (data.outputs as string[] ?? []).map(BigInt);

      const { transfer: decrypted, proof } = this.noteIndexer.buildProofBundle(
        ciphertext, this.arbiterPrivKey, ecdhPubKey, nonce, onChainOutputs, entry.createdTxHash,
      );
      const senderName = this.pubkeyToName.get(decrypted.senderPubKey[0]) ?? "unknown";
      const payload: DecryptedNotePayload = {
        amount: Number(entry.value),
        ownerName: investor,
        ownerAddress: id.evmAddress,
        counterpartyName: senderName,
        counterpartyAddress: this.identities[senderName]?.evmAddress ?? null,
        createdAt: entry.createdAt,
        proofBundle: proof,
      };
      this._decryptedCache.set(cacheKey, payload);
      results.push(payload);
    }
    return results;
  }

  /**
   * Fetch the transfer event for the given txHash and feed it to the ledger.
   * Safe to call multiple times — ledger upserts are idempotent.
   */
  private async indexTransferEvent(txHash: string): Promise<void> {
    const events = await this.paladin.bidx.decodeTransactionEvents(txHash, enforcedAbi.abi, "");
    const evt = events?.find((e: { soliditySignature?: string }) =>
      e.soliditySignature?.includes("UTXOTransferNonRepudiationEnforced"));
    if (!evt) return;
    const payload = this.eventToTransferPayload(evt);
    if (!payload) return;
    ingestTransferEvent(this.ledger, this.noteIndexer, payload, this.arbiterPrivKey, txHash, new Date().toISOString());
  }

  private async indexForcedTransferEvent(txHash: string, seizedCommitments: string[]): Promise<void> {
    const events = await this.paladin.bidx.decodeTransactionEvents(txHash, enforcedAbi.abi, "");
    const evt = events?.find((e: { soliditySignature?: string }) =>
      e.soliditySignature?.includes("UTXOForcedTransferEnforced"));
    if (!evt) return;
    const base = this.eventToTransferPayload(evt);
    if (!base) return;
    const payload: ForcedTransferEventPayload = { ...base, spentCommitments: seizedCommitments };
    ingestForcedTransferEvent(this.ledger, this.noteIndexer, payload, this.arbiterPrivKey, txHash, new Date().toISOString());
  }

  private eventToTransferPayload(evt: unknown): TransferEventPayload | null {
    const data = (evt as { data?: Record<string, unknown> }).data;
    if (!data?.encryptedValuesForArbiter || !data?.ecdhPublicKey || !data?.encryptionNonce) return null;
    return {
      encryptedValuesForArbiter: (data.encryptedValuesForArbiter as string[]).map(BigInt),
      ecdhPublicKey: (data.ecdhPublicKey as string[]).map(BigInt),
      encryptionNonce: BigInt(data.encryptionNonce as string),
      outputs: (data.outputs as string[] ?? []).map(BigInt),
    };
  }


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


  /** Ensure the domain submit key has gas before a private transaction. */
  private async ensureSubmitKeyFunded(): Promise<void> {
    if (!IS_SEPOLIA || !SEPOLIA_RPC_URL || !this.zeto) return;
    try {
      await fundDomainSubmitKey(SEPOLIA_RPC_URL, this.zeto.address);
    } catch {
      // Key may not exist yet (first private tx). Log and continue — Paladin
      // creates it lazily and subsequent calls will fund it.
      console.log("[session] Submit key not found yet, will fund after first transfer");
    }
  }

  private async resetPaladinState(): Promise<void> {
    const { execSync } = require("child_process");
    try {
      console.log("[session] Wiping Paladin state DB...");
      execSync('docker exec paladin-postgres psql -U postgres -d paladin_demo -c "DROP SCHEMA public CASCADE; CREATE SCHEMA public;"', { timeout: 10000 });
      console.log("[session] Restarting Paladin node...");
      execSync("docker restart paladin-node1", { timeout: 30000 });
      await ensurePaladinReady(this.paladin, 10);
      console.log("[session] Paladin restarted with clean state");
    } catch (e: unknown) {
      console.warn(`[session] resetPaladinState failed: ${e instanceof Error ? e.message : e}`);
    }
  }

  private ensureSetup(): void {
    if (!this.setupComplete || !this.trexSuite || !this.zeto)
      throw new Error("Session not set up. Call POST /api/setup first.");
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
    // Wipe identity state before re-resolving. Without this a restart()
    // leaks dynamic investors (dave/eve added in the previous session)
    // into this.identities; getActors() then surfaces them in the UI,
    // and their stale pubkeys (derived from the previous runId) will not
    // match anything on the freshly-deployed identity registry, so a
    // later KYC/transfer for them fails with a cryptic error. Clearing
    // here makes resolveActors idempotent regardless of prior state.
    // restore() will re-populate dynamic investors from the persisted
    // list immediately after calling this helper.
    this.verifiers = {};
    this.identities = {};
    this.pubkeyToName.clear();

    for (const name of PERSISTENT_NAMES) {
      const v = this.paladin.getVerifiers(`${name}@${this.nodeId}`)[0];
      this.verifiers[name] = v;
      this.identities[name] = await resolveActorIdentity(this.getDisplayName(name), v);
    }
    for (const name of INVESTOR_NAMES) {
      const v = this.paladin.getVerifiers(`${name}-${this.runId}@${this.nodeId}`)[0];
      this.verifiers[name] = v;
      this.identities[name] = await resolveActorIdentity(this.getDisplayName(name), v);
    }
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

}

function errStr(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}
