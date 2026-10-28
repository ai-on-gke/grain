package ghwebhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// GitHubSignatureHeader is GitHub's HMAC-SHA256 webhook signature header.
	GitHubSignatureHeader = "X-Hub-Signature-256"
	// GitHubEventHeader names the webhook event kind (e.g. "pull_request").
	GitHubEventHeader = "X-GitHub-Event"
	// GitHubDeliveryHeader carries GitHub's unique UUID per webhook delivery.
	GitHubDeliveryHeader = "X-GitHub-Delivery"

	// RelaySignatureHeader and RelayTimestampHeader authenticate internal
	// event reads between grain daemon and github-webhook, using the same
	// timestamped-HMAC scheme as pkg/checkpost.
	RelaySignatureHeader = "X-Grain-Webhook-Signature"
	RelayTimestampHeader = "X-Grain-Webhook-Timestamp"

	// MaxClockSkew bounds replay tolerance on internal relay requests.
	MaxClockSkew = 5 * time.Minute
)

var (
	ErrNoSecret     = errors.New("ghwebhook: service secret is not configured")
	ErrUnsigned     = errors.New("ghwebhook: missing signature")
	ErrBadSignature = errors.New("ghwebhook: signature does not match")
	ErrStaleRequest = errors.New("ghwebhook: timestamp is outside the accepted window")
)

// SignGitHubPayload computes GitHub's X-Hub-Signature-256 header value
// ("sha256=<hex>") for body under keyMaterial.
func SignGitHubPayload(keyMaterial string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(keyMaterial))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// VerifyGitHubSignature verifies GitHub's X-Hub-Signature-256 header against
// one or more configured webhook keys (supporting comma-separated key
// rotation with zero downtime). Constant-time hmac.Equal is used for every
// candidate key.
func VerifyGitHubSignature(configuredKeys, sigHeader string, body []byte) error {
	keys := splitKeys(configuredKeys)
	if len(keys) == 0 {
		return ErrNoSecret
	}
	sigHex, ok := strings.CutPrefix(strings.TrimSpace(sigHeader), "sha256=")
	if !ok || sigHex == "" {
		return ErrUnsigned
	}
	wantBytes, err := hex.DecodeString(sigHex)
	if err != nil || len(wantBytes) != sha256.Size {
		return ErrBadSignature
	}
	for _, k := range keys {
		mac := hmac.New(sha256.New, []byte(k))
		mac.Write(body)
		if hmac.Equal(mac.Sum(nil), wantBytes) {
			return nil
		}
	}
	return ErrBadSignature
}

func splitKeys(raw string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\n' }) {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// SignRelay signs an internal daemon-to-gateway HTTP request over its
// timestamp, method, path+query, and body.
func SignRelay(relayKey string, ts time.Time, method, requestURI string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(relayKey))
	fmt.Fprintf(mac, "%d.%s %s.", ts.Unix(), method, requestURI)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyRelay authenticates an internal daemon-to-gateway request.
func VerifyRelay(relayKey, sigHeader, tsHeader, method, requestURI string, body []byte, now time.Time) error {
	if strings.TrimSpace(relayKey) == "" {
		return ErrNoSecret
	}
	if sigHeader == "" || tsHeader == "" {
		return ErrUnsigned
	}
	secs, err := strconv.ParseInt(tsHeader, 10, 64)
	if err != nil {
		return ErrStaleRequest
	}
	ts := time.Unix(secs, 0)
	if d := now.Sub(ts); d > MaxClockSkew || d < -MaxClockSkew {
		return ErrStaleRequest
	}
	expected := SignRelay(relayKey, ts, method, requestURI, body)
	if !hmac.Equal([]byte(expected), []byte(sigHeader)) {
		return ErrBadSignature
	}
	return nil
}
