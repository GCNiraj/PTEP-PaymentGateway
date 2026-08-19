#!/usr/bin/env bash
set -euo pipefail

# Intra-only DKPG test script:
# 1) beneficiary inquiry
# 2) initiate intra transfer
# 3) intra status check
#
# Usage:
#   chmod +x scripts/test_intra_dkpg.sh
#   ./scripts/test_intra_dkpg.sh
#
# Optional env overrides:
#   BASE_URL=http://localhost:5001
#   SOURCE_APP=SRC_AVS_0201
#   AMOUNT=100.00
#   BENE_BANK_CODE=1060
#   BENE_ACCOUNT_NUMBER=100100365856
#   BENE_CUST_NAME="Beneficiary Name"
#   SOURCE_ACCOUNT_NAME="Remitter Name"
#   SOURCE_ACCOUNT_NUMBER=100100148337
#   NARRATION="Intra transfer test"
#   STATUS_WAIT_SECONDS=2
#   AUTO_FILL_FROM_RESPONSE=1
#   SAVE_ARTIFACTS=1
#   ARTIFACT_DIR=tmp/intra_test_20260217T120000Z

require_cmd() {
  local cmd="$1"
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "error: missing required command: $cmd"
    exit 1
  fi
}

require_cmd curl
require_cmd jq

BASE_URL="${BASE_URL:-http://localhost:5001}"
BASE_URL="${BASE_URL%/}"

SOURCE_APP="${SOURCE_APP:-SRC_AVS_0201}"
AMOUNT="${AMOUNT:-100.00}"
CURRENCY="${CURRENCY:-BTN}"
PAYMENT_TYPE="${PAYMENT_TYPE:-INTRA}"

BENE_BANK_CODE="${BENE_BANK_CODE:-1060}"
BENE_ACCOUNT_NUMBER="${BENE_ACCOUNT_NUMBER:-100100365856}"
BENE_CUST_NAME="${BENE_CUST_NAME:-Beneficiary Name}"

SOURCE_ACCOUNT_NAME="${SOURCE_ACCOUNT_NAME:-Remitter Name}"
SOURCE_ACCOUNT_NUMBER="${SOURCE_ACCOUNT_NUMBER:-100100148337}"
NARRATION="${NARRATION:-Intra transfer test}"

STATUS_WAIT_SECONDS="${STATUS_WAIT_SECONDS:-2}"
AUTO_FILL_FROM_RESPONSE="${AUTO_FILL_FROM_RESPONSE:-1}"
SAVE_ARTIFACTS="${SAVE_ARTIFACTS:-1}"
ARTIFACT_DIR="${ARTIFACT_DIR:-tmp/intra_test_$(date -u +%Y%m%dT%H%M%SZ)}"

LAST_HTTP_CODE=""
LAST_BODY=""

new_request_id() {
  local step="$1"
  printf "REQ-INTRA-%s-%s-%s" "$step" "$(date -u +%Y%m%d%H%M%S)" "$(tr -dc '0-9' </dev/urandom | head -c 4)"
}

new_stan() {
  tr -dc '0-9' </dev/urandom | head -c 6
}

save_artifact() {
  local step="$1"
  local kind="$2"
  local content="$3"

  if [[ "$SAVE_ARTIFACTS" != "1" ]]; then
    return 0
  fi

  mkdir -p "$ARTIFACT_DIR"
  local path="$ARTIFACT_DIR/${step,,}_${kind}.json"
  if echo "$content" | jq . >/dev/null 2>&1; then
    echo "$content" | jq . >"$path"
  else
    echo "$content" >"$path"
  fi
}

post_json() {
  local step="$1"
  local endpoint="$2"
  local payload="$3"

  local tmp_body
  tmp_body="$(mktemp)"

  save_artifact "$step" "request" "$payload"

  LAST_HTTP_CODE="$(curl -sS -o "$tmp_body" -w '%{http_code}' \
    -X POST "$BASE_URL$endpoint" \
    -H 'Content-Type: application/json' \
    -d "$payload")"
  LAST_BODY="$(cat "$tmp_body")"
  rm -f "$tmp_body"
  save_artifact "$step" "response" "$LAST_BODY"

  echo
  echo "[$step] POST $endpoint"
  echo "[$step] HTTP: $LAST_HTTP_CODE"
  echo "[$step] Response:"
  echo "$LAST_BODY" | jq -C . 2>/dev/null || echo "$LAST_BODY"

  if [[ "$LAST_HTTP_CODE" -lt 200 || "$LAST_HTTP_CODE" -ge 300 ]]; then
    echo "[$step] failed: non-2xx HTTP status"
    exit 1
  fi

  local response_code
  response_code="$(echo "$LAST_BODY" | jq -r '.response_code // .response.response_code // empty' 2>/dev/null || true)"
  if [[ -n "$response_code" && "$response_code" != "0000" ]]; then
    local response_message
    response_message="$(echo "$LAST_BODY" | jq -r '.response_message // .response.response_message // .response_description // .response.response_description // empty' 2>/dev/null || true)"
    echo "[$step] failed: response_code=$response_code ${response_message:+message=$response_message}"
    exit 1
  fi
}

echo "Running DKPG intra flow against $BASE_URL"

