package ui

import (
	"strings"
	"testing"
	"time"
)

// The line the daemon logs is the one GET /api/logs/{source} redacts:
// change one and the other has to follow.
func TestDatabaseConfirmLogLineIsRedacted(t *testing.T) {
	var d databaseConfirmations
	token, _, err := d.issue(databaseConfirmBinding("admin@example.com", "postgres://h/db", true, true), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	line := databaseConfirmLogLine(token, "database upgrade requested")
	if !strings.Contains(line, token) {
		t.Fatalf("log line does not carry the token: %q", line)
	}
	got := redactDatabaseConfirmTokens([]string{line})[0]
	if strings.Contains(got, token) || !strings.Contains(got, "database upgrade requested") {
		t.Fatalf("redacted line = %q", got)
	}
}
