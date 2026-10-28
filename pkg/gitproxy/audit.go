package gitproxy

// Append-only, one-entry-per-request audit log. docs/design.md: "select
// the credential for that repo... and log the tuple." Every decision the
// proxy makes -- allowed, denied, or misconfigured -- gets an entry, so
// the powerful (personal-token) end of the credential ladder staying
// mostly unused is something an operator can actually verify rather than
// assume.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// AuditEntry is one decision the proxy made.
type AuditEntry struct {
	Time    time.Time
	Sandbox string
	// Host is the git host the request was for, empty for the default.
	Host       string
	Owner      string
	Repo       string
	Action     string
	Credential string // empty when no credential was selected
	Outcome    string
	// Ref, Task, Template and Declaration are set on the entry recorded
	// for each update of a ref other than a run's own branch that was let
	// through (RefGrant): the ref, the task whose run pushed it, and the
	// template and declaration file whose `push:` allowed it.
	Ref         string
	Task        string
	Template    string
	Declaration string
}

// AuditLog records one AuditEntry per request.
type AuditLog interface {
	Record(entry AuditEntry)
}

// NullAuditLog discards every entry.
type NullAuditLog struct{}

func (NullAuditLog) Record(AuditEntry) {}

// FileAuditLog appends one JSON line per entry to a file.
type FileAuditLog struct {
	path string
}

// NewFileAuditLog returns a FileAuditLog appending to path, creating the
// directory that holds it. The file itself is created on the first Record.
func NewFileAuditLog(path string) (*FileAuditLog, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return &FileAuditLog{path: path}, nil
}

func (l *FileAuditLog) Record(entry AuditEntry) {
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return // an audit failure must not take the proxy itself down
	}
	defer f.Close()

	line := struct {
		Time        string `json:"time"`
		Sandbox     string `json:"sandbox"`
		Host        string `json:"host,omitempty"`
		Repo        string `json:"repo"`
		Action      string `json:"action"`
		Credential  string `json:"credential,omitempty"`
		Outcome     string `json:"outcome"`
		Ref         string `json:"ref,omitempty"`
		Task        string `json:"task,omitempty"`
		Template    string `json:"template,omitempty"`
		Declaration string `json:"declaration,omitempty"`
	}{
		Time:        entry.Time.UTC().Format(time.RFC3339Nano),
		Sandbox:     entry.Sandbox,
		Host:        entry.Host,
		Repo:        fmt.Sprintf("%s/%s", entry.Owner, entry.Repo),
		Action:      entry.Action,
		Credential:  entry.Credential,
		Outcome:     entry.Outcome,
		Ref:         entry.Ref,
		Task:        entry.Task,
		Template:    entry.Template,
		Declaration: entry.Declaration,
	}
	data, err := json.Marshal(line)
	if err != nil {
		return
	}
	_, _ = f.Write(append(data, '\n'))
}

// RecordingAuditLog collects entries in memory. For tests.
type RecordingAuditLog struct {
	Entries []AuditEntry
}

func (l *RecordingAuditLog) Record(entry AuditEntry) {
	l.Entries = append(l.Entries, entry)
}
