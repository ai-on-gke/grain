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
