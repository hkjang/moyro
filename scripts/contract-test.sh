#!/usr/bin/env bash
# Smoke test the core /api/v4 contract against a running moyro server.
# Usage: BASE=http://localhost:8065 ./scripts/contract-test.sh
set -euo pipefail

BASE="${BASE:-http://localhost:8065}"
API="$BASE/api/v4"
SUFFIX="$(date +%s)"
USER="tester_$SUFFIX"
EMAIL="tester_$SUFFIX@example.com"
PASS="P@ssw0rd123"

say() { printf "\033[36m• %s\033[0m\n" "$*"; }
fail() { printf "\033[31m✗ %s\033[0m\n" "$*"; exit 1; }
ok() { printf "\033[32m✓ %s\033[0m\n" "$*"; }

say "ping"
curl -fsS "$API/system/ping" | grep -q '"status":"OK"' || fail "ping"
ok "ping"

say "register"
REG=$(curl -fsS -X POST "$API/users" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"$USER\",\"email\":\"$EMAIL\",\"password\":\"$PASS\"}")
echo "$REG" | grep -q '"id"' || fail "register"
ok "register"

say "login"
LOGIN=$(curl -fsS -X POST "$API/users/login" \
  -H "Content-Type: application/json" \
  -d "{\"login_id\":\"$USER\",\"password\":\"$PASS\"}")
