#!/bin/bash
# Comprehensive API endpoint test.
#
# Assumes Paladin + API server are running.
#   - API_KEY (optional): sent as X-API-Key on every request if set
#   - Captures the session token from /setup and forwards it as
#     X-Session-Token on all write requests (writes return 423 Locked without it).
#
# Usage:
#   ./test-endpoints.sh              # runs full flow, then restart path
#   SKIP_RESTART=1 ./test-endpoints.sh   # stop after the main flow
set -euo pipefail

API="${API_URL:-http://localhost:3001/api}"
API_KEY="${API_KEY:-}"
PASS=0
FAIL=0
SESSION_TOKEN=""

# ---- HTTP helpers ----

_headers() {
  printf -- '-H "Content-Type: application/json"'
  [ -n "$API_KEY" ] && printf -- ' -H "X-API-Key: %s"' "$API_KEY"
  [ -n "$SESSION_TOKEN" ] && printf -- ' -H "X-Session-Token: %s"' "$SESSION_TOKEN"
}

post() {
  local path="$1" body="${2:-}"
  if [ -n "$body" ]; then
    eval curl -s -X POST "$API/$path" $(_headers) -d "'$body'"
  else
    eval curl -s -X POST "$API/$path" $(_headers)
  fi
}

get() {
  local path="$1"
  eval curl -s "$API/$path" $(_headers)
}

check() {
  local name="$1" expected="$2" actual="$3"
  if echo "$actual" | grep -q "$expected"; then
    echo "  ✓ $name"
    PASS=$((PASS + 1))
  else
    echo "  ✗ $name (expected '$expected')"
    echo "    got: $(echo "$actual" | head -c 400)"
    FAIL=$((FAIL + 1))
  fi
}

jq_get() {
  python3 -c "import json,sys; d=json.load(sys.stdin); keys='$1'.split('.'); v=d
for k in keys:
  v = v[int(k)] if k.isdigit() else v.get(k, '')
print(v)" 2>/dev/null || echo ""
}

# ---- Tests ----

echo "=== 1. Health ==="
R=$(get health)
check "health status" '"ok"' "$R"
echo "  (health: $R)"

echo ""
echo "=== 2. Setup (fresh: ~12-15 min | reuse: ~2-3 min) ==="
R=$(post setup)
check "setupComplete" '"setupComplete":true' "$R"
check "has actors" '"bank"' "$R"
check "has contracts" '"token"' "$R"
check "bank public=500000" '"public":500000' "$R"
check "bank private=500000" '"private":500000' "$R"
check "alice kyc=true" '"alice":{"kyc":true' "$R"
check "charlie kyc=false" '"charlie":{"kyc":false' "$R"
check "sessionToken issued" '"sessionToken"' "$R"

# Capture the token so subsequent writes pass requireToken.
SESSION_TOKEN=$(echo "$R" | python3 -c "import json,sys; print(json.load(sys.stdin).get('sessionToken',''))" 2>/dev/null)
if [ -z "$SESSION_TOKEN" ]; then
  echo "  ✗ failed to capture sessionToken from /setup response"; FAIL=$((FAIL+1))
else
  echo "  (sessionToken: ${SESSION_TOKEN:0:12}...)"
fi

echo ""
echo "=== 3. Token lock: write without token returns 423 ==="
UNAUTH=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/freeze/alice" -H 'Content-Type: application/json' ${API_KEY:+-H "X-API-Key: $API_KEY"})
check "locked without token" "423" "$UNAUTH"

echo ""
echo "=== 4. Public transfer: Bank→Alice 10K ==="
R=$(post transfer '{"from":"bank","to":"alice","amount":10000,"mode":"PUBLIC"}')
check "success" '"success":true' "$R"
check "has txHash" '"txHash":"0x' "$R"
check "alice pub=10000" '"public":10000' "$R"
check "bank pub=490000" '"public":490000' "$R"
SUMMARY=$(echo "$R" | python3 -c "import json,sys; print(json.load(sys.stdin).get('transaction',{}).get('uiSummary',''))" 2>/dev/null)
check "summary" "Bank sent 10,000 DBT to Alice" "$SUMMARY"

