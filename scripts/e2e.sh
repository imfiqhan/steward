#!/usr/bin/env bash
# End-to-end smoke test: boots the example app on a temp SQLite database and
# drives login → grid → JSON → create → delete through curl.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d)"
JAR="$WORK/cookies.txt"
ADDR="127.0.0.1:8399"
BASE="http://$ADDR/admin"
trap 'kill $SERVER_PID 2>/dev/null || true; wait $SERVER_PID 2>/dev/null || true; rm -rf "$WORK"' EXIT

echo "==> building example"
(cd "$ROOT/example" && go build -o "$WORK/example" .)

echo "==> booting on $ADDR"
"$WORK/example" -addr ":8399" -db "$WORK/e2e.db" >"$WORK/server.log" 2>&1 &
SERVER_PID=$!

for _ in $(seq 1 50); do
  curl -sf -o /dev/null "$BASE/auth/login" && break
  sleep 0.2
done

fail() { echo "FAIL: $1"; echo "--- server log:"; tail -20 "$WORK/server.log"; exit 1; }

echo "==> unauthenticated redirect"
code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/")
[ "$code" = "302" ] || fail "expected 302 for anonymous /admin/, got $code"

echo "==> login"
curl -s -c "$JAR" -o "$WORK/login.html" "$BASE/auth/login"
TOKEN=$(grep -o 'name="_token" value="[^"]*"' "$WORK/login.html" | sed 's/.*value="//;s/"$//')
[ -n "$TOKEN" ] || fail "no CSRF token on login page"
code=$(curl -s -b "$JAR" -c "$JAR" -o /dev/null -w '%{http_code}' \
  -d "username=admin&password=admin&_token=$TOKEN" "$BASE/auth/login")
[ "$code" = "302" ] || fail "login expected 302, got $code"

echo "==> grid HTML + fragment"
curl -s -b "$JAR" -o "$WORK/grid.html" "$BASE/posts"
grep -q "table" "$WORK/grid.html" || fail "posts grid missing table"
curl -s -b "$JAR" -H "HX-Request: true" -o "$WORK/frag.html" "$BASE/posts"
if grep -q "<html" "$WORK/frag.html"; then fail "fragment contains <html>"; fi

echo "==> JSON negotiation"
total=$(curl -s -b "$JAR" -H "Accept: application/json" "$BASE/posts" | python3 -c 'import json,sys;print(json.load(sys.stdin)["total"])')
[ "$total" -ge 1 ] || fail "JSON index total < 1"

echo "==> create post (422 then success)"
curl -s -b "$JAR" -o "$WORK/create.html" "$BASE/posts/create"
CSRF=$(grep -o 'name="csrf-token" content="[^"]*"' "$WORK/create.html" | sed 's/.*content="//;s/"$//')
code=$(curl -s -b "$JAR" -o /dev/null -w '%{http_code}' -X POST \
  -H "X-CSRF-Token: $CSRF" -H "Accept: application/json" \
  --data-urlencode "Title=" "$BASE/posts")
[ "$code" = "422" ] || fail "empty create expected 422, got $code"
out=$(curl -s -b "$JAR" -X POST -H "X-CSRF-Token: $CSRF" -H "Accept: application/json" \
  --data-urlencode "Title=e2e post" --data-urlencode "Body=made by e2e" \
  --data-urlencode "Status=draft" --data-urlencode "AuthorID=1" "$BASE/posts")
echo "$out" | grep -q '"status":true' || fail "create failed: $out"

echo "==> delete created post"
newid=$(curl -s -b "$JAR" -H "Accept: application/json" "$BASE/posts?posts_q=e2e" | python3 -c 'import json,sys;print(json.load(sys.stdin)["items"][0]["ID"])')
out=$(curl -s -b "$JAR" -X DELETE -H "X-CSRF-Token: $CSRF" -H "Accept: application/json" "$BASE/posts/$newid")
echo "$out" | grep -q '"status":true' || fail "delete failed: $out"

echo "==> CSV export"
curl -s -b "$JAR" -o "$WORK/export.csv" "$BASE/posts?posts_export=all"
head -1 "$WORK/export.csv" | grep -q "Title" || fail "CSV export missing header"

echo "e2e OK"
