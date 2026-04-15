/*
 * Private T-REX API server.
 *
 * Environment:
 *   API_PORT       Port (default: 3001)
 *   API_KEY        Required API key for X-API-Key header
 *   ALCHEMY_API_KEY, WALLET_SEED — loaded from .env by sepolia.ts
 */

import express from "express";
import cors from "cors";
import rateLimit from "express-rate-limit";
import routes, { autoWarmIfPersisted } from "./routes";
import * as dotenv from "dotenv";
import * as path from "path";
import * as fs from "fs";

dotenv.config({ path: path.resolve(__dirname, "../.env") });

const PORT = parseInt(process.env.API_PORT ?? "3001");
const API_KEY = process.env.API_KEY;

const app = express();
app.use(cors());
app.use(express.json());

// Rate limiting — tiered by cost. Read ops are cheap (state lookup). Write ops
// trigger on-chain transactions (~12s each on Sepolia). Admin ops deploy contracts
// (~4 min). Limits prevent accidental DOS during a live demo presentation.
const readLimiter = rateLimit({ windowMs: 60_000, max: 60, standardHeaders: true, legacyHeaders: false, message: { error: "Too many requests" } });
const writeLimiter = rateLimit({ windowMs: 60_000, max: 10, standardHeaders: true, legacyHeaders: false, message: { error: "Too many requests. Please wait a moment." } });
const adminLimiter = rateLimit({ windowMs: 60_000, max: 2, standardHeaders: true, legacyHeaders: false, message: { error: "Setup rate limited. Please wait 1 minute." } });

app.use("/api/health", readLimiter);
app.use("/api/state", readLimiter);
app.use("/api/notes", readLimiter);
app.use("/api/setup", adminLimiter);
app.use("/api/reset", adminLimiter);
// All other POST routes get write limiter (applied as default below)

// API key auth (skip for health)
app.use("/api", (req, res, next) => {
  if (req.path === "/health") return next();
  if (API_KEY && req.headers["x-api-key"] !== API_KEY) {
    res.status(401).json({ error: "Invalid or missing API key" });
    return;
  }
  next();
});

// Apply write limiter to all POST routes not already covered
app.post("/api/:path", writeLimiter);

app.use("/api", routes);

// Error handler — extract meaningful message from Paladin/ethers/axios errors
app.use((err: any, _req: express.Request, res: express.Response, _next: express.NextFunction) => {
  const status = err.status ?? 500;
  // Axios errors from Paladin SDK wrap the real message in response.data
  let message = err.message ?? "Internal server error";
  const rpcError = err.response?.data?.error;
  if (rpcError) message = typeof rpcError === "string" ? rpcError : rpcError.message ?? JSON.stringify(rpcError);
  else if (err.code === "INSUFFICIENT_FUNDS" || message.includes("insufficient funds"))
    message = "Insufficient gas funds. Contact the operator.";
  else if (message.includes("socket hang up") || message.includes("ECONNREFUSED"))
    message = "Paladin node not responding. Please try again.";

  console.error(`[api] Error (${status}): ${message}`);
  if (status === 500) console.error(err.stack ?? err);
  res.status(status).json({ error: message });
});

const SESSION_FILE = path.resolve(__dirname, "../data/session.json");

app.listen(PORT, "0.0.0.0", () => {
  console.log(`[api] Private T-REX API server on http://0.0.0.0:${PORT}`);
  if (API_KEY) console.log(`[api] API key auth enabled`);
  else console.log(`[api] WARNING: No API_KEY set`);
  console.log(`[api] Rate limits: read=60/min, write=10/min, admin=2/min`);

  // Auto-warm in the background if we have a persisted deployment on disk.
  // Setup uses the fast reuse path (~2-3 min on Sepolia) — skips all
  // contract deploys, only registers fresh investors + reconciles balances.
  // The listen callback returns immediately so the server is responsive
  // to /health while warmup runs.
  if (fs.existsSync(SESSION_FILE)) {
    console.log(`[api] Persisted session found, auto-warming (reuse path)...`);
    autoWarmIfPersisted().catch((err) =>
      console.error(`[api] Auto-warm failed — run POST /api/setup manually: ${err.message}`),
    );
  } else {
    console.log(`[api] No persisted session — run POST /api/setup to deploy contracts`);
  }
});
