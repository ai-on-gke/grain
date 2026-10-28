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
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

var accessCodeShape = regexp.MustCompile(`^[A-Z2-7]{4}(-[A-Z2-7]{4}){7}$`)

func TestAccessCodesAreRandomAndCopyPasteable(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		code, err := newAccessCode()
		if err != nil {
			t.Fatal(err)
		}
		if !accessCodeShape.MatchString(code) {
			t.Fatalf("access code %q is not eight dash-separated groups of four base32 characters", code)
		}
		raw, err := accessCodeEncoding.DecodeString(canonicalAccessCode(code))
		if err != nil || len(raw)*8 < 128 {
			t.Fatalf("access code %q decodes to %d bits (%v), want at least 128", code, len(raw)*8, err)
		}
		if seen[code] {
			t.Fatalf("access code %q generated twice", code)
		}
		seen[code] = true
	}
}

func TestEnsureLocalAPITokenWritesAnAccessCodeOnlyItsOwnerReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), LocalAPITokenFileName)
	code, err := EnsureLocalAPIToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if !accessCodeShape.MatchString(code) {
		t.Errorf("a new local API token %q is not an access code", code)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("access code file mode %o, want 0600", perm)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		t.Errorf("access code file owned by uid %d, want this process's %d", st.Uid, os.Getuid())
	}

	// A restart keeps it.
	again, err := EnsureLocalAPIToken(path)
	if err != nil || again != code {
		t.Errorf("EnsureLocalAPIToken on restart: %q, %v; want the same %q", again, err, code)
	}
	// And an installation from before access codes keeps its token.
	if err := os.WriteFile(path, []byte("grain-local-0123abcd\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o604); err != nil {
		t.Fatal(err)
	}
	if kept, err := EnsureLocalAPIToken(path); err != nil || kept != "grain-local-0123abcd" {
		t.Errorf("an existing token: %q, %v; want it kept", kept, err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("an existing token's file left mode %o, want what anybody else could read taken away", fi.Mode().Perm())
	}
}

func TestRotateLocalAPITokenReplacesTheCodeAndKeepsTheMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), LocalAPITokenFileName)
	old, err := EnsureLocalAPIToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	code, err := RotateLocalAPIToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if code == old || !accessCodeShape.MatchString(code) {
		t.Errorf("rotated code %q (old %q)", code, old)
	}
	b, _ := os.ReadFile(path)
	if strings.TrimSpace(string(b)) != code {
		t.Errorf("file holds %q after rotation, want %q", b, code)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o640 {
		t.Errorf("rotation changed the file's mode to %o, want the 0640 it had", fi.Mode().Perm())
	}
}

func TestAccessCodeFileNoticesRotationAndRemoval(t *testing.T) {
	path := filepath.Join(t.TempDir(), LocalAPITokenFileName)
	old, err := EnsureLocalAPIToken(path)
	if err != nil {
		t.Fatal(err)
	}
	f := &accessCodeFile{path: path}
	if got := f.current(); got != old {
		t.Fatalf("current() = %q, want %q", got, old)
	}
	code, err := RotateLocalAPIToken(path)
	if err != nil {
		t.Fatal(err)
	}
	f.checked = time.Time{} // as if accessCodeRecheck had passed
	if got := f.current(); got != code {
		t.Errorf("after rotation current() = %q, want %q", got, code)
	}
	os.Remove(path)
	f.checked = time.Time{}
	if got := f.current(); got != "" {
		t.Errorf("with the file gone current() = %q, want no code at all", got)
	}
}

func TestAccessSessionsExpireAndDieWithTheirCode(t *testing.T) {
	code, _ := newAccessCode()
	other, _ := newAccessCode()
	now := time.Now()
	tok := accessSessionToken(code, now)
	if strings.Contains(tok, canonicalAccessCode(code)) {
		t.Fatalf("session token %q carries the code", tok)
	}
	if _, ok := verifyAccessSession(tok, code, time.Hour, now.Add(30*time.Minute)); !ok {
		t.Error("a live session was refused")
	}
	if _, ok := verifyAccessSession(tok, code, time.Hour, now.Add(2*time.Hour)); ok {
		t.Error("an expired session was accepted")
	}
	if _, ok := verifyAccessSession(tok, other, time.Hour, now); ok {
		t.Error("a session signed with a rotated-away code was accepted")
	}
	if _, ok := verifyAccessSession(accessSessionToken(code, now.Add(time.Hour)), code, time.Hour, now); ok {
		t.Error("a session issued in the future was accepted")
	}
	forged := strings.Replace(tok, "v1.", "v1.9", 1)
	if _, ok := verifyAccessSession(forged, code, time.Hour, now); ok {
		t.Error("a session with an edited issue time was accepted")
	}
	if _, ok := verifyAccessSession(tok, "", time.Hour, now); ok {
		t.Error("a session was accepted with no code configured")
	}
}

