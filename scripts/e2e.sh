#!/usr/bin/env bash
# End-to-end test for months 1 to 3: starts two doc-service instances and the
# gateway against the compose MySQL, registers fresh users, walks the README
# verification flows (HTTP and WebSocket, the latter through cmd/wsprobe) plus
# the permission matrix, kills an instance with -9 and checks nothing acked
# was lost, then proves the balancer's stickiness, fail-closed and drain
# behaviour. Exits non-zero on any failed expectation. Requires: docker
# (docs_mysql healthy), curl, jq, go, lsof.
#
#   ./scripts/e2e.sh            # default ports 9000 / 9001 / 9011
#   GW_PORT=9100 DOC_PORT=9101 DOC_PORT2=9111 ./scripts/e2e.sh
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GW_PORT="${GW_PORT:-9000}"
DOC_PORT="${DOC_PORT:-9001}"
DOC_PORT2="${DOC_PORT2:-9011}"
GW="http://localhost:$GW_PORT"
DOC="http://localhost:$DOC_PORT"
DOC2="http://localhost:$DOC_PORT2"
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
  [ -n "${GW_PID:-}" ] && kill "$GW_PID" 2>/dev/null
  # doc-service instances are started by port; go run leaves the compiled
  # gateway child listening too, so kill by port.
  lsof -ti ":$GW_PORT" -ti ":$DOC_PORT" -ti ":$DOC_PORT2" 2>/dev/null | xargs kill 2>/dev/null
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
if lsof -ti ":$GW_PORT" -ti ":$DOC_PORT" -ti ":$DOC_PORT2" >/dev/null 2>&1; then
  echo "port $GW_PORT, $DOC_PORT or $DOC_PORT2 already in use"; exit 2
fi
ok "mysql healthy, ports free"

# ---- start services --------------------------------------------------------
say "starting services"
# Compiled once: `go run` per probe would add seconds to every check.
( cd "$ROOT/doc-service" && go build -o "$LOG_DIR/wsprobe" ./cmd/wsprobe && go build -o "$LOG_DIR/doc" ./cmd/doc ) || { echo "build failed"; exit 2; }
PROBE="$LOG_DIR/wsprobe"
# start_doc PORT: one doc-service instance, logging to doc-PORT.log. The
# drain is shortened from the default 5 s so the shutdown test stays quick;
# it still exceeds the gateway's 1 s poll interval below, as it must.
start_doc() {
  ( GATEWAY_SHARED_KEY="$KEY" SERVER_PORT="$1" SHUTDOWN_DRAIN_SECONDS=2 WS_ALLOWED_ORIGINS="http://localhost:$GW_PORT" exec "$LOG_DIR/doc" ) >>"$LOG_DIR/doc-$1.log" 2>&1 &
  disown
}
start_doc "$DOC_PORT"
start_doc "$DOC_PORT2"
( cd "$ROOT/gateway" && JWT_SECRET="e2e-jwt-secret" GATEWAY_SHARED_KEY="$KEY" SERVER_PORT="$GW_PORT" \
    DOC_SERVICE_URLS="$DOC,$DOC2" LB_HEALTH_INTERVAL_SECONDS=1 exec go run ./cmd/gateway ) >"$LOG_DIR/gateway.log" 2>&1 &
GW_PID=$!; disown
for _ in $(seq 1 60); do
  curl -sf "$GW/health" >/dev/null && curl -sf "$DOC/health" >/dev/null && curl -sf "$DOC2/health" >/dev/null && break
  sleep 1
