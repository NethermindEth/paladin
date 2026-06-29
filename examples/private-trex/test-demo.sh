#!/bin/bash
set -e

API="http://localhost:3001/api"
TOKEN=""
PASS=0
FAIL=0

green() { printf "\033[32m+ %s\033[0m\n" "$1"; PASS=$((PASS+1)); }
red()   { printf "\033[31m- %s\033[0m\n" "$1"; FAIL=$((FAIL+1)); }

call() {
  local method=$1 path=$2 body=$3
  local args=(-s -w "\n%{http_code}" -X "$method" "$API$path" -H "Content-Type: application/json")
  [ -n "$TOKEN" ] && args+=(-H "X-Session-Token: $TOKEN")
  [ -n "$body" ] && args+=(-d "$body")
  curl "${args[@]}"
}

jq_field() { echo "$1" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d$2)" 2>/dev/null; }

# Assert response.balances.<actor>.<kind> equals expected; body is the response JSON.
assert_balance() {
  local body=$1 actor=$2 kind=$3 expected=$4 label=$5
  local got
  got=$(echo "$body" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d['balances']['$actor']['$kind'])" 2>/dev/null)
  if [ "$got" = "$expected" ]; then
    green "$label ($actor.$kind=$got)"
  else
    red "$label ($actor.$kind expected=$expected got=$got)"
  fi
}

# Fetch full state and assert a balance — used after endpoints that don't return
# balances in their response (freeze, unfreeze).
assert_balance_via_state() {
  local actor=$1 kind=$2 expected=$3 label=$4
  local body got
  body=$(call GET /state | sed '$d')
  got=$(echo "$body" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d['balances']['$actor']['$kind'])" 2>/dev/null)
  if [ "$got" = "$expected" ]; then
    green "$label ($actor.$kind=$got)"
  else
    red "$label ($actor.$kind expected=$expected got=$got)"
  fi
}

# Assert regulator-visible live note count for an investor (notes with
# status=UNSPENT). Reads /state so it reflects the indexer ledger.
assert_live_notes() {
  local actor=$1 expected=$2 label=$3
  local body got
  body=$(call GET /state | sed '$d')
  got=$(echo "$body" | python3 -c "import sys,json; d=json.load(sys.stdin); notes=d.get('shieldedNotes',{}).get('$actor',[]); print(sum(1 for n in notes if n.get('status')=='UNSPENT'))" 2>/dev/null)
  if [ "$got" = "$expected" ]; then
    green "$label ($actor live notes=$got)"
  else
    red "$label ($actor live notes expected=$expected got=$got)"
  fi
}

echo "============================================"
echo "  Private ERC-3643 Demo -- Integration Tests"
echo "============================================"
echo ""

# 1. Health
echo "--- Health Check ---"
RESP=$(call GET /health)
CODE=$(echo "$RESP" | tail -1)
[ "$CODE" = "200" ] && green "Health endpoint" || red "Health endpoint (HTTP $CODE)"

# 2. Start demo
echo ""
echo "--- Start Demo ---"
RESP=$(call POST /start)
CODE=$(echo "$RESP" | tail -1)
BODY=$(echo "$RESP" | sed '$d')
if [ "$CODE" = "200" ]; then
  TOKEN=$(jq_field "$BODY" '["sessionToken"]')
  green "Start demo (token=${TOKEN:0:8}...)"
else
  red "Start demo failed (HTTP $CODE)"
  echo "$BODY"
  exit 1
fi

# 3. Initial balances
echo ""
echo "--- Initial Balances ---"
RESP=$(call GET /state)
BODY=$(echo "$RESP" | sed '$d')
BANK_PUB=$(jq_field "$BODY" '["balances"]["bank"]["public"]')
BANK_PRIV=$(jq_field "$BODY" '["balances"]["bank"]["private"]')
echo "  Bank: public=$BANK_PUB private=$BANK_PRIV"

# 4. Public transfer bank -> alice
echo ""
echo "--- Test 1: Public Transfer Bank -> Alice (10,000 DBT) ---"
RESP=$(call POST /transfer '{"from":"bank","to":"alice","amount":10000,"mode":"PUBLIC"}')
BODY=$(echo "$RESP" | sed '$d')
S=$(jq_field "$BODY" '["success"]')
[ "$S" = "True" ] && green "Public transfer" || red "Public transfer (success=$S)"
assert_balance "$BODY" alice public 10000 "  Alice.public"
assert_balance "$BODY" bank  public 490000 "  Bank.public"

# 5. Private transfer bank -> alice
echo ""
echo "--- Test 2: Private Transfer Bank -> Alice (5,000 DBT) ---"
RESP=$(call POST /transfer '{"from":"bank","to":"alice","amount":5000,"mode":"PRIVATE"}')
BODY=$(echo "$RESP" | sed '$d')
S=$(jq_field "$BODY" '["success"]')
[ "$S" = "True" ] && green "Private transfer bank->alice" || red "Private transfer bank->alice (success=$S)"
assert_balance "$BODY" alice private 5000  "  Alice.private"
assert_balance "$BODY" bank  private 495000 "  Bank.private"
assert_live_notes alice 1 "  Alice has 1 live note post-receive"