TOKEN=$(printf '%s' "$LOGIN" | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
[ -n "$TOKEN" ] || fail "login — no token"
ok "login — token len=${#TOKEN}"

hdr="Authorization: Bearer $TOKEN"

say "me"
curl -fsS "$API/users/me" -H "$hdr" | grep -q "$USER" || fail "me"
ok "me"

say "create team"
TEAM=$(curl -fsS -X POST "$API/teams" -H "$hdr" -H "Content-Type: application/json" \
  -d "{\"name\":\"team-$SUFFIX\",\"display_name\":\"Team $SUFFIX\"}")
TEAM_ID=$(printf '%s' "$TEAM" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
[ -n "$TEAM_ID" ] || fail "team create"
ok "team id=$TEAM_ID"

say "list my teams"
curl -fsS "$API/users/me/teams" -H "$hdr" | grep -q "$TEAM_ID" || fail "team list"
ok "team list"

say "create channel"
CH=$(curl -fsS -X POST "$API/channels" -H "$hdr" -H "Content-Type: application/json" \
  -d "{\"team_id\":\"$TEAM_ID\",\"name\":\"general\",\"display_name\":\"General\",\"type\":\"O\"}")
CH_ID=$(printf '%s' "$CH" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
[ -n "$CH_ID" ] || fail "channel create"
ok "channel id=$CH_ID"

say "list channels for team"
curl -fsS "$API/users/me/teams/$TEAM_ID/channels" -H "$hdr" | grep -q "$CH_ID" || fail "channel list"
ok "channel list"

say "create post"
POST=$(curl -fsS -X POST "$API/posts" -H "$hdr" -H "Content-Type: application/json" \
  -d "{\"channel_id\":\"$CH_ID\",\"message\":\"hello from contract test\"}")
POST_ID=$(printf '%s' "$POST" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
[ -n "$POST_ID" ] || fail "post create"
ok "post id=$POST_ID"

say "list posts"
LIST=$(curl -fsS "$API/channels/$CH_ID/posts" -H "$hdr")
echo "$LIST" | grep -q "$POST_ID" || fail "post list"
echo "$LIST" | grep -q '"order"' || fail "post list shape — missing order"
echo "$LIST" | grep -q '"posts"' || fail "post list shape — missing posts"
ok "post list"

say "edit post"
EDIT=$(curl -fsS -X PUT "$API/posts/$POST_ID" -H "$hdr" -H "Content-Type: application/json" \
  -d "{\"id\":\"$POST_ID\",\"message\":\"edited body\"}")
echo "$EDIT" | grep -q '"message":"edited body"' || fail "post edit"
ok "post edit"

say "add reaction"
REACT=$(curl -fsS -X POST "$API/reactions" -H "$hdr" -H "Content-Type: application/json" \
  -d "{\"post_id\":\"$POST_ID\",\"emoji_name\":\"+1\"}")
echo "$REACT" | grep -q '"emoji_name":"+1"' || fail "reaction add"
ok "reaction add"

say "list reactions"
curl -fsS "$API/posts/$POST_ID/reactions" -H "$hdr" | grep -q '"emoji_name":"+1"' || fail "reaction list"
ok "reaction list"

say "remove reaction"
ME_ID=$(curl -fsS "$API/users/me" -H "$hdr" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
curl -fsS -X DELETE "$API/users/$ME_ID/posts/$POST_ID/reactions/+1" -H "$hdr" | grep -q '"status":"OK"' || fail "reaction remove"
LEFT=$(curl -fsS "$API/posts/$POST_ID/reactions" -H "$hdr")
[ "$LEFT" = "[]" ] || fail "reaction list after remove — got: $LEFT"
ok "reaction remove"

say "upload file"
TMPDIR="${TMPDIR:-/tmp}"
mkdir -p "$TMPDIR"
TMPF="$TMPDIR/moyro-contract-$SUFFIX.txt"
printf "contract test payload" > "$TMPF"
# Windows curl.exe (git-bash) can't resolve MSYS /tmp paths — translate to Windows form.
if command -v cygpath >/dev/null 2>&1; then
  CURL_TMPF="$(cygpath -w "$TMPF")"
else
  CURL_TMPF="$TMPF"
fi
UP=$(curl -fsS -X POST "$API/files?channel_id=$CH_ID" -H "$hdr" -F "files=@$CURL_TMPF;filename=contract.txt")
FILE_ID=$(printf '%s' "$UP" | sed -n 's/.*"file_infos":\[{"id":"\([^"]*\)".*/\1/p')
[ -n "$FILE_ID" ] || fail "file upload"
ok "file id=$FILE_ID"

say "post with file attachment"
FPOST=$(curl -fsS -X POST "$API/posts" -H "$hdr" -H "Content-Type: application/json" \
  -d "{\"channel_id\":\"$CH_ID\",\"message\":\"with file\",\"file_ids\":[\"$FILE_ID\"]}")
echo "$FPOST" | grep -q "\"$FILE_ID\"" || fail "post with file — file_id not in post"
ok "post with file"

say "download file"
DL=$(curl -fsS "$API/files/$FILE_ID" -H "$hdr")
[ "$DL" = "contract test payload" ] || fail "file download — got: $DL"
ok "file download"

say "channel members list"
curl -fsS "$API/channels/$CH_ID/members" -H "$hdr" | grep -q "$ME_ID" || fail "channel members"
ok "channel members"

say "post search"
SEARCH=$(curl -fsS -X POST "$API/teams/$TEAM_ID/posts/search" -H "$hdr" -H "Content-Type: application/json" \
  -d '{"terms":"edited","per_page":20}')
echo "$SEARCH" | grep -q "$POST_ID" || fail "post search"
ok "post search"

say "logout"
curl -fsS -X POST "$API/users/logout" -H "$hdr" | grep -q '"status":"OK"' || fail "logout"
ok "logout"

say "delete post"
# Re-login since we just revoked the session.
TOKEN=$(curl -fsS -X POST "$API/users/login" -H "Content-Type: application/json" \
  -d "{\"login_id\":\"$USER\",\"password\":\"$PASS\"}" | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
hdr="Authorization: Bearer $TOKEN"
curl -fsS -X DELETE "$API/posts/$POST_ID" -H "$hdr" | grep -q '"status":"OK"' || fail "post delete"
ok "post delete"

printf "\n\033[32mALL CONTRACT CHECKS PASSED\033[0m\n"