done
expect "gateway /health" '{"status":"ok"}' "$(curl -s "$GW/health")"
expect "doc-service /health" "{\"instance\":\"doc-$DOC_PORT\",\"status\":\"ok\"}" "$(curl -s "$DOC/health")"
expect "second doc-service /health" "{\"instance\":\"doc-$DOC_PORT2\",\"status\":\"ok\"}" "$(curl -s "$DOC2/health")"
expect "every doc-service response names its instance" "doc-$DOC_PORT" "$(curl -s -o /dev/null -D - "$DOC/nope" | awk 'tolower($1)=="x-doc-instance:" {print $2}' | tr -d '\r')"

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
req GET "/documents/$DOC_ID/ops" "$TOKEN_A"
expect "ops catch-up on a fresh document -> 200 []" "200 []" "$STATUS $(echo "$BODY" | jq -c .)"
req GET "/documents/$DOC_ID/ops" "$TOKEN_C"
expect "ops as non-member -> 403" "403" "$STATUS"
req GET "/documents/$DOC_ID/ops?from=x" "$TOKEN_A"
expect "ops with bad from -> 400, single error object" "400 1" "$STATUS $(echo "$BODY" | jq -c 'select(.error) | 1')"
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
expect "bad page body is exactly one error object" "invalid page parameter" "$(printf '%s' "$BODY" | jq -r .error 2>/dev/null)"
req GET "/documents?page=999999999999999999" "$TOKEN_C"
expect "huge page is clamped, not a 500" "200" "$STATUS"

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

# ---- websocket (README month 2 block) ---------------------------------------
say "websocket (README month 2 block)"
WS="ws://localhost:$GW_PORT/ws"
ticket() { req POST /ws-ticket "$1"; echo "$BODY" | jq -r .ticket; }
instance_of() { # PATH TOKEN -> the X-Doc-Instance the gateway relayed
  curl -s -o /dev/null -D - "$GW$1" -H "Authorization: Bearer $2" | awk 'tolower($1)=="x-doc-instance:" {print $2}' | tr -d '\r'
}
frame() { echo "$1" | grep '^{' | jq -c "select(.type==\"$2\")" | head -n1; }   # first frame of a type in a probe's output

req POST /documents "$TOKEN_A" '{"title":"Live"}'
LIVE=$(echo "$BODY" | jq -r .id)
req PUT "/documents/$LIVE/members/$ID_B" "$TOKEN_A" '{"role":"viewer"}'

req POST /ws-ticket "$TOKEN_A"
expect "POST /ws-ticket -> 201 with a 30 s ticket" "201 30 64" "$STATUS $(echo "$BODY" | jq -r '"\(.expires_in) \(.ticket|length)"')"
req POST /ws-ticket
expect "POST /ws-ticket without token -> 401" "401" "$STATUS"
T_A=$(ticket "$TOKEN_A")

# A stays connected for a while so the live session can be observed from
# the HTTP side before the last socket leaves and the snapshot is written.
A_OUT="$LOG_DIR/a.frames"
"$PROBE" --wait 3s "$WS?doc=$LIVE&ticket=$T_A" '{"type":"op","v":0,"op":["hello"],"seq":1}' >"$A_OUT" &
A_PROBE=$!
sleep 1
expect "ticket is single-use: replay -> 401" "http 401" "$("$PROBE" "$WS?doc=$LIVE&ticket=$T_A")"

# persist before ack: the row is there; the live read is ahead of the stored snapshot
req GET "/documents/$LIVE/ops" "$TOKEN_A"
expect "ops log has the acked op at version 1" "1 $ID_A [\"hello\"]" "$(echo "$BODY" | jq -r '.[0] | "\(.version) \(.user_id) \(.op|tojson)"')"
req GET "/documents/$LIVE" "$TOKEN_A"
expect "GET /documents/:id answers from the live session (v1, hello)" "1 hello" "$(echo "$BODY" | jq -r '"\(.version) \(.content)"')"
expect "while the stored snapshot is still v0" "0" "$(docker exec -i docs_mysql mysql -uroot -proot -N -e \
  "SELECT version FROM docs_service_db.documents WHERE id=$LIVE" 2>/dev/null)"
wait $A_PROBE
OUT=$(cat "$A_OUT")
expect "first frame is the snapshot: v0, role owner, empty" "snapshot 0 owner  $ID_A" \
  "$(frame "$OUT" snapshot | jq -r '"\(.type) \(.v) \(.role) \(.content) \(.presence[0].user_id)"')"
expect "op is acked with v1 and the client's seq" "ack 1 1" "$(frame "$OUT" ack | jq -r '"\(.type) \(.v) \(.seq)"')"
expect "last socket gone: the snapshot is written (v1)" "1" "$(docker exec -i docs_mysql mysql -uroot -proot -N -e \
  "SELECT version FROM docs_service_db.documents WHERE id=$LIVE" 2>/dev/null)"
