// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package checkpost

// The one thing standing between this endpoint and the internet.
//
// A shared secret and an HMAC over the request, which is the shape
// GitHub itself uses for webhooks and the shape a workflow step can
// produce in three lines of shell with nothing installed:
//
//	sig=$(printf '%s.%s %s.%s' "$ts" POST /v1/verdicts "$body" | openssl dgst -sha256 -hmac "$SECRET" -r | cut -d' ' -f1)
//
// That constraint -- what can a `run:` block do with no `uses:` and no
// network -- is why this is not a bearer token, an OAuth exchange or a
// signed JWT. A bearer token would also work, and would be simpler, but
// it travels in every request in a form that any log or proxy in front
// of this service records verbatim; an HMAC over the request does not,
// and costs the reporter one more line.
//
// The timestamp is what stops a replay. Without it, anybody who captured
// one request could repost the verdict it carried for as long as the
// secret lived -- and the verdict most worth reposting is a success.
//
// The method and path are what stop a captured request being pointed
// somewhere else. A read has no body, so a signature over the timestamp
// and the body alone is the same for every read made in the same second,
// and one read request lifted out of a proxy log would have read every
// repository's verdicts for as long as its timestamp stayed fresh -- the
// enumeration Server's doc comment says signing reads exists to prevent.
//
// The path is the route as this service sees it ("/v1/verdicts/acme/
// widgets/<sha>"), without whatever prefix a reporter's base URL carries
// in front of it, and without a query string: none of the routes reads
// one.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// SignatureHeader carries the hex HMAC, and TimestampHeader the unix
// seconds it was computed at. Named with this service's own prefix
// rather than something generic so that a proxy in front of it cannot
// confuse them with anybody else's.
const (
	SignatureHeader = "X-Checkpost-Signature"
	TimestampHeader = "X-Checkpost-Timestamp"
)

// MaxClockSkew is how far a reporter's clock may be from this service's
// before its request is refused.
//
// Five minutes, which is GitHub's own webhook tolerance and comfortably
// more than the drift between two machines running NTP. It bounds the
// replay window to the same five minutes: a captured request is
// repostable until its timestamp ages out, and no longer.
const MaxClockSkew = 5 * time.Minute

// ErrUnsigned and its siblings are the ways a request fails
// authentication. They are deliberately not distinguished in what the
// server says back -- see Server.report -- but they are separate here so
// a test can assert which one happened.
var (
	ErrUnsigned     = errors.New("checkpost: no signature")
	ErrBadSignature = errors.New("checkpost: signature does not match")
	ErrStaleRequest = errors.New("checkpost: timestamp is outside the accepted window")
)

// Sign returns the value for SignatureHeader over one request: its
// method, its path (as the service routes it) and its body, at ts.
//
// Exported because the reporter needs it too: `checkpost report` (the
// same binary, in client mode) signs with this, so there is one
// implementation of the scheme rather than one in Go and one in shell
// that drift apart the first time either is touched.
func Sign(secret string, ts time.Time, method, path string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.%s %s.", ts.Unix(), method, path)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// signLegacy is the scheme before the method and path were signed:
// "<ts>." + body. Kept only so verify can go on accepting it for a
// report while workflows still carrying the old `run:` step move over --
// see verify.
func signLegacy(secret string, ts time.Time, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", ts.Unix())
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify checks a request's signature, over its method, path and body,
// and its freshness. It accepts only the current scheme; the server's
// transition window for the old one is verify's legacyReports.
//
// hmac.Equal rather than ==, because a byte-by-byte comparison that
// returns early leaks, through timing, how much of a guessed signature
// was right -- which is enough to find the rest one byte at a time.
func Verify(secret, signature, timestamp, method, path string, body []byte, now time.Time) error {
	_, err := verify(secret, signature, timestamp, method, path, body, now, false)
	return err
}

// verify is Verify with the transition window: with legacyReports set, a
// POST signed the old way -- over the timestamp and body only -- is
// accepted too, and legacy says that is what happened so the server can
// log it and an operator can see who still has to move.
//
// Only a POST. A report's body names the repository, commit and check it
// is about, so the old signature already pinned what a report could do,
// and the window costs nothing but a replay of the same report to the
// same route -- which the timestamp bounds and supersedes refuses
// anyway. A read's old signature pinned nothing, which is the whole of
// the hole this closes, so a read signed the old way is refused outright:
// the only reader is grain's own Client, which ships with this change.
func verify(secret, signature, timestamp, method, path string, body []byte, now time.Time, legacyReports bool) (legacy bool, err error) {
	if signature == "" || timestamp == "" {
		return false, ErrUnsigned
	}
	secs, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false, ErrStaleRequest
	}
	ts := time.Unix(secs, 0)
	if d := now.Sub(ts); d > MaxClockSkew || d < -MaxClockSkew {
		return false, ErrStaleRequest
	}
	if hmac.Equal([]byte(Sign(secret, ts, method, path, body)), []byte(signature)) {
		return false, nil
	}
	if legacyReports && method == "POST" &&
		hmac.Equal([]byte(signLegacy(secret, ts, body)), []byte(signature)) {
		return true, nil
	}
	return false, ErrBadSignature
}
