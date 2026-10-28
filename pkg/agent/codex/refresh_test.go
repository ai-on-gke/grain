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

package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// rotatingAuthority is a strict-rotation token authority: each refresh
// token works once, and a spent one is refused with refresh_token_reused.
type rotatingAuthority struct {
	mu       sync.Mutex
	current  string
	serial   int
	requests int
	lifetime time.Duration
	// hold, when set, delays every answer, so concurrent refreshers
	// overlap at the authority.
	hold   time.Duration
	refuse string // when set, every request is refused with this code
	body   []map[string]string
}

func newRotatingAuthority(t *testing.T) (*rotatingAuthority, string) {
	a := &rotatingAuthority{current: "rt-0", lifetime: time.Hour}
	server := httptest.NewServer(a)
	t.Cleanup(server.Close)
	return a, server.URL + "/oauth/token"
}

func (a *rotatingAuthority) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var request map[string]string
	json.NewDecoder(r.Body).Decode(&request)
	if a.hold > 0 {
		time.Sleep(a.hold)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests++
	a.body = append(a.body, request)
	w.Header().Set("Content-Type", "application/json")
	if a.refuse != "" {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"error":{"code":%q,"message":"refused by the stub"}}`, a.refuse)
		return
	}
	if request["refresh_token"] != a.current {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"code":"refresh_token_reused","message":"already used"}}`)
		return
	}
	a.serial++
	a.current = fmt.Sprintf("rt-%d", a.serial)
	json.NewEncoder(w).Encode(map[string]string{
		"id_token":      stubIDToken(),
		"access_token":  stubJWT(map[string]any{"exp": time.Now().Add(a.lifetime).Unix(), "sub": fmt.Sprintf("at-%d", a.serial)}),
		"refresh_token": a.current,
	})
}

// writeLogin writes an auth.json whose access token expires at exp and
// was last refreshed at lastRefresh, with a key grain does not know.
func writeLogin(t *testing.T, path string, exp, lastRefresh time.Time) {
	t.Helper()
	data, _ := json.Marshal(map[string]any{
		"OPENAI_API_KEY": nil,
		"future_key":     map[string]any{"kept": true},
		"tokens": map[string]any{
			"id_token":      stubIDToken(),
			"access_token":  stubJWT(map[string]any{"exp": exp.Unix(), "sub": "at-0"}),
			"refresh_token": "rt-0",
			"account_id":    stubAccountID,
		},
		"last_refresh": lastRefresh.UTC().Format(time.RFC3339),
	})
	if err := WriteAuthFile(path, data); err != nil {
		t.Fatal(err)
	}
}

func readTokens(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatalf("login no longer parses: %v\n%s", err, data)
	}
	return object
}

func TestRefreshAuthFileWaitsUntilHalfTheLifetimeHasPassed(t *testing.T) {
	authority, url := newRotatingAuthority(t)
	path := filepath.Join(t.TempDir(), "codex-auth.json")
	now := time.Now()
	// Issued 4h ago, expiring in 6h: 60% of its life is left.
	writeLogin(t, path, now.Add(6*time.Hour), now.Add(-4*time.Hour))
	outcome, err := RefreshAuthFile(context.Background(), path, RefreshOptions{TokenURL: url})
	if err != nil || outcome != RefreshNotDue {
		t.Fatalf("got %v, %v; want not due", outcome, err)
	}
	// An hour later, under half is left.
	later := func() time.Time { return now.Add(time.Hour + time.Minute) }
	outcome, err = RefreshAuthFile(context.Background(), path, RefreshOptions{TokenURL: url, Now: later})
	if err != nil || outcome != RefreshDone {
		t.Fatalf("got %v, %v; want done", outcome, err)
	}
	if authority.requests != 1 {
		t.Fatalf("authority saw %d requests, want 1", authority.requests)
	}
	got := authority.body[0]
	if got["client_id"] != refreshClientID || got["grant_type"] != "refresh_token" || got["refresh_token"] != "rt-0" {
		t.Errorf("refresh request %v is not codex's grant", got)
	}
}

func TestRefreshDueAtNeverLeavesLessThanTheMinimumLead(t *testing.T) {
	now := time.Now()
	l := &login{accessToken: stubJWT(map[string]any{"exp": now.Add(5 * time.Minute).Unix(), "iat": now.Add(-5 * time.Minute).Unix()})}
	if due := l.refreshDueAt(); !now.After(due) {
		t.Errorf("a 10-minute token with 5 minutes left is due at %v, after now", due)
	}
	l = &login{accessToken: stubJWT(map[string]any{"exp": now.Add(50 * time.Minute).Unix()})}
	if due := l.refreshDueAt(); !now.After(due) {
		t.Errorf("a token of unknown lifetime with 50 minutes left is due at %v, after now", due)
	}
	l = &login{accessToken: "not-a-jwt", lastRefresh: now.Add(-time.Hour)}
	if due := l.refreshDueAt(); now.After(due) {
		t.Errorf("a token without exp refreshed an hour ago is already due (%v)", due)
	}
}