INQUIRY_REQUEST_ID="$(new_request_id INQ)"
INQUIRY_STAN="$(new_stan)"
INQUIRY_PAYLOAD="$(
  jq -n \
    --arg request_id "$INQUIRY_REQUEST_ID" \
    --arg stan_number "$INQUIRY_STAN" \
    --arg source_app "$SOURCE_APP" \
    --arg amount "$AMOUNT" \
    --arg currency "$CURRENCY" \
    --arg bene_bank_code "$BENE_BANK_CODE" \
    --arg bene_account_number "$BENE_ACCOUNT_NUMBER" \
    --arg source_account_name "$SOURCE_ACCOUNT_NAME" \
    --arg source_account_number "$SOURCE_ACCOUNT_NUMBER" \
    '{
      request_id: $request_id,
      stan_number: $stan_number,
      source_app: $source_app,
      amount: $amount,
      currency: $currency,
      bene_bank_code: $bene_bank_code,
      bene_account_number: $bene_account_number,
      source_account_name: $source_account_name,
      source_account_number: $source_account_number,
      soure_account_number: $source_account_number
    }'
)"

post_json "INQUIRY" "/api/dkpg/beneficiary/account-inquiry" "$INQUIRY_PAYLOAD"

INQUIRY_ID="$(echo "$LAST_BODY" | jq -r '.response_data.inquiry_id // .response.response_data.inquiry_id // empty' 2>/dev/null || true)"
if [[ -z "$INQUIRY_ID" ]]; then
  echo "[INQUIRY] failed: inquiry_id not found in response"
  exit 1
fi
echo "[INQUIRY] inquiry_id=$INQUIRY_ID"

TRANSFER_REQUEST_ID="$(new_request_id INIT)"
TRANSFER_STAN="$(new_stan)"
TRANSACTION_DATETIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
TRANSFER_PAYLOAD="$(
  jq -n \
    --arg request_id "$TRANSFER_REQUEST_ID" \
    --arg inquiry_id "$INQUIRY_ID" \
    --arg transaction_datetime "$TRANSACTION_DATETIME" \
    --arg stan_number "$TRANSFER_STAN" \
    --arg source_app "$SOURCE_APP" \
    --arg transaction_amount "$AMOUNT" \
    --arg currency "$CURRENCY" \
    --arg payment_type "$PAYMENT_TYPE" \
    --arg source_account_name "$SOURCE_ACCOUNT_NAME" \
    --arg source_account_number "$SOURCE_ACCOUNT_NUMBER" \
    --arg bene_cust_name "$BENE_CUST_NAME" \
    --arg bene_account_number "$BENE_ACCOUNT_NUMBER" \
    --arg bene_bank_code "$BENE_BANK_CODE" \
    --arg narration "$NARRATION" \
    '{
      request_id: $request_id,
      inquiry_id: $inquiry_id,
      transaction_datetime: $transaction_datetime,
      stan_number: $stan_number,
      source_app: $source_app,
      transaction_amount: ($transaction_amount|tonumber),
      currency: $currency,
      payment_type: $payment_type,
      source_account_name: $source_account_name,
      source_account_number: $source_account_number,
      bene_cust_name: $bene_cust_name,
      bene_account_number: $bene_account_number,
      bene_bank_code: $bene_bank_code,
      narration: $narration
    }'
)"

post_json "INITIATE" "/api/dkpg/initiate/transaction" "$TRANSFER_PAYLOAD"

STATUS_REFERENCE_NO="$TRANSFER_STAN"
if [[ "$AUTO_FILL_FROM_RESPONSE" == "1" ]]; then
  REF_FROM_RESPONSE="$(echo "$LAST_BODY" | jq -r \
    '.response_data.reference_no //
     .response.response_data.reference_no //
     .response_data.stan_number //
     .response.response_data.stan_number //
     .response_data.transaction_id //
     .response.response_data.transaction_id //
     empty' 2>/dev/null || true)"
  if [[ -n "$REF_FROM_RESPONSE" ]]; then
    STATUS_REFERENCE_NO="$REF_FROM_RESPONSE"
  fi
fi

echo "[INITIATE] reference_no(for status)=$STATUS_REFERENCE_NO"

if [[ "$STATUS_WAIT_SECONDS" -gt 0 ]]; then
  echo "[STATUS] waiting ${STATUS_WAIT_SECONDS}s before checking status..."
  sleep "$STATUS_WAIT_SECONDS"
fi

STATUS_REQUEST_ID="$(new_request_id STAT)"
TRANSACTION_DATE="$(date -u +%Y-%m-%d)"
STATUS_PAYLOAD="$(
  jq -n \
    --arg request_id "$STATUS_REQUEST_ID" \
    --arg reference_no "$STATUS_REFERENCE_NO" \
    --arg transaction_date "$TRANSACTION_DATE" \
    --arg bene_account_number "$BENE_ACCOUNT_NUMBER" \
    '{
      request_id: $request_id,
      reference_no: $reference_no,
      transaction_date: $transaction_date,
      bene_account_number: $bene_account_number
    }'
)"

post_json "STATUS" "/api/dkpg/intra-transaction/status" "$STATUS_PAYLOAD"

echo
echo "Intra flow completed."
echo "inquiry_id=$INQUIRY_ID"
echo "reference_no=$STATUS_REFERENCE_NO"
if [[ "$SAVE_ARTIFACTS" == "1" ]]; then
  echo "artifacts=$ARTIFACT_DIR"
fi
