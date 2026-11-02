#!/bin/sh
# The curl matrix of docs/test-plans/2026-11-02.md §1 "not proved" 1: each
# row sends one request to a running `grain daemon` started with the flags
# in run-daemon.sh and prints the status code and the start of the body.
#
#   STUB=path/to/iapstub KEYS=dir/with/k1.pem,k2.pem DAEMON=http://127.0.0.1:4300 \
#   CODE=$(cat data/local-api-token) ./matrix.sh
set -u
: "${STUB:?}" "${KEYS:?}" "${DAEMON:?}" "${CODE:?}"
AUD=${AUD:-/projects/123/global/backendServices/456}
ISS=${ISS:-https://iap.test.local}
EMAIL=${EMAIL:-owner@example.com}
PATH_=${PATH_:-/api/auth/user}

sign() { "$STUB" sign -dir "$KEYS" -email "$EMAIL" -aud "$AUD" -iss "$ISS" "$@"; }

row() {
	name=$1
	shift
	out=$(curl -s -w '\n%{http_code}' "$@" "$DAEMON$PATH_")
	code=$(printf '%s' "$out" | tail -n1)
	body=$(printf '%s' "$out" | sed '$d' | tr '\n' ' ' | cut -c1-160)
	printf '| %s | %s | `%s` |\n' "$name" "$code" "$body"
}

H=X-Goog-IAP-JWT-Assertion
row "valid assertion, no access code" -H "$H: $(sign)"
row "expired (exp -10m)" -H "$H: $(sign -exp -10m -iat -20m)"
row "wrong audience" -H "$H: $(AUD=/projects/999/global/backendServices/1 sign)"
row "wrong issuer" -H "$H: $(ISS=https://evil.example sign)"
row "unknown kid (k2, unpublished)" -H "$H: $(sign -kid k2)"
row "kid k1, signed by k2" -H "$H: $(sign -kid k1 -key k2)"
row "bad signature (byte flipped)" -H "$H: $(sign -badsig)"
row "not a JWT" -H "$H: garbage"
row "issued 10m in the future" -H "$H: $(sign -iat 10m -exp 1h)"
row "no assertion, no code" 
row "no assertion, access code" -H "Authorization: Bearer $CODE"
row "plaintext X-Goog-Authenticated-User-Email only" -H "X-Goog-Authenticated-User-Email: accounts.google.com:$EMAIL"
row "plaintext email + X-Goog-Authenticated-User-Id" -H "X-Goog-Authenticated-User-Email: accounts.google.com:$EMAIL" -H "X-Goog-Authenticated-User-Id: accounts.google.com:1"
row "invalid assertion + valid access code" -H "$H: $(sign -badsig)" -H "Authorization: Bearer $CODE"
