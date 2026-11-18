#!/usr/bin/env bash
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# probe_summaries.sh executes §2 item 3 of docs/test-plans/2026-11-16.md
# against a live grain daemon with curl under:
#   - admin (verified IAP JWT + local access code)
#   - repo-scoped human role (limited@example.com -> custom/widgets-read)
#   - ci-only human role (ci-only@example.com -> custom/ci-only, expect 403)
#   - repo-scoped external agent token (gma_..., custom/widgets-read)
#   - ci-only external agent token (gma_..., custom/ci-only, expect 403)
#   - acting agent task (X-Grain-Acting-Task: 5000, api:tasks:read@acme/widgets)
set -euo pipefail

BASE_URL="${1:-http://127.0.0.1:4290}"
LOCAL_TOKEN="${2:?usage: probe_summaries.sh <base-url> <local-api-token> <iap-tokens-json> [label]}"
IAP_JSON="${3:?usage: probe_summaries.sh <base-url> <local-api-token> <iap-tokens-json> [label]}"
LABEL="${4:-daemon}"

TMPDIR_PROBE=$(mktemp -d)
touch "$TMPDIR_PROBE/created_ids"
cleanup() {
  if [ -f "$TMPDIR_PROBE/created_ids" ]; then
    while IFS= read -r tid; do
      [ -n "$tid" ] && curl -sS -X DELETE -H "Authorization: Bearer $LOCAL_TOKEN" \
        "$BASE_URL/api/external-agents/tokens/$tid" >/dev/null 2>&1 || true
    done < "$TMPDIR_PROBE/created_ids"
  fi
  rm -rf "$TMPDIR_PROBE"
}
trap cleanup EXIT

ADMIN_JWT=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["admin@example.com"])' "$IAP_JSON")
LIMITED_JWT=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["limited@example.com"])' "$IAP_JSON")
CIONLY_JWT=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["ci-only@example.com"])' "$IAP_JSON")

echo "=== GET /api/tasks/summaries curl probes ($LABEL) ==="

# Mint external agent tokens via admin IAP JWT.
mint_token() {
  local name="$1" scope_set="$2"
  local resp
  resp=$(curl -fsS -X POST \
    -H "X-Goog-IAP-JWT-Assertion: $ADMIN_JWT" \
    -H "Content-Type: application/json" \
    -d "{\"name\":\"$name\",\"scopeSet\":\"$scope_set\"}" \
    "$BASE_URL/api/external-agents/tokens")
  local id tok
  id=$(python3 -c 'import json,sys; d=json.loads(sys.argv[1]); print(d["id"])' "$resp")
  tok=$(python3 -c 'import json,sys; d=json.loads(sys.argv[1]); print(d["token"])' "$resp")
  echo "$id" >> "$TMPDIR_PROBE/created_ids"
  printf '%s' "$tok"
}

GMA_WIDGETS=$(mint_token "walk-widgets-reader-$$" "custom/widgets-read")
GMA_CIONLY=$(mint_token "walk-ci-only-$$" "custom/ci-only")
echo "minted external agent tokens: widgets=${GMA_WIDGETS:0:10}... ci-only=${GMA_CIONLY:0:10}..."

# Helper: do_curl <out_prefix> <path> [curl header args...]
do_curl() {
  local prefix="$1" path="$2"
  shift 2
  local status
  status=$(curl -sS -o "$TMPDIR_PROBE/$prefix.body" -D "$TMPDIR_PROBE/$prefix.hdr" \
    -w '%{http_code}' "$@" "$BASE_URL$path")
  printf '%s' "$status"
}

hdr_val() {
  local file="$1" name="$2"
  awk -v k="$(printf '%s' "$name" | tr '[:upper:]' '[:lower:]')" '
    {
      sub(/\r$/, "")
      idx = index($0, ":")
      if (idx > 0) {
        h = tolower(substr($0, 1, idx-1))
        if (h == k) {
          v = substr($0, idx+1)
          sub(/^ +/, "", v)
          print v
          exit
        }
      }
    }' "$file"
}

