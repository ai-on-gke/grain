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

package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/objectstore"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func fakeAmbientHelper(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	state := t.TempDir()

	sleepCmd := ""
	if _, err := os.Stat("/usr/bin/perl"); err == nil {
		sleepCmd = `/usr/bin/perl -e 'select(undef, undef, undef, 0.05)'`
	} else if _, err := os.Stat("/bin/sleep"); err == nil {
		sleepCmd = `/bin/sleep 0.05`
	}

	gh := "#!/bin/sh\n" +
		`if [ -f "` + state + `/sleep-50ms" ]; then ` + sleepCmd + `; fi` + "\n" +
		`echo "$*" >> "` + state + `/gh-calls"` + "\n" +
		`host="github.com"` + "\n" +
		`if [ "$1 $2 $3" = "auth token --hostname" ] && [ -n "$4" ]; then host="$4"; fi` + "\n" +
		`f="` + state + `/gh-token-$host"` + "\n" +
		`[ -f "$f" ] || f="` + state + `/gh-token"` + "\n" +
		`if [ ! -f "$f" ]; then echo "not logged in" >&2; exit 1; fi` + "\n" +
		`read -r tok < "$f"` + "\n" +
		`echo "$tok"` + "\n"

	git := "#!/bin/sh\n" +
		`[ "$1 $2" = "credential fill" ] || exit 2` + "\n" +
		`if [ -f "` + state + `/sleep-50ms" ]; then ` + sleepCmd + `; fi` + "\n" +
		`proto=""; host=""` + "\n" +
		`while read -r line; do` + "\n" +
		`  [ -z "$line" ] && break` + "\n" +
		`  case "$line" in` + "\n" +
		`    protocol=*) proto="${line#protocol=}" ;;` + "\n" +
		`    host=*) host="${line#host=}" ;;` + "\n" +
		`  esac` + "\n" +
		`done` + "\n" +
		`echo "$host" >> "` + state + `/git-calls"` + "\n" +
		`echo "protocol=$proto"` + "\n" +
		`echo "host=$host"` + "\n" +
		`f="` + state + `/git-password-$host"` + "\n" +
		`[ -f "$f" ] || f="` + state + `/git-password"` + "\n" +
		`if [ "$proto" = https ] && [ -f "$f" ]; then` + "\n" +
		`  read -r pw < "$f"` + "\n" +
		`  echo "username=x-access-token"` + "\n" +
		`  echo "password=$pw"` + "\n" +
		`fi` + "\n"

	for name, script := range map[string]string{"gh": gh, "git": git} {
		writeExecutableStub(t, filepath.Join(bin, name), script)
	}
	t.Setenv("PATH", bin)
	t.Setenv("GRAIN_IN_CONTAINER", "0")
	return state
}

func writeHelperState(t *testing.T, state, name, val string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(state, name), []byte(val+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func helperCallCount(t *testing.T, state, name string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(state, name))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(raw), "\n")
}