# 6. Private transfer alice -> bob
echo ""
echo "--- Test 3: Private Transfer Alice -> Bob (2,000 DBT) ---"
RESP=$(call POST /transfer '{"from":"alice","to":"bob","amount":2000,"mode":"PRIVATE"}')
BODY=$(echo "$RESP" | sed '$d')
S=$(jq_field "$BODY" '["success"]')
[ "$S" = "True" ] && green "Private transfer alice->bob" || red "Private transfer alice->bob (success=$S)"
assert_balance "$BODY" alice private 3000 "  Alice.private (post-send)"
assert_balance "$BODY" bob   private 2000 "  Bob.private"
# After Alice→Bob, Alice's original 5,000 note is spent; she has a 3,000 change note.
# Bob has 1 new 2,000 note.
assert_live_notes alice 1 "  Alice live notes after sending (change only)"
assert_live_notes bob   1 "  Bob live notes after receive"

# 7. Freeze alice
echo ""
echo "--- Test 4: Freeze Alice ---"
RESP=$(call POST /freeze/alice)
CODE=$(echo "$RESP" | tail -1)
[ "$CODE" -lt 300 ] && green "Freeze alice" || red "Freeze alice (HTTP $CODE)"

# 8. Transfer from frozen alice should fail
echo ""
echo "--- Test 5: Transfer from Frozen Alice (expect failure) ---"
RESP=$(call POST /transfer '{"from":"alice","to":"bob","amount":500,"mode":"PRIVATE"}')
BODY=$(echo "$RESP" | sed '$d')
S=$(jq_field "$BODY" '["success"]')
[ "$S" = "False" ] && green "Frozen transfer correctly rejected" || red "Frozen transfer should fail (success=$S)"
# Alice's balance must be untouched by the failed transfer attempt.
assert_balance_via_state alice private 3000 "  Alice.private (unchanged after failed send)"

# 9. Clawback alice
echo ""
echo "--- Test 6: Clawback Alice ---"
RESP=$(call POST /clawback/alice)
CODE=$(echo "$RESP" | tail -1)
BODY=$(echo "$RESP" | sed '$d')
[ "$CODE" -lt 300 ] && green "Clawback alice" || red "Clawback alice (HTTP $CODE): $(jq_field "$BODY" '["error"]')"
# This is the critical check the prior version of this script was missing —
# response must reflect post-seizure state so the UI doesn't leave the Clawback
# button clickable. See waitForBalanceDrop in session.ts.
assert_balance "$BODY" alice private 0      "  Alice.private → 0 after clawback"
assert_balance "$BODY" bank  private 498000 "  Bank.private += seized 3000"
# Regulator view: Alice must have 0 live notes after seizure; Bob untouched.
assert_live_notes alice 0 "  Alice live notes → 0 after clawback"
assert_live_notes bob   1 "  Bob live notes unchanged"

# 10. Transfer from zero-balance alice should fail
echo ""
echo "--- Test 7: Transfer from Clawbacked Alice (expect failure: zero balance) ---"
RESP=$(call POST /transfer '{"from":"alice","to":"bob","amount":100,"mode":"PRIVATE"}')
BODY=$(echo "$RESP" | sed '$d')
S=$(jq_field "$BODY" '["success"]')
[ "$S" = "False" ] && green "Zero-balance transfer correctly rejected" || red "Zero-balance transfer should fail"

# 11. Unfreeze alice
echo ""
echo "--- Test 8: Unfreeze Alice ---"
RESP=$(call POST /freeze/alice)
CODE=$(echo "$RESP" | tail -1)
[ "$CODE" -lt 300 ] && green "Unfreeze alice" || red "Unfreeze alice (HTTP $CODE)"

# 12. Public transfer to unfrozen alice
echo ""
echo "--- Test 9: Public Transfer to Unfrozen Alice (1,000 DBT) ---"
RESP=$(call POST /transfer '{"from":"bank","to":"alice","amount":1000,"mode":"PUBLIC"}')
BODY=$(echo "$RESP" | sed '$d')
S=$(jq_field "$BODY" '["success"]')
[ "$S" = "True" ] && green "Transfer to unfrozen alice" || red "Transfer to unfrozen alice (success=$S)"
assert_balance "$BODY" alice public 11000 "  Alice.public"

# 13. KYC charlie
echo ""
echo "--- Test 10: KYC Charlie ---"
RESP=$(call POST /kyc/charlie)
CODE=$(echo "$RESP" | tail -1)
[ "$CODE" -lt 300 ] && green "KYC charlie" || red "KYC charlie (HTTP $CODE)"

