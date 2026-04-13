# Private T-REX: Troubleshooting (Live Demo Recovery)

Quick reference for when things break mid-demo. Keep this open in a terminal tab.

All commands assume you're in `/Users/dkgoutham/paladin/examples/private-trex/`.

---

## The Nuclear Option (fixes 95% of problems)

Copy-paste this whole block — it's a full restart that takes ~5 minutes:

```bash
# 1. Kill the API server
lsof -ti:3002 | xargs kill -9 2>/dev/null

# 2. Restart Paladin fresh (clears stuck block indexer, stale DB)
./start-sepolia.sh --clean && ./start-sepolia.sh --start

# 3. Restart API server (background)
API_PORT=3002 npx ts-node api/server.ts > /tmp/api-server.log 2>&1 &

# 4. Wait for API to come up
sleep 5

# 5. Pre-warm the demo session (~4 min on Sepolia)
curl -s --max-time 600 -X POST http://localhost:3002/api/setup | jq

# 6. Refresh the browser tab at localhost:3000, click "End Demo", then "Start Demo"
```

---

## Specific Errors and Fixes

### "Call to transfer failed: undefined"
Paladin's block indexer is stuck in a DB constraint loop. **Nuclear option.**

### "Demo in progress. Only the presenter can perform this action."
Another browser tab holds the session token. Either close the other tab, or click "End Demo" → "Start Demo" again (issues a new token, invalidating the other tab).

### "insufficient funds for gas"
The funder wallet (`0xF9D5dB1a82f22e12E240dD760ED7D731437aDaC6`) is out of Sepolia ETH.
- Send more ETH from your MetaMask to that address
- Then click "End Demo" → "Start Demo"

### "Receiver is not KYC verified"
Expected! This is the demo showing KYC enforcement. Alice → Charlie fails until Bank approves Charlie's KYC request.

### "Account is frozen — transfers blocked"
Expected! This is the demo showing account freeze enforcement.

### "Insufficient private balance"
You tried to send a PRIVATE transfer but the sender has no private tokens. For the first private transfer, Bank must send private tokens to the investor first.

### "PD020503: WebSocket reconnected during JSON/RPC call"
Transient Alchemy connection issue. Wait 30 seconds and retry. If persistent, use the nuclear option.

### Frontend shows "Connecting to API server..." forever
API server is down or not reachable.
```bash
# Check if API is alive
curl -s http://localhost:3002/api/health

# If no response, restart it:
lsof -ti:3002 | xargs kill -9 2>/dev/null
API_PORT=3002 npx ts-node api/server.ts > /tmp/api-server.log 2>&1 &
```

### "Transfer failed" or "Input state N not found" after clawback
After freezing and clawing back an investor's funds, attempting a private
transfer **from** that investor may fail with a confusing "Input state not
found" error instead of the expected "Insufficient private balance".

**What happened:** Paladin's in-memory state cache (`creatingStates`) can
retain stale entries for states that were consumed by a `forcedTransfer`
(clawback). The transfer assembler picks those ghost entries, then the
state store correctly says "gone" — causing a retry loop that eventually
times out.

This only happens when Paladin has been through abnormal conditions:
- Server crash or kill mid-transaction (between assemble and finalize)
- Failed transactions that never finalized (unfunded submit keys, missing circuits)
- Multiple deploy/teardown cycles without a clean restart

Under normal demo operation (clean setup → transfers → freeze → clawback), the
cleanup path works correctly and no ghost states appear.

**Quick fix (no DB wipe, no redeploy, ~10 seconds):**
```bash
docker restart paladin-node1
```
This clears the in-memory ghost entries without touching Postgres. The
sequencer rebuilds from the DB on restart, which has correct spend records.
Wait 10 seconds, then retry.

**If `docker restart` doesn't help**, the DB itself may have inconsistent
state from a prior crash. Use the nuclear option (full `--clean`).

**Prevention:** Don't kill the API server or Paladin while a private
transfer is in progress (spinner showing "generating ZK proof"). Wait for
it to complete or fail before stopping anything.

### Transfer spinner stuck for 3+ minutes
Something is wrong on the Paladin side. Check:
```bash
# Is Paladin responsive?
curl -sf http://127.0.0.1:8548/ -X POST -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","method":"ptx_getTransactionReceipt","params":["00000000-0000-0000-0000-000000000000"],"id":1}'

# Check for errors in Paladin logs
docker logs paladin-node1 2>&1 | tail -30

# Check API server logs
tail -30 /tmp/api-server.log
```

If Paladin shows `duplicate key value violates unique constraint` errors → **nuclear option**.

