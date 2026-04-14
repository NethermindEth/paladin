# Private T-REX API Server

REST API for the Private T-REX demo. Wraps Paladin's TypeScript SDK into endpoints that the frontend consumes.

## Quick Start

```bash
# Prerequisites: Paladin + Postgres running (via start-sepolia.sh --start)
# .env with ALCHEMY_API_KEY and WALLET_SEED

npm run api                                         # Start server on :3001

# Setup returns a sessionToken — save it for write operations
TOKEN=$(curl -s -X POST localhost:3001/api/setup | jq -r '.sessionToken')

curl localhost:3001/api/state                       # Read: anyone with API key
curl -X POST localhost:3001/api/transfer \           # Write: needs session token
  -H "Content-Type: application/json" \
  -H "X-Session-Token: $TOKEN" \
  -d '{"from":"bank","to":"alice","amount":10000,"mode":"PUBLIC"}'
```

## Architecture

```
Frontend (Vercel/localhost) → fetch() + X-API-Key → API Server (:3001) → PaladinClient → Paladin (:8548) → Sepolia
```

The server holds a singleton `DemoSession` in memory — actors, contracts, arbiter key, compliance SMT, note index. The arbiter private key never leaves this process.

## Security

### API Key
Set `API_KEY` in `.env` to require an `X-API-Key` header on all requests (except `/api/health`). If unset, all requests are accepted.

### Session Token (presenter lock)

Prevents audience members or a second presenter from disrupting a live demo.

**Flow:**
1. Presenter calls `POST /api/setup` → response includes `sessionToken` (random 32 hex chars)
2. Frontend stores the token in memory (not localStorage — lost on tab close by design)
3. Every write/admin request sends `X-Session-Token` header
4. If another browser tries a write endpoint without the token → `423 Locked: Demo in progress`
5. `POST /api/reset` issues a new token — previous token is invalidated

**Why in-memory, not JWT?** This is a single-machine demo tool, not a distributed system. The token is a simple mutex, not an auth mechanism.

**Access tiers:**

| Tier | Who | Requires Token | Endpoints |
|------|-----|----------------|-----------|
| Read | Anyone with API key (audience) | No | `GET /state`, `GET /notes/:investor`, `GET /health` |
| Write | Presenter only | Yes | All `POST` (transfer, kyc, freeze, clawback, decrypt, request) |
| Admin | Presenter only | Yes | `POST /setup`, `POST /reset` |

### Rate Limiting
| Tier | Limit | Endpoints |
|------|-------|-----------|
| Read | 60/min per IP | `GET /state`, `GET /notes`, `GET /health` |
| Write | 10/min per IP | All `POST` endpoints |
| Admin | 2/min per IP | `POST /setup`, `POST /reset` |

## Endpoints

### Lifecycle

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/health` | Server + session status |
| `POST` | `/api/setup` | Deploy T-REX + Zeto, KYC actors, mint, deposit. Returns full state. |
| `POST` | `/api/reset` | New actors + contracts, same infrastructure. Returns full state. |
| `GET` | `/api/state` | Current session state with live on-chain balances. |

### Transfers

| Method | Path | Body | Description |
|--------|------|------|-------------|
| `POST` | `/api/transfer` | `{ from, to, amount, mode }` | Execute public or private transfer. `mode`: `"PUBLIC"` or `"PRIVATE"`. |

**Response:**
```json
{ "success": true, "transaction": { "txId": "...", "txHash": "0x...", "type": "PUBLIC_TRANSFER", "uiSummary": "..." }, "balances": { ... } }
```

On failure (KYC, frozen, insufficient balance):
```json
{ "success": false, "error": "Receiver not KYCed", "transaction": null, "balances": { ... } }
```

### Request → Approve Flow

| Method | Path | Body | Description |
|--------|------|------|-------------|
| `POST` | `/api/request` | `{ type, actor, to?, amount?, mode? }` | Investor submits KYC or transfer request. `type`: `"KYC"` or `"TRANSFER"`. |
| `POST` | `/api/request/:id/approve` | — | Bank approves a pending request. |
| `POST` | `/api/request/:id/reject` | — | Bank rejects a pending request. |

### Compliance

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/kyc/:actor` | Direct KYC approval (bank-initiated, no request flow). |
| `POST` | `/api/freeze/:actor` | Toggle freeze/unfreeze. |
| `POST` | `/api/clawback/:actor` | Seize all private funds from a frozen actor via `forcedTransfer`. |