expect "no ticket -> 401" "http 401" "$("$PROBE" "$WS?doc=$LIVE")"
expect "ticket never appears in the gateway log" "0" "$(grep -c "$T_A" "$LOG_DIR/gateway.log")"
expect "ticket never appears in the doc-service log" "0" "$(cat "$LOG_DIR"/doc-*.log | grep -c "$T_A")"
expect "stranger with a valid ticket -> 403 before the upgrade" "http 403" "$("$PROBE" "$WS?doc=$LIVE&ticket=$(ticket "$TOKEN_C")")"
expect "foreign Origin -> 403" "http 403" "$("$PROBE" --origin http://evil.test "$WS?doc=$LIVE&ticket=$(ticket "$TOKEN_A")")"
OUT=$("$PROBE" --origin "http://localhost:$GW_PORT" "$WS?doc=$LIVE&ticket=$(ticket "$TOKEN_A")")
expect "the editor's Origin is allowed" "snapshot" "$(frame "$OUT" snapshot | jq -r .type)"

# viewer: sees the text, cannot edit, socket stays open
OUT=$("$PROBE" "$WS?doc=$LIVE&ticket=$(ticket "$TOKEN_B")" '{"type":"op","v":1,"op":[5,"!"],"seq":1}' '{"type":"ping"}')
expect "viewer snapshot carries the live text" "viewer 1 hello" "$(frame "$OUT" snapshot | jq -r '"\(.role) \(.v) \(.content)"')"
expect "viewer op -> error forbidden, no close" "forbidden " "$(frame "$OUT" error | jq -r .code) $(echo "$OUT" | grep '^close' || true)"

# editor: an op against a stale base is transformed, everyone else sees it
req PUT "/documents/$LIVE/members/$ID_B" "$TOKEN_A" '{"role":"editor"}'
"$PROBE" --wait 3s "$WS?doc=$LIVE&ticket=$(ticket "$TOKEN_A")" '{"type":"op","v":1,"op":[5," world"],"seq":7}' >"$A_OUT" &
A_PROBE=$!
sleep 1
OUT=$("$PROBE" "$WS?doc=$LIVE&ticket=$(ticket "$TOKEN_B")" '{"type":"op","v":1,"op":["> ",5],"seq":1}' '{"type":"cursor","pos":2,"sel":2}')
expect "B's op against v1 is acked as v3 (transformed past A's v2)" "ack 3" "$(frame "$OUT" ack | jq -r '"\(.type) \(.v)"')"
wait $A_PROBE
expect "A was acked v2 with seq 7" "2 7" "$(frame "$(cat "$A_OUT")" ack | jq -r '"\(.v) \(.seq)"')"
expect "A received B's op as v3 from user B" "3 $ID_B [\"> \",11]" "$(frame "$(cat "$A_OUT")" op | jq -r '"\(.v) \(.user_id) \(.op|tojson)"')"
expect "A received B's cursor" "$ID_B 2 3" "$(frame "$(cat "$A_OUT")" cursor | jq -r '"\(.user_id) \(.pos) \(.v)"')"
expect "A saw B join and leave (presence)" "2 1" "$(grep '^{' "$A_OUT" | jq -r 'select(.type=="presence") | (.users|length)' | tr '\n' ' ' | sed 's/ $//')"
req GET "/documents/$LIVE" "$TOKEN_B"
expect "both sides converged: live text is '> hello world' at v3" "3 > hello world" "$(echo "$BODY" | jq -r '"\(.version) \(.content)"')"
req GET "/documents/$LIVE/ops?from=1" "$TOKEN_B"
expect "catch-up from v1 returns v2 and v3" "2 3" "$(echo "$BODY" | jq -r '[.[].version] | join(" ")')"