If Paladin logs show `not enough values for input signal` → circuit depth
mismatch. The compiled circuit artifacts in the Docker image don't match
Paladin's Go SMT height constants. Regenerate circuits from the circom
sources (`./start-sepolia.sh --circuits` or `cd zeto/zkp/circuits && node
scripts/gen.js -c <circuit_name>`), then rebuild the Docker image.

### Port 3000 already in use
```bash
lsof -ti:3000 | xargs kill -9
```

### Port 3002 already in use
```bash
lsof -ti:3002 | xargs kill -9
```

---

## Status Check Commands

```bash
# Is Paladin running?
docker ps | grep paladin-node1

# Is Paladin responsive?
curl -sf http://127.0.0.1:8548/ -X POST -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","method":"ptx_getTransactionReceipt","params":["00000000-0000-0000-0000-000000000000"],"id":1}'

# Is API server running?
curl -s http://localhost:3002/api/health

# Is an active session loaded?
curl -s http://localhost:3002/api/health | jq '.sessionActive'

# How much ETH does the funder have?
curl -s -X POST "https://eth-sepolia.g.alchemy.com/v2/$ALCHEMY_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","method":"eth_getBalance","params":["0xF9D5dB1a82f22e12E240dD760ED7D731437aDaC6","latest"],"id":1}' \
  | jq -r '.result' | awk '{printf "%.4f ETH\n", strtonum($1)/1e18}'
```

---

## Before-Demo Checklist

5 minutes before your audience arrives:

```bash
# 1. Ensure Paladin and API are freshly restarted
./start-sepolia.sh --clean && ./start-sepolia.sh --start
lsof -ti:3002 | xargs kill -9 2>/dev/null
API_PORT=3002 npx ts-node api/server.ts > /tmp/api-server.log 2>&1 &

# 2. Pre-warm (4 min on Sepolia)
curl -s --max-time 600 -X POST http://localhost:3002/api/setup | jq

# 3. Check funder balance — should be >= 2 ETH for a comfortable demo
curl -s -X POST "https://eth-sepolia.g.alchemy.com/v2/$ALCHEMY_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","method":"eth_getBalance","params":["0xF9D5dB1a82f22e12E240dD760ED7D731437aDaC6","latest"],"id":1}' \
  | jq -r '.result' | awk '{printf "Funder: %.4f ETH\n", strtonum($1)/1e18}'

# 4. Start the frontend (separate terminal)
cd /Users/dkgoutham/paladin-monorepo/private-trex-ui && npm run dev

# 5. Open localhost:3000 — verify landing page appears
# 6. Click "Start Demo" — verify instant load into Bank dashboard
# 7. Click "End Demo" — verify return to landing
```

You're now ready for your audience.

---

## Funder Address (send Sepolia ETH here)

```
0xF9D5dB1a82f22e12E240dD760ED7D731437aDaC6
```

**Cost estimate:**
- One `setup` (first time): ~0.5 ETH
- Full 15-min demo (transfers, KYC, freeze, clawback): ~0.5 ETH
- **Keep at least 2 ETH in the funder wallet before each demo**

---

## Running for Extended Periods (days/weeks)

You can keep the same Paladin instance running for a week with multiple
demo cycles. The pattern:

1. `./start-sepolia.sh --start` once
2. `POST /api/setup` once (~15 min deploy)
3. Demo as many times as you want via the UI
4. Fresh actors: `POST /api/restart` (redeploys T-REX + Zeto, ~15 min)
5. If something gets stuck: `docker restart paladin-node1`, wait 10s, retry
6. Nuclear only if restart doesn't help: `--clean` + `--start` + `/api/setup`

### What `docker restart paladin-node1` does
- Clears all in-memory sequencer state (ghost `creatingStates` entries, stale locks)
- Preserves Postgres (key mappings, transaction history, state records)
- Preserves on-chain contracts (T-REX, Zeto, everything deployed)
- Paladin reconnects to Alchemy, re-syncs the block indexer
- Takes ~10 seconds
- **Try this first** for any unexplained transfer failure

### What `--clean` does (and when you need it)
- Drops the `paladin_demo` Postgres database entirely
- Removes the Paladin container
- After `--clean`, you **must** run `--start` then `/api/setup` (~15 min full redeploy)
- All prior on-chain contracts become unreachable (Paladin lost the key→address mappings)
- Funder wallet is unaffected (derived from `WALLET_SEED`, not stored in DB)
- Use this only when `docker restart` doesn't fix the problem

### What you DON'T need to clean
- The Docker image itself (`paladin:test`) — never needs rebuilding unless you change Go or circuit code
- `data/deploy.json` — automatically overwritten by each `/api/setup`
- The funder wallet — deterministic from seed, always works

---

## Contact

If none of the above works, stop the demo and debug offline. The common failure modes are all in this document.
