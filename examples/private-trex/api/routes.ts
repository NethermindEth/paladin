/*
 * API route handlers.
 *
 * Access tiers:
 *   READ  — anyone with API key (audience can view live state)
 *   WRITE — requires X-Session-Token header (only the presenter)
 *   ADMIN — requires X-Session-Token + used for setup/reset
 */

import { Router, Request, Response, NextFunction } from "express";
import crypto from "crypto";
import { DemoSession } from "./session";

type AsyncHandler = (req: Request, res: Response, next: NextFunction) => Promise<void>;
const wrap = (fn: AsyncHandler): AsyncHandler => (req, res, next) => fn(req, res, next).catch(next);

let session: DemoSession | null = null;
let sessionToken: string | null = null;
let warmupInFlight: Promise<void> | null = null;

/**
 * Called from server.ts on startup when a persisted session file is found.
 * Constructs a DemoSession and runs setup() — which takes the reuse path
 * automatically because the file exists. Idempotent + safe to await from
 * multiple places.
 */
export function autoWarmIfPersisted(): Promise<void> {
  if (warmupInFlight) return warmupInFlight;
  const s = new DemoSession();
  session = s;
  warmupInFlight = s.setup()
    .then(
      () => { console.log("[api] Auto-warm complete — session ready"); },
      (err) => { session = null; throw err; },
    )
    .finally(() => { warmupInFlight = null; });
  return warmupInFlight;
}

function getSession(): DemoSession {
  if (!session?.setupComplete) throw Object.assign(new Error("No active session — call POST /api/setup first"), { status: 400 });
  return session;
}

/**
 * Reject write/admin requests from anyone who doesn't hold the session token.
 * Returns 423 (Locked), not 401 — the caller IS authenticated (API key is valid)
 * but the resource is locked by an active demo session they don't own.
 */
function requireToken(req: Request, res: Response, next: NextFunction): void {
  if (!sessionToken) return next();
  if (req.headers["x-session-token"] === sessionToken) return next();
  res.status(423).json({ error: "Demo in progress. Only the presenter can perform this action." });
}

const router = Router();

// --- READ ---

router.get("/health", (_req, res) => {
  res.json({
    status: "ok",
    sessionActive: session?.setupComplete ?? false,
    warming: !!warmupInFlight && !session?.setupComplete,
    locked: !!sessionToken,
  });
});

router.get("/state", wrap(async (_req, res) => {
  const balances = await getSession().getBalances();
  res.json({ ...getSession().getState(), balances });
}));

router.get("/notes/:investor", wrap(async (req, res) => {
  res.json({ notes: getSession().getNotes(req.params.investor as string) });
}));

// --- WRITE ---

router.post("/transfer", requireToken, wrap(async (req, res) => {
  const s = getSession();
  const { from, to, amount, mode } = req.body;
  if (!from || !to || !amount || !mode) { res.status(400).json({ error: "Required: from, to, amount, mode" }); return; }
  try {
    const tx = await s.transfer(from, to, Number(amount), mode);
    res.json({ success: true, transaction: tx, balances: await s.getBalances() });
  } catch (err: any) {
    res.json({ success: false, error: err.message, transaction: err.transaction ?? null, balances: await s.getBalances() });
  }
}));

router.post("/request", requireToken, wrap(async (req, res) => {
  const s = getSession();
  const { type, actor, to, amount, mode } = req.body;
  if (!type || !actor) { res.status(400).json({ error: "Required: type, actor" }); return; }
  const request = s.submitRequest(type, actor, { to, amount: amount ? Number(amount) : undefined, mode });
  res.json({ request, pendingRequests: s.pendingRequests });
}));

router.post("/request/:id/approve", requireToken, wrap(async (req, res) => {
  const s = getSession();
  const result = await s.approveRequest(req.params.id as string);
  res.json({ success: true, transaction: result ?? null, investorStatuses: s.investorStatuses, pendingRequests: s.pendingRequests, balances: await s.getBalances() });
}));

router.post("/request/:id/reject", requireToken, wrap(async (req, res) => {
  getSession().rejectRequest(req.params.id as string);
  res.json({ pendingRequests: getSession().pendingRequests });
}));