func startNamespaceProxyForRedactionTest(t *testing.T, dataDir string, secretStore *secrets.Store, sandboxCount int) (string, []string, *model.Store) {
	t.Helper()
	if sandboxCount < 1 {
		sandboxCount = 1
	}
	store, db, err := OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	secretsDir := filepath.Join(dataDir, "secrets")
	if err := os.MkdirAll(filepath.Join(secretsDir, "github"), 0o700); err != nil {
		t.Fatal(err)
	}
	tokenMap := make(map[string]string, sandboxCount)
	sbTokens := make([]string, sandboxCount)
	ctx := context.Background()
	ref := model.RepoRef{Owner: "acme", Name: "widgets"}
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	for i := 0; i < sandboxCount; i++ {
		sbName := "sb-" + string(rune('a'+(i/10))) + string(rune('0'+(i%10)))
		taskID := "t-" + sbName
		tok := "sandbox-token-" + sbName
		tokenMap[sbName] = tok
		sbTokens[i] = tok
		if err := store.PutTask(ctx, model.Task{ID: taskID, Title: "Test task " + taskID, Body: "body", Target: &ref}); err != nil {
			t.Fatal(err)
		}
		if err := store.StartRun(ctx, model.Run{
			ID:        "run-" + sbName,
			TaskID:    taskID,
			Sandbox:   sbName,
			Attempt:   1,
			StartedAt: now,
		}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
	}
	tokensJSON, _ := json.Marshal(tokenMap)
	if err := os.WriteFile(filepath.Join(secretsDir, "sandbox-tokens.json"), tokensJSON, 0o600); err != nil {
		t.Fatal(err)
	}

	uiSrv := ui.NewServer(ui.Config{
		DataDir:  dataDir,
		Actor:    ui.DefaultActor("deployment"),
		SourceDB: db,
		DBDriver: "sqlite",
	}, store)
	LiveAPI.Set(uiSrv)

	proxyURL, _, stop, err := startGitProxy(dataDir, store, secretStore, "", false, NewDirect(""), nil, nil, nil)
	if err != nil {
		t.Fatalf("startGitProxy: %v", err)
	}
	t.Cleanup(func() { _ = stop(context.Background()) })
	return proxyURL, sbTokens, store
}

func fetchProxyNamespace(t *testing.T, proxyURL, sbToken, path string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, proxyURL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth("x-access-token", sbToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

func TestStartGitProxyNamespaceAmbientRedactionSingleflightAndCache(t *testing.T) {
	state := fakeAmbientHelper(t)
	const (
		ghAmbientTok   = "deadbeef0123456789abcdef0123456789abcdef"
		corpAmbientTok = "feedface9876543210fedcba9876543210fedcba"
	)
	writeHelperState(t, state, "sleep-50ms", "1")
	writeHelperState(t, state, "gh-token", ghAmbientTok)
	writeHelperState(t, state, "git-password-depot.code.corp.goog", corpAmbientTok)

	dataDir := t.TempDir()
	githubDir := filepath.Join(dataDir, "secrets", "github")
	setupSet, err := gitproxy.LoadCredentialSet(githubDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := setupSet.SetAmbient("gh-amb", "github.com", "gh"); err != nil {
		t.Fatal(err)
	}
	if err := setupSet.SetAmbientOn("corp-amb", gitproxy.CredentialHost{
		Host: "depot.code.corp.goog",
		Kind: model.GitHostGitHub,
	}, gitproxy.AmbientSourceGit); err != nil {
		t.Fatal(err)
	}

	const concurrent = 20
	proxyURL, sbTokens, store := startNamespaceProxyForRedactionTest(t, dataDir, nil, concurrent)
	for i := 0; i < concurrent; i++ {
		sbName := "sb-" + string(rune('a'+(i/10))) + string(rune('0'+(i%10)))
		if _, err := store.AddComment(context.Background(), model.Comment{
			TaskID:    "t-" + sbName,
			Body:      "tokens in task comment: " + ghAmbientTok + " and " + corpAmbientTok,
			CreatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	statuses := make([]int, concurrent)
	bodies := make([]string, concurrent)
	for i := 0; i < concurrent; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sbName := "sb-" + string(rune('a'+(idx/10))) + string(rune('0'+(idx%10)))
			<-start
			statuses[idx], bodies[idx] = fetchProxyNamespace(t, proxyURL, sbTokens[idx], "/fs/task/t-"+sbName+"/record.md")
		}(i)
	}
	close(start)
	wg.Wait()

	// Fire additional sequential requests inside the 5-minute TTL window.
	for i := 0; i < 5; i++ {
		st, body := fetchProxyNamespace(t, proxyURL, sbTokens[0], "/fs/task/t-sb-a0/record.md")
		statuses = append(statuses, st)
		bodies = append(bodies, body)
	}

	for i, body := range bodies {
		if statuses[i] != http.StatusOK {
			t.Fatalf("request %d status = %d, want 200 (body=%q)", i, statuses[i], body)
		}
		if strings.Contains(body, ghAmbientTok) || strings.Contains(body, corpAmbientTok) {
			t.Fatalf("request %d leaked ambient token in body: %q", i, body)
		}
		if !strings.Contains(body, "[redacted: github credential gh-amb]") {
			t.Fatalf("request %d missing gh-amb redaction label in body: %q", i, body)
		}
		if !strings.Contains(body, "[redacted: github credential corp-amb]") {
			t.Fatalf("request %d missing corp-amb redaction label in body: %q", i, body)
		}
	}

	if got := helperCallCount(t, state, "gh-calls"); got != 1 {
		t.Fatalf("gh ambient helper ran %d times across %d requests, want 1", got, len(bodies))
	}
	if got := helperCallCount(t, state, "git-calls"); got != 1 {
		t.Fatalf("git ambient helper ran %d times across %d requests, want 1", got, len(bodies))
	}
}

func TestStartGitProxyNamespaceStaticTokenHotRedaction(t *testing.T) {
	state := fakeAmbientHelper(t)
	const (
		ambientTok = "aaaaaaaa11111111bbbbbbbb22222222cccccccc"
		staticTok1 = "cafebabe11223344556677889900aabbccddeeff"
		staticTok2 = "00112233445566778899aabbccddeeff00112233"
		storedTok  = "99887766554433221100ffeeddccbbaa99887766"
	)
	writeHelperState(t, state, "gh-token", ambientTok)

	dataDir := t.TempDir()
	secStore := secrets.New(SecretsDir(dataDir))
	githubDir := filepath.Join(dataDir, "secrets", "github")
	setupSet, err := gitproxy.LoadCredentialSet(githubDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := setupSet.SetAmbient("op-amb", "github.com", "gh"); err != nil {
		t.Fatal(err)
	}

	proxyURL, sbTokens, store := startNamespaceProxyForRedactionTest(t, dataDir, secStore, 1)
	sbToken := sbTokens[0]
	if _, err := store.AddComment(context.Background(), model.Comment{
		TaskID:    "t-sb-a0",
		Body:      strings.Join([]string{ambientTok, staticTok1, staticTok2, storedTok}, " "),
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	// Baseline: only ambientTok exists and is redacted.
	st, body := fetchProxyNamespace(t, proxyURL, sbToken, "/fs/task/t-sb-a0/record.md")
	if st != http.StatusOK || strings.Contains(body, ambientTok) || !strings.Contains(body, staticTok1) {
		t.Fatalf("initial read status=%d body=%q", st, body)
	}

	// Write a plaintext .token file AFTER startGitProxy is already serving.
	tokenPath := filepath.Join(githubDir, "hot-bot.token")
	if err := os.WriteFile(tokenPath, []byte(staticTok1+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, body = fetchProxyNamespace(t, proxyURL, sbToken, "/fs/task/t-sb-a0/record.md")
	if st != http.StatusOK || strings.Contains(body, staticTok1) || !strings.Contains(body, "[redacted: github credential hot-bot]") {
		t.Fatalf("after adding hot-bot.token: status=%d body=%q", st, body)
	}

	// Rotate that plaintext .token file in place without restarting startGitProxy.
	if err := os.WriteFile(tokenPath, []byte(staticTok2+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, body = fetchProxyNamespace(t, proxyURL, sbToken, "/fs/task/t-sb-a0/record.md")
	if st != http.StatusOK || strings.Contains(body, staticTok2) || !strings.Contains(body, staticTok1) {
		t.Fatalf("after rotating hot-bot.token: status=%d body=%q", st, body)
	}

	// Write a secret-store-backed credential via a separate CredentialSet instance
	// (testing both secretStore-backed and secretsDir-only redaction paths).
	writerSet, err := gitproxy.LoadCredentialSet(githubDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := writerSet.SetToken("store-bot", storedTok); err != nil {
		t.Fatal(err)
	}
	st, body = fetchProxyNamespace(t, proxyURL, sbToken, "/fs/task/t-sb-a0/record.md")
	if st != http.StatusOK || strings.Contains(body, storedTok) {
		t.Fatalf("after SetToken(store-bot): status=%d body=%q", st, body)
	}

	if got := helperCallCount(t, state, "gh-calls"); got != 1 {
		t.Fatalf("gh ambient helper ran %d times across static token hot-reloads, want 1", got)
	}
	if out, _ := BuildNamespaceRedactor(dataDir).Text(storedTok); strings.Contains(out, storedTok) {
		t.Fatalf("BuildNamespaceRedactor(dataDir) leaked store-bot: %q", out)
	}
}

func TestStartGitProxyNamespaceAmbientConfigRotationAndRemoval(t *testing.T) {
	state := fakeAmbientHelper(t)
	const (
		ghTok1 = "1111111122222222333333334444444455555555"
		gitTok = "aaaaaaaabbbbbbbbccccccccddddddddeeeeeeee"
		gheTok = "9999999988888888777777776666666655555555"
		ghTok2 = "7777777766666666555555554444444433333333"
	)
	writeHelperState(t, state, "gh-token-github.com", ghTok1)
	writeHelperState(t, state, "git-password-github.com", gitTok)
	writeHelperState(t, state, "git-password-ghe.example.com", gheTok)

	dataDir := t.TempDir()
	githubDir := filepath.Join(dataDir, "secrets", "github")
	setupSet, err := gitproxy.LoadCredentialSet(githubDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := setupSet.SetAmbient("rot", "github.com", "gh"); err != nil {
		t.Fatal(err)
	}

	proxyURL, sbTokens, store := startNamespaceProxyForRedactionTest(t, dataDir, nil, 1)
	sbToken := sbTokens[0]
	if _, err := store.AddComment(context.Background(), model.Comment{
		TaskID:    "t-sb-a0",
		Body:      strings.Join([]string{ghTok1, gitTok, gheTok, ghTok2}, " "),
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	// 1. Initial source=gh redacts ghTok1.
	_, body := fetchProxyNamespace(t, proxyURL, sbToken, "/fs/task/t-sb-a0/record.md")
	if strings.Contains(body, ghTok1) || !strings.Contains(body, "[redacted: github credential rot]") {
		t.Fatalf("step 1 (initial gh): body=%q", body)
	}

	// 2. Rotate Source in rot.ambient.json from gh -> git via an external CredentialSet.
	extSet, err := gitproxy.LoadCredentialSet(githubDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := extSet.SetAmbient("rot", "github.com", "git"); err != nil {
		t.Fatal(err)
	}
	_, body = fetchProxyNamespace(t, proxyURL, sbToken, "/fs/task/t-sb-a0/record.md")
	if strings.Contains(body, gitTok) || !strings.Contains(body, ghTok1) {
		t.Fatalf("step 2 (Source rotated gh->git): body=%q", body)
	}

	// 3. Rotate Host in rot.ambient.json + rot.host.json to ghe.example.com.
	if err := extSet.SetAmbientOn("rot", gitproxy.CredentialHost{
		Host: "ghe.example.com",
		Kind: model.GitHostGitHub,
	}, gitproxy.AmbientSourceGit); err != nil {
		t.Fatal(err)
	}
	_, body = fetchProxyNamespace(t, proxyURL, sbToken, "/fs/task/t-sb-a0/record.md")
	if strings.Contains(body, gheTok) || !strings.Contains(body, gitTok) {
		t.Fatalf("step 3 (Host rotated to ghe.example.com): body=%q", body)
	}

	// 4. Remove rot.ambient.json completely: stale gheTok must no longer be redacted.
	if err := extSet.Remove("rot"); err != nil {
		t.Fatal(err)
	}
	_, body = fetchProxyNamespace(t, proxyURL, sbToken, "/fs/task/t-sb-a0/record.md")
	if !strings.Contains(body, gheTok) || strings.Contains(body, "[redacted: github credential rot]") {
		t.Fatalf("step 4 (after Remove): stale ambient entry survived removal: body=%q", body)
	}

	// 5. Re-create rot.ambient.json with updated gh output: must fetch fresh ghTok2 rather than serving pre-removal ghTok1.
	writeHelperState(t, state, "gh-token-github.com", ghTok2)
	if err := extSet.SetAmbient("rot", "github.com", "gh"); err != nil {
		t.Fatal(err)
	}
	_, body = fetchProxyNamespace(t, proxyURL, sbToken, "/fs/task/t-sb-a0/record.md")
	if strings.Contains(body, ghTok2) || !strings.Contains(body, ghTok1) {
		t.Fatalf("step 5 (re-created rot.ambient.json): body=%q", body)
	}
}

func TestTranscriptObjectRedactorSharesAmbientCacheAndHotReloadsTokens(t *testing.T) {
	state := fakeAmbientHelper(t)
	const (
		ambTok    = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
		staticTok = "1234567890abcdef1234567890abcdef12345678"
	)
	writeHelperState(t, state, "sleep-50ms", "1")
	writeHelperState(t, state, "gh-token", ambTok)

	dataDir := t.TempDir()
	secStore := secrets.New(SecretsDir(dataDir))
	githubDir := filepath.Join(dataDir, "secrets", "github")
	setupSet, err := gitproxy.LoadCredentialSet(githubDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := setupSet.SetAmbient("op", "github.com", "gh"); err != nil {
		t.Fatal(err)
	}

	redactorFn := transcriptObjectRedactor(Config{DataDir: dataDir}, secStore)

	// Concurrent redactor builds share the lazy CredentialSet and its singleflight fetch.
	const concurrent = 10
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, concurrent)
	outputs := make([]string, concurrent)
	for i := 0; i < concurrent; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			r, rerr := redactorFn()
			if rerr != nil {
				errs[idx] = rerr
				return
			}
			outputs[idx], _ = r.Text("saw " + ambTok)
		}(i)
	}
	close(start)
	wg.Wait()
	for i := 0; i < concurrent; i++ {
		if errs[i] != nil {
			t.Fatalf("redactorFn[%d]: %v", i, errs[i])
		}
		if strings.Contains(outputs[i], ambTok) || !strings.Contains(outputs[i], "[redacted: github credential op]") {
			t.Fatalf("redactorFn[%d] output = %q", i, outputs[i])
		}
	}

	// Wire into a real TranscriptObjectStore and flush multiple runs.
	mem := objectstore.NewMemory()
	liveDir := filepath.Join(dataDir, "state", "transcripts")
	if err := os.MkdirAll(liveDir, 0o700); err != nil {
		t.Fatal(err)
	}
	tStore, err := NewTranscriptObjectStore(mem, filepath.Join(dataDir, "cache"), liveDir, redactorFn)
	if err != nil {
		t.Fatal(err)
	}

	// Add a static .token after redactorFn was already initialized and used.
	if err := os.WriteFile(filepath.Join(githubDir, "pat.token"), []byte(staticTok+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rawPath := filepath.Join(liveDir, "run-a")
	line := `{"type":"assistant","text":"` + ambTok + ` and ` + staticTok + `"}` + "\n"
	if err := os.WriteFile(rawPath, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	if saved, err := tStore.FlushRunEnd(context.Background(), "run-a", rawPath, "summary "+ambTok+" "+staticTok); err != nil || !saved {
		t.Fatalf("FlushRunEnd(run-a) = (%v, %v), want (true, nil)", saved, err)
	}
	for _, key := range []string{"transcripts/run-a.jsonl", "transcripts/run-a.md"} {
		data, ok := mem.Bytes(key)
		if !ok {
			t.Fatalf("missing %s in object store", key)
		}
		if strings.Contains(string(data), ambTok) || strings.Contains(string(data), staticTok) {
			t.Fatalf("%s leaked token: %s", key, data)
		}
	}

	if got := helperCallCount(t, state, "gh-calls"); got != 1 {
		t.Fatalf("gh ambient helper ran %d times across concurrent and sequential transcript flushes, want 1", got)
	}

	// Removing op.ambient.json stops redacting ambTok on the next transcript flush.
	if err := setupSet.Remove("op"); err != nil {
		t.Fatal(err)
	}
	rawPathB := filepath.Join(liveDir, "run-b")
	if err := os.WriteFile(rawPathB, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	if saved, err := tStore.FlushRunEnd(context.Background(), "run-b", rawPathB, "summary "+ambTok+" "+staticTok); err != nil || !saved {
		t.Fatalf("FlushRunEnd(run-b) = (%v, %v), want (true, nil)", saved, err)
	}
	for _, key := range []string{"transcripts/run-b.jsonl", "transcripts/run-b.md"} {
		dataB, ok := mem.Bytes(key)
		if !ok {
			t.Fatalf("missing %s in object store", key)
		}
		if !strings.Contains(string(dataB), ambTok) || strings.Contains(string(dataB), staticTok) {
			t.Fatalf("after removing op.ambient.json, %s = %s", key, dataB)
		}
	}
}
