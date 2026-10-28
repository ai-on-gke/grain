// Package gritapi defines the wire types and HTTP client for grit's /v1 API
// (docs/design/grit-split.md, "The API" and "Event stream").
//
// It is a neutral package shared by grain and grit, so it imports no
// grain-only or grit-only internal packages.
package gritapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/attest"
	"github.com/ai-on-gke/grain/pkg/treeid"
)

// ClientHeader is the HTTP header carrying `<id>.<secret>` client tokens
// on direct grit requests and behind an identity proxy.
const ClientHeader = "Grit-Client"

// Scope names a grit permission scope granted to a client for a repository
// pattern (`*`, `owner/*`, or `owner/repo`).
type Scope string

const (
	ScopeSubmit     Scope = "submit"
	ScopeRead       Scope = "read"
	ScopeCancel     Scope = "cancel"
	ScopeCacheAdmin Scope = "cache-admin"
	ScopeRelease    Scope = "release"
)

// AllScopes lists every valid grit client scope.
var AllScopes = []Scope{
	ScopeSubmit,
	ScopeRead,
	ScopeCancel,
	ScopeCacheAdmin,
	ScopeRelease,
}

// ValidScope reports whether s is a known Scope.
func ValidScope(s Scope) bool {
	for _, k := range AllScopes {
		if s == k {
			return true
		}
	}
	return false
}

// PublicKeyInfo describes an active or retired attestation verification key in Info.
type PublicKeyInfo struct {
	KeyID     string           `json:"keyId"`
	PublicPEM string           `json:"publicPem,omitempty"`
	Status    attest.KeyStatus `json:"status"`
}

// Info is the response body of `GET /v1/info`.
type Info struct {
	Version               string          `json:"version"`
	Epoch                 string          `json:"epoch"`
	Executor              string          `json:"executor"`
	PackageCache          bool            `json:"packageCache"`
	AttestationPublicKeys []PublicKeyInfo `json:"attestationPublicKeys,omitempty"`
	Capacity              Capacity        `json:"capacity"`
}

// Capacity reports grit's current job concurrency counters.
type Capacity struct {
	Running int `json:"running"`
	Queued  int `json:"queued"`
	Limit   int `json:"limit"`
}

// NegotiateTreesRequest is the request body of `POST /v1/trees:negotiate`.
type NegotiateTreesRequest struct {
	Trees []string `json:"trees"`
}

// NegotiateTreesResponse is the response body of `POST /v1/trees:negotiate`.
type NegotiateTreesResponse struct {
	Missing []string `json:"missing"`
}

// SecretManifestEntry describes one capability secret binding in StartRun.
type SecretManifestEntry struct {
	Capability string `json:"capability"`
	Kind       string `json:"kind"` // "env" or "file"
	Target     string `json:"target"`
	SHA256     string `json:"sha256"`
}

// SecretPayload carries the per-request capability secrets resolved by grain.
// Blobs maps lowercase hex SHA-256 digests to raw secret bytes (serialized as
// base64 in JSON).
type SecretPayload struct {
	Manifest []SecretManifestEntry `json:"manifest,omitempty"`
	Blobs    map[string][]byte     `json:"blobs,omitempty"`
}

// StartRun is the request body of `POST /v1/runs`.
type StartRun struct {
	RequestID      string         `json:"requestId"`
	Repo           string         `json:"repo"`
	Ref            string         `json:"ref"`
	Commit         string         `json:"commit"`
	Tree           treeid.ID      `json:"tree"`
	Event          string         `json:"event"`
	TrustedRelease bool           `json:"trustedRelease,omitempty"`
	Jobs           []string       `json:"jobs,omitempty"`
	Secrets        *SecretPayload `json:"secrets,omitempty"`
	Token          string         `json:"token,omitempty"`
	Attempt        int            `json:"attempt,omitempty"`
}

// ComputeRequestID derives the deterministic idempotency key for a StartRun
// from (repo, commit, tree.sha256, event, sorted jobs, attempt).
func ComputeRequestID(r StartRun) string {
	jobs := append([]string(nil), r.Jobs...)
	sort.Strings(jobs)
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "repo=%s\ncommit=%s\ntree=%s\nevent=%s\njobs=%s\nattempt=%d\n",
		r.Repo, r.Commit, r.Tree.SHA256, r.Event, strings.Join(jobs, ","), r.Attempt)
	return hex.EncodeToString(h.Sum(nil))
}

// RetriggerRequest is the request body of `POST /v1/runs/{id}/retrigger`.
type RetriggerRequest struct {
	RequestID string         `json:"requestId,omitempty"`
	Jobs      []string       `json:"jobs,omitempty"`
	Secrets   *SecretPayload `json:"secrets,omitempty"`
	Token     string         `json:"token,omitempty"`
}

