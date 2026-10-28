package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"
)

// CredentialAccessEntry records one use of a team's credential that did
// not go through the secrets store: today, a git proxy request a team's
// task run (or a team's CI job) made, and which credential it went out
// under. It is the team-scoped half of the proxy's own audit.log, which
// stays a deployment file only a system admin can read (/api/logs).
//
// Nothing in it is credential material: Credential is the credential's
// name, and Outcome is passed through RedactSecretMaterial before it is
// written.
type CredentialAccessEntry struct {
	ID         int64     `json:"id"`
	Timestamp  time.Time `json:"timestamp"`
	TaskID     string    `json:"taskId,omitempty"`
	RunID      string    `json:"runId,omitempty"`
	Sandbox    string    `json:"sandbox,omitempty"`
	Kind       string    `json:"kind"`
	Resource   string    `json:"resource"`
	Action     string    `json:"action,omitempty"`
	Credential string    `json:"credential,omitempty"`
	Outcome    string    `json:"outcome,omitempty"`
}

// CredentialAccessGit is CredentialAccessEntry.Kind for a git proxy request.
const CredentialAccessGit = "git"

// CredentialAccessConnection is CredentialAccessEntry.Kind for a change to
// what one of the team's connections runs: its CLI path
// (AgentBackend.CLIPath), which decides the binary the daemon execs with
// that connection's credential.
const CredentialAccessConnection = "connection"

// CredentialAccessModelProxy is CredentialAccessEntry.Kind for a model
// proxy request a team's run made that the proxy refused, or that met a
// usage limit on the team connection it was forwarded with.
const CredentialAccessModelProxy = "model-proxy"

// TeamAuditKindSecret is TeamAuditEvent.Kind for a secret resolution.
const TeamAuditKindSecret = "secret"

// TeamAuditEvent is one row of a team's audit trail, as
// GET /api/deployment/audit-logs answers it: a secret resolution
// (secret_resolution_audit) or a credential access
// (credential_access_audit), in one shape, newest first.
type TeamAuditEvent struct {
	Timestamp  time.Time `json:"timestamp"`
	Kind       string    `json:"kind"`
	TaskID     string    `json:"taskId,omitempty"`
	RunID      string    `json:"runId,omitempty"`
	Sandbox    string    `json:"sandbox,omitempty"`
	Resource   string    `json:"resource"`
	Action     string    `json:"action,omitempty"`
	Credential string    `json:"credential,omitempty"`
	Outcome    string    `json:"outcome,omitempty"`
	Principal  string    `json:"principal,omitempty"`
}

// secretMaterialPatterns are the shapes of credential material that could
// end up inside a free-text audit field -- most plausibly an upstream
// error message quoting a URL or a header back. Each is replaced
// wholesale by RedactSecretMaterial.
var secretMaterialPatterns = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(-----END [A-Z ]*PRIVATE KEY-----|$)`),
	regexp.MustCompile(`"private_key"\s*:\s*"[^"]*"?`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{16,}`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{16,}`),
	regexp.MustCompile(`\bya29\.[A-Za-z0-9._\-]+`),
	regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{30,}`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_\-]{16,}`),
	regexp.MustCompile(`(?i)\b(bearer|basic|token)\s+[A-Za-z0-9._~+/=\-]{8,}`),
}

// userinfoPattern is the user:password@ of a URL.
var userinfoPattern = regexp.MustCompile(`://[^/\s@]+@`)

// RedactSecretMaterial returns s with anything shaped like credential
// material -- a token, a bearer header, a URL's userinfo, a private key
// -- replaced by [REDACTED]. Team audit trails are read by team admins
// who are not trusted with the material itself, only with the fact that
// it was used.
func RedactSecretMaterial(s string) string {
	if s == "" {
		return s
	}
	s = userinfoPattern.ReplaceAllString(s, "://[REDACTED]@")
	for _, p := range secretMaterialPatterns {
		s = p.ReplaceAllString(s, "[REDACTED]")
	}
	return s
}

// AuditAttribution is who a sandbox's credential use is charged to.
type AuditAttribution struct {
	TaskID string
	RunID  string
}

