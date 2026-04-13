# Private ERC-3643 Demo — System Architecture

## Overview

A demo for institutional audiences showing regulated tokenized securities (ERC-3643 / T-REX) with a Zeto AENKNR-E shielded pool on Ethereum Sepolia. Three roles: Bank (custody + compliance), Investor (transfer + hold), Regulator (selective disclosure).

```
┌─────────────────────────────────────────────────────┐
│                    Frontend (Next.js)                │
│  http://localhost:3000                               │
│  Zustand store polls GET /state every 10s            │
│  Session token stored in localStorage                │
│  All writes require X-Session-Token header           │
└─────────────┬───────────────────────────────────────┘
              │ HTTP (REST)
              ▼
┌─────────────────────────────────────────────────────┐
│              API Server (Express + ts-node)          │
│  http://0.0.0.0:3001                                 │
│                                                      │
│  DemoSession — one session at a time                 │
│    ├── Contract refs (T-REX suite, Zeto address)     │
│    ├── Actor identities (BIP32-derived keys)         │
│    ├── Compliance SMT (in-memory, persisted leaves)  │
│    ├── Arbiter keypair (for note decryption)         │
│    ├── Investor statuses, transactions, notes        │
│    └── Persisted to data/session.json on every       │
│        state-changing operation                      │
│                                                      │
│  On startup:                                         │
│    if data/session.json exists → restore (~5s)       │
│    else → wait for POST /setup (~12 min)             │
│                                                      │
│  Before every operation:                             │
│    ensurePaladinReady() — 5x retry with backoff      │
│                                                      │
│  After every private transfer:                       │
│    autoFundSubmitKey() — fire-and-forget top-up      │
└─────────────┬───────────────────────────────────────┘
              │ JSON-RPC over HTTP
              ▼
┌─────────────────────────────────────────────────────┐
│              Paladin Node (Docker)                   │
│  http://0.0.0.0:8548                                 │
│                                                      │
│  Core (Go → libcore.so via JNI):                     │
│    ├── Private Tx Manager — assembles ZK proofs,     │
│    │   manages state locks, coordinates signing      │
│    ├── Public Tx Manager — submits base-layer txs,   │
│    │   manages nonces, polls receipts                │
│    ├── Block Indexer — watches Sepolia via Alchemy    │
│    │   WS, processes events, updates internal state  │
│    ├── Key Manager — BIP32 derivation from seed,     │
│    │   manages wallet keys + domain submit keys      │
│    └── State Manager — per-contract state DB,        │
│        tracks UTXOs, nullifiers, SMT roots           │
│                                                      │
│  Zeto Domain Plugin (Go → libzeto.so):               │
│    ├── Circuit-specific witness assembly              │
│    ├── Groth16 proof generation (WASM prover)        │
│    ├── Internal KYC + Nullifier SMTs (per-contract)  │
│    └── Compliance root stored in contract "extras"   │
│                                                      │
│  Connects to:                                        │
│    ├── PostgreSQL (paladin-postgres:5432)             │
│    └── Alchemy Sepolia (HTTP + WebSocket)            │
└─────────────┬───────────────────────────────────────┘
              │ eth_sendRawTransaction
              ▼
┌─────────────────────────────────────────────────────┐
│              Ethereum Sepolia                        │
│                                                      │
│  On-chain contracts:                                 │
│    ├── T-REX Suite (13 contracts):                   │
│    │   Token, IdentityRegistry, ModularCompliance,   │
│    │   ClaimTopicsRegistry, TrustedIssuersRegistry,  │
│    │   IdentityRegistryStorage, ClaimIssuer,         │
│    │   Identity, TransferManager, etc.               │
│    │                                                 │
│    ├── Zeto AENKNR-E (via ZetoFactory proxy):        │
│    │   Shielded pool with UTXO model,                │
│    │   Poseidon commitments, BabyJubJub keys,        │
│    │   Groth16 verifier, compliance root slot,       │
│    │   enforcer + arbiter key slots                  │
│    │                                                 │
│    └── Supporting contracts:                         │
│        ZetoFactory, Codec, TransferFacet,            │
│        Poseidon2/3 hash libraries                    │
└─────────────────────────────────────────────────────┘
```

## Session Lifecycle

### First-Time Setup (CLI, ~12 minutes)

Run from terminal before the demo:
```bash
curl -X POST http://<server>:3001/api/setup
```

This performs:
1. Resolve 6 actor identities (BIP32 key derivation via Paladin)
2. Fund actors on Sepolia (0.1-0.5 ETH each from funder wallet)
3. Deploy 13 T-REX contracts + wire them together
4. Deploy Zeto AENKNR-E via ZetoFactory
5. Set codec + transfer facet (diamond-lite pattern)
6. Set ERC20 link (Zeto pool backs T-REX token)
7. Register Zeto escrow address on T-REX Identity Registry
8. Generate arbiter keypair, set arbiter + enforcer keys
9. Register 5 actors on KYC tree + post compliance root
10. Mint 1,000,000 DBT to bank, deposit 500,000 to shielded pool
11. Attempt to fund domain submit key (best-effort)
12. **Persist entire session to `data/session.json`**

