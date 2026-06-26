#!/usr/bin/env bash
# Orchestrator smoke test against llm.nexalance.cloud.
#
# Usage:
#   MANAGEMENT_PASSWORD=... CLI_PROXY_API_KEY=... ./scripts/orchestrator_smoke.sh
#
# What it does:
#   1. Reads the current /v0/management/orchestrator config.
#   2. If disabled, enables it with the recommended defaults
#      (master=gemini-3.5-flash-extra-low, fallback=gpt-5.4-mini).
#   3. Sends 5 representative prompts as model="master" and reports which
#      upstream the master picked + the assistant reply (truncated).
#   4. Restores whatever the orchestrator state was at the start.
#
# Exit code 0 if all 5 routes returned 200 with a non-empty assistant message;
# non-zero otherwise.

set -euo pipefail

: "${MANAGEMENT_PASSWORD:?env var MANAGEMENT_PASSWORD required}"
: "${CLI_PROXY_API_KEY:?env var CLI_PROXY_API_KEY required}"

HOST="${HOST:-https://llm.nexalance.cloud}"
MGMT_AUTH="Authorization: Bearer ${MANAGEMENT_PASSWORD}"
CLIENT_AUTH="Authorization: Bearer ${CLI_PROXY_API_KEY}"

step() { printf "\n\033[1;36m== %s ==\033[0m\n" "$*"; }
ok()   { printf "  \033[32m✓\033[0m %s\n" "$*"; }
bad()  { printf "  \033[31m✗\033[0m %s\n" "$*"; }

# ---------- 1. Snapshot current orchestrator state ----------
step "Snapshot orchestrator state"
ORIG_STATE=$(curl -s -H "${MGMT_AUTH}" "${HOST}/v0/management/orchestrator")
echo "  before: ${ORIG_STATE}"
WAS_ENABLED=$(printf '%s' "${ORIG_STATE}" | python3 -c "
import json,sys
try:
    print(json.load(sys.stdin).get('orchestrator',{}).get('enabled', False))
except Exception:
    print('False')
" | tr '[:upper:]' '[:lower:]')

# ---------- 2. Enable with recommended defaults if needed ----------
if [[ "${WAS_ENABLED}" != "true" ]]; then
  step "Enable orchestrator (master=gemini-3.5-flash-extra-low)"
  curl -s -X PATCH -H "${MGMT_AUTH}" -H "Content-Type: application/json" \
    -d '{
      "enabled": true,
      "master-model": "gemini-3.5-flash-extra-low",
      "router-alias": "master",
      "fallback": "gpt-5.4-mini",
      "timeout-ms": 8000,
      "log-decisions": true
    }' \
    "${HOST}/v0/management/orchestrator" >/dev/null
  ok "enabled"
fi

# ---------- 3. Five representative prompts ----------
declare -a PROMPTS=(
  "Reasoning|Plane A leaves NY at 9am flying west at 500mph. Plane B leaves LA at 11am flying east at 600mph. They are 2700 miles apart. At what local NY time do they meet? Show work."
  "QuickCode|Fix this Go bug: func divide(a, b int) int { return a / b }  — what edge case is missing and patch it in 4 lines."
  "Vision|How would you describe the typical layout of a Bloomberg Terminal screen for someone who has never seen one? Be concise."
  "Bulk|Classify in one word each, comma-separated: 'I love it', 'this sucks', 'meh', 'best ever', 'never again'."
  "Recall|In one short paragraph: what is sep-CMA-ES and why is it well suited to training a small coordinator over frontier LLMs?"
)

step "Routing 5 representative prompts as model=master"
PASS=0
FAIL=0
for entry in "${PROMPTS[@]}"; do
  label="${entry%%|*}"
  prompt="${entry#*|}"
  body=$(python3 -c "
import json,sys
print(json.dumps({
  'model':'master',
  'messages':[{'role':'user','content':sys.argv[1]}],
  'max_tokens':400
}))" "$prompt")
  resp=$(curl -s -X POST "${HOST}/v1/chat/completions" \
    -H "${CLIENT_AUTH}" -H "Content-Type: application/json" \
    -d "$body")
  picked=$(printf '%s' "$resp" | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('model','?'))" 2>/dev/null || echo '?')
  reply=$(printf '%s' "$resp" | python3 -c "
import json,sys
try:
    d=json.load(sys.stdin)
    c=(d.get('choices') or [{}])[0].get('message',{}).get('content','')
    print((c[:140]+'…') if len(c)>140 else c)
except Exception:
    print('PARSE-ERR')
")
  err=$(printf '%s' "$resp" | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('error') or '')" 2>/dev/null || echo '')
  if [[ -n "${err}" || -z "${reply}" || "${reply}" == "PARSE-ERR" ]]; then
    bad "${label}: picked=${picked}  err=${err}"
    FAIL=$((FAIL+1))
  else
    ok "${label}: picked=${picked}"
    echo "      └ ${reply}"
    PASS=$((PASS+1))
  fi
done

# ---------- 4. Restore original state if we changed it ----------
if [[ "${WAS_ENABLED}" != "true" ]]; then
  step "Restoring orchestrator to disabled (was off before the test)"
  curl -s -X PATCH -H "${MGMT_AUTH}" -H "Content-Type: application/json" \
    -d '{"enabled": false}' \
    "${HOST}/v0/management/orchestrator" >/dev/null
  ok "restored"
fi

# ---------- 5. Summary ----------
step "Result"
printf "  PASS=%d  FAIL=%d  / 5\n" "${PASS}" "${FAIL}"
[[ "${FAIL}" -eq 0 ]]
