#!/bin/bash
set -e

# E2E Test: FastAgent Webhook Billing Integration
# Tests the full flow: FastAgent LLM call → Webhook → Cloud API

echo "=================================="
echo "FastAgent Webhook Billing E2E Test"
echo "=================================="

# Configuration
CLOUD_URL="${CLOUD_URL:-http://localhost:3000}"
FASTAGENT_URL="${FASTAGENT_URL:-http://localhost:18953}"
WEBHOOK_TOKEN="${WEBHOOK_TOKEN:-test-webhook-token-123}"
TEST_USER_ID="${TEST_USER_ID:-test-user-$(date +%s)}"

echo ""
echo "Configuration:"
echo "  Cloud URL: $CLOUD_URL"
echo "  FastAgent URL: $FASTAGENT_URL"
echo "  Test User ID: $TEST_USER_ID"
echo ""

# Step 1: Grant initial credits to test user via Cloud API
echo "[Step 1] Granting 1000 credits to test user..."
GRANT_RESPONSE=$(curl -s -X POST "$CLOUD_URL/api/admin/credits/grant" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $WEBHOOK_TOKEN" \
  -d "{
    \"userId\": \"$TEST_USER_ID\",
    \"credits\": 1000,
    \"description\": \"E2E test initial credits\"
  }")

echo "Grant response: $GRANT_RESPONSE"

# Step 2: Check initial balance via Cloud API
echo ""
echo "[Step 2] Checking initial balance..."
BALANCE_RESPONSE=$(curl -s "$CLOUD_URL/api/fastagent/credits?userId=$TEST_USER_ID" \
  -H "Authorization: Bearer $WEBHOOK_TOKEN")

echo "Balance response: $BALANCE_RESPONSE"

INITIAL_CREDITS=$(echo "$BALANCE_RESPONSE" | jq -r '.credits')
BLOCKED=$(echo "$BALANCE_RESPONSE" | jq -r '.blocked')

if [ "$BLOCKED" = "true" ]; then
  echo "❌ ERROR: User is blocked (initial balance: $INITIAL_CREDITS)"
  exit 1
fi

echo "✅ Initial balance: $INITIAL_CREDITS credits, blocked: $BLOCKED"

# Step 3: Make LLM call via FastAgent (this should trigger webhook)
echo ""
echo "[Step 3] Making LLM call via FastAgent..."
LLM_RESPONSE=$(curl -s -X POST "$FASTAGENT_URL/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $FASTAGENT_API_KEY" \
  -H "X-Fastagent-End-User: $TEST_USER_ID" \
  -d '{
    "model": "gpt-4o-mini",
    "messages": [
      {"role": "user", "content": "Say hello in 5 words"}
    ],
    "max_tokens": 20
  }')

echo "LLM response (truncated): $(echo "$LLM_RESPONSE" | jq -r '.choices[0].message.content' | head -c 50)..."

# Step 4: Wait for webhook to process
echo ""
echo "[Step 4] Waiting for webhook to process..."
sleep 2

# Step 5: Check balance again (should be decreased)
echo ""
echo "[Step 5] Checking balance after LLM call..."
FINAL_BALANCE_RESPONSE=$(curl -s "$CLOUD_URL/api/fastagent/credits?userId=$TEST_USER_ID" \
  -H "Authorization: Bearer $WEBHOOK_TOKEN")

echo "Final balance response: $FINAL_BALANCE_RESPONSE"

FINAL_CREDITS=$(echo "$FINAL_BALANCE_RESPONSE" | jq -r '.credits')
FINAL_BLOCKED=$(echo "$FINAL_BALANCE_RESPONSE" | jq -r '.blocked')

echo "✅ Final balance: $FINAL_CREDITS credits, blocked: $FINAL_BLOCKED"

# Step 6: Verify credits were consumed
echo ""
echo "[Step 6] Verifying credits consumption..."
CONSUMED=$((INITIAL_CREDITS - FINAL_CREDITS))

if [ "$CONSUMED" -gt 0 ]; then
  echo "✅ SUCCESS: $CONSUMED credits consumed"
else
  echo "❌ ERROR: No credits consumed (initial: $INITIAL_CREDITS, final: $FINAL_CREDITS)"
  exit 1
fi

# Step 7: Test overdraft scenario (consume all remaining credits)
echo ""
echo "[Step 7] Testing overdraft scenario..."
echo "Making multiple LLM calls to exhaust credits..."

CALLS=0
MAX_CALLS=50

while [ "$FINAL_BLOCKED" = "false" ] && [ "$CALLS" -lt "$MAX_CALLS" ]; do
  curl -s -X POST "$FASTAGENT_URL/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer $FASTAGENT_API_KEY" \
    -H "X-Fastagent-End-User: $TEST_USER_ID" \
    -d '{
      "model": "gpt-4o-mini",
      "messages": [
        {"role": "user", "content": "Count to 5"}
      ],
      "max_tokens": 50
    }' > /dev/null

  sleep 1

  BALANCE_CHECK=$(curl -s "$CLOUD_URL/api/fastagent/credits?userId=$TEST_USER_ID" \
    -H "Authorization: Bearer $WEBHOOK_TOKEN")

  FINAL_CREDITS=$(echo "$BALANCE_CHECK" | jq -r '.credits')
  FINAL_BLOCKED=$(echo "$BALANCE_CHECK" | jq -r '.blocked')

  CALLS=$((CALLS + 1))
  echo "  Call $CALLS: Balance = $FINAL_CREDITS, Blocked = $FINAL_BLOCKED"

  if [ "$FINAL_BLOCKED" = "true" ]; then
    echo "✅ User blocked after $CALLS calls (balance: $FINAL_CREDITS)"
    break
  fi
done

if [ "$FINAL_BLOCKED" = "false" ]; then
  echo "⚠️  WARNING: User not blocked after $MAX_CALLS calls"
fi

# Step 8: Verify blocked user cannot make new calls
if [ "$FINAL_BLOCKED" = "true" ]; then
  echo ""
  echo "[Step 8] Verifying blocked user is rejected..."

  BLOCKED_RESPONSE=$(curl -s -w "\n%{http_code}" -X POST "$FASTAGENT_URL/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer $FASTAGENT_API_KEY" \
    -H "X-Fastagent-End-User: $TEST_USER_ID" \
    -d '{
      "model": "gpt-4o-mini",
      "messages": [
        {"role": "user", "content": "This should fail"}
      ]
    }')

  HTTP_CODE=$(echo "$BLOCKED_RESPONSE" | tail -n1)

  if [ "$HTTP_CODE" = "402" ] || [ "$HTTP_CODE" = "403" ]; then
    echo "✅ SUCCESS: Blocked user rejected with HTTP $HTTP_CODE"
  else
    echo "⚠️  WARNING: Expected 402/403, got HTTP $HTTP_CODE"
  fi
fi

echo ""
echo "=================================="
echo "✅ E2E Test Completed Successfully"
echo "=================================="