func TestRefreshAuthFileMergesIntoTheSymlinkTargetAtomically(t *testing.T) {
	authority, url := newRotatingAuthority(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "codex-auth.json")
	writeLogin(t, target, time.Now().Add(-time.Minute), time.Now().Add(-2*time.Hour))
	// The arrangement WithAuthFile makes: a run's auth.json is a link.
	link := filepath.Join(t.TempDir(), "auth.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	outcome, err := RefreshAuthFile(context.Background(), link, RefreshOptions{TokenURL: url})
	if err != nil || outcome != RefreshDone {
		t.Fatalf("got %v, %v; want done", outcome, err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the run's link was replaced: %v %v", info, err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("refreshed login has mode %v, want 0600", info.Mode().Perm())
	}
	object := readTokens(t, link)
	if _, ok := object["future_key"]; !ok {
		t.Errorf("an unknown key was dropped: %v", object)
	}
	tokens := object["tokens"].(map[string]any)
	if tokens["refresh_token"] != authority.current {
		t.Errorf("stored refresh token %v, authority will honour %v", tokens["refresh_token"], authority.current)
	}
	if tokens["account_id"] != stubAccountID {
		t.Errorf("tokens.account_id was dropped: %v", tokens)
	}
	if tokenSubject(tokens["access_token"].(string)) != "at-1" {
		t.Errorf("access token was not replaced: %v", tokens["access_token"])
	}
	if lastRefresh, _ := time.Parse(time.RFC3339Nano, object["last_refresh"].(string)); time.Since(lastRefresh) > time.Minute {
		t.Errorf("last_refresh %v was not updated", object["last_refresh"])
	}
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".codex-auth.json.") && !strings.HasSuffix(entry.Name(), ".refresh.lock") {
			t.Errorf("left %s behind", entry.Name())
		}
	}
}

// TestRefreshersOfOneLoginSpendItsRefreshTokenOnce is the property the
// whole refresher exists for: however many refreshers -- goroutines of one
// daemon, or separate daemon generations, which share nothing but the
// lock file -- find one login due at once, the authority sees one request.
func TestRefreshersOfOneLoginSpendItsRefreshTokenOnce(t *testing.T) {
	authority, url := newRotatingAuthority(t)
	authority.hold = 200 * time.Millisecond
	path := filepath.Join(t.TempDir(), "codex-auth.json")
	writeLogin(t, path, time.Now().Add(-time.Minute), time.Now().Add(-2*time.Hour))
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 1 {
				// A different generation: it shares only the lock file.
				refreshLocks.Delete(path)
			}
			_, errs[i] = RefreshAuthFile(context.Background(), path, RefreshOptions{TokenURL: url})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("refresher %d: %v", i, err)
		}
	}
	if authority.requests != 1 {
		t.Errorf("authority saw %d refresh requests, want 1", authority.requests)
	}
	if got := readTokens(t, path)["tokens"].(map[string]any)["refresh_token"]; got != authority.current {
		t.Errorf("stored %v, authority will honour %v", got, authority.current)
	}
}

func TestRefreshAuthFileRecordsAPermanentRefusalAndKeepsTheLogin(t *testing.T) {
	for _, code := range []string{"refresh_token_reused", "refresh_token_expired", "refresh_token_invalidated", "invalid_grant"} {
		t.Run(code, func(t *testing.T) {
			authority, url := newRotatingAuthority(t)
			authority.refuse = code
			path := filepath.Join(t.TempDir(), "codex-auth.json")
			writeLogin(t, path, time.Now().Add(-time.Minute), time.Now().Add(-2*time.Hour))
			before, _ := os.ReadFile(path)
			_, err := RefreshAuthFile(context.Background(), path, RefreshOptions{TokenURL: url})
			var permanent *PermanentRefreshError
			if !errors.As(err, &permanent) || permanent.Code != code {
				t.Fatalf("got %v, want a permanent %s refusal", err, code)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(before) {
				t.Fatalf("the login was changed or removed by a refused refresh: %v", err)
			}
			failure := ReadRefreshFailure(path)
			if failure == nil || failure.Code != code {
				t.Fatalf("recorded failure %+v, want %s", failure, code)
			}
			// Nothing more is sent for a refused token.
			outcome, err := RefreshAuthFile(context.Background(), path, RefreshOptions{TokenURL: url})
			if err != nil || outcome != RefreshBlocked || authority.requests != 1 {
				t.Fatalf("after a refusal: %v, %v, %d requests", outcome, err, authority.requests)
			}
			// A new upload clears it.
			writeLogin(t, path, time.Now().Add(-time.Minute), time.Now().Add(-2*time.Hour))
			if failure := ReadRefreshFailure(path); failure != nil {
				t.Errorf("a new login still reports %+v", failure)
			}
		})
	}
}

func TestRefreshAuthFileRetriesATransientFailure(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream down", http.StatusBadGateway)
	}))
	defer failing.Close()
	path := filepath.Join(t.TempDir(), "codex-auth.json")
	writeLogin(t, path, time.Now().Add(-time.Minute), time.Now().Add(-2*time.Hour))
	_, err := RefreshAuthFile(context.Background(), path, RefreshOptions{TokenURL: failing.URL})
	var permanent *PermanentRefreshError
	if err == nil || errors.As(err, &permanent) {
		t.Fatalf("got %v, want a transient error", err)
	}
	if failure := ReadRefreshFailure(path); failure != nil {
		t.Errorf("a transient failure was recorded as permanent: %+v", failure)
	}
	_, url := newRotatingAuthority(t)
	if outcome, err := RefreshAuthFile(context.Background(), path, RefreshOptions{TokenURL: url}); err != nil || outcome != RefreshDone {
		t.Errorf("retry: %v, %v", outcome, err)
	}
}

