package policy

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// AuditEntry is one line of the local audit log: a task this machine's
// local policy refused at dispatch, and why. The workstation's owner
// reads it from a record on their own disk that no HTTP route writes or
// erases.
type AuditEntry struct {
	Time time.Time `json:"time"`
	// Message is what wrote the entry: AuditLocalPolicy.
	Message string `json:"message"`
	// Task is the task local policy judged.
	Task string `json:"task,omitempty"`
	// Repo is the repository the task targets, if any.
	Repo string `json:"repo,omitempty"`
	// Outcome is what local policy did: AuditRefused.
	Outcome string `json:"outcome"`
	// Reason says why.
	Reason string `json:"reason,omitempty"`
	// Requester is who local policy judged the task as requested by --
	// its root requester (Requester).
	Requester string `json:"requester,omitempty"`
	// Stage is where in dispatch local policy refused the task.
	Stage string `json:"stage,omitempty"`
}

// AuditLocalPolicy is the Message of an entry the orchestrator's local
// policy writes when it refuses a task at dispatch.
const AuditLocalPolicy = "local-policy"

// AuditRefused is the Outcome of a refusal.
const AuditRefused = "refused"

// maxAuditField bounds each string an entry carries, so that one task's
// fields cannot grow the log by a megabyte an entry.
const maxAuditField = 512

func clipAuditField(s string) string {
	if len(s) <= maxAuditField {
		return s
	}
	cut := maxAuditField
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + fmt.Sprintf("… (%d bytes)", len(s))
}

// AuditLog is an append-only file of AuditEntry, one JSON object a line.
// It is opened O_APPEND and owner-only, and nothing in this package ever
// truncates, rewrites or deletes it: an entry reaches it only as
// JSON-encoded fields, so no task's fields can forge a line or remove
// one.
type AuditLog struct {
	mu sync.Mutex
	f  *os.File
}

// OpenAuditLog opens (creating it if need be) the audit log at path.
func OpenAuditLog(path string) (*AuditLog, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("policy: opening the local audit log: %w", err)
	}
	return &AuditLog{f: f}, nil
}

// Write appends e. A write that fails is logged: the entry is lost, but
// the task it records has already been refused by then.
func (a *AuditLog) Write(e AuditEntry) {
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	for _, f := range []*string{&e.Message, &e.Task, &e.Repo, &e.Reason, &e.Requester, &e.Stage} {
		*f = clipAuditField(*f)
	}
	b, err := json.Marshal(e)
	if err != nil {
		log.Printf("grain policy: encoding an audit entry: %v", err)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, err := a.f.Write(append(b, '\n')); err != nil {
		log.Printf("grain policy: writing the local audit log: %v", err)
	}
}

// Close closes the file.
func (a *AuditLog) Close() error { return a.f.Close() }

// ReadAuditLog reads every entry in the audit log at path, for a test
// or an operator's tool.
func ReadAuditLog(path string) ([]AuditEntry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []AuditEntry
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if line == "" {
			continue
		}
		var e AuditEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return out, fmt.Errorf("policy: audit log line %q: %w", line, err)
		}
		out = append(out, e)
	}
	return out, nil
}
