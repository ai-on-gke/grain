package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/capability/dbaccess"
	"github.com/bwsalmon/grain/pkg/ci"
	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/gitproxy"
	"github.com/bwsalmon/grain/pkg/mcp"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/namespace"
	"github.com/bwsalmon/grain/pkg/redact"
)

func fakeSandboxEnv(vars map[string]string, creds string) sandboxEnv {
	return sandboxEnv{
		Getenv: func(k string) string { return vars[k] },
		ReadFile: func(string) ([]byte, error) {
			if creds == "" {
				return nil, os.ErrNotExist
			}
			return []byte(creds), nil
		},
		CredsPath: "/home/debian/.git-credentials",
	}
}

func TestDetectSandboxProxy(t *testing.T) {
	for _, tc := range []struct {
		name        string
		vars        map[string]string
		creds       string
		base, token string
		ok          bool
	}{
		{name: "git credentials", creds: "https://x-access-token:ghs_x@github.com\nhttp://x-access-token:tok-1@127.0.0.1:9728\n",
			base: "http://127.0.0.1:9728", token: "tok-1", ok: true},
		{name: "env names the proxy", vars: map[string]string{"GRAIN_SANDBOX_PROXY": "http://127.0.0.1:9728/", "GRAIN_SANDBOX_TOKEN": "tok-2"},
			base: "http://127.0.0.1:9728", token: "tok-2", ok: true},
		{name: "env proxy, token from credentials", vars: map[string]string{"GRAIN_SANDBOX_PROXY": "http://127.0.0.1:9728"},
			creds: "http://x-access-token:tok-3@127.0.0.1:9728\n", base: "http://127.0.0.1:9728", token: "tok-3", ok: true},
		{name: "not a sandbox", creds: "https://x-access-token:ghs_x@github.com\n"},
		{name: "turned off", vars: map[string]string{"GRAIN_SANDBOX_CLI": "0"}, creds: "http://x:t@127.0.0.1:9728\n"},
		{name: "pointed at a daemon", vars: map[string]string{"GRAIN_SERVER": "http://localhost:8080"}, creds: "http://x:t@127.0.0.1:9728\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, token, ok := detectSandboxProxy(fakeSandboxEnv(tc.vars, tc.creds))
			if base != tc.base || token != tc.token || ok != tc.ok {
				t.Errorf("got (%q, %q, %v), want (%q, %q, %v)", base, token, ok, tc.base, tc.token, tc.ok)
			}
		})
	}
}

func TestIsSandboxCommandLeavesOtherModesAlone(t *testing.T) {
	for _, args := range [][]string{{"ls", "ci://a/b"}, {"ci", "log"}, {"task", "read", "1"}, {"db", "query", "SELECT 1"}, {"help"}, {"pr", "--help"}} {
		if !isSandboxCommand(args) {
			t.Errorf("%q should be a sandbox command", args)
		}
	}
	for _, args := range [][]string{{}, {"mcpserver"}, {"capability", "serve"}, {"db", "upgrade-postgres"}, {"ci", "run"}, {"task", "create"}, {"daemon"}} {
		if isSandboxCommand(args) {
			t.Errorf("%q should not be a sandbox command", args)
		}
	}
}

type cliTestScopes map[string]model.NamespaceScope

func (s cliTestScopes) SandboxNamespaceScope(_ context.Context, sb string) (model.NamespaceScope, bool, error) {
	sc, ok := s[sb]
	return sc, ok, nil
}