assert_eq() {
  local label="$1" got="$2" want="$3"
  if [ "$got" != "$want" ]; then
    echo "FAIL [$label]: got='$got' want='$want'" >&2
    exit 1
  fi
}

# Verify identities via /api/whoami
check_whoami() {
  local label="$1"
  shift
  local st
  st=$(do_curl "whoami_$label" "/api/whoami" "$@")
  assert_eq "whoami $label status" "$st" "200"
  python3 -c '
import json, sys
d = json.load(open(sys.argv[1]))
a = d.get("actor", {})
print("  whoami [%s]: kind=%s id=%s source=%s verified=%s allowed=%s" % (
    sys.argv[2], a.get("kind"), a.get("id"), a.get("source"), d.get("verified"), d.get("allowed")))
' "$TMPDIR_PROBE/whoami_$label.body" "$label"
}

check_whoami "admin-iap" -H "X-Goog-IAP-JWT-Assertion: $ADMIN_JWT"
check_whoami "limited-iap" -H "X-Goog-IAP-JWT-Assertion: $LIMITED_JWT"
check_whoami "ci-only-iap" -H "X-Goog-IAP-JWT-Assertion: $CIONLY_JWT"

# 1. Refused callers (lacking tasks:read) -> 403
for pair in "ci-only-iap|X-Goog-IAP-JWT-Assertion: $CIONLY_JWT" "gma-ci-only|Authorization: Bearer $GMA_CIONLY"; do
  name="${pair%%|*}"
  hdr="${pair#*|}"
  st=$(do_curl "deny_$name" "/api/tasks/summaries?ids=4997" -H "$hdr")
  assert_eq "$name refused status" "$st" "403"
  cc=$(hdr_val "$TMPDIR_PROBE/deny_$name.hdr" "Cache-Control")
  echo "  PASS [$name]: 403 Forbidden (Cache-Control: $cc)"
done

# 2. Core probe function for an authorized caller.
# Usage: probe_caller <name> <mode: admin|widgets> <curl header args...>
probe_caller() {
  local name="$1" mode="$2"
  shift 2
  local hdrs=("$@")

  # 2a. Mixed ids: 4997 (acme/widgets), 4998 (acme/secret), 4999 (no-repo), 999999 (missing)
  local st
  st=$(do_curl "${name}_mixed" "/api/tasks/summaries?ids=4997,4998,4999,999999" "${hdrs[@]}")
  assert_eq "$name mixed status" "$st" "200"
  python3 - "$TMPDIR_PROBE/${name}_mixed.body" "$name" "$mode" <<'PY'
import json, sys
body_path, name, mode = sys.argv[1:4]
raw = open(body_path).read()
data = json.loads(raw)
tasks = data["tasks"]
keys = sorted(tasks.keys())
if mode == "admin":
    assert keys == ["4997", "4998", "4999"], f"{name}: got keys {keys}"
else:
    assert keys == ["4997"], f"{name}: got keys {keys}, want ['4997']"
    assert "Secret body" not in raw, f"{name}: leaked secret task body!"
    assert "No-repo body" not in raw, f"{name}: leaked no-repo task body!"
t4997 = tasks["4997"]
c_len = len(t4997.get("comments") or [])
a_len = len(t4997.get("attempts") or [])
assert c_len == 20, f"{name}: default comments_limit gave {c_len}, want 20"
assert a_len == 10, f"{name}: default attempts_limit gave {a_len}, want 10"
assert t4997.get("olderComments", 0) >= 5, f"{name}: olderComments={t4997.get('olderComments')}"
print(f"  PASS [{name}] mixed ids -> keys={keys} (default limits: comments={c_len} (+{t4997.get('olderComments')} older), attempts={a_len})")
PY

  # 2b. Limit clamping (999 -> clamped to 500, returning all 25+ comments & 12 attempts)
  st=$(do_curl "${name}_clamp" "/api/tasks/summaries?ids=4997&comments_limit=999&attempts_limit=999" "${hdrs[@]}")
  assert_eq "$name clamp status" "$st" "200"
  python3 - "$TMPDIR_PROBE/${name}_clamp.body" "$name" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))["tasks"]["4997"]
