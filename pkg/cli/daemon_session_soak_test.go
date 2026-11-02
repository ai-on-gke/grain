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

package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/runhost"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// soakStubClaude writes the stub CLI the session soak runs. Every exec
// appends its argv to dir/invocations, records what its sandbox holds
// under attachments/ (dir/attachments-N), sleeps dir/sleep seconds
// (default 5) -- long enough for a message to arrive mid-turn -- and
// ends the turn with a resumable session.
func soakStubClaude(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "claude")
	script := `#!/bin/sh
d=` + dir + `
echo "$(date -u +%H:%M:%S) $*" >> $d/invocations
n=$(wc -l < $d/invocations | tr -d ' ')
prev=
sid=soak-$n
for a in "$@"; do
	if [ "$prev" = --mcp-config ]; then cp "$a" $d/mcp-$n.json 2>/dev/null; fi
	if [ "$prev" = --resume ]; then sid=$a; fi
	prev=$a
done
cat > $d/stdin-$n 2>/dev/null
{ pwd; ls -la attachments 2>&1; } > $d/attachments-$n
echo '{"type":"system","subtype":"init","session_id":"'"$sid"'"}'
echo '{"type":"assistant","session_id":"'"$sid"'","message":{"content":[{"type":"text","text":"turn '"$n"' started"}]}}'
task=$(sed -n 's/.*"-task", "\([^"]*\)".*/\1/p' $d/mcp-$n.json)
s=$(cat $d/sleep 2>/dev/null || echo 5)
mcp() {
	args=$(sed -n 's/.*"args": \[\([^]]*\)\].*/\1/p' $d/mcp-$n.json | tr -d '",')
	{ echo '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"stub","version":"0"}}}'
	  echo '{"jsonrpc":"2.0","method":"notifications/initialized"}'
	  echo '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"'"$1"'","arguments":'"$2"'}}'
	  sleep "$3"; } | /usr/local/bin/grain $args > $d/mcp-out-$n 2>&1
}
if [ -e $d/ask-$task ]; then
	rm -f $d/ask-$task
	sleep 8
	echo "$(date -u +%H:%M:%S) ask_question task=$task n=$n" >> $d/events
	mcp ask_question '{"question":"Which file should I edit?","summary":"Pick the file to edit"}' 5
	echo '{"type":"result","subtype":"success","is_error":false,"result":"asked","session_id":"'"$sid"'"}'
	exit 0
fi
if [ -e $d/wait-$task ]; then
	rm -f $d/wait-$task
	echo "$(date -u +%H:%M:%S) wait_for_checks task=$task n=$n" >> $d/events
	mcp wait_for_checks '{"timeout_seconds":120}' 130
fi
sleep "$s"
{ pwd; ls -la attachments 2>&1; } > $d/attachments-$n-end
echo '{"type":"result","subtype":"success","is_error":false,"result":"turn '"$n"' answered","session_id":"'"$sid"'"}'
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "token"), []byte("sk-ant-oat01-stub"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestSessionLifecycleSoak is the live soak of docs/test-plans/2026-11-02.md
// §8 (docs/test-runs/2026-11-02-session-lifecycle-soak.md): the real
// daemon on the Docker run host, a director and a keep_running chat
// session, for GRAIN_SOAK_DURATION. It logs a timeline to
// GRAIN_SOAK_LOG (default: the test log) and is not part of CI.
func TestSessionLifecycleSoak(t *testing.T) {
	if os.Getenv("GRAIN_RUNHOST_DOCKER") != "1" || os.Getenv("GRAIN_SOAK_DURATION") == "" {
		t.Skip("set GRAIN_RUNHOST_DOCKER=1 and GRAIN_SOAK_DURATION to run the session soak")
	}
	soak, err := time.ParseDuration(os.Getenv("GRAIN_SOAK_DURATION"))
	if err != nil {
		t.Fatal(err)
	}
	image := runHostTestImage(t)

	const owner, repoName = "acme", "widgets"
	const chatID = "soak-chat"
	upstream := t.TempDir()
	bare := initBareRepo(t, filepath.Join(upstream, owner, repoName+".git"))
	seedDir := filepath.Join(t.TempDir(), "seed")
	runLive(t, t.TempDir(), "git", "clone", bare, seedDir)
	runLive(t, seedDir, "git", "config", "user.email", "seed@example.com")
	runLive(t, seedDir, "git", "config", "user.name", "seed")
	if err := os.WriteFile(filepath.Join(seedDir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runLive(t, seedDir, "git", "add", "README.md")
	runLive(t, seedDir, "git", "commit", "-q", "-m", "initial commit")
	runLive(t, seedDir, "git", "push", "-q", "origin", "main")
	sim := &syncedSim{sim: githubsim.New(owner, repoName, bare, "main")}
	githubHost := githubHostServer(t, sim, upstream)

	dataDir := os.Getenv("GRAIN_SOAK_DATA_DIR")
	if dataDir == "" {
		dataDir = t.TempDir()
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "secrets", "github"), 0o755); err != nil {
		t.Fatal(err)
	}
	credDir := filepath.Join(dataDir, "stub")
	if err := os.MkdirAll(credDir, 0o755); err != nil {
		t.Fatal(err)
	}
	claudePath := soakStubClaude(t, credDir)
	sandboxDir := filepath.Join(dataDir, "sandboxes")
	if err := os.MkdirAll(sandboxDir, 0o755); err != nil {
		t.Fatal(err)
	}

	logf := t.Logf
	if p := os.Getenv("GRAIN_SOAK_LOG"); p != "" {
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		var mu sync.Mutex
		logf = func(format string, args ...any) {
			mu.Lock()
			defer mu.Unlock()
			fmt.Fprintf(f, time.Now().UTC().Format("15:04:05.000")+" "+format+"\n", args...)
		}
	}

	store, db, err := daemon.OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), soak+20*time.Minute)
	defer cancel()

	deployment, err := store.InstanceID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	driver := runhost.Docker{}
	t.Cleanup(func() {
		infos, _ := driver.List(context.Background(), deployment)
		for _, i := range infos {
			_ = driver.Destroy(context.Background(), i.Name)
		}
	})

	uiAddr := freeLocalAddr(t)
	cfg := daemon.Config{
		DataDir: dataDir, SandboxDir: sandboxDir, MaxWorkers: 2, PollInterval: time.Second,
		ClaudePath:   claudePath,
		RunHostImage: image, RunHostCredentialDir: credDir,
		GithubHost: githubHost, GithubInsecureHTTP: true,
		UiAddr: uiAddr,
	}
	seedTestDaemonConnections(t, cfg, `git:
  - credential: anonymous