// TestSandboxCLIAgainstTheProxy runs the commands end to end against the
// real proxy handler.
func TestSandboxCLIAgainstTheProxy(t *testing.T) {
	store := ci.NewRunStore(10)
	at := time.Now()
	store.Put(ci.Run{ID: "run-1", Repo: "bwsalmon/grain", Ref: "grain/task-7", Conclusion: ci.ConclusionFailure, StartedAt: &at,
		Jobs: []ci.JobRun{{Name: "go", Conclusion: ci.ConclusionFailure, Steps: []ci.StepRun{
			{Name: "Test", Conclusion: ci.ConclusionFailure, Output: "--- FAIL: TestWidget (0.01s)\nFAIL\n"}}}}})
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "playbooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "playbooks", "gke.md"), []byte("# GKE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(t.TempDir(), "tokens.json")
	raw, _ := json.Marshal(map[string]string{"sb": "tok"})
	if err := os.WriteFile(tokenFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	tokens, err := gitproxy.LoadSandboxTokens(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	proxy := &gitproxy.GitProxy{
		Tokens:    tokens,
		Namespace: &namespace.Server{CIStore: store, SrcDir: src, Redactor: redact.New(nil)},
		NamespaceScopes: cliTestScopes{"sb": {TaskID: "7", RunID: "r", TaskRepo: "bwsalmon/grain", TaskBranch: "grain/task-7",
			MayReadCI: true, AllowedRepos: map[string]bool{"bwsalmon/grain": true}, MayReadPlaybooks: true}},
	}
	srv := httptest.NewServer(gitproxy.NewHandler(proxy))
	defer srv.Close()
	env := fakeSandboxEnv(map[string]string{"GRAIN_SANDBOX_PROXY": srv.URL, "GRAIN_SANDBOX_TOKEN": "tok"}, "")

	run := func(args ...string) (string, string, int) {
		var out, errOut bytes.Buffer
		code, handled := maybeRunSandboxCLI(args, env, &out, &errOut)
		if !handled {
			t.Fatalf("%q not handled in sandbox mode", args)
		}
		return out.String(), errOut.String(), code
	}

	if out, _, code := run("help"); code != 0 || !strings.Contains(out, "grain ci log") || !strings.Contains(out, srv.URL) {
		t.Errorf("help: %d %s", code, out)
	}
	if out, _, code := run("ci", "log", "--help"); code != 0 || !strings.HasPrefix(out, "grain ci log") {
		t.Errorf("ci log --help: %d %s", code, out)
	}
	if out, errOut, code := run("ls", "ci://bwsalmon/grain"); code != 0 || strings.TrimSpace(out) != "run-1/" {
		t.Errorf("ls: %d %q %s", code, out, errOut)
	}
	if out, errOut, code := run("ci", "log", "--job", "go", "--step", "Test"); code != 0 || !strings.Contains(out, "--- FAIL: TestWidget") {
		t.Errorf("ci log: %d %q %s", code, out, errOut)
	}
	if out, errOut, code := run("grep", "--", "--- FAIL", "ci://bwsalmon/grain/run-1/go/Test.log"); code != 0 || !strings.Contains(out, "TestWidget") {
		t.Errorf("grep: %d %q %s", code, out, errOut)
	}
	if out, errOut, code := run("ci", "list", "--conclusion", "failure"); code != 0 || !strings.Contains(out, "run-1 grain/task-7") {
		t.Errorf("ci list: %d %q %s", code, out, errOut)
	}
	if out, errOut, code := run("pr", "status"); code != 0 || !strings.Contains(out, "go: failure") {
		t.Errorf("pr status: %d %q %s", code, out, errOut)
	}
	if out, errOut, code := run("playbooks"); code != 0 || !strings.Contains(out, "gke.md") {
		t.Errorf("playbooks: %d %q %s", code, out, errOut)
	}
	if out, errOut, code := run("read", "src://playbooks/gke.md"); code != 0 || out != "# GKE\n" {
		t.Errorf("read playbook: %d %q %s", code, out, errOut)
	}
	// Refusals come back in unknownToolMessage's words.
	_, errOut, code := run("read", "src://pkg/ci/engine.go")
	if code != 1 || !strings.Contains(errOut, `does not hold the "self-debug" capability`) || !strings.Contains(errOut, "a human attaches it to the task") {
		t.Errorf("src without self-debug: %d %s", code, errOut)
	}
	_, errOut, code = run("db", "query", "SELECT 1")
	if code != 1 || !strings.Contains(errOut, `"db-read"`) {
		t.Errorf("db query without db-read: %d %s", code, errOut)
	}
}

func TestSandboxCLIOutsideASandboxIsNotHandled(t *testing.T) {
	_, handled := maybeRunSandboxCLI([]string{"ls", "ci://a/b"}, fakeSandboxEnv(nil, ""), &bytes.Buffer{}, &bytes.Buffer{})
	if handled {
		t.Error("handled outside a sandbox")
	}
	if _, err := fsPath("not-a-uri"); err == nil || errors.Is(err, nil) {
		t.Error("fsPath accepted a non-URI")
	}
}

type cliTestIssues struct{}

func (cliTestIssues) ListIssues(owner, repo, label string) ([]github.Issue, error) {
	return []github.Issue{{Number: 5, Title: "Widget wobbles", State: "open", Author: "octo",
		Labels: map[string]struct{}{"bug": {}}}}, nil
}

func (cliTestIssues) GetIssue(owner, repo string, number int) (github.Issue, error) {
	if number != 5 {
		return github.Issue{}, errors.New("not found")
	}
	return github.Issue{Number: 5, Title: "Widget wobbles", State: "open", Author: "octo", Body: "It wobbles at speed."}, nil
}

func (cliTestIssues) ListComments(owner, repo string, number int) ([]github.Comment, error) {
	return []github.Comment{{User: "maintainer", Body: "Seen it too."}}, nil
}

func (cliTestIssues) ListReviewComments(owner, repo string, number int) ([]github.ReviewComment, error) {
	return nil, nil
}

type cliTestDB struct{ queries *[]string }

func (d cliTestDB) QueryDB(_ context.Context, query string, _ []any) (dbaccess.QueryResult, error) {
	*d.queries = append(*d.queries, query)
	return dbaccess.QueryResult{Columns: []string{"id"}, Rows: []map[string]any{{"id": "t-7"}}, Count: 1}, nil
}

func (d cliTestDB) ExecDB(context.Context, string, []any) (dbaccess.ExecResult, error) {
	return dbaccess.ExecResult{}, errors.New("a write reached the database")
}

// cliTestBackend answers /cli/issues and /cli/db/query with the tools
// themselves, over fakes, as the daemon's backend does over the real
// readers.
type cliTestBackend struct{ db cliTestDB }

func (cliTestBackend) IssueTools(_ context.Context, scope namespace.Scope) []mcp.Tool {
	owner, repo, _ := strings.Cut(scope.TaskRepo, "/")
	return mcp.NewIssueTools(cliTestIssues{}, mcp.IssueScope{DefaultOwner: owner, DefaultRepo: repo})
}

func (b cliTestBackend) DBQueryTool(_ context.Context, scope namespace.Scope) mcp.Tool {
	return dbaccess.Tools(dbaccess.Config{TaskID: scope.TaskIDStr, Executor: b.db})[0]
}

// TestSandboxCLIIssuesAndDBAgainstTheProxy runs grain issue and grain db
// query end to end against the proxy, which answers them with
// list_issues, read_issue and query_grain_db -- read-only -- for a run
// holding read-issues and db-read.
func TestSandboxCLIIssuesAndDBAgainstTheProxy(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "tokens.json")
	raw, _ := json.Marshal(map[string]string{"sb": "tok", "bare": "tok-bare"})
	if err := os.WriteFile(tokenFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	tokens, err := gitproxy.LoadSandboxTokens(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	var queries []string
	proxy := &gitproxy.GitProxy{
		Tokens:    tokens,
		Namespace: &namespace.Server{Redactor: redact.New(nil)},
		NamespaceScopes: cliTestScopes{
			"sb":   {TaskID: "7", RunID: "r", TaskRepo: "bwsalmon/grain", MayReadIssues: true, MayReadDB: true},
			"bare": {TaskID: "8", RunID: "r8", TaskRepo: "bwsalmon/grain"},
		},
		CLI: cliTestBackend{db: cliTestDB{queries: &queries}},
	}
	srv := httptest.NewServer(gitproxy.NewHandler(proxy))
	defer srv.Close()
	runAs := func(token string, args ...string) (string, string, int) {
		env := fakeSandboxEnv(map[string]string{"GRAIN_SANDBOX_PROXY": srv.URL, "GRAIN_SANDBOX_TOKEN": token}, "")
		var out, errOut bytes.Buffer
		code, handled := maybeRunSandboxCLI(args, env, &out, &errOut)
		if !handled {
			t.Fatalf("%q not handled in sandbox mode", args)
		}
		return out.String(), errOut.String(), code
	}

	if out, errOut, code := runAs("tok", "issue", "list"); code != 0 ||
		!strings.Contains(out, "Open issues for bwsalmon/grain (1)") || !strings.Contains(out, "#5 [open] Widget wobbles (by @octo) -- labels: bug") {
		t.Errorf("issue list: %d %q %s", code, out, errOut)
	}
	if out, errOut, code := runAs("tok", "issue", "read", "5", "--repo", "acme/widgets"); code != 0 ||
		!strings.Contains(out, "Issue #5: Widget wobbles") || !strings.Contains(out, "It wobbles at speed.") || !strings.Contains(out, "Seen it too.") {
		t.Errorf("issue read: %d %q %s", code, out, errOut)
	}
	if _, errOut, code := runAs("tok", "issue", "read", "9"); code != 1 || !strings.Contains(errOut, "Error reading issue #9") {
		t.Errorf("issue read of a missing issue: %d %s", code, errOut)
	}
	if out, errOut, code := runAs("tok", "db", "query", "SELECT", "id", "FROM", "task"); code != 0 ||
		!strings.Contains(out, "Query returned 1 row(s)") || !strings.Contains(out, `"t-7"`) {
		t.Errorf("db query: %d %q %s", code, out, errOut)
	}
	if _, errOut, code := runAs("tok", "db", "query", "DELETE FROM task"); code != 1 || !strings.Contains(errOut, "Query rejected") {
		t.Errorf("db query with a write: %d %s", code, errOut)
	}
	if len(queries) != 1 || queries[0] != "SELECT id FROM task" {
		t.Errorf("queries that reached the database = %q, want only the SELECT", queries)
	}
	if _, errOut, code := runAs("tok-bare", "issue", "list"); code != 1 || !strings.Contains(errOut, `"read-issues"`) {
		t.Errorf("issue list without read-issues: %d %s", code, errOut)
	}
}