// JobStatus is the state of one job within a RunStatus.
type JobStatus struct {
	ID          string        `json:"id"`
	Name        string        `json:"name,omitempty"`
	Workflow    string        `json:"workflow,omitempty"`
	Status      string        `json:"status"` // "queued", "running", "completed"
	Conclusion  string        `json:"conclusion,omitempty"`
	Cached      bool          `json:"cached,omitempty"`
	Attempt     int           `json:"attempt,omitempty"`
	DurationMs  int64         `json:"durationMs,omitempty"`
	Tests       *attest.Tests `json:"tests,omitempty"`
	StartedAt   *time.Time    `json:"startedAt,omitempty"`
	CompletedAt *time.Time    `json:"completedAt,omitempty"`
}

// RunStatus is the response body of `POST /v1/runs` and `GET /v1/runs/{id}`.
type RunStatus struct {
	ID                string      `json:"id"`
	RequestID         string      `json:"requestId"`
	Repo              string      `json:"repo"`
	Ref               string      `json:"ref"`
	Commit            string      `json:"commit"`
	Tree              treeid.ID   `json:"tree"`
	Event             string      `json:"event"`
	TrustedRelease    bool        `json:"trustedRelease,omitempty"`
	Status            string      `json:"status"` // "queued", "running", "completed"
	Conclusion        string      `json:"conclusion,omitempty"`
	Jobs              []JobStatus `json:"jobs,omitempty"`
	AttestationSHA256 string      `json:"attestationSha256,omitempty"`
	CreatedAt         time.Time   `json:"createdAt"`
	StartedAt         *time.Time  `json:"startedAt,omitempty"`
	CompletedAt       *time.Time  `json:"completedAt,omitempty"`
}

// EventKind identifies a `/v1/events` transition record.
type EventKind string

const (
	EventRunQueued           EventKind = "run.queued"
	EventRunPlanned          EventKind = "run.planned"
	EventJobStarted          EventKind = "job.started"
	EventJobAttemptCompleted EventKind = "job.attempt.completed"
	EventJobCompleted        EventKind = "job.completed"
	EventRunAttested         EventKind = "run.attested"
	EventRunCompleted        EventKind = "run.completed"
)

// FaultSummary carries structured failure information on a job event.
type FaultSummary struct {
	Kind    string `json:"kind,omitempty"`
	Step    string `json:"step,omitempty"`
	Message string `json:"message,omitempty"`
}

// Event is one monotonic transition record in `GET /v1/events`.
type Event struct {
	Seq               int64         `json:"seq"`
	At                time.Time     `json:"at"`
	RunID             string        `json:"runId"`
	RequestID         string        `json:"requestId,omitempty"`
	Repo              string        `json:"repo"`
	Kind              EventKind     `json:"kind"`
	Job               string        `json:"job,omitempty"`
	Attempt           int           `json:"attempt,omitempty"`
	Conclusion        string        `json:"conclusion,omitempty"`
	Cached            bool          `json:"cached,omitempty"`
	DurationMs        int64         `json:"durationMs,omitempty"`
	Tests             *attest.Tests `json:"tests,omitempty"`
	Fault             *FaultSummary `json:"fault,omitempty"`
	AttestationSHA256 string        `json:"attestationSha256,omitempty"`
	Jobs              []JobStatus   `json:"jobs,omitempty"`
}

// EventBatch is the response body of `GET /v1/events`.
type EventBatch struct {
	Epoch   string  `json:"epoch"`
	Events  []Event `json:"events"`
	NextSeq int64   `json:"nextSeq"`
}

// APIError is the structured error response returned by `/v1` routes.
type APIError struct {
	StatusCode int    `json:"-"`
	Code       string `json:"error"`
	Message    string `json:"message,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	Epoch      string `json:"epoch,omitempty"`
	OldestSeq  int64  `json:"oldestSeq,omitempty"`
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("grit api (%d %s): %s", e.StatusCode, e.Code, e.Message)
	}
	return fmt.Sprintf("grit api (%d %s)", e.StatusCode, e.Code)
}

// IsTreeMissing reports whether err is a `428 tree_missing` error.
func IsTreeMissing(err error) (string, bool) {
	var apiErr *APIError
	if ok := asAPIError(err, &apiErr); ok && apiErr.StatusCode == 428 && apiErr.Code == "tree_missing" {
		return apiErr.SHA256, true
	}
	return "", false
}

// IsCursorGone reports whether err is a `410 cursor_gone` error.
func IsCursorGone(err error) (*APIError, bool) {
	var apiErr *APIError
	if ok := asAPIError(err, &apiErr); ok && apiErr.StatusCode == 410 && apiErr.Code == "cursor_gone" {
		return apiErr, true
	}
	return nil, false
}

func asAPIError(err error, target **APIError) bool {
	if err == nil {
		return false
	}
	if e, ok := err.(*APIError); ok {
		*target = e
		return true
	}
	return false
}

// DecodeAPIError parses an error response body into *APIError.
func DecodeAPIError(statusCode int, body []byte) error {
	var apiErr APIError
	if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Code != "" {
		apiErr.StatusCode = statusCode
		return &apiErr
	}
	return &APIError{
		StatusCode: statusCode,
		Code:       "http_error",
		Message:    strings.TrimSpace(string(body)),
	}
}