### Subsequent Starts (~5 seconds)

When the API server restarts (crash, reboot, code update):
1. Reads `data/session.json`
2. Pings Paladin with exponential backoff (up to 5 attempts)
3. Resolves actor identities from same BIP32 seed (deterministic)
4. Reconnects to existing Zeto contract by address
5. Rebuilds compliance SMT from persisted leaf data
6. Session ready — no on-chain transactions needed

### Demo Presentation

Presenter opens the UI and clicks "Launch Demo":
1. Frontend calls `POST /start`
2. Server checks session exists (setup must have been run beforehand)
3. Issues a session token (random 32-char hex)
4. Token returned in response, stored in browser `localStorage`
5. All subsequent write requests include `X-Session-Token` header
6. `requireToken` middleware rejects writes without a valid token

### New Demo (Full Redeploy)

Presenter clicks "New Demo" in the UI:
1. Frontend calls `POST /restart` with session token
2. Server resets all in-memory state, generates new runId
3. Deploys fresh T-REX + Zeto contracts
4. Persists new session to disk
5. Issues new session token

## Access Control

| Tier | Endpoints | Auth Required | Purpose |
|------|-----------|---------------|---------|
| READ | `GET /health, /state, /notes/:investor` | API key only | Audience can view live state |
| WRITE | `POST /transfer, /kyc, /freeze, /clawback, /decrypt, /request, /add-investor, /remove-investor` | API key + session token | Only the presenter |
| ADMIN | `POST /setup, /start, /restart` | API key only | Session management |

**Token lifecycle:**
- Issued by `/setup` or `/start` — whoever calls it becomes the presenter
- Stored in `localStorage` — survives page refresh within the same browser
- Sent as `X-Session-Token` header on every write request
- Invalid token returns HTTP 423 (Locked): "Demo in progress. Only the presenter can perform this action."
- `POST /start` re-issues the token, invalidating the previous one — use this to transfer presenter control

**Troubleshooting:**
- If the presenter can't write after a page refresh: check `localStorage` for `private-trex-session-token`
- If "Demo in progress" error in a new tab: call `POST /start` again to re-claim (invalidates old tab)
- If API key is configured (`API_KEY` env var), all requests need `X-API-Key` header

## How the API Server Uses Paladin

The API server communicates with Paladin via **JSON-RPC over HTTP** (port 8548). The Paladin SDK (`@lfdecentralizedtrust/paladin-sdk`) wraps this into typed TypeScript methods.

### Public Transactions (T-REX operations)

```
API Server                          Paladin                         Sepolia
    │                                  │                               │
    │  ptx.sendTransaction(PUBLIC)     │                               │
    │ ─────────────────────────────►   │                               │
    │                                  │  eth_sendRawTransaction       │
    │                                  │ ─────────────────────────────►│
    │                                  │                               │
    │                                  │  Block confirmed              │
    │                                  │ ◄─────────────────────────────│
    │  pollForReceipt (300s timeout)   │                               │
    │ ◄─────────────────────────────   │                               │
```

Used for: T-REX deploy, mint, transfer, registerIdentity, setAddressFrozen, setComplianceRoot, setArbiter, setEnforcer

### Private Transactions (Zeto operations)

```
API Server                          Paladin                         Sepolia
    │                                  │                               │
    │  zeto.transfer(PRIVATE)          │                               │
    │ ─────────────────────────────►   │                               │
    │                                  │  1. Assemble: select coins,   │
    │                                  │     build witness inputs      │
    │                                  │  2. Sign: generate Groth16    │
    │                                  │     proof via WASM prover     │
    │                                  │  3. Endorse: verify proof     │
    │                                  │  4. Submit: domain submit key │
    │                                  │     signs EVM tx              │
    │                                  │ ─────────────────────────────►│
    │                                  │                               │
    │                                  │  Block confirmed, events      │
    │                                  │  indexed, UTXOs updated       │
    │                                  │ ◄─────────────────────────────│
    │  pollForReceipt (600s timeout)   │                               │
    │ ◄─────────────────────────────   │                               │
```

Used for: Zeto deploy, deposit, transfer, forcedTransfer (clawback)

### Domain Submit Key

The domain submit key is a BIP32-derived EVM address that Paladin auto-allocates for each Zeto contract. It signs the base-layer transaction that carries the ZK proof. Key facts:

- **Created lazily**: only exists in Paladin's DB after the first non-deposit private transfer
- **Needs Sepolia ETH**: each private tx costs ~0.01-0.02 ETH in gas
- **Auto-funded**: after every successful private transfer, the API server queries Paladin's DB for the key address and tops it up to 0.15 ETH from the funder wallet
- **BIP32 path**: auto-assigned by Paladin (not a fixed path like the actors)

## Resilience Mechanisms

### Paladin Health Check (`ensurePaladinReady`)

Before every write operation, the server sends a dummy RPC call to Paladin:
- On success: proceed immediately
- On failure: retry with exponential backoff (2s, 4s, 6s, 8s, 10s)
- After 5 failures: return error to the user

Handles: Alchemy WS idle disconnection, Paladin container restart, temporary network issues.

### Session Persistence (`data/session.json`)

Written after every state-changing operation (setup, KYC, freeze, add investor).

Contains:
- `runId` — scopes investor identity BIP32 paths
- `trexSuite` — all T-REX contract addresses
- `zetoAddress` — Zeto shielded pool address
- `arbiterPrivKey` / `arbiterPubKey` — for note decryption
- `investorStatuses` — KYC and frozen flags per actor
- `dynamicInvestors` — names of investors added during the session
- `complianceLeaves` — BabyJubJub pubkey X/Y + KYC status for each actor

On restore: actors re-resolved from deterministic BIP32 paths, compliance SMT rebuilt from leaves, Zeto reconnected by address. No on-chain work needed.

### Auto-Funding (`autoFundSubmitKey`)

After every successful private transfer (fire-and-forget, non-blocking):
1. Query Paladin's PostgreSQL for the domain submit key address
2. Check its Sepolia ETH balance
3. If below 0.15 ETH, send a top-up from the funder wallet

### Frontend Polling

`GET /state` every 10 seconds while demo is active. Keeps balances, transactions, and pending requests fresh. Handles the "come back after hours" scenario — the poll recovers current state from the server.

## Key Addresses (Derived from WALLET_SEED)

| Role | BIP32 Path | Purpose |
|------|-----------|---------|
| Funder | `m/44'/60'/0'` | Distributes Sepolia ETH to all other addresses |
| Deployer | `m/44'/60'/1'` | Deploys contracts (issuer identity) |
| Bank | `m/44'/60'/2'` | Custody operations, Zeto owner |
| Regulator | `m/44'/60'/3'` | Read-only, note decryption |
| Alice | `m/44'/60'/4'` (per runId) | Investor |
| Bob | `m/44'/60'/5'` (per runId) | Investor |
| Charlie | `m/44'/60'/6'` (per runId) | Investor |
| Dynamic investors | `m/44'/60'/7'+` | Added on demand |
| Domain submit key | Auto-allocated by Paladin | Submits private txs on-chain |

## Failure Modes and Recovery

| Failure | Symptom | Auto-Recovery | Manual Recovery |
|---------|---------|--------------|-----------------|
| Alchemy WS drops after idle | Next tx slow (indexer catching up) | `ensurePaladinReady` retries with backoff | `docker restart paladin-node1` |
| API server crashes | Frontend shows "Failed to fetch" | Restores from `session.json` on restart | Start the API server |
| Domain submit key out of ETH | "insufficient funds" in Paladin logs | `autoFundSubmitKey` after next transfer | Fund manually from funder wallet |
| Paladin container OOM/crash | All operations fail | N/A | `docker restart paladin-node1` |
| Compliance SMT drift | "CheckSMTProof failed" or "Failed to decode root" | SMT rebuilt from persisted leaves on restore | Delete `session.json`, re-run setup |
| Funder wallet depleted | Setup fails at funding step | N/A | Send Sepolia ETH to funder address |

## File Map

| File | Purpose |
|------|---------|
| `api/server.ts` | Express server, rate limiting, API key auth, auto-warm on startup |
| `api/routes.ts` | Route handlers, session token middleware, setup/start/restart lifecycle |
| `api/session.ts` | DemoSession class — all business logic, persistence, health checks |
| `src/trex.ts` | T-REX contract deployment and interaction helpers |
| `src/helpers.ts` | Zeto admin operations (KYC, arbiter, enforcer, compliance root) |
| `src/identity.ts` | Actor identity resolution and compliance root posting |
| `src/complianceSmt.ts` | In-memory Sparse Merkle Tree for KYC/frozen status |
| `src/authorityIndexer.ts` | Note decryption and proof bundle construction |
| `src/fund-actors.ts` | Sepolia ETH funding for actors and domain submit key |
| `src/sepolia.ts` | Network config, timeouts, Etherscan URL helpers |
| `data/deploy.json` | Infrastructure contract addresses (ZetoFactory, Codec, etc.) |
| `data/session.json` | Persisted session state (survives API server restarts) |
| `config.json` | Paladin node connection config |
| `.env` | `ALCHEMY_API_KEY`, `WALLET_SEED` |