# protocol violations
expect "malformed JSON -> close 1003" "close 1003" "$("$PROBE" "$WS?doc=$LIVE&ticket=$(ticket "$TOKEN_A")" '{"type":' | grep '^close' | cut -d' ' -f1,2)"
expect "op ahead of the server -> close 4400" "close 4400" "$("$PROBE" "$WS?doc=$LIVE&ticket=$(ticket "$TOKEN_A")" '{"type":"op","v":99,"op":["x"],"seq":1}' | grep '^close' | cut -d' ' -f1,2)"
expect "unknown frame type -> error bad_frame" "bad_frame" "$(frame "$("$PROBE" "$WS?doc=$LIVE&ticket=$(ticket "$TOKEN_A")" '{"type":"dance"}')" error | jq -r .code)"

# HTTP changes reach the sockets: rename, revoke, delete
B_OUT="$LOG_DIR/b.frames"
"$PROBE" --wait 3s "$WS?doc=$LIVE&ticket=$(ticket "$TOKEN_B")" >"$B_OUT" &
B_PROBE=$!
sleep 1
req PATCH "/documents/$LIVE" "$TOKEN_A" '{"title":"Live, renamed"}'
req DELETE "/documents/$LIVE/members/$ID_B" "$TOKEN_A"
wait $B_PROBE
expect "B got the title frame after PATCH" "Live, renamed" "$(frame "$(cat "$B_OUT")" title | jq -r .title)"
expect "B's socket closed with 4003 on unshare" "close 4003" "$(grep '^close' "$B_OUT" | cut -d' ' -f1,2)"
"$PROBE" --wait 3s "$WS?doc=$LIVE&ticket=$(ticket "$TOKEN_A")" >"$A_OUT" &
A_PROBE=$!
sleep 1
req DELETE "/documents/$LIVE" "$TOKEN_A"
wait $A_PROBE
expect "A's socket closed with 4004 on delete" "close 4004" "$(grep '^close' "$A_OUT" | cut -d' ' -f1,2)"

# kill -9 loses no acked op
req POST /documents "$TOKEN_A" '{"title":"Crash"}'
CRASH=$(echo "$BODY" | jq -r .id)
OUT=$("$PROBE" "$WS?doc=$CRASH&ticket=$(ticket "$TOKEN_A")" '{"type":"op","v":0,"op":["survives"],"seq":1}')
expect "op acked before the crash" "1" "$(frame "$OUT" ack | jq -r .v)"
CRASH_PORT=$(instance_of "/documents/$CRASH" "$TOKEN_A"); CRASH_PORT=${CRASH_PORT#doc-}
kill -9 "$(lsof -ti "TCP:$CRASH_PORT" -sTCP:LISTEN)" 2>/dev/null; sleep 0.5   # the listener only: -i :port also matches the gateway's proxy connections
start_doc "$CRASH_PORT"
for _ in $(seq 1 30); do curl -sf "http://localhost:$CRASH_PORT/health" >/dev/null && break; sleep 0.5; done
sleep 1.5   # the balancer's next sweep (1 s) puts the instance back
OUT=$("$PROBE" "$WS?doc=$CRASH&ticket=$(ticket "$TOKEN_A")")
expect "after kill -9 and restart the snapshot is v1 with the text" "1 survives" "$(frame "$OUT" snapshot | jq -r '"\(.v) \(.content)"')"
req DELETE "/documents/$CRASH" "$TOKEN_A"

# ---- load balancer (README month 3 block) -----------------------------------
say "load balancer (README month 3 block)"
# Every route about one document lands on one instance: three sockets, then
# the HTTP read. Documents are created until both instances hold one (the
# hash spreads them; twelve tries without both would be a one-in-two-thousand
# event, so a miss here is a bug).
ON_1=""; ON_2=""; LB_DOCS=""
for i in $(seq 1 12); do
  req POST /documents "$TOKEN_A" "{\"title\":\"LB $i\"}"; D=$(echo "$BODY" | jq -r .id); LB_DOCS="$LB_DOCS $D"
  SEEN=""
  for _ in 1 2 3; do SEEN="$SEEN $("$PROBE" "$WS?doc=$D&ticket=$(ticket "$TOKEN_A")" | awk '$1=="instance" {print $2}')"; done
  HTTP=$(instance_of "/documents/$D" "$TOKEN_A")
  expect "doc $D: three sockets and the HTTP read on one instance ($HTTP)" " $HTTP $HTTP $HTTP" "$SEEN"
  case "$HTTP" in "doc-$DOC_PORT") ON_1=$D;; "doc-$DOC_PORT2") ON_2=$D;; esac
  [ -n "$ON_1" ] && [ -n "$ON_2" ] && break