assert len(d["comments"]) >= 25 and len(d["attempts"]) >= 12, f"{sys.argv[2]}: clamp failed {len(d['comments'])}/{len(d['attempts'])}"
PY

  # 2c. Identical omission for unreadable vs no-repo vs missing (scoped callers)
  if [ "$mode" = "widgets" ]; then
    local st_f st_n st_m et_f et_n et_m
    st_f=$(do_curl "${name}_omit_foreign" "/api/tasks/summaries?ids=4998" "${hdrs[@]}")
    st_n=$(do_curl "${name}_omit_norepo"  "/api/tasks/summaries?ids=4999" "${hdrs[@]}")
    st_m=$(do_curl "${name}_omit_missing" "/api/tasks/summaries?ids=999999" "${hdrs[@]}")
    assert_eq "$name omit foreign status" "$st_f" "200"
    assert_eq "$name omit norepo status"  "$st_n" "200"
    assert_eq "$name omit missing status" "$st_m" "200"
    cmp -s "$TMPDIR_PROBE/${name}_omit_foreign.body" "$TMPDIR_PROBE/${name}_omit_missing.body" || {
      echo "FAIL [$name]: foreign vs missing body differed!" >&2; exit 1
    }
    cmp -s "$TMPDIR_PROBE/${name}_omit_norepo.body" "$TMPDIR_PROBE/${name}_omit_missing.body" || {
      echo "FAIL [$name]: no-repo vs missing body differed!" >&2; exit 1
    }
    et_f=$(hdr_val "$TMPDIR_PROBE/${name}_omit_foreign.hdr" "ETag")
    et_n=$(hdr_val "$TMPDIR_PROBE/${name}_omit_norepo.hdr" "ETag")
    et_m=$(hdr_val "$TMPDIR_PROBE/${name}_omit_missing.hdr" "ETag")
    assert_eq "$name omit foreign vs missing ETag" "$et_f" "$et_m"
    assert_eq "$name omit norepo vs missing ETag"  "$et_n" "$et_m"
    echo "  PASS [$name] identical omission: foreign(4998) == no-repo(4999) == missing(999999) -> 200 $(tr -d '\n' < "$TMPDIR_PROBE/${name}_omit_missing.body") ETag=$et_m"
  fi

  # 2d. 50 unique ids (+ duplicate -> 51 tokens, 50 unique) -> 200; 51 unique ids -> 400
  local ids50 ids51
  ids50=$(python3 -c 'print(",".join(str(i) for i in range(1,51)) + ",1")')
  ids51=$(python3 -c 'print(",".join(str(i) for i in range(1,52)))')
  st=$(do_curl "${name}_50" "/api/tasks/summaries?ids=$ids50&comments_limit=1&attempts_limit=1" "${hdrs[@]}")
  assert_eq "$name 50 unique (+dup) status" "$st" "200"
  st=$(do_curl "${name}_51" "/api/tasks/summaries?ids=$ids51" "${hdrs[@]}")
  assert_eq "$name 51 unique status" "$st" "400"

  # 2e. Malformed ids and bad query params -> 400
  local bad_queries=(
    ""
    "ids="
    "ids=4997,"
    "ids=,4997"
    "ids=4997,,4998"
    "ids=bad/id"
    "ids=../1"
    "ids=a..b"
    "ids=bad%20id"
    "ids=-1"
    "ids=4997&comments_limit=0"
    "ids=4997&comments_limit=-5"
    "ids=4997&comments_limit=abc"
    "ids=4997&attempts_limit=0"
  )
  for bq in "${bad_queries[@]}"; do
    local p="/api/tasks/summaries"
    [ -n "$bq" ] && p="$p?$bq"
    st=$(do_curl "${name}_bad" "$p" "${hdrs[@]}")
    assert_eq "$name bad query ($bq)" "$st" "400"
  done

  # 2f. ?repo= rejected (400) for own repo, foreign repo, and empty repo=
  for rq in "ids=4997&repo=acme/widgets" "ids=4997&repo=acme/secret" "ids=4997&repo="; do
    st=$(do_curl "${name}_repo" "/api/tasks/summaries?$rq" "${hdrs[@]}")
    assert_eq "$name ?repo= ($rq)" "$st" "400"
  done
  echo "  PASS [$name] validation: 50+dup=200, 51=400, ${#bad_queries[@]} malformed queries=400, 3 ?repo= variants=400"

  # 2g. ETag + 304 + change invalidation
  local et1 et2 et3
  st=$(do_curl "${name}_et1" "/api/tasks/summaries?ids=4997,4998&comments_limit=1&attempts_limit=1" "${hdrs[@]}")
  assert_eq "$name etag initial" "$st" "200"
  et1=$(hdr_val "$TMPDIR_PROBE/${name}_et1.hdr" "ETag")
  [ -n "$et1" ] || { echo "FAIL [$name]: missing ETag" >&2; exit 1; }
  st=$(do_curl "${name}_et304" "/api/tasks/summaries?ids=4997,4998&comments_limit=1&attempts_limit=1" "${hdrs[@]}" -H "If-None-Match: $et1")
  assert_eq "$name If-None-Match 304" "$st" "304"
  local sz304=0
  [ -f "$TMPDIR_PROBE/${name}_et304.body" ] && sz304=$(wc -c < "$TMPDIR_PROBE/${name}_et304.body")
  assert_eq "$name 304 empty body" "$sz304" "0"

  # Mutate foreign task 4998: for scoped callers, ETag of ids=4997,4998 MUST NOT change (no existence/activity oracle!)
  if [ "$mode" = "widgets" ]; then
    curl -fsS -X POST -H "Authorization: Bearer $LOCAL_TOKEN" -H "Content-Type: application/json" \
      -d "{\"body\":\"probe foreign mutation for $name\"}" \
      "$BASE_URL/api/tasks/4998/comments" >/dev/null
    st=$(do_curl "${name}_et_foreign_mut" "/api/tasks/summaries?ids=4997,4998&comments_limit=1&attempts_limit=1" "${hdrs[@]}" -H "If-None-Match: $et1")
    assert_eq "$name after foreign mutation still 304" "$st" "304"
  fi

  # Mutate readable task 4997: ETag MUST change -> 200 with distinct ETag
  curl -fsS -X POST -H "Authorization: Bearer $LOCAL_TOKEN" -H "Content-Type: application/json" \
    -d "{\"body\":\"probe readable mutation for $name\"}" \
    "$BASE_URL/api/tasks/4997/comments" >/dev/null
  st=$(do_curl "${name}_et2" "/api/tasks/summaries?ids=4997,4998&comments_limit=1&attempts_limit=1" "${hdrs[@]}" -H "If-None-Match: $et1")
  assert_eq "$name after readable mutation 200" "$st" "200"
  et2=$(hdr_val "$TMPDIR_PROBE/${name}_et2.hdr" "ETag")
  if [ -z "$et2" ] || [ "$et2" = "$et1" ]; then
    echo "FAIL [$name]: ETag did not change after comment on 4997 ($et1 vs $et2)" >&2
    exit 1
  fi
  st=$(do_curl "${name}_et3" "/api/tasks/summaries?ids=4997,4998&comments_limit=1&attempts_limit=1" "${hdrs[@]}" -H "If-None-Match: $et2")
  assert_eq "$name revalidate new ETag 304" "$st" "304"
  echo "  PASS [$name] ETag: $et1 -> 304 -> mutate 4997 -> 200 $et2 -> 304"
}

probe_caller "admin-iap"       "admin"   -H "X-Goog-IAP-JWT-Assertion: $ADMIN_JWT"
probe_caller "local-token"     "admin"   -H "Authorization: Bearer $LOCAL_TOKEN"
probe_caller "limited-iap"     "widgets" -H "X-Goog-IAP-JWT-Assertion: $LIMITED_JWT"
probe_caller "gma-widgets"     "widgets" -H "Authorization: Bearer $GMA_WIDGETS"
probe_caller "acting-agent-5k" "widgets" -H "Authorization: Bearer $LOCAL_TOKEN" -H "X-Grain-Acting-Task: 5000"

echo "PASS: all curl probes succeeded on $LABEL"