models:
  - id: claude
    framework: claude
    models: [claude-sonnet-5]
    credential_file: `+filepath.Join(credDir, "token")+`
`)
	setTTL := func(ttl time.Duration) {
		stored, err := store.GetConfig(ctx)
		if err != nil || stored == nil {
			t.Fatalf("reading the config: %v", err)
		}
		stored.SessionSandboxIdleTTL = ttl
		if err := store.PutConfig(ctx, *stored); err != nil {
			t.Fatal(err)
		}
		logf("SETTING sessionSandboxIdleTtl=%s", ttl)
	}
	setTTL(30 * time.Minute)
	initBareRepo(t, filepath.Join(upstream, owner, "docs.git"))
	if stored, err := store.GetConfig(ctx); err == nil && stored != nil {
		sessions := model.DefaultConsoleSessions()
		for i := range sessions {
			if sessions[i].ID == model.DirectorSessionID {
				sessions[i].Writes = []model.WriteTarget{
					{Repo: model.RepoRef{Owner: owner, Name: repoName}},
					{Repo: model.RepoRef{Owner: owner, Name: "docs"}},
				}
			}
		}
		stored.ConsoleSessions = sessions
		if err := store.PutConfig(ctx, *stored); err != nil {
			t.Fatal(err)
		}
	}

	human := model.Principal{Kind: model.PrincipalHuman, ID: "tester"}
	chat := model.Task{
		ID: chatID, Intent: model.IntentImplement, Interactive: true, KeepRunning: true,
		Title: "keep-running chat soak", Body: "hello chat",
		Origin:  model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Binding: model.BindingDirective,
		Target:  &model.RepoRef{Owner: owner, Name: repoName},
	}
	chat.Approval = &model.Attribution{Actor: human}
	if err := store.PutTask(ctx, chat); err != nil {
		t.Fatal(err)
	}

	client := ui.NewClient(ui.Config{Actor: ui.DefaultActor("tester"), Capabilities: ui.OfferedCapabilities()}, store)
	comment := func(taskID, text string, attach string) {
		var ups []ui.AttachmentUpload
		if attach != "" {
			ups = []ui.AttachmentUpload{{Filename: attach, ContentType: "text/plain",
				Content: base64.StdEncoding.EncodeToString([]byte("attachment " + attach + "\n"))}}
		}
		if err := client.AddComment(ctx, taskID, text, ups); err != nil {
			logf("COMMENT %s %q FAILED: %v", taskID, text, err)
			return
		}
		logf("COMMENT %s %q attach=%q", taskID, text, attach)
	}

	directorID := func() string {
		tasks, _ := store.ListTasks(ctx)
		for _, tk := range tasks {
			if id, _ := model.ConsoleSessionIDOf(tk); id == model.DirectorSessionID {
				return tk.ID
			}
		}
		return ""
	}

	containers := func() []string {
		out, _ := exec.Command("docker", "ps", "-a", "--format", "{{.Names}}:{{.Status}}").Output()
		var names []string
		for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if l != "" {
				names = append(names, l)
			}
		}
		sort.Strings(names)
		return names
	}
	sandboxesAPI := func() string {
		code, err := os.ReadFile(filepath.Join(dataDir, "local-api-token"))
		if err != nil {
			return "no token"
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+uiAddr+"/api/sandboxes", nil)
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(code)))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "error: " + err.Error()
		}
		defer resp.Body.Close()
		var rep struct {
			Sandboxes []struct {
				Sandbox string `json:"sandbox"`
				Kind    string `json:"kind"`
			} `json:"sandboxes"`
			Budget struct {
				Known bool `json:"known"`
				Pools []struct {
					Pool  string `json:"pool"`
					Count int    `json:"count"`
				} `json:"pools"`
			} `json:"budget"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&rep)
		b, _ := json.Marshal(rep)
		return string(b)
	}
	snapshot := func(label string) {
		var parts []string
		for _, id := range []string{directorID(), chatID} {
			if id == "" {
				continue
			}
			st, _ := store.State(ctx, id)
			runs, _ := store.Runs(ctx, id)
			last := ""
			if len(runs) > 0 {
				r := runs[len(runs)-1]
				last = fmt.Sprintf("%s sb=%s outcome=%q", r.ID, r.Sandbox, r.Outcome)
			}
			parts = append(parts, fmt.Sprintf("%s state=%s runs=%d last=[%s]", id, st, len(runs), last))
		}
		logf("SNAP %s | %s | docker=%v | sandboxes=%s", label, strings.Join(parts, " ; "), containers(), sandboxesAPI())
	}

	start := func() (context.CancelFunc, <-chan error) {
		dctx, stop := context.WithCancel(ctx)
		done, _ := startLiveDaemon(dctx, cfg)
		logf("DAEMON started ui=%s data=%s", uiAddr, dataDir)
		return stop, done
	}
	stopDaemon := func(stop context.CancelFunc, done <-chan error) {
		stop()
		select {
		case err := <-done:
			logf("DAEMON stopped: %v", err)
		case <-time.After(2 * time.Minute):
			t.Fatalf("daemon did not stop")
		}
	}

	stop, done := start()
	defer func() { stopDaemon(stop, done) }()
	if !pollUntil(ctx, 2*time.Minute, func() bool { return directorID() != "" }) {
		t.Fatalf("no director task appeared")
	}
	started := time.Now()
	dir := directorID()
	logf("DIRECTOR task %s", dir)
	lastSandbox := func(id string) string {
		runs, _ := store.Runs(ctx, id)
		if len(runs) == 0 {
			return ""
		}
		return runs[len(runs)-1].Sandbox
	}
	dumpWaitLedger := func(label string) {
		rows, err := db.QueryContext(ctx, `SELECT run_id, source, blocked_ms FROM task_run_wait_ledger`)
		if err != nil {
			logf("WAITLEDGER %s: %v", label, err)
			return
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var run, src string
			var ms int64
			_ = rows.Scan(&run, &src, &ms)
			out = append(out, fmt.Sprintf("%s/%s=%dms", run, src, ms))
		}
		logf("WAITLEDGER %s %v", label, out)
	}
	dumpTask := func(label, id string) {
		tk, err := store.GetTask(ctx, id)
		if err != nil {
			logf("TASK %s %s: %v", label, id, err)
			return
		}
		var grants []string
		for _, g := range tk.Grants {
			grants = append(grants, fmt.Sprintf("%s/%s", g.Capability, g.Via))
		}
		logf("TASK %s %s writes=%+v keepRunning=%v grants=%v", label, id, tk.Writes, tk.KeepRunning, grants)
	}
	dumpTask("start", dir)
	stub := func(name string) { _ = os.WriteFile(filepath.Join(credDir, name), nil, 0o644) }
	editedCapability := ""
	at := func(m float64) time.Time { return started.Add(time.Duration(m * float64(soak) / 60)) }
	phase := map[string]bool{}
	once := func(name string, when float64) bool {
		if phase[name] || time.Now().Before(at(when)) {
			return false
		}
		phase[name] = true
		logf("PHASE %s", name)
		return true
	}
	turn := 0
	for time.Now().Before(started.Add(soak)) {
		idle := time.Now().After(at(8)) && time.Now().Before(at(12))
		if !idle {
			turn++
			if turn%3 == 0 {
				comment(dir, fmt.Sprintf("director message %d with a file", turn), fmt.Sprintf("between-%d.txt", turn))
			} else {
				comment(dir, fmt.Sprintf("director message %d", turn), "")
			}
			time.Sleep(2 * time.Second)
			if turn%4 == 0 {
				comment(dir, fmt.Sprintf("director mid-turn message %d", turn), fmt.Sprintf("midturn-%d.txt", turn))
			}
		}
		snapshot(fmt.Sprintf("turn-%d", turn))
		if once("person-edit", 2) {
			tk, _ := store.GetTask(ctx, dir)
			for _, g := range tk.Grants {
				if g.Via == model.GrantByGrain && g.Capability != "durable" {
					editedCapability = g.Capability
					break
				}
			}
			if err := client.SetCapability(ctx, dir, editedCapability, false); err != nil {
				logf("person-edit %s: %v", editedCapability, err)
			}
			logf("PERSON removed capability %q from the director task", editedCapability)
			dumpTask("after-person-edit", dir)
		}
		if once("ttl-shorten", 8.5) {
			setTTL(time.Minute)
			released := pollUntil(ctx, 3*time.Minute, func() bool {
				_, err := os.Stat(filepath.Join(sandboxDir, lastSandbox(dir)))
				return os.IsNotExist(err)
			})
			logf("TTL director sandbox %s released=%v", lastSandbox(dir), released)
			snapshot("after-ttl")
		}
		if once("ttl-restore", 12) {
			setTTL(30 * time.Minute)
		}
		if once("restart-mid-turn", 18) {
			stopDaemon(stop, done)
			stop, done = start()
		}
		if once("ask-over-comment", 25) {
			stub("ask-" + dir)
			comment(dir, "please pick a file", "")
			time.Sleep(4 * time.Second)
			comment(dir, "typed while the agent asks: use README.md", "")
			for i := 0; i < 8; i++ {
				time.Sleep(5 * time.Second)
				snapshot("ask")
			}
		}
		if once("wait-restart", 32) {
			stub("wait-" + chatID)
			if pollUntil(ctx, 3*time.Minute, func() bool {
				raw, _ := os.ReadFile(filepath.Join(credDir, "events"))
				return strings.Contains(string(raw), "wait_for_checks task="+chatID)
			}) {
				time.Sleep(10 * time.Second)
				dumpWaitLedger("before-restart")
				stopDaemon(stop, done)
				stop, done = start()
				time.Sleep(20 * time.Second)
				dumpWaitLedger("after-restart")
			}
		}
		if once("offline-writes-edit", 40) {
			dumpTask("before-offline-edit", dir)
			stopDaemon(stop, done)
			dropConsoleSessionWrite(t, filepath.Join(daemon.StateRepoDir(dataDir)), "docs", logf)
			stop, done = start()
			time.Sleep(20 * time.Second)
			dumpTask("after-offline-edit", dir)
		}
		time.Sleep(30 * time.Second)
	}
	dumpWaitLedger("end")
	dumpTask("end", dir)
	snapshot("end")
}