done
expect "documents spread over both instances" "both" "$([ -n "$ON_1" ] && [ -n "$ON_2" ] && echo both || echo "on $DOC_PORT: '$ON_1', on $DOC_PORT2: '$ON_2'")"
expect "members route like the document" "$(instance_of "/documents/$ON_2" "$TOKEN_A")" "$(instance_of "/documents/$ON_2/members" "$TOKEN_A")"
expect "ops route like the document" "$(instance_of "/documents/$ON_1" "$TOKEN_A")" "$(instance_of "/documents/$ON_1/ops" "$TOKEN_A")"

RR=""; for _ in 1 2 3 4; do RR="$RR $(instance_of /documents "$TOKEN_A")"; done
read -r R1 R2 R3 R4 <<<"$RR"
expect "GET /documents alternates between the instances" "alternating" "$([ "$R1" != "$R2" ] && [ "$R1" = "$R3" ] && [ "$R2" = "$R4" ] && echo alternating || echo "$RR")"

# A membership removal reaches the live session on the right instance: B's
# socket is closed by the process that handled the HTTP call, on both.
for D in "$ON_1" "$ON_2"; do
  req PUT "/documents/$D/members/$ID_B" "$TOKEN_A" '{"role":"viewer"}'
  B_OUT="$LOG_DIR/b-$D.frames"
  "$PROBE" --wait 5s "$WS?doc=$D&ticket=$(ticket "$TOKEN_B")" >"$B_OUT" &
  B_PROBE=$!; sleep 1
  req DELETE "/documents/$D/members/$ID_B" "$TOKEN_A"
  sleep 1
  STILL=$(kill -0 "$B_PROBE" 2>/dev/null && echo "still open" || echo closed)
  wait "$B_PROBE"
  expect "doc $D on $(instance_of "/documents/$D" "$TOKEN_A"): unshare closes B's socket with 4003 within 1 s" "closed close 4003" "$STILL $(grep '^close' "$B_OUT" | cut -d' ' -f1,2)"
done

# Fail closed: the ring is never rebuilt, so a dead instance's documents
# answer 503 until it is back, and come back on the same instance.
req GET /health/backends "$TOKEN_ADMIN"
expect "/health/backends lists both instances healthy" "200 localhost:$DOC_PORT=true localhost:$DOC_PORT2=true" \
  "$STATUS $(echo "$BODY" | jq -r '.doc_service[] | "\(.backend)=\(.healthy)"' | tr '\n' ' ' | sed 's/ $//')"
req GET /health/backends "$TOKEN_B"
expect "/health/backends as user -> 403" "403" "$STATUS"
kill -9 "$(lsof -ti "TCP:$DOC_PORT2" -sTCP:LISTEN)"; sleep 2.5   # one poll interval plus margin
req GET /health/backends "$TOKEN_ADMIN"
expect "killed instance is marked down" "false" "$(echo "$BODY" | jq -r ".doc_service[] | select(.backend==\"localhost:$DOC_PORT2\") | .healthy")"
req GET "/documents/$ON_2" "$TOKEN_A"
expect "document on the dead instance -> 503, not rerouted" "503 the instance serving this document is unavailable" "$STATUS $(echo "$BODY" | jq -r .error)"
expect "socket to the dead instance -> 503 too" "http 503" "$("$PROBE" "$WS?doc=$ON_2&ticket=$(ticket "$TOKEN_A")")"
req GET "/documents/$ON_1" "$TOKEN_A"
expect "document on the live instance -> 200" "200" "$STATUS"
OK=0; for _ in 1 2 3 4; do req GET /documents "$TOKEN_A"; [ "$STATUS" = 200 ] && OK=$((OK+1)); done
expect "GET /documents -> 200 always" "4" "$OK"
expect "GET /documents is served by the survivor only" "doc-$DOC_PORT" "$(for _ in 1 2 3; do instance_of /documents "$TOKEN_A"; done | sort -u | tr '\n' ' ' | sed 's/ $//')"
start_doc "$DOC_PORT2"
for _ in $(seq 1 30); do curl -sf "$DOC2/health" >/dev/null && break; sleep 0.5; done
sleep 1.5
req GET /health/backends "$TOKEN_ADMIN"
expect "restarted instance is back" "true" "$(echo "$BODY" | jq -r ".doc_service[] | select(.backend==\"localhost:$DOC_PORT2\") | .healthy")"
expect "its documents come back on the same instance" "doc-$DOC_PORT2" "$(instance_of "/documents/$ON_2" "$TOKEN_A")"

