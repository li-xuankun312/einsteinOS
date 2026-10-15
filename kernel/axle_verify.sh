#!/bin/bash
# einsteinOS theorem verifier — Axle Lean 4 API
# Shadow exec on real machine replaces ARG placeholders
#
# Usage by kernel:
#   axle_verify.sh "$FORMAL_STATEMENT" "$PROOF"
# Exit 0 = verified, Exit 1 = failed, Exit 2 = error

FORMAL_STATEMENT="${1:?usage: axle_verify.sh FORMAL_STATEMENT PROOF}"
PROOF="${2:?usage: axle_verify.sh FORMAL_STATEMENT PROOF}"

# Cookie/CSRF — refreshed by kernel when expired
AXLE_COOKIE="${AXLE_COOKIE:-provertools_session=eyJicm93c2VyX2FwaV9jc3JmIjogIllGYm5BZ2FQS3ZQdTgwR21KUFVaTkhWVzVHY0hfWF9ZYnhFZnhKcElDdU0ifQ==.atBFEQ.XT1vAvHEhv3QOhNsvZlJwDItLcQ}"
AXLE_CSRF="${AXLE_CSRF:-YFbnAgaPKvPu80GmJPUZNHVW5GcH_X_YbxEfxJpICuM}"

# Build JSON payload with python (handles escaping)
PAYLOAD=$(python3 -c "
import json, sys
print(json.dumps({
    'formal_statement': sys.argv[1],
    'content': sys.argv[2],
    'permitted_sorries': [],
    'ignore_imports': True,
    'allow_statement_axioms': True,
    'allow_bv_decide': True,
    'environment': 'lean-4.35.0',
    'timeout_seconds': 120
}))
" "$FORMAL_STATEMENT" "$PROOF")

RESULT=$(curl -s --max-time 180 \
  'https://axle.axiommath.ai/api/v1/verify_proof' \
  -H 'content-type: application/json' \
  -b "$AXLE_COOKIE" \
  -H "x-browser-api-csrf: $AXLE_CSRF" \
  -H 'x-request-source: web' \
  -H 'origin: https://axle.axiommath.ai' \
  -H 'referer: https://axle.axiommath.ai/verify_proof' \
  --data-raw "$PAYLOAD")

if [ -z "$RESULT" ]; then
  echo "ERROR: no response from axle"
  exit 2
fi

# Parse result
python3 -c "
import json, sys
try:
    d = json.loads(sys.argv[1])
except:
    print('ERROR: invalid JSON response')
    sys.exit(2)

okay = d.get('okay', False)
ms = d.get('timings', {}).get('total_ms', '?')
tool_errs = d.get('tool_messages', {}).get('errors', [])
lean_errs = d.get('lean_messages', {}).get('errors', [])
failed = d.get('failed_declarations', [])

print(f'okay={okay} time={ms}ms')
for e in tool_errs + lean_errs:
    print(f'  ERR: {e}')
if failed:
    print(f'  failed: {failed}')

sys.exit(0 if okay else 1)
" "$RESULT"