// dropConsoleSessionWrite removes the director's writes entry naming repo
// from tables/grain_config.json's console_sessions column in the state
// repository at dir, and commits it, as an operator editing grain's
// configuration while the daemon is down would.
func dropConsoleSessionWrite(t *testing.T, dir, repo string, logf func(string, ...any)) {
	t.Helper()
	path := filepath.Join(dir, "tables", "grain_config.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		col, _ := row["console_sessions"].(string)
		var sessions []map[string]any
		if err := json.Unmarshal([]byte(col), &sessions); err != nil {
			logf("OFFLINE-EDIT console_sessions is not JSON (%q): %v", col, err)
			return
		}
		for _, sess := range sessions {
			writes, _ := sess["writes"].([]any)
			var kept []any
			for _, w := range writes {
				if b, _ := json.Marshal(w); strings.Contains(string(b), `"`+repo+`"`) {
					continue
				}
				kept = append(kept, w)
			}
			sess["writes"] = kept
		}
		b, _ := json.Marshal(sessions)
		row["console_sessions"] = string(b)
		logf("OFFLINE-EDIT console_sessions before=%s after=%s", col, b)
	}
	out, _ := json.MarshalIndent(rows, "", "  ")
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", dir, "-c", "user.email=op@example.com", "-c", "user.name=op", "commit", "-qam", "drop a director write")
	if b, err := cmd.CombinedOutput(); err != nil {
		logf("OFFLINE-EDIT commit: %v %s", err, b)
	}
}
