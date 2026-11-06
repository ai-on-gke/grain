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

package dispatchcondition_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/dispatchcondition"
)

func TestBlockAndClearAreRecordedAndLogged(t *testing.T) {
	now := time.Date(2026, 11, 4, 15, 0, 0, 0, time.UTC)
	var lines []string
	s := &dispatchcondition.Set{
		Now:  func() time.Time { return now },
		Logf: func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) },
	}
	if s.Blocked() || s.Why() != "" || s.Active() != nil {
		t.Fatal("a fresh set is blocked")
	}

	s.Block("git-proxy", errors.New("listen tcp 0.0.0.0:7100: bind: address already in use"), "free the port and restart")
	if !s.Blocked() {
		t.Fatal("Blocked() = false after Block")
	}
	active := s.Active()
	if len(active) != 1 || active[0].Name != "git-proxy" || !active[0].Since.Equal(now) ||
		active[0].Remedy != "free the port and restart" {
		t.Fatalf("Active() = %+v", active)
	}
	if got, want := s.Why(), "git proxy: listen tcp 0.0.0.0:7100: bind: address already in use"; got != want {
		t.Errorf("Why() = %q, want %q", got, want)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "dispatch blocked [git-proxy]") || !strings.Contains(lines[0], "address already in use") {
		t.Errorf("log lines after Block = %q", lines)
	}

	// Re-reporting the same condition moves nothing and logs nothing.
	now = now.Add(time.Minute)
	s.Block("git-proxy", errors.New("still in use"), "")
	if got := s.Active(); len(got) != 1 || !got[0].Since.Equal(now.Add(-time.Minute)) || got[0].Error != "still in use" {
		t.Errorf("Active() after a repeat Block = %+v, want the original Since and the new text", got)
	}
	if len(lines) != 1 {
		t.Errorf("a repeat Block logged: %q", lines)
	}

	// A second condition joins it; Why names the oldest and counts the rest.
	s.Block("reconciler", errors.New("panic: nil map"), "")
	if got := s.Why(); !strings.HasPrefix(got, "git proxy: still in use (and reconciler)") {
		t.Errorf("Why() with two = %q", got)
	}

	now = now.Add(time.Minute)
	s.Clear("git-proxy")
	s.Clear("git-proxy") // nothing to do
	s.Clear("never-set") // nothing to do
	if got := s.Active(); len(got) != 1 || got[0].Name != "reconciler" {
		t.Errorf("Active() after Clear = %+v", got)
	}
	if len(lines) != 3 || !strings.Contains(lines[2], "dispatch unblocked [git-proxy] after 2m0s") {
		t.Errorf("log lines after Clear = %q", lines)
	}
	s.Clear("reconciler")
	if s.Blocked() {
		t.Error("Blocked() = true with everything cleared")
	}

	history := s.History()
	if len(history) != 4 {
		t.Fatalf("History() has %d events, want 4: %+v", len(history), history)
	}
	for i, want := range []struct {
		set  bool
		name string
	}{{true, "git-proxy"}, {true, "reconciler"}, {false, "git-proxy"}, {false, "reconciler"}} {
		if history[i].Set != want.set || history[i].Condition.Name != want.name {
			t.Errorf("History()[%d] = %+v, want set=%v %s", i, history[i], want.set, want.name)
		}
	}
	if !history[2].At.Equal(now) || !history[2].Condition.Since.Equal(now.Add(-2*time.Minute)) {
		t.Errorf("the clear event = %+v, want At now and the condition's original Since", history[2])
	}
}

func TestNilSetIsNotBlocked(t *testing.T) {
	var s *dispatchcondition.Set
	if s.Blocked() || s.Why() != "" || s.Active() != nil || s.History() != nil {
		t.Error("a nil Set reports a condition")
	}
}

func TestBlockWithNilErrorStillNamesTheCondition(t *testing.T) {
	s := &dispatchcondition.Set{Logf: func(string, ...any) {}}
	s.Block("sandbox-backend", nil, "")
	if got := s.Why(); got != "sandbox backend: unknown error" {
		t.Errorf("Why() = %q", got)
	}
}