# 14. Private transfer to newly KYC'd charlie
echo ""
echo "--- Test 11: Private Transfer Bank -> Charlie (3,000 DBT) ---"
RESP=$(call POST /transfer '{"from":"bank","to":"charlie","amount":3000,"mode":"PRIVATE"}')
BODY=$(echo "$RESP" | sed '$d')
S=$(jq_field "$BODY" '["success"]')
[ "$S" = "True" ] && green "Private transfer bank->charlie" || red "Private transfer bank->charlie (success=$S)"
assert_balance "$BODY" charlie private 3000   "  Charlie.private"
assert_balance "$BODY" bank    private 495000 "  Bank.private (=498000 post-clawback − 3000 to charlie)"

# 11a. Second clawback while another investor is ALSO frozen.
# Regression check for the smtProofForForcedTransferCompliance bug where
# the prover's reconstructed compliance tree only marked the seized owner
# FROZEN, producing a root that diverged from the on-chain root and
# causing "Invalid proof" on the verifier.
echo ""
echo "--- Test 11b: Freeze Charlie + Freeze Bob, Clawback Bob (regression) ---"
RESP=$(call POST /freeze/charlie)
CODE=$(echo "$RESP" | tail -1)
[ "$CODE" -lt 300 ] && green "Freeze charlie" || red "Freeze charlie (HTTP $CODE)"

RESP=$(call POST /freeze/bob)
CODE=$(echo "$RESP" | tail -1)
[ "$CODE" -lt 300 ] && green "Freeze bob" || red "Freeze bob (HTTP $CODE)"

RESP=$(call POST /clawback/bob)
CODE=$(echo "$RESP" | tail -1)
BODY=$(echo "$RESP" | sed '$d')
if [ "$CODE" -lt 300 ]; then
  green "Clawback bob with charlie also frozen"
  assert_balance "$BODY" bob  private 0      "  Bob.private → 0 after clawback"
  assert_balance "$BODY" bank private 497000 "  Bank.private += seized 2000"
else
  red "Clawback bob failed (HTTP $CODE): $(jq_field "$BODY" '["error"]')"
fi

# Clean up: unfreeze both so downstream tests behave normally.
call POST /freeze/charlie > /dev/null
call POST /freeze/bob > /dev/null

# 15. Add dynamic investor
echo ""
echo "--- Test 12: Add New Investor ---"
RESP=$(call POST /add-investor)
CODE=$(echo "$RESP" | tail -1)
BODY=$(echo "$RESP" | sed '$d')
NEW_NAME=$(jq_field "$BODY" '["name"]')
[ "$CODE" -lt 300 ] && [ -n "$NEW_NAME" ] && green "Added investor: $NEW_NAME" || red "Add investor failed (HTTP $CODE)"

# 16. KYC new investor
echo ""
echo "--- Test 13: KYC $NEW_NAME ---"
RESP=$(call POST "/kyc/$NEW_NAME")
CODE=$(echo "$RESP" | tail -1)
[ "$CODE" -lt 300 ] && green "KYC $NEW_NAME" || red "KYC $NEW_NAME (HTTP $CODE)"

# 17. Private transfer to new investor
echo ""
echo "--- Test 14: Private Transfer Bank -> $NEW_NAME (1,000 DBT) ---"
RESP=$(call POST /transfer "{\"from\":\"bank\",\"to\":\"$NEW_NAME\",\"amount\":1000,\"mode\":\"PRIVATE\"}")
BODY=$(echo "$RESP" | sed '$d')
S=$(jq_field "$BODY" '["success"]')
[ "$S" = "True" ] && green "Private transfer bank->$NEW_NAME" || red "Private transfer bank->$NEW_NAME (success=$S)"

# 18. Access control: write without token should get 423
echo ""
echo "--- Test 15: Write Without Token (expect 423) ---"
OLD_TOKEN=$TOKEN
TOKEN=""
RESP=$(call POST /transfer '{"from":"bank","to":"alice","amount":100,"mode":"PUBLIC"}')
CODE=$(echo "$RESP" | tail -1)
[ "$CODE" = "423" ] && green "Unauthenticated write blocked (423)" || red "Expected 423, got $CODE"
TOKEN=$OLD_TOKEN

# 19. Final balances
echo ""
echo "--- Final Balances ---"
RESP=$(call GET /state)
BODY=$(echo "$RESP" | sed '$d')
echo "$BODY" | python3 -c "
import sys, json
d = json.load(sys.stdin)
bals = d.get('balances', {})
for name in sorted(bals.keys()):
    b = bals[name]
    print(f'  {name:10s} public={b[\"public\"]:>10,}  private={b[\"private\"]:>10,}')
"

echo ""
echo "============================================"
echo "  Results: $PASS passed, $FAIL failed"
echo "============================================"
[ "$FAIL" -eq 0 ] && exit 0 || exit 1
