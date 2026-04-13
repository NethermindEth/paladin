# Private T-REX: Presenter Guide

Step-by-step script for the live demo. Total time: ~15 minutes.

## Before the Demo

```bash
# 1. Ensure Paladin is running
./start-sepolia.sh --start

# 2. Start API server and pre-warm (deploys contracts, ~4 min)
npm run api &
curl -X POST localhost:3001/api/setup

# 3. Start frontend (separate terminal)
cd /path/to/private-trex-ui
npm run dev
```

Open **http://localhost:3000**. You should see the Bank Dashboard with:
- Public Treasury: 500,000 DBT
- Private Pool: 500,000 DBT

**Session locking:** When you open the UI and it loads the setup state, your browser holds a session token. Audience members can open the same URL and watch in real-time (read-only), but cannot click action buttons or reset the demo. Only your browser tab controls the demo.

---

## ACT 1: Public Transfers (Minutes 1–4)

**Message:** _"Standard Ethereum is fully transparent — every transfer is visible."_

### Step 1: Bank sends 10K public to Alice
1. **Bank Dashboard** → **Transfers** tab
2. Recipient: **Alice**, Amount: **10000**, Mode: **PUBLIC**
3. Click **Confirm & Send**
4. Wait for green toast with Etherscan link → click it to show the audience the on-chain transaction
5. _"Sender, receiver, amount — all visible to anyone."_

### Step 2: Alice sends 5K public to Bob
1. **TopBar** → Switch to **Alice (Investor)**
2. Click **New Transfer**
3. Recipient: **Bob**, Amount: **5000**, Mode: **PUBLIC**
4. Click **Send**
5. _"Again — fully transparent. Anyone with Etherscan can see who sent what to whom."_

---

## ACT 2: Shielded Transfers (Minutes 4–8)

**Message:** _"Now let's do the same thing — but privately."_

### Step 3: Bank sends 20K private to Alice
1. **TopBar** → Switch to **Bank Dashboard**
2. **Transfers** tab → Recipient: **Alice**, Amount: **20000**, Mode: **PRIVATE**
3. Click **Confirm & Send**
4. Loading: "Generating ZK proof..." (~15-20s)
5. Green toast with Etherscan link → click it
6. _"The transaction exists on-chain — but the amount and counterparties are completely hidden."_

### Step 4: Alice sends 5K private to Bob
1. **TopBar** → Switch to **Alice (Investor)**
2. Click **New Transfer**
3. Recipient: **Bob**, Amount: **5000**, Mode: **PRIVATE**
4. Click **Send**
5. _"Same chain, same explorer — but no one can tell who sent what to whom."_

---

## ACT 3: Selective Disclosure (Minutes 8–11)

**Message:** _"Privacy doesn't mean no oversight. The regulator can see everything."_

### Step 5: Regulator decrypts Alice's transfers
1. **TopBar** → Switch to **Regulator**
2. Click **Alice** in the left panel
3. See encrypted notes (shimmer blocks — hidden data)
4. Click **Select All Encrypted** → Click **Decrypt Selected**
5. Notes reveal: amount, owner, counterparty
6. _"The regulator holds the arbiter key. They can decrypt any transfer, any time."_

### Step 6: Regulator decrypts Bob's transfers
1. Click **Bob** in the left panel
2. Select notes → **Decrypt Selected**
3. _"Full visibility for the regulator — zero visibility for the public."_

---

## ACT 4: Compliance Enforcement (Minutes 11–15)

**Message:** _"Privacy does NOT break compliance."_

### Step 7: Alice tries to send to Charlie (not KYC'd) — FAILS
1. **TopBar** → Switch to **Alice (Investor)**
2. Click **New Transfer** → Recipient: **Charlie**, Amount: **2000**, Mode: **PRIVATE**
3. Click **Send**
4. **Red toast:** "Receiver is not KYC verified"
5. _"The ZK circuit enforces KYC on-chain. No KYC, no transfer — even in private."_

### Step 8: Charlie requests KYC
1. **TopBar** → Switch to **Charlie (Investor)**
2. See yellow banner: "Not KYC verified"
3. Click **Request KYC**

### Step 9: Bank approves Charlie's KYC
1. **TopBar** → Switch to **Bank Dashboard**
2. **Requests** tab → See Charlie's KYC request
3. Click **Approve**
4. _"Bank reviews and approves. Charlie is now KYC'd."_

### Step 10: Alice sends 2K private to Charlie — SUCCEEDS
1. **TopBar** → Switch to **Alice (Investor)**
2. **New Transfer** → Charlie, 2000, PRIVATE → **Send**
3. Green toast — transfer succeeds
4. _"Now that Charlie is KYC'd, the transfer goes through."_

### Step 11: Bank freezes Charlie
1. **TopBar** → Switch to **Bank Dashboard**
2. **Investors** tab → Click action button on Charlie → **Freeze**
3. _"Compliance issue detected. Charlie's account is now frozen."_

### Step 12: Show Charlie is blocked
1. **TopBar** → Switch to **Charlie (Investor)**
2. See red banner: "Account frozen. Transfers are blocked."
3. New Transfer button is gone
4. _"Charlie cannot move any tokens — public or private."_

### Step 13: Bank claws back Charlie's funds
1. **TopBar** → Switch to **Bank Dashboard**
2. **Investors** tab → Action button on Charlie → **Clawback**
3. Toast: "Clawback: 2,000 DBT seized from Charlie" with Etherscan link
4. Charlie's private balance: 0
5. _"The enforcer seizes all of Charlie's private tokens. The assets are recovered."_

### Step 14: Show final state
1. **Bank Dashboard** → **Overview** tab
2. Point out final balances
3. **History** tab → All transactions with Etherscan links

---

## Closing

_"To recap what we just demonstrated:"_

1. **Public transfers** — fully visible on block explorer
2. **Private transfers** — on-chain but amounts and parties hidden by zero-knowledge proofs
3. **Regulatory access** — the arbiter decrypts everything, the public sees nothing
4. **KYC enforcement** — unregistered receivers are rejected at the circuit level
5. **Account freeze** — frozen accounts cannot transact
6. **Asset seizure** — the enforcer recovers private tokens via forced transfer

_"Same asset, same blockchain, same settlement guarantees — with institutional-grade privacy and compliance."_

---

## Troubleshooting

| Problem | Fix |
|---------|-----|
| "Connecting to API server..." on page load | API server not running. `npm run api` in private-trex/ |
| "No active session" errors | Run `curl -X POST localhost:3001/api/setup` |
| Transaction timeout | Paladin may need restart: `./start-sepolia.sh --clean && --start` |
| "Insufficient private balance" | You tried a PRIVATE transfer but the sender has no private tokens. Use PUBLIC mode or deposit to pool first. |

## Reset for Next Demo

Click **New Demo** button in the top-right (shows a confirmation dialog), or via curl:
```bash
# TOKEN is from the original setup response
curl -X POST localhost:3001/api/reset -H "X-Session-Token: $TOKEN"
```

This creates new actors, re-deploys T-REX + Zeto, re-KYCs, re-mints. Existing infrastructure contracts are reused. Takes ~4 min on Sepolia. A new session token is issued — the previous one is invalidated.