func TestMatchesAccessCode(t *testing.T) {
	code := "ABCD-EFGH-IJKL-MNOP-QRST-UVWX-YZ23-4567"
	for _, got := range []string{code, strings.ToLower(code), "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567", " " + code + "\n"} {
		if !matchesAccessCode(got, code) {
			t.Errorf("%q did not match %q", got, code)
		}
	}
	for _, got := range []string{"", "ABCD", code + "A", "ABCD-EFGH-IJKL-MNOP-QRST-UVWX-YZ23-4566"} {
		if matchesAccessCode(got, code) {
			t.Errorf("%q matched %q", got, code)
		}
	}
	if matchesAccessCode("", "") {
		t.Error("an empty code matched an empty one")
	}
}

func TestAccessLimiterLimitsPerSourceAndGlobally(t *testing.T) {
	var l accessLimiter
	now := time.Now()
	for i := 0; i < accessSourceBurst; i++ {
		if l.sourceLimited("a", now) {
			t.Fatalf("source a refused after %d wrong codes, want %d allowed", i, accessSourceBurst)
		}
		if over := l.failed("a", now); over != (i == accessSourceBurst-1) {
			t.Fatalf("wrong code %d from a: over its limit %v", i+1, over)
		}
	}
	if !l.sourceLimited("a", now) {
		t.Error("source a still allowed past its burst")
	}
	if l.sourceLimited("b", now) {
		t.Error("source b refused for a's wrong codes")
	}
	if l.sourceLimited("a", now.Add(accessSourceRefill+time.Millisecond)) {
		t.Error("source a still refused after a refill interval")
	}

	// The global limit refuses nobody before their code is compared
	// (grain/task-4430): a source that has sent no wrong code is never
	// held to what other sources sent. One that sends a wrong code while
	// it is spent is held for a minute.
	var g accessLimiter
	for i := 0; i < 3*accessGlobalBurst; i++ {
		g.failed(fmt.Sprintf("x%d", i), now)
	}
	if g.sourceLimited("fresh", now) {
		t.Error("a fresh source refused while the global limit is spent")
	}
	if !g.failed("fresh", now) {
		t.Error("a wrong code while the global limit is spent left its source under its own limit")
	}
	if !g.sourceLimited("fresh", now.Add(accessSourceRefill*(accessSourceBurst-1))) {
		t.Error("a source that guessed while the global limit was spent was let guess again within the minute")
	}
	if g.sourceLimited("fresh", now.Add(accessSourceRefill*accessSourceBurst+time.Millisecond)) {
		t.Error("a source that guessed while the global limit was spent was still held after a minute")
	}
	// However long the guessing went on, the global bucket refills
	// within a couple of minutes of it stopping.
	later := now.Add(2*accessGlobalBurst*accessGlobalRefill + time.Second)
	if g.global.level(later, accessGlobalBurst, accessGlobalRefill) < 1 {
		t.Error("the global bucket was still spent two minutes after the guessing stopped")
	}
}

func TestCouldBeAccessCode(t *testing.T) {
	code := "ABCD-EFGH-IJKL-MNOP-QRST-UVWX-YZ23-4567"
	for _, got := range []string{code, "abcdefghijklmnopqrstuvwxyz234566", "WRNG-WRNG-WRNG-WRNG-WRNG-WRNG-WRNG-WRNG"} {
		if !couldBeAccessCode(got, code) {
			t.Errorf("%q not counted as a guess at %q", got, code)
		}
	}
	for _, got := range []string{
		"",
		"ABCD",
		code + "A",
		"gxt_0123456789abcdef0123456789abcdef", // an executor token
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.c2ln", // a JWT
		"ABCD.EFGH.IJKL.MNOP.QRST.UVWX.YZ23.4567"[:32],
	} {
		if couldBeAccessCode(got, code) {
			t.Errorf("%q counted as a guess at %q", got, code)
		}
	}
}

func TestExecutorBootstrapRoute(t *testing.T) {
	for target, want := range map[string]bool{
		"/_grain/executor":                      true,
		"/api/executors/connect":                true,
		"/api/executors/stream":                 true,
		"/api/hostd/connect":                    true,
		"/install-executor.sh":                  true,
		"/api/executors/install.sh":             true,
		"/api/executors/bin/grain-linux-amd64":  true,
		"/install/linux/amd64":                  true,
		"/install/linux/amd64.sha256":           true,
		"/api/executors":                        false,
		"/api/executors/tokens":                 false,
		"/api/executors/bin/":                   false,
		"/api/executors/bin/../../api/settings": false,
		"/install/linux":                        false,
		"/install/../api":                       false,
		"/install/linux/amd64/x":                false,
		"/api/tasks":                            false,
	} {
		r := httptest.NewRequest(http.MethodGet, "http://grain.example/", nil)
		r.URL.Path = target
		if got := executorBootstrapRoute(r); got != want {
			t.Errorf("GET %s: executorBootstrapRoute = %v, want %v", target, got, want)
		}
		r.Method = http.MethodPost
		if executorBootstrapRoute(r) {
			t.Errorf("POST %s counted as remote execution's", target)
		}
	}
}