// SandboxAuditAttribution is the live run on sandbox, as the task and run
// a credential use from it is recorded against. ok is false,
// with a nil error, for a sandbox with nothing running on it.
func (s *Store) SandboxAuditAttribution(ctx context.Context, sandbox string) (AuditAttribution, bool, error) {
	if sandbox == "" {
		return AuditAttribution{}, false, nil
	}
	var a AuditAttribution
	err := s.db.QueryRowContext(ctx,
		"SELECT r.`id`, r.`task_id` FROM `task_run` r "+
			"WHERE r.`sandbox` = ? AND r.`finished_at` IS NULL "+
			"ORDER BY r.`started_at` DESC LIMIT 1", sandbox).Scan(&a.RunID, &a.TaskID)
	if errors.Is(err, sql.ErrNoRows) {
		return AuditAttribution{}, false, nil
	}
	if err != nil {
		return AuditAttribution{}, false, fmt.Errorf("attributing sandbox %s: %w", sandbox, err)
	}
	return a, true, nil
}

// RecordCredentialAccess appends entry to credential_access_audit, with
// its free-text fields redacted.
func (s *Store) RecordCredentialAccess(ctx context.Context, entry CredentialAccessEntry) error {
	ts := entry.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	kind := entry.Kind
	if kind == "" {
		kind = CredentialAccessGit
	}
	return s.write(ctx, "record credential access audit", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"INSERT INTO `credential_access_audit` (`timestamp`, `task_id`, `run_id`, `sandbox`, `kind`, `resource`, `action`, `credential`, `outcome`) "+
				"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
			ts.UTC(), entry.TaskID, entry.RunID, entry.Sandbox, kind,
			RedactSecretMaterial(entry.Resource), entry.Action, entry.Credential, RedactSecretMaterial(entry.Outcome))
		if err != nil {
			return fmt.Errorf("recording credential access: %w", err)
		}
		return nil
	})
}

// ListCredentialAccess returns the deployment's most recent credential
// accesses, newest first.
func (s *Store) ListCredentialAccess(ctx context.Context, limit int) ([]CredentialAccessEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT `id`, `timestamp`, `task_id`, `run_id`, `sandbox`, `kind`, `resource`, `action`, `credential`, `outcome` "+
			"FROM `credential_access_audit` ORDER BY `timestamp` DESC, `id` DESC LIMIT ?",
		limit)
	if err != nil {
		return nil, fmt.Errorf("listing credential access audit: %w", err)
	}
	defer rows.Close()
	entries := []CredentialAccessEntry{}
	for rows.Next() {
		var e CredentialAccessEntry
		if err := rows.Scan(&e.ID, &e.Timestamp, &e.TaskID, &e.RunID, &e.Sandbox, &e.Kind, &e.Resource, &e.Action, &e.Credential, &e.Outcome); err != nil {
			return nil, fmt.Errorf("scanning credential access audit row: %w", err)
		}
		e.Timestamp = e.Timestamp.UTC()
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// ListTeamAuditEvents is the deployment's audit trail: its secret
// resolutions and its credential accesses merged, newest first, at most
// limit of them.
func (s *Store) ListTeamAuditEvents(ctx context.Context, limit int) ([]TeamAuditEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	secretsUsed, err := s.ListSecretAudit(ctx, limit)
	if err != nil {
		return nil, err
	}
	accesses, err := s.ListCredentialAccess(ctx, limit)
	if err != nil {
		return nil, err
	}
	events := make([]TeamAuditEvent, 0, len(secretsUsed)+len(accesses))
	for _, e := range secretsUsed {
		resource := e.SecretName
		if e.Key != "" {
			resource += "/" + e.Key
		}
		events = append(events, TeamAuditEvent{
			Timestamp: e.Timestamp, Kind: TeamAuditKindSecret,
			TaskID: e.TaskID, RunID: e.RunID, Resource: resource,
			Action: "resolve", Principal: e.Principal,
		})
	}
	for _, e := range accesses {
		events = append(events, TeamAuditEvent{
			Timestamp: e.Timestamp, Kind: e.Kind,
			TaskID: e.TaskID, RunID: e.RunID, Sandbox: e.Sandbox, Resource: e.Resource,
			Action: e.Action, Credential: e.Credential, Outcome: e.Outcome,
		})
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].Timestamp.After(events[j].Timestamp) })
	if len(events) > limit {
		events = events[:limit]
	}
	return events, nil
}