// A condition is read from places the journal is not -- every queued
// task's reason, which an agent reads from its sandbox, GET /api/config,
// the health route -- so a multi-line error, which is runDaemon's
// recovered panic and its goroutine stack, is kept to its first line
// there. The stack is in the journal, where run() logs the whole error.
func TestBlockKeepsOnlyTheErrorsFirstLine(t *testing.T) {
	var lines []string
	s := &dispatchcondition.Set{Logf: func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }}
	stack := "goroutine 42 [running]:\nruntime/debug.Stack()\n\t/usr/local/go/src/runtime/debug/stack.go:26 +0x5e\n" +
		"github.com/ai-on-gke/grain/pkg/daemon.runDaemon.func1()\n\t/home/builder/grain/pkg/daemon/run.go:1140 +0x3c"
	s.Block("reconciler", fmt.Errorf("panic: runtime error: index out of range [3] with length 2\n%s", stack), "")

	got := s.Active()[0].Error
	if strings.Contains(got, "\n") || strings.Contains(got, "goroutine") || strings.Contains(got, "/home/builder") {
		t.Errorf("Error = %q, want the panic's first line without its stack", got)
	}
	if !strings.HasPrefix(got, "panic: runtime error: index out of range [3] with length 2") || !strings.Contains(got, "journal") {
		t.Errorf("Error = %q, want the first line and a pointer to the journal", got)
	}
	if why := s.Why(); strings.Contains(why, "\n") {
		t.Errorf("Why() = %q, want one line", why)
	}
	if len(lines) != 1 || strings.Contains(lines[0], "goroutine") {
		t.Errorf("log lines = %q, want one, without the stack", lines)
	}

	// A one-line error is kept as it is, and trailing whitespace is not
	// a second line.
	s.Block("git-proxy", errors.New("listen tcp :7100: address already in use\n"), "")
	if got := s.Active()[1].Error; got != "listen tcp :7100: address already in use" {
		t.Errorf("Error = %q, want the one line, trimmed", got)
	}
}

// The text a condition keeps is served to every reader of a queued
// task's reason -- an agent in its sandbox among them -- and to the
// health, config and feed routes, so a Set with a Redact runs the text
// through it before anything, the journal line included, sees it. A
// re-report with different words moves Version, so a cache keyed on it
// re-reads them; the same words move nothing.
func TestBlockRedactsTheTextItKeepsAndVersionsAChangeOfWords(t *testing.T) {
	var lines []string
	s := &dispatchcondition.Set{
		Logf:   func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) },
		Redact: func(text string) string { return strings.ReplaceAll(text, "ghp_secret", "[REDACTED]") },
	}
	s.Block("reconciler", errors.New("posting to https://x:ghp_secret@github.com failed"), "")
	if got := s.Active()[0].Error; strings.Contains(got, "ghp_secret") || !strings.Contains(got, "[REDACTED]") {
		t.Errorf("Error = %q, want the credential redacted", got)
	}
	if why := s.Why(); strings.Contains(why, "ghp_secret") {
		t.Errorf("Why() = %q, want the credential redacted", why)
	}
	if len(lines) != 1 || strings.Contains(lines[0], "ghp_secret") {
		t.Errorf("log lines = %q, want one, with the credential redacted", lines)
	}
	if got := s.History()[0].Condition.Error; strings.Contains(got, "ghp_secret") {
		t.Errorf("History()[0].Condition.Error = %q, want the credential redacted", got)
	}

	v := s.Version()
	s.Block("reconciler", errors.New("posting to https://x:ghp_secret@github.com failed"), "")
	if s.Version() != v {
		t.Errorf("Version moved from %d to %d on a re-report with the same words", v, s.Version())
	}
	s.Block("reconciler", errors.New("something else"), "")
	if s.Version() == v {
		t.Error("Version did not move on a re-report with different words")
	}
	if len(s.History()) != 1 {
		t.Errorf("History() has %d events after re-reports, want the one set", len(s.History()))
	}
}
