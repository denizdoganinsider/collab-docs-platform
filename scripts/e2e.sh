#!/usr/bin/env bash
# End-to-end test for month 1: starts doc-service and the gateway against the
# compose MySQL, registers fresh users, and walks the README verification
# flow plus the permission matrix over real HTTP. Exits non-zero on any
# failed expectation. Requires: docker (docs_mysql healthy), curl, jq, go.
#
#   ./scripts/e2e.sh            # default ports 9000 / 9001
#   GW_PORT=9100 DOC_PORT=9101 ./scripts/e2e.sh
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GW_PORT="${GW_PORT:-9000}"
DOC_PORT="${DOC_PORT:-9001}"
GW="http://localhost:$GW_PORT"
DOC="http://localhost:$DOC_PORT"
KEY="e2e-gateway-key-$RANDOM"
JSON='Content-Type: application/json'
LOG_DIR="$(mktemp -d)"
PASS=0; FAIL=0

say()  { printf '\033[1m%s\033[0m\n' "$*"; }
ok()   { PASS=$((PASS+1)); printf '  \033[32mok\033[0m   %s\n' "$1"; }
bad()  { FAIL=$((FAIL+1)); printf '  \033[31mFAIL\033[0m %s\n         want: %s\n         got:  %s\n' "$1" "$2" "$3"; }
expect() { # name want got
  if [ "$2" = "$3" ]; then ok "$1"; else bad "$1" "$2" "$3"; fi
}
# req METHOD PATH [TOKEN] [BODY] [EXTRA_HEADER...] -> prints "<status>\n<body>"
req() {
  local method=$1 path=$2 token=${3:-} body=${4:-}; shift 4 2>/dev/null || shift $#
  local args=(-s -X "$method" "$GW$path" -H "$JSON" -w '\n%{http_code}')
  [ -n "$token" ] && args+=(-H "Authorization: Bearer $token")
  [ -n "$body" ] && args+=(-d "$body")
  for h in "$@"; do args+=(-H "$h"); done
  local out; out=$(curl "${args[@]}")
  STATUS=${out##*$'\n'}; BODY=${out%$'\n'*}
}

cleanup() {
  [ -n "${DOC_PID:-}" ] && kill "$DOC_PID" 2>/dev/null
  [ -n "${GW_PID:-}" ] && kill "$GW_PID" 2>/dev/null
  # go run leaves the compiled child listening; kill by port as a backstop.
  lsof -ti ":$GW_PORT" -ti ":$DOC_PORT" 2>/dev/null | xargs kill 2>/dev/null
  wait 2>/dev/null
  if [ "$FAIL" -ne 0 ]; then echo "logs kept in $LOG_DIR"; else rm -rf "$LOG_DIR"; fi
}
trap cleanup EXIT

# ---- preflight ---------------------------------------------------------------
say "preflight"
for tool in docker curl jq go lsof; do command -v "$tool" >/dev/null || { echo "missing: $tool"; exit 2; }; done
if [ "$(docker inspect -f '{{.State.Health.Status}}' docs_mysql 2>/dev/null)" != "healthy" ]; then
  echo "docs_mysql is not healthy; run: docker compose up -d"; exit 2
fi
if lsof -ti ":$GW_PORT" -ti ":$DOC_PORT" >/dev/null 2>&1; then
  echo "port $GW_PORT or $DOC_PORT already in use"; exit 2
fi
ok "mysql healthy, ports free"

# ---- start services --------------------------------------------------------
say "starting services"
( cd "$ROOT/doc-service" && GATEWAY_SHARED_KEY="$KEY" SERVER_PORT="$DOC_PORT" exec go run ./cmd/doc ) >"$LOG_DIR/doc.log" 2>&1 &
DOC_PID=$!; disown
( cd "$ROOT/gateway" && JWT_SECRET="e2e-jwt-secret" GATEWAY_SHARED_KEY="$KEY" SERVER_PORT="$GW_PORT" \
    DOC_SERVICE_URLS="$DOC" exec go run ./cmd/gateway ) >"$LOG_DIR/gateway.log" 2>&1 &
GW_PID=$!; disown
for _ in $(seq 1 60); do
  curl -sf "$GW/health" >/dev/null && curl -sf "$DOC/health" >/dev/null && break
  sleep 1
done
expect "gateway /health" '{"status":"ok"}' "$(curl -s "$GW/health")"
expect "doc-service /health" "{\"instance\":\"doc-$DOC_PORT\",\"status\":\"ok\"}" "$(curl -s "$DOC/health")"

# ---- static + routing ------------------------------------------------------
say "static page and routing"
expect "GET / is the editor" "200 text/html; charset=utf-8" "$(curl -s -o /dev/null -w '%{http_code} %{content_type}' "$GW/")"
expect "unknown path is 404, not 401" "404" "$(curl -s -o /dev/null -w '%{http_code}' "$GW/nope")"
expect "doc-service unknown path is 404" "404" "$(curl -s -o /dev/null -w '%{http_code}' "$DOC/nope")"

# ---- auth ------------------------------------------------------------------
say "auth"
SUFFIX="$(date +%s)$RANDOM"
EMAIL_A="e2e-a-$SUFFIX@test.com"; EMAIL_B="e2e-b-$SUFFIX@test.com"; EMAIL_C="e2e-c-$SUFFIX@test.com"
PW="Passw0rd1"

req POST /register "" "{\"email\":\"$EMAIL_A\",\"password\":\"$PW\"}"
expect "register A -> 201" "201" "$STATUS"; ID_A=$(echo "$BODY" | jq -r .id)
expect "register body has no password hash" "null" "$(echo "$BODY" | jq -r .password_hash)"
req POST /register "" "{\"email\":\"$EMAIL_A\",\"password\":\"$PW\"}"
expect "duplicate email -> 409" "409" "$STATUS"
req POST /register "" "{\"email\":\"$EMAIL_B\",\"password\":\"password\"}"
expect "weak password -> 400" "400" "$STATUS"
req POST /register "" "{\"email\":\"not-an-email\",\"password\":\"$PW\"}"
expect "bad email -> 400" "400" "$STATUS"
req POST /register "" "{\"email\":\"$EMAIL_B\",\"password\":\"$PW\"}"
expect "register B -> 201" "201" "$STATUS"; ID_B=$(echo "$BODY" | jq -r .id)
req POST /register "" "{\"email\":\"$EMAIL_C\",\"password\":\"$PW\"}"
expect "register C -> 201" "201" "$STATUS"; ID_C=$(echo "$BODY" | jq -r .id)

req POST /login "" "{\"email\":\"$EMAIL_A\",\"password\":\"wrong\"}"
expect "wrong password -> 401" "401" "$STATUS"; MSG_WRONG=$(echo "$BODY" | jq -r .error)
req POST /login "" "{\"email\":\"nobody-$SUFFIX@test.com\",\"password\":\"$PW\"}"
expect "unknown user -> 401" "401" "$STATUS"
expect "wrong password and unknown user are indistinguishable" "$MSG_WRONG" "$(echo "$BODY" | jq -r .error)"

login() { req POST /login "" "{\"email\":\"$1\",\"password\":\"$PW\"}"; echo "$BODY" | jq -r .token; }
TOKEN_A=$(login "$EMAIL_A"); TOKEN_B=$(login "$EMAIL_B"); TOKEN_C=$(login "$EMAIL_C")
expect "login returns a three-part JWT" "2" "$(printf '%s' "$TOKEN_A" | tr -cd '.' | wc -c | tr -d ' ')"

req GET /me "$TOKEN_A"
expect "/me -> 200" "200" "$STATUS"
expect "/me is A" "$EMAIL_A" "$(echo "$BODY" | jq -r .email)"
req GET /me
expect "/me without token -> 401" "401" "$STATUS"
req GET /me "garbage"
expect "/me with garbage token -> 401" "401" "$STATUS"
req GET /admin/users "$TOKEN_A"
expect "/admin/users as user -> 403" "403" "$STATUS"

# ---- documents: README verification block ----------------------------------
say "documents (README month 1 block)"
req POST /documents "$TOKEN_A" '{"title":"Design notes"}'
expect "create -> 201" "201" "$STATUS"
DOC_ID=$(echo "$BODY" | jq -r .id)
expect "create: role owner, version 0" "owner 0" "$(echo "$BODY" | jq -r '"\(.role) \(.version)"')"
req POST /documents "$TOKEN_A" '{"title":"   "}'
expect "create with blank title -> 400" "400" "$STATUS"

req GET "/documents/$DOC_ID" "$TOKEN_B"
expect "B reads before share -> 403 (not 404)" "403" "$STATUS"
req PUT "/documents/$DOC_ID/members/$ID_B" "$TOKEN_A" '{"role":"viewer"}'
expect "A shares B as viewer -> 200" "200" "$STATUS"
req GET "/documents/$DOC_ID" "$TOKEN_B"
expect "B reads as viewer -> 200" "200" "$STATUS"
expect "B's role is viewer" "viewer" "$(echo "$BODY" | jq -r .role)"
expect "members list has owner and viewer" "owner viewer" "$(echo "$BODY" | jq -r '[.members[].role] | join(" ")')"
req PATCH "/documents/$DOC_ID" "$TOKEN_B" '{"title":"x"}'
expect "viewer renames -> 403" "403" "$STATUS"

# the boundary holds: identity headers from a client are discarded
req GET /documents "$TOKEN_B" "" "X-User-ID: $ID_A" "X-User-Role: admin" "X-Gateway-Key: guess"
expect "forged X-User-ID: B still sees B's list" "$DOC_ID viewer" "$(echo "$BODY" | jq -r '.[] | "\(.id) \(.role)"')"
expect "direct doc-service call without gateway key -> 401" "401" \
  "$(curl -s -o /dev/null -w '%{http_code}' "$DOC/documents" -H "X-User-ID: $ID_A")"
expect "direct doc-service call with wrong key -> 401" "401" \
  "$(curl -s -o /dev/null -w '%{http_code}' "$DOC/documents" -H "X-User-ID: $ID_A" -H "X-Gateway-Key: nope")"

# ---- permission matrix -----------------------------------------------------
say "permission matrix"
req PUT "/documents/$DOC_ID/members/$ID_B" "$TOKEN_A" '{"role":"editor"}'
expect "A promotes B to editor -> 200" "200" "$STATUS"
req PATCH "/documents/$DOC_ID" "$TOKEN_B" '{"title":"Renamed by B"}'
expect "editor renames -> 200" "200" "$STATUS"
expect "rename persisted" "Renamed by B" "$(echo "$BODY" | jq -r .title)"
req PUT "/documents/$DOC_ID/members/$ID_C" "$TOKEN_B" '{"role":"viewer"}'
expect "editor shares -> 403" "403" "$STATUS"
req DELETE "/documents/$DOC_ID" "$TOKEN_B"
expect "editor deletes -> 403" "403" "$STATUS"
req GET "/documents/$DOC_ID/members" "$TOKEN_C"
expect "non-member lists members -> 403" "403" "$STATUS"
req GET "/documents" "$TOKEN_C"
expect "non-member's list is empty" "0" "$(echo "$BODY" | jq length)"
req PUT "/documents/$DOC_ID/members/$ID_C" "$TOKEN_A" '{"role":"owner"}'
expect "assigning owner role -> 400" "400" "$STATUS"
req PUT "/documents/$DOC_ID/members/$ID_A" "$TOKEN_A" '{"role":"viewer"}'
expect "owner demoting self -> 400" "400" "$STATUS"
req DELETE "/documents/$DOC_ID/members/$ID_A" "$TOKEN_A"
expect "owner removing self -> 400" "400" "$STATUS"
req DELETE "/documents/$DOC_ID/members/$ID_B" "$TOKEN_A"
expect "A unshares B -> 204" "204" "$STATUS"
req GET "/documents/$DOC_ID" "$TOKEN_B"
expect "B reads after unshare -> 403" "403" "$STATUS"
req DELETE "/documents/$DOC_ID/members/$ID_B" "$TOKEN_A"
expect "unshare twice -> 404" "404" "$STATUS"
req GET "/documents/abc" "$TOKEN_A"
expect "non-numeric id -> 400" "400" "$STATUS"

# ---- pagination ------------------------------------------------------------
say "pagination"
for i in 1 2 3; do req POST /documents "$TOKEN_C" "{\"title\":\"C doc $i\"}"; done
req GET "/documents?page=1&per_page=2" "$TOKEN_C"
expect "page 1 of C's docs has 2" "2" "$(echo "$BODY" | jq length)"
expect "newest first" "C doc 3" "$(echo "$BODY" | jq -r '.[0].title')"
req GET "/documents?page=2&per_page=2" "$TOKEN_C"
expect "page 2 has the remaining 1" "1" "$(echo "$BODY" | jq length)"
req GET "/documents?page=x" "$TOKEN_C"
expect "bad page -> 400" "400" "$STATUS"

# ---- admin -----------------------------------------------------------------
say "admin"
docker exec -i docs_mysql mysql -uroot -proot -e "UPDATE docs_gateway_db.users SET role='admin' WHERE id=$ID_A" 2>/dev/null
TOKEN_ADMIN=$(login "$EMAIL_A")
req GET "/admin/users?per_page=2" "$TOKEN_ADMIN"
expect "/admin/users as admin -> 200" "200" "$STATUS"
expect "/admin/users page shape" "2" "$(echo "$BODY" | jq '.users | length')"
req GET "/admin/documents?per_page=1" "$TOKEN_ADMIN"
expect "/admin/documents as admin -> 200 via proxy" "200" "$STATUS"
expect "/admin/documents is paginated" "1" "$(echo "$BODY" | jq '.documents | length')"
req GET "/admin/documents" "$TOKEN_B"
expect "/admin/documents as user -> 403 at the gateway" "403" "$STATUS"
expect "doc-service re-checks role (defence in depth)" "403" \
  "$(curl -s -o /dev/null -w '%{http_code}' "$DOC/admin/documents" -H "X-Gateway-Key: $KEY" -H "X-User-ID: $ID_B" -H "X-User-Role: user")"

# ---- delete ----------------------------------------------------------------
say "delete"
req DELETE "/documents/$DOC_ID" "$TOKEN_A"
expect "owner deletes -> 204" "204" "$STATUS"
req GET "/documents/$DOC_ID" "$TOKEN_A"
expect "read after delete -> 403" "403" "$STATUS"
expect "no rows left for the document" "0	0" "$(docker exec -i docs_mysql mysql -uroot -proot -N -e \
  "SELECT (SELECT COUNT(*) FROM docs_service_db.documents WHERE id=$DOC_ID),(SELECT COUNT(*) FROM docs_service_db.document_members WHERE doc_id=$DOC_ID)" 2>/dev/null)"

# ---- observability ---------------------------------------------------------
say "observability"
RID="e2e-trace-$SUFFIX"
curl -s -o /dev/null "$GW/documents" -H "Authorization: Bearer $TOKEN_A" -H "X-Request-ID: $RID"
expect "request id reaches doc-service log" "1" "$(grep -c "$RID" "$LOG_DIR/doc.log")"
curl -s -o /dev/null "$GW/documents?page=1&leak=SECRET-$SUFFIX" -H "Authorization: Bearer $TOKEN_A"
expect "query string never logged (gateway)" "0" "$(grep -c "SECRET-$SUFFIX" "$LOG_DIR/gateway.log")"
expect "query string never logged (doc-service)" "0" "$(grep -c "SECRET-$SUFFIX" "$LOG_DIR/doc.log")"
expect "Authorization never appears in a doc-service log line" "0" "$(grep -ci -E 'authorization|bearer' "$LOG_DIR/doc.log")"
expect "no 5xx in either log" "0" "$(grep -c '"status":5' "$LOG_DIR/gateway.log" "$LOG_DIR/doc.log" | awk -F: '{s+=$2} END {print s}')"

# ---- teardown --------------------------------------------------------------
say "teardown"
req GET "/documents?per_page=100" "$TOKEN_C"
for id in $(echo "$BODY" | jq -r '.[].id'); do req DELETE "/documents/$id" "$TOKEN_C"; done
docker exec -i docs_mysql mysql -uroot -proot -e \
  "DELETE FROM docs_gateway_db.users WHERE email LIKE 'e2e-%-$SUFFIX@test.com'" 2>/dev/null
expect "e2e users removed" "0" "$(docker exec -i docs_mysql mysql -uroot -proot -N -e \
  "SELECT COUNT(*) FROM docs_gateway_db.users WHERE email LIKE 'e2e-%-$SUFFIX@test.com'" 2>/dev/null)"

echo
printf 'passed: %d  failed: %d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
