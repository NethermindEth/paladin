# Private T-REX: Sepolia + UI Roadmap

## Current State (as of 2026-04-09)

Phase 1 (Sepolia) and Phase 2 (repeatable demos) are **complete**. The 17-step CLI demo runs end-to-end on Sepolia with all ZK proofs, compliance enforcement, and regulatory decryption working.

Infrastructure contracts (14 total) are deployed once and reused across all demo runs. Each `./start-sepolia.sh --demo` creates fresh actors, T-REX token, and Zeto pool. Costs ~0.5-0.8 ETH per run on Sepolia.

The UI frontend exists at `private-trex-ui/` with three dashboards (Bank, Investor, Regulator) — fully designed but using mock data. Phase 3 connects it to a real backend.

Key addresses:
- Funder wallet: `0xF9D5dB1a82f22e12E240dD760ED7D731437aDaC6` (m/44'/60'/0')
- ZetoFactory: `0xb57af2992fa895aab1b096fd4196c502dd877669`

---

## Phase 1: Sepolia Testnet Integration [COMPLETE]

### What Was Built

| File | Purpose |
|------|---------|
| `start-sepolia.sh` | Sepolia lifecycle script (Postgres, Paladin, deploy, demo) |
| `paladin-config-base-sepolia.yaml` | Base config with Alchemy URL placeholders |
| `.env.example` | Template for `ALCHEMY_API_KEY`, `WALLET_SEED` |
| `src/sepolia.ts` | Shared network config (timeouts, Etherscan URLs, detection) |
| `src/fund-actors.ts` | BIP32-derived funder wallet, actor funding, domain key funding |
| `src/deploy.ts` | Contract deployment with resume support from `deploy-partial.json` |

### Key Decisions

- **`fromBlock: latest`** in generated config — avoids 15+ min indexer catchup on Alchemy free tier.
- **BIP32 funder at m/44'/60'/0'** — Paladin uses 1-based indices, so 0' never collides. User funds deployer (m/44'/60'/1'), script bootstraps funder automatically.
- **Domain submit keys funded at m/44'/60'/7'-10'/0/0/0** — seed-derived, no DB queries, handles resolution order variance.
- **Poll timeouts**: 120s (standard) / 300s (ZK proof) on Sepolia vs 30s/120s on Besu.
- **Verifier ABIs from `zeto/solidity/artifacts/`** — not from `integration-test/helpers/abis/` (those were stale and caused proof verification failures).

### Verification ✅

- [x] `./start-sepolia.sh` deploys all infrastructure contracts to Sepolia
- [x] Factory address visible on Sepolia Etherscan
- [x] Paladin connects to Sepolia, domain loads
- [x] `./start-sepolia.sh --demo` runs full 17-step flow on Sepolia
- [x] Public T-REX transfers visible on Etherscan (sender, receiver, amount)
- [x] Zeto transfers visible on Etherscan but opaque (nullifiers + commitments only)
- [x] Regulator decrypts shielded transfers, values match expected amounts

---

## Phase 2: Repeatable Demo Runs [COMPLETE]

### Contract Taxonomy

**Deploy once (infrastructure)** — 14 contracts in `data/deploy.json`:

Poseidon libraries, SmtLib, 4 Groth16 verifiers, AENKNRECodec, transfer facet, implementation, ZetoFactory (impl + proxy). Immutable unless circuit/Solidity code changes.

**Deploy per demo run** — fresh state each time:

T-REX suite (6 contracts) + Zeto token instance (via `ZetoFactory.newZeto()`). Each run gets clean token state, empty UTXO tree, empty KYC/compliance SMTs.

### Isolation

Each run uses a random `runId` (e.g., `k7m2p1`) appended to actor names. Different `runId` = different BIP32 derivation paths = completely unrelated addresses. Previous runs' on-chain contracts are abandoned (inert).

### Verification ✅

- [x] `./start-sepolia.sh --demo` twice without redeploying infrastructure
- [x] Second run creates fresh T-REX + Zeto + actors
- [x] `data/deploy.json` guard skips infrastructure redeploy
- [x] `--demo` auto-starts Postgres + Paladin if not running (`ensure_paladin`)

---

## Phase 3: UI Integration (API Server + Frontend)

### Architecture

```
┌─────────────────────────────────────────────────────────┐
│  Frontend (Vercel / localhost:3000)                      │
│  Next.js 16 + Zustand + React Query                     │
│                                                          │
│  TopBar (role switcher, contracts, "New Demo" button)    │
│  ┌──────────┐ ┌──────────────┐ ┌────────────────┐      │
│  │   Bank   │ │   Investor   │ │   Regulator    │      │
│  │Dashboard │ │  Dashboard   │ │  Dashboard     │      │
│  └──────────┘ └──────────────┘ └────────────────┘      │
│       │              │                │                  │
│   Zustand store ← fetch() + X-API-Key header            │
└───────────────────────┬─────────────────────────────────┘
                        │  https://vm:3001/api
┌───────────────────────┼─────────────────────────────────┐
│  API Server (Express) │  private-trex/api/              │
│                       │                                  │
│  ┌────────────────────┴────────────────────────┐        │
│  │  DemoSession (singleton, in-memory)          │        │
│  │                                              │        │
│  │  runId, actors, arbiterPrivKey               │        │
│  │  complianceSmt, trexSuite, zetoInstance       │        │
│  │  transactions[], shieldedNotes[]              │        │
│  │  investorStatuses, balances (cached)          │        │
│  │  pubkeyToName map (for decryption)           │        │
│  └──────────────────────────────────────────────┘        │
│                       │                                  │
│  Imports existing src/ modules directly:                 │
│  trex.ts, helpers.ts, identity.ts,                       │
│  complianceSmt.ts, authorityIndexer.ts                   │
│                       │                                  │
│  PaladinClient → http://127.0.0.1:8548                  │
└───────────────────────┼─────────────────────────────────┘
                        │
                ┌───────┴────────┐
                │  Paladin Node  │
                │  (Docker)      │
                │       ↕        │
                │    Sepolia     │
                └────────────────┘
```

**Why Express, not Next.js API routes:** The demo logic uses Paladin SDK, `maci-crypto`, `circomlibjs`, and an in-memory compliance SMT. These heavyweight Node.js dependencies need to persist in-memory state across requests. Express runs in `private-trex/` and imports `src/` modules directly. Next.js stays a thin frontend.

**Hosting model:** Paladin + API server run on a remote VM (always on). Frontend on Vercel or localhost. API protected with `X-API-Key` header.

### UX Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Setup | **Pre-warmed via CLI** | No live deployment in front of audience. Run `./start-sepolia.sh --demo` before presenting. |
| Transaction wait | **Spinner + Etherscan link** | Show "Submitting to Sepolia..." with immediate Etherscan tx link. Audience sees pending tx on-chain. |
| Request flow | **Request → Approve** | Investors request KYC/transfers. Bank approves. Shows institutional workflow. |
| Note discovery | **Auto-tracked in session** | API records every shielded transfer. Regulator sees them instantly. No on-chain indexing. |
| Reset | **Both CLI and UI button** | CLI: `./start-sepolia.sh --reset`. UI: "New Demo" button in TopBar. Same backend endpoint. |
| Investor transfers | **Full form** | Modal: recipient dropdown, amount, PUBLIC/PRIVATE mode. Shows realistic UX. |
| Clawback | **Clean update + toast** | Balance updates to 0. Toast: "Clawback complete — 5,000 DBT seized." |
| Currency | **Token amount only** | "10,000 DBT". No mocked USD. |

### API Endpoints

```
POST   /api/setup                     Deploy T-REX + Zeto, KYC, mint, deposit (pre-warm)
POST   /api/reset                     New actors + re-KYC + re-mint (reuses contracts)
GET    /api/state                     Full session state for frontend hydration

POST   /api/transfer                  { from, to, amount, mode: "PUBLIC"|"PRIVATE" }
POST   /api/request                   { type: "KYC"|"TRANSFER", actor, ...details }
POST   /api/request/:id/approve       Bank approves a pending request
POST   /api/request/:id/reject        Bank rejects a pending request

POST   /api/kyc/:actor                Direct KYC add (bank-initiated)
POST   /api/freeze/:actor             Toggle freeze/unfreeze
POST   /api/clawback/:actor           ForcedTransfer — seize frozen actor's funds

GET    /api/notes/:investor           Shielded notes for an investor (session-tracked)
POST   /api/decrypt                   { investor, noteIds: [...] }

GET    /api/health                    Server + Paladin connectivity status
```

### `GET /api/state` Response

Single payload that hydrates the entire Zustand store:

```json
{
  "setupComplete": true,
  "actors": {
    "bank": { "name": "bank", "displayName": "Bank", "evmAddress": "0x...", "babyjubPubKey": ["0x...", "0x..."], "role": "bank" }
  },
  "contracts": {
    "trex": { "token": "0x...", "identityRegistry": "0x..." },
    "zeto": { "address": "0x..." }
  },
  "balances": { "bank": { "public": 490000, "private": 955000 } },
  "investorStatuses": { "alice": { "kyc": true, "frozen": false } },
  "transactions": [{ "txId": "...", "type": "MINT", "txHash": "0x...", "status": "CONFIRMED", ... }],
  "pendingRequests": [],
  "shieldedNotes": {
    "alice": [{ "noteId": "...", "txHash": "0x...", "decrypted": null }]
  }
}
```

### `POST /api/transfer` Response

```json
{
  "success": true,
  "txHash": "0x...",
  "transaction": { "txId": "...", "type": "SHIELDED_TRANSFER", ... },
  "balances": { "bank": { "public": 490000, "private": 450000 }, "alice": { ... } }
}
```

On failure:
```json
{
  "success": false,
  "error": "Receiver not KYCed",
  "errorCode": "KYC_REQUIRED"
}
```

### DemoSession

```typescript
interface DemoSession {
  runId: string;
  paladin: PaladinClient;

  // Actors — resolved at setup
  actors: Record<ActorName, { verifier: PaladinVerifier; identity: ActorIdentity }>;

  // Keys — in-memory only, never sent to frontend
  arbiterPrivKey: bigint;
  arbiterPubKey: string[];
  pubkeyToName: Map<string, string>;  // babyjub x-coord → actor name

  // Contract state
  complianceSmt: ComplianceSmtManager;
  trexSuite: TREXSuite;
  zetoInstance: ZetoInstance;
  deployData: DeployData;

  // Session state (sent to frontend via GET /api/state)
  transactions: TransactionRecord[];
  shieldedNotes: Record<string, ShieldedNote[]>;  // auto-tracked per investor
  investorStatuses: Record<string, InvestorStatus>;
  pendingRequests: PendingRequest[];
}
```

### Demo Flow: How Each UI Action Maps to API

**Pre-demo (CLI, before audience arrives):**

```bash
./start-sepolia.sh --start       # Ensures Paladin + Postgres running
curl -X POST vm:3001/api/setup   # Or: npm run api:setup
```

Setup runs Steps 1-6 sequentially. Takes ~3-4 min on Sepolia. Frontend opens to ready state.

**ACT 1 — Public Transfers:**

| Presenter Action | Dashboard | API Call |
|-----------------|-----------|----------|
| Switch to Alice view | TopBar | Client-side only |
| Alice requests 10K from Bank | Investor → "Request Transfer" | `POST /api/request { type: "TRANSFER", actor: "alice", amount: 10000, mode: "PUBLIC" }` |
| Switch to Bank view | TopBar | Client-side only |
| Bank approves request | Bank → Requests → "Approve" | `POST /api/request/:id/approve` |
| Show on Etherscan | Click tx link | Client-side: `etherscanUrl(txHash)` |
| Switch to Alice view | TopBar | Client-side only |
| Alice sends 2K to Bob | Investor → Transfer form | `POST /api/transfer { from: "alice", to: "bob", amount: 2000, mode: "PUBLIC" }` |

**ACT 2 — Shielded Transfers:**

Same `/transfer` endpoint with `mode: "PRIVATE"`. Spinner shows "Generating ZK proof..." with Etherscan link.

**ACT 3 — Regulator Disclosure:**

| Presenter Action | Dashboard | API Call |
|-----------------|-----------|----------|
| Switch to Regulator view | TopBar | Client-side only |
| Select Alice | Regulator → investor list | `GET /api/notes/alice` |
| Select encrypted notes | Checkboxes | Client-side only |
| Click "Decrypt Selected" | Decrypt button | `POST /api/decrypt { investor: "alice", noteIds: [...] }` |

Arbiter private key never leaves the API server.

**ACT 4 — Compliance:**

| Presenter Action | Dashboard | API Call |
|-----------------|-----------|----------|
| Alice → Charlie (fails) | Investor → Transfer form | `POST /api/transfer { from: "alice", to: "charlie", amount: 5000, mode: "PRIVATE" }` → error |
| Switch to Charlie view | TopBar | Client-side only |
| Charlie requests KYC | Investor → "Request KYC" | `POST /api/request { type: "KYC", actor: "charlie" }` |
| Switch to Bank view | TopBar | Client-side only |
| Bank approves KYC | Bank → Requests → "Approve" | `POST /api/request/:id/approve` |
| Alice → Charlie (succeeds) | Investor → Transfer form | `POST /api/transfer { ... }` |
| Bank freezes Charlie | Bank → Investors → "Freeze" | `POST /api/freeze/charlie` |
| Charlie → Bob (fails) | Investor → Transfer form | `POST /api/transfer { ... }` → error |
| Bank claws back | Bank → Investors → "Clawback" | `POST /api/clawback/charlie` |

### Frontend Changes

**Store (`app-store.ts`):**
- Remove all mock data
- Add async actions: `fetchState()`, `executeTransfer()`, `submitRequest()`, `approveRequest()`, `rejectRequest()`, `approveKyc()`, `freezeActor()`, `clawbackActor()`, `fetchNotes()`, `decryptNotes()`, `resetDemo()`
- Add `loading: Record<string, boolean>` and `error: string | null` state
- Add `apiUrl` and `apiKey` config (from env)

**Bank Dashboard:**
- Wire "Confirm & Send" in TransferView to `executeTransfer()`
- Wire "Approve"/"Reject" in PendingRequests to `approveRequest()`/`rejectRequest()`
- Add action menu on investor rows: KYC toggle, Freeze/Unfreeze, Clawback
- Wire deposit button in TreasuryView

**Investor Dashboard:**
- Add transfer modal (recipient dropdown, amount, mode selector)
- Wire "Request KYC" button to `submitRequest({ type: "KYC" })`
- Wire transfer form to `executeTransfer()`

**Regulator Dashboard:**
- Replace `MOCK_NOTES` with `GET /api/notes/:investor`
- Replace `MOCK_DECRYPTIONS` with `POST /api/decrypt`
- Use store's `shieldedNotes` instead of local state

**Shared:**
- Add toast notification component (success/error with Etherscan link)
- Add loading overlay for long operations (ZK proofs)
- Add "New Demo" button in TopBar → `POST /api/reset`
- Delete unused duplicate components in `components/shared/`

### Project Structure

```
private-trex/
├── api/
│   ├── server.ts              # Express app, CORS, API key auth middleware
│   ├── session.ts             # DemoSession class
│   └── routes.ts              # All endpoint handlers
├── src/
│   ├── index.ts               # CLI entrypoint (unchanged)
│   ├── trex.ts                # T-REX operations (unchanged)
│   ├── helpers.ts             # Zeto admin operations (unchanged)
│   ├── identity.ts            # Actor identity resolution (unchanged)
│   ├── complianceSmt.ts       # Compliance SMT (unchanged)
│   ├── authorityIndexer.ts    # Arbiter decryption (unchanged)
│   ├── deploy.ts              # Infrastructure deployment (unchanged)
│   ├── fund-actors.ts         # Sepolia funding (unchanged)
│   └── sepolia.ts             # Network config (unchanged)
├── start.sh                   # Local Besu (unchanged)
├── start-sepolia.sh           # Sepolia lifecycle
└── ...

private-trex-ui/
├── src/
│   ├── store/
│   │   └── app-store.ts       # MODIFIED: async API actions, remove mocks
│   ├── components/
│   │   ├── bank/bank-dashboard.tsx         # MODIFIED: wire all buttons
│   │   ├── investor/investor-dashboard.tsx # MODIFIED: transfer modal, KYC request
│   │   ├── regulator/regulator-dashboard.tsx # MODIFIED: real notes + decryption
│   │   ├── shared/top-bar.tsx              # MODIFIED: New Demo button, real contracts
│   │   └── shared/toast.tsx                # NEW: notification system
│   ├── lib/
│   │   ├── utils.ts           # Unchanged
│   │   └── api.ts             # NEW: API client with key auth
│   └── types/
│       └── index.ts           # Minor additions (loading state types)
└── ...
```

---

## Implementation Order

### Milestone 1: Sepolia [COMPLETE]
### Milestone 2: Repeatable Demos [COMPLETE]

### Milestone 3: API Server

1. Create `api/server.ts` — Express app with CORS, API key middleware, error handling
2. Create `api/session.ts` — DemoSession class wrapping existing `src/` modules
3. Create `api/routes.ts` — `/setup`, `/reset`, `/state`, `/health` endpoints
4. Add action endpoints — `/transfer`, `/kyc`, `/freeze`, `/clawback`, `/request`
5. Add disclosure endpoints — `/notes/:investor`, `/decrypt`
6. Test full flow via curl: setup → transfer → KYC → freeze → clawback → decrypt

### Milestone 4: Frontend Integration

1. Create `lib/api.ts` — API client with `X-API-Key` header
2. Replace mock store with async API-backed actions in `app-store.ts`
3. Wire Bank dashboard (transfers, requests, investor actions)
4. Wire Investor dashboard (transfer modal, KYC request)
5. Wire Regulator dashboard (real notes, decryption)
6. Add toast notifications + loading states
7. Add "New Demo" button in TopBar
8. Delete dead mock data and unused duplicate components

### Milestone 5: Deployment + Polish

1. Deploy API server to VM with systemd/PM2
2. Deploy frontend to Vercel
3. Configure CORS + API key for production
4. Test full demo flow on deployed infrastructure
5. Document presenter script (step-by-step click guide)