echo ""
echo "=== 5. Public transfer: Alice→Bob 2K ==="
R=$(post transfer '{"from":"alice","to":"bob","amount":2000,"mode":"PUBLIC"}')
check "success" '"success":true' "$R"
check "alice pub=8000" '"public":8000' "$R"

echo ""
echo "=== 6. Private transfer: Bank→Alice 50K ==="
R=$(post transfer '{"from":"bank","to":"alice","amount":50000,"mode":"PRIVATE"}')
check "success" '"success":true' "$R"
check "has txHash" '"txHash":"0x' "$R"
check "type=SHIELDED" '"SHIELDED_TRANSFER"' "$R"
TX_HASH=$(echo "$R" | python3 -c "import json,sys; print(json.load(sys.stdin).get('transaction',{}).get('txHash',''))" 2>/dev/null)
echo "  (txHash: $TX_HASH)"

echo ""
echo "=== 7. Private transfer: Alice→Bob 20K ==="
R=$(post transfer '{"from":"alice","to":"bob","amount":20000,"mode":"PRIVATE"}')
check "success" '"success":true' "$R"

echo ""
echo "=== 8. Get state (verify balances) ==="
R=$(get state)
BANK_PUB=$(echo "$R" | python3 -c "import json,sys; print(json.load(sys.stdin)['balances']['bank']['public'])" 2>/dev/null)
ALICE_PRIV=$(echo "$R" | python3 -c "import json,sys; print(json.load(sys.stdin)['balances']['alice']['private'])" 2>/dev/null)
BOB_PRIV=$(echo "$R" | python3 -c "import json,sys; print(json.load(sys.stdin)['balances']['bob']['private'])" 2>/dev/null)
check "bank pub=490000" "490000" "$BANK_PUB"
[ "${ALICE_PRIV:-0}" -gt 0 ] && { echo "  ✓ alice has private balance ($ALICE_PRIV)"; PASS=$((PASS+1)); } || { echo "  ✗ alice priv=0"; FAIL=$((FAIL+1)); }
check "bob priv=20000" "20000" "$BOB_PRIV"

echo ""
echo "=== 9. Notes: Alice ==="
R=$(get notes/alice)
NOTE_COUNT=$(echo "$R" | python3 -c "import json,sys; print(len(json.load(sys.stdin).get('notes',[])))" 2>/dev/null)
check "alice has notes" "1" "$NOTE_COUNT"
NOTE_ID=$(echo "$R" | python3 -c "import json,sys; print(json.load(sys.stdin)['notes'][0]['noteId'])" 2>/dev/null)
echo "  (noteId: $NOTE_ID)"

echo ""
echo "=== 10. Decrypt Alice's notes ==="
R=$(post decrypt "{\"investor\":\"alice\",\"noteIds\":[\"$NOTE_ID\"]}")
DECRYPTED=$(echo "$R" | python3 -c "import json,sys; d=json.load(sys.stdin); print(len(d.get('decrypted',[])))" 2>/dev/null)
check "decrypted count > 0" "1" "$DECRYPTED"
AMOUNT=$(echo "$R" | python3 -c "import json,sys; d=json.load(sys.stdin)['decrypted'][0]; print(d['amount'])" 2>/dev/null)
echo "  (decrypted amount: $AMOUNT)"

echo ""
echo "=== 11. KYC reject: Alice→Charlie (not KYC'd) ==="
R=$(post transfer '{"from":"alice","to":"charlie","amount":5000,"mode":"PRIVATE"}')
check "fails" '"success":false' "$R"
check "error set" 'error' "$R"

echo ""
echo "=== 12. Request: Charlie requests KYC ==="
R=$(post request '{"type":"KYC","actor":"charlie"}')
REQ_ID=$(echo "$R" | python3 -c "import json,sys; print(json.load(sys.stdin).get('request',{}).get('id',''))" 2>/dev/null)
check "request created" '"type":"KYC"' "$R"
echo "  (requestId: $REQ_ID)"