func TestRefreshAuthFileHonoursTheOverrideCodexReads(t *testing.T) {
	authority, url := newRotatingAuthority(t)
	t.Setenv(RefreshTokenURLOverrideEnv, url)
	path := filepath.Join(t.TempDir(), "codex-auth.json")
	writeLogin(t, path, time.Now().Add(-time.Minute), time.Now().Add(-2*time.Hour))
	if outcome, err := RefreshAuthFile(context.Background(), path, RefreshOptions{}); err != nil || outcome != RefreshDone {
		t.Fatalf("got %v, %v", outcome, err)
	}
	if authority.requests != 1 {
		t.Errorf("the override authority saw %d requests", authority.requests)
	}
}

func TestRefreshAuthFileLeavesAnAPIKeyLoginAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex-auth.json")
	if err := WriteAuthFile(path, []byte(`{"OPENAI_API_KEY":"sk-test"}`)); err != nil {
		t.Fatal(err)
	}
	if outcome, err := RefreshAuthFile(context.Background(), path, RefreshOptions{TokenURL: "http://127.0.0.1:1/unused", Force: true}); err != nil || outcome != RefreshNotApplicable {
		t.Errorf("got %v, %v", outcome, err)
	}
}

func TestRefresherSkipsMissingFilesAndRenewsTheRest(t *testing.T) {
	authority, url := newRotatingAuthority(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "codex-auth.json")
	writeLogin(t, path, time.Now().Add(-time.Minute), time.Now().Add(-2*time.Hour))
	var logged []string
	r := &Refresher{
		Files:   func() []string { return []string{"", filepath.Join(dir, "missing.json"), path, path} },
		Options: RefreshOptions{TokenURL: url},
		Logf:    func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) },
	}
	r.RefreshDue(context.Background())
	if authority.requests != 1 {
		t.Errorf("authority saw %d requests, want 1: %v", authority.requests, logged)
	}
}

// An upload through a symlinked login (an operator's codex-auth.json
// pointing at a login other processes share) replaces the login it
// points at, as a refresh does, and clears the refusal recorded there.
// It used to rename over the link: the shared file kept the refused
// tokens and the link became a private copy.
func TestWriteAuthFileReplacesTheSymlinkTarget(t *testing.T) {
	authority, url := newRotatingAuthority(t)
	authority.refuse = "refresh_token_reused"
	target := filepath.Join(t.TempDir(), "auth.json")
	writeLogin(t, target, time.Now().Add(-time.Minute), time.Now().Add(-2*time.Hour))
	link := filepath.Join(t.TempDir(), "codex-auth.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	RefreshAuthFile(context.Background(), link, RefreshOptions{TokenURL: url})
	if ReadRefreshFailure(link) == nil {
		t.Fatal("the refusal was not recorded")
	}
	if err := WriteAuthFile(link, []byte(`{"tokens":{"refresh_token":"rt-uploaded"}}`)); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the upload replaced the link: %v %v", info, err)
	}
	tokens := readTokens(t, target)["tokens"].(map[string]any)
	if tokens["refresh_token"] != "rt-uploaded" {
		t.Errorf("the shared login still holds %v", tokens["refresh_token"])
	}
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("uploaded login: %v, %v; want mode 0600", info, err)
	}
	if _, err := os.Stat(failurePath(target)); !os.IsNotExist(err) {
		t.Errorf("the refusal recorded for the replaced login survived: %v", err)
	}
}

func TestRemoveAuthFileClearsTheRecordedFailure(t *testing.T) {
	authority, url := newRotatingAuthority(t)
	authority.refuse = "refresh_token_expired"
	path := filepath.Join(t.TempDir(), "codex-auth.json")
	writeLogin(t, path, time.Now().Add(-time.Minute), time.Now().Add(-2*time.Hour))
	RefreshAuthFile(context.Background(), path, RefreshOptions{TokenURL: url})
	target, _ := filepath.EvalSymlinks(path)
	if err := RemoveAuthFile(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(failurePath(target)); !os.IsNotExist(err) {
		t.Errorf("failure record survived the login: %v", err)
	}
	if err := RemoveAuthFile(path); !os.IsNotExist(err) {
		t.Errorf("removing a missing login: %v, want not-exist", err)
	}
}