### Regulator Disclosure

| Method | Path | Body | Description |
|--------|------|------|-------------|
| `GET` | `/api/notes/:investor` | — | List shielded notes for an investor (auto-tracked from transfers). |
| `POST` | `/api/decrypt` | `{ investor, noteIds }` | Decrypt selected notes using the arbiter key. Returns plaintext amounts + counterparties. |

**Decrypt response:**
```json
{
  "decrypted": [{ "amount": 50000, "ownerName": "alice", "counterpartyName": "bank", "ownerAddress": "0x...", "counterpartyAddress": "0x...", "createdAt": "..." }],
  "notes": [{ "noteId": "...", "decrypted": { ... } }]
}
```

## State Shape (`GET /api/state`)

Single payload that hydrates the frontend's Zustand store:

```json
{
  "setupComplete": true,
  "runId": "k7m2p1",
  "actors": { "bank": { "name": "bank", "displayName": "Bank", "evmAddress": "0x...", "babyjubPubKey": ["0x...", "0x..."], "role": "bank" } },
  "contracts": { "trex": { "token": "0x...", "identityRegistry": "0x..." }, "zeto": { "address": "0x..." } },
  "balances": { "bank": { "public": 490000, "private": 500000 }, "alice": { "public": 10000, "private": 50000 } },
  "investorStatuses": { "alice": { "kyc": true, "frozen": false }, "charlie": { "kyc": false, "frozen": false } },
  "transactions": [{ "txId": "...", "type": "SHIELDED_TRANSFER", "txHash": "0x...", "status": "CONFIRMED", "uiSummary": "...", "visibility": "PRIVATE" }],
  "pendingRequests": [],
  "shieldedNotes": { "alice": [{ "noteId": "...", "createdTxHash": "0x...", "decrypted": null }] }
}
```

## Demo Flow (via curl)

All write/admin requests require `-H "X-Session-Token: $TOKEN"`.

```bash
# Setup — returns sessionToken
TOKEN=$(curl -s -X POST localhost:3001/api/setup | jq -r '.sessionToken')

# ACT 1: Public transfers
curl -s -X POST localhost:3001/api/transfer -H "Content-Type: application/json" \
  -H "X-Session-Token: $TOKEN" -d '{"from":"bank","to":"alice","amount":10000,"mode":"PUBLIC"}'

# ACT 2: Shielded transfers (takes ~20s for ZK proof)
curl -s -X POST localhost:3001/api/transfer -H "Content-Type: application/json" \
  -H "X-Session-Token: $TOKEN" -d '{"from":"bank","to":"alice","amount":50000,"mode":"PRIVATE"}'

# ACT 3: Regulator decryption (read = no token needed)
curl -s localhost:3001/api/notes/alice
curl -s -X POST localhost:3001/api/decrypt -H "Content-Type: application/json" \
  -H "X-Session-Token: $TOKEN" -d '{"investor":"alice","noteIds":["..."]}'

# ACT 4: Compliance
curl -s -X POST localhost:3001/api/request -H "Content-Type: application/json" \
  -H "X-Session-Token: $TOKEN" -d '{"type":"KYC","actor":"charlie"}'
# ... approve, freeze, clawback follow the same pattern
```

## Files

| File | Purpose |
|------|---------|
| `server.ts` | Express app — CORS, API key auth, rate limiting |
| `session.ts` | `DemoSession` class — Paladin SDK orchestration, state, arbiter key, compliance SMT |
| `routes.ts` | Route handlers — tiered access (read/write/admin), session token enforcement |
| `test-endpoints.sh` | Automated test script (37 assertions, full demo flow) |

## Testing

```bash
# With Paladin running:
npm run api &
bash api/test-endpoints.sh    # 37/37 tests, ~10 min on Sepolia
```

## Environment Variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `ALCHEMY_API_KEY` | Yes | — | Alchemy Sepolia RPC key |
| `WALLET_SEED` | Yes | — | BIP32 seed (64 hex chars) |
| `API_PORT` | No | `3001` | Server port |
| `API_KEY` | No | — | API key for `X-API-Key` header auth |