echo ""
echo "=== 13. Approve: Bank approves KYC ==="
R=$(post "request/$REQ_ID/approve")
check "success" '"success":true' "$R"
check "charlie kyc=true" '"charlie":{"kyc":true' "$R"

echo ""
echo "=== 14. Alice→Charlie 5K (now KYC'd) ==="
R=$(post transfer '{"from":"alice","to":"charlie","amount":5000,"mode":"PRIVATE"}')
check "success" '"success":true' "$R"

echo ""
echo "=== 15. Freeze Charlie ==="
R=$(post freeze/charlie)
check "success" '"success":true' "$R"
check "charlie frozen" '"frozen":true' "$R"

echo ""
echo "=== 16. Charlie→Bob (frozen, should fail) ==="
R=$(post transfer '{"from":"charlie","to":"bob","amount":2000,"mode":"PRIVATE"}')
check "fails" '"success":false' "$R"

echo ""
echo "=== 17. Clawback Charlie ==="
R=$(post clawback/charlie)
check "success" '"success":true' "$R"
check "type=CLAWBACK" '"CLAWBACK"' "$R"
CHARLIE_PRIV=$(echo "$R" | python3 -c "import json,sys; print(json.load(sys.stdin)['balances']['charlie']['private'])" 2>/dev/null)
check "charlie priv=0" "0" "$CHARLIE_PRIV"

echo ""
echo "=== 18. Final state ==="
R=$(get state)
TXNS=$(echo "$R" | python3 -c "import json,sys; print(len(json.load(sys.stdin).get('transactions',[])))" 2>/dev/null)
[ "${TXNS:-0}" -ge 8 ] && { echo "  ✓ transaction count >= 8 ($TXNS)"; PASS=$((PASS+1)); } || { echo "  ✗ tx count ($TXNS) < 8"; FAIL=$((FAIL+1)); }

if [ "${SKIP_RESTART:-0}" != "1" ]; then
  echo ""
  echo "=== 19. Restart (full redeploy) ==="
  R=$(post restart)
  check "setupComplete still true" '"setupComplete":true' "$R"
  check "bank pub=500000 after restart" '"public":500000' "$R"
  check "bank priv=500000 after restart" '"private":500000' "$R"

  # Fresh alice/bob/charlie have clean balances.
  ALICE_PUB=$(echo "$R" | python3 -c "import json,sys; print(json.load(sys.stdin)['balances']['alice']['public'])" 2>/dev/null || echo 999)
  [ "${ALICE_PUB:-999}" -eq 0 ] && { echo "  ✓ fresh alice has 0 public"; PASS=$((PASS+1)); } || { echo "  ✗ fresh alice pub=${ALICE_PUB} (expected 0)"; FAIL=$((FAIL+1)); }

  check "new sessionToken" '"sessionToken"' "$R"
  NEW_TOKEN=$(echo "$R" | python3 -c "import json,sys; print(json.load(sys.stdin).get('sessionToken',''))" 2>/dev/null)
  [ "$NEW_TOKEN" != "$SESSION_TOKEN" ] && { echo "  ✓ sessionToken rotated"; PASS=$((PASS+1)); } || { echo "  ✗ sessionToken unchanged"; FAIL=$((FAIL+1)); }
  SESSION_TOKEN="$NEW_TOKEN"

  echo ""
  echo "=== 20. Post-restart: Bank→Alice 7K public (fresh alice) ==="
  R=$(post transfer '{"from":"bank","to":"alice","amount":7000,"mode":"PUBLIC"}')
  check "success" '"success":true' "$R"
  check "alice pub=7000" '"public":7000' "$R"
fi

echo ""
echo "================================"
echo "PASSED: $PASS  FAILED: $FAIL"
[ "$FAIL" -eq 0 ] && echo "ALL TESTS PASSED" || { echo "SOME TESTS FAILED"; exit 1; }