router.post("/kyc/:actor", requireToken, wrap(async (req, res) => {
  const s = getSession();
  await s.approveKyc(req.params.actor as string);
  res.json({ success: true, investorStatuses: s.investorStatuses, balances: await s.getBalances() });
}));

router.post("/freeze/:actor", requireToken, wrap(async (req, res) => {
  const s = getSession();
  const actor = req.params.actor as string;
  await s.setFrozen(actor, !s.investorStatuses[actor]?.frozen);
  res.json({ success: true, investorStatuses: s.investorStatuses });
}));

router.post("/clawback/:actor", requireToken, wrap(async (req, res) => {
  const s = getSession();
  const tx = await s.clawback(req.params.actor as string);
  res.json({ success: true, transaction: tx, balances: await s.getBalances(), investorStatuses: s.investorStatuses });
}));

router.post("/decrypt", requireToken, wrap(async (req, res) => {
  const s = getSession();
  const { investor, noteIds } = req.body;
  if (!investor || !noteIds?.length) { res.status(400).json({ error: "Required: investor, noteIds" }); return; }
  const decrypted = await s.decryptNotes(investor, noteIds);
  res.json({ decrypted, notes: s.getNotes(investor) });
}));

router.post("/add-investor", requireToken, wrap(async (_req, res) => {
  const s = getSession();
  const { name, displayName } = await s.addInvestor();
  res.json({
    success: true, name, displayName,
    actors: s.getActors(),
    investorStatuses: s.investorStatuses,
    balances: await s.getBalances(),
  });
}));

router.post("/remove-investor/:name", requireToken, wrap(async (req, res) => {
  const s = getSession();
  s.removeInvestor(req.params.name as string);
  res.json({
    success: true,
    actors: s.getActors(),
    investorStatuses: s.investorStatuses,
    balances: await s.getBalances(),
  });
}));

// --- ADMIN ---

/**
 * Pre-warm. Auto-branches inside DemoSession.setup():
 *   - first ever call: full deploy (~12-15 min on Sepolia)
 *   - subsequent calls with intact Paladin DB: reuse path (~2-3 min)
 *   - called while server-startup auto-warm is still running: joins that
 *     in-flight warmup instead of starting a second one.
 */
router.post("/setup", wrap(async (_req, res) => {
  // Join an in-flight auto-warm rather than starting a second one.
  if (warmupInFlight) await warmupInFlight;

  // Already warmed? Just return the state — setup() is idempotent but
  // re-running it would churn on-chain for no benefit.
  if (!session?.setupComplete) {
    session = new DemoSession();
    warmupInFlight = session.setup().finally(() => { warmupInFlight = null; });
    await warmupInFlight;
  }
  sessionToken = crypto.randomBytes(16).toString("hex");
  res.json({ ...session!.getState(), balances: await session!.getBalances(), sessionToken });
}));

/**
 * "Start Demo" entry point — claim presenter role on the pre-warmed session.
 * Issues a new token, invalidating any previous presenter's control.
 * Returns the existing session state (fast, no on-chain work).
 */
router.post("/start", wrap(async (_req, res) => {
  if (!session?.setupComplete) {
    res.status(400).json({ error: "No active session — run setup from the CLI first" });
    return;
  }
  sessionToken = crypto.randomBytes(16).toString("hex");
  res.json({ ...session.getState(), balances: await session.getBalances(), sessionToken });
}));

/**
 * Restart the demo without redeploying contracts.
 * Reclaims tokens from investors, normalizes bank to 500K/500K, creates fresh
 * alice/bob/charlie, clears UI state. ~2-4 min on Sepolia.
 */
router.post("/restart", requireToken, wrap(async (_req, res) => {
  if (!session?.setupComplete) {
    res.status(400).json({ error: "No active session — run setup from the CLI first" });
    return;
  }
  await session.restart();
  sessionToken = crypto.randomBytes(16).toString("hex");
  res.json({ ...session.getState(), balances: await session.getBalances(), sessionToken });
}));

export default router;
