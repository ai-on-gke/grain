package gitproxy

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileAuditLogAppendsOneJSONLinePerEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "git-proxy", "audit.log")
	log, err := NewFileAuditLog(path)
	if err != nil {
		t.Fatal(err)
	}
	log.Record(AuditEntry{
		Time:    time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Sandbox: "sandbox-0", Owner: "acme", Repo: "widgets",
		Action: "git-upload-pack", Credential: "bot", Outcome: "forwarded: 200",
	})
	log.Record(AuditEntry{
		Sandbox: "sandbox-1", Owner: "acme", Repo: "gadgets",
		Action: "git-receive-pack", Outcome: "denied: not allow-listed",
	})

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	var lines []map[string]any
	for scanner.Scan() {
		var m map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, m)
	}
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	if lines[0]["repo"] != "acme/widgets" || lines[0]["credential"] != "bot" {
		t.Errorf("line 0 = %v", lines[0])
	}
	if lines[1]["outcome"] != "denied: not allow-listed" {
		t.Errorf("line 1 = %v", lines[1])
	}
	if _, ok := lines[1]["credential"]; ok {
		t.Errorf("expected no credential field when none was selected, got %v", lines[1]["credential"])
	}
}

func TestRecordingAuditLogCollectsEntriesInMemory(t *testing.T) {
	log := &RecordingAuditLog{}
	log.Record(AuditEntry{Sandbox: "sandbox-0", Owner: "acme", Repo: "widgets", Action: "info/refs"})
	if len(log.Entries) != 1 || log.Entries[0].Sandbox != "sandbox-0" {
		t.Errorf("Entries = %+v", log.Entries)
	}
}

func TestNullAuditLogDiscardsEverything(t *testing.T) {
	// Must not panic; nothing else to assert.
	NullAuditLog{}.Record(AuditEntry{})
}