# Graceful drain: SIGTERM turns /health 503 at once while the listener and
# the sockets stay up for SHUTDOWN_DRAIN_SECONDS (2 s here), the balancer
# stops routing within a poll, then the sockets close with 1001.
"$PROBE" --wait 6s "$WS?doc=$ON_1&ticket=$(ticket "$TOKEN_A")" >"$A_OUT" &
A_PROBE=$!; sleep 1
kill -TERM "$(lsof -ti "TCP:$DOC_PORT" -sTCP:LISTEN)"
expect "/health answers 503 draining right after SIGTERM" "503 draining" "$(curl -s -o /dev/null -w '%{http_code}' "$DOC/health") $(curl -s "$DOC/health" | jq -r .status)"
sleep 1
expect "the open socket is still open 1 s into the drain" "still open" "$(kill -0 "$A_PROBE" 2>/dev/null && echo "still open" || echo closed)"
req GET /health/backends "$TOKEN_ADMIN"
expect "balancer stopped routing to the draining instance within one poll" "false" "$(echo "$BODY" | jq -r ".doc_service[] | select(.backend==\"localhost:$DOC_PORT\") | .healthy")"
wait "$A_PROBE"
expect "after the drain the socket closed with 1001" "close 1001" "$(grep '^close' "$A_OUT" | cut -d' ' -f1,2)"
for _ in $(seq 1 20); do lsof -ti "TCP:$DOC_PORT" -sTCP:LISTEN >/dev/null 2>&1 || break; sleep 0.5; done
expect "the process exited after the drain" "gone" "$(lsof -ti "TCP:$DOC_PORT" -sTCP:LISTEN >/dev/null 2>&1 && echo listening || echo gone)"
start_doc "$DOC_PORT"
for _ in $(seq 1 30); do curl -sf "$DOC/health" >/dev/null && break; sleep 0.5; done
sleep 1.5
req GET /health/backends "$TOKEN_ADMIN"
expect "both instances healthy again" "true true" "$(echo "$BODY" | jq -r '.doc_service[].healthy' | tr '\n' ' ' | sed 's/ $//')"
for D in $LB_DOCS; do req DELETE "/documents/$D" "$TOKEN_A"; done

# ---- observability ---------------------------------------------------------
say "observability"
RID="e2e-trace-$SUFFIX"
curl -s -o /dev/null "$GW/documents" -H "Authorization: Bearer $TOKEN_A" -H "X-Request-ID: $RID"
expect "request id reaches the doc-service log" "1" "$(cat "$LOG_DIR"/doc-*.log | grep -c "$RID")"
curl -s -o /dev/null "$GW/documents?page=1&leak=SECRET-$SUFFIX" -H "Authorization: Bearer $TOKEN_A"
expect "query string never logged (gateway)" "0" "$(grep -c "SECRET-$SUFFIX" "$LOG_DIR/gateway.log")"
expect "query string never logged (doc-service)" "0" "$(cat "$LOG_DIR"/doc-*.log | grep -c "SECRET-$SUFFIX")"
expect "Authorization never appears in a doc-service log line" "0" "$(cat "$LOG_DIR"/doc-*.log | grep -ci -E 'authorization|bearer')"
expect "no 500 or 502 in any log (503 is the balancer failing closed)" "0" "$(cat "$LOG_DIR/gateway.log" "$LOG_DIR"/doc-*.log | grep -cE '"status":50[02]')"

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
