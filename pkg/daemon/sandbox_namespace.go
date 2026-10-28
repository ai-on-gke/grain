package daemon

// sandbox_namespace.go fills in what the sandbox proxy's namespace
// (pkg/namespace) and /cli/ reads (pkg/gitproxy's cli.go) answer from,
// beyond grain CI's records: the task://, memory://, deploy:// and
// config:// roots, and GitHub issues and grain's database. Stage 2 of
// docs/design/sandbox-first-agent-surface.md.
//
// Each is the backend of the MCP tool it replaces, asked the same way:
// read_grain_task* (DaemonTasks), query_merge_queue (daemonMergeQueue),
// read_grain_metrics (DaemonMetrics), read_pipeline_changes
// (DaemonPipeline), read_deployment_health and read_deployment_state
// (pkg/capability/deploymenthealth), read_grain_log
// (pkg/capability/grainlog, with its redactor), list_issues and
// read_issue (issueTools) and query_grain_db (pkg/capability/dbaccess).
// Those reach grain's API over a ui.HTTPClient from a forked mcpserver;
// here the same client is pointed at the same API handler in process
// (LiveAPI), the way the CI API listener answers (CIAPI.SetUpstream), so
// what a run reads over the proxy is what the tool would have told it.
//
// Who may read what is not decided here. The proxy resolves the live
// run's scope (model.Store.SandboxNamespaceScope) and pkg/namespace and
// pkg/gitproxy hold every path and route to it before any of this is
// asked.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/bwsalmon/grain/pkg/capability/dbaccess"
	"github.com/bwsalmon/grain/pkg/capability/deploymenthealth"
	"github.com/bwsalmon/grain/pkg/capability/grainlog"
	"github.com/bwsalmon/grain/pkg/mcp"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/namespace"
	"github.com/bwsalmon/grain/pkg/staterepo"
	"github.com/bwsalmon/grain/pkg/ui"
)

// LiveAPI is grain's own API handler (ui.Server) as an http.RoundTripper,
// for the in-process client the sandbox namespace reads through. The UI
// server publishes it once built (startUIServer); until then -- and on a
// daemon that never builds one -- every request fails, saying so.
var LiveAPI = &inProcessAPI{}

type inProcessAPI struct {
	handler atomic.Pointer[http.Handler]
}

// Set publishes h as the handler requests are answered by.
func (a *inProcessAPI) Set(h http.Handler) { a.handler.Store(&h) }

// RoundTrip answers req with the published handler, as an in-process
// request: no remote address and no credential, which the API runs as
// the deployment's own actor (ui.Server.ServeHTTP).
func (a *inProcessAPI) RoundTrip(req *http.Request) (*http.Response, error) {
	h := a.handler.Load()
	if h == nil {
		return nil, errors.New("grain's API is not serving yet")
	}
	in := req.Clone(req.Context())
	in.RequestURI = in.URL.RequestURI()
	in.RemoteAddr = ""
	in.Host = "localhost"
	in.Header.Del("Authorization")
	in.Header.Del("X-Grain-Tunnel-Token")
	if in.Body == nil {
		in.Body = http.NoBody
	}
	rec := httptest.NewRecorder()
	(*h).ServeHTTP(rec, in)
	return rec.Result(), nil
}

// inProcessAPIClient is a ui.HTTPClient over api: the client every
// daemon* reader below already takes.
func inProcessAPIClient(api http.RoundTripper) *ui.HTTPClient {
	return &ui.HTTPClient{
		BaseURL:       "http://localhost",
		HTTP:          &http.Client{Transport: api},
		TokenProvider: func(context.Context) (string, error) { return "", nil },
		RetryPolicy:   &ui.RetryPolicy{}, // one attempt: there is no network to wait out
	}
}

// sandboxNamespaceConfig is what wireSandboxNamespace needs of the daemon.
type sandboxNamespaceConfig struct {
	DataDir      string
	Store        *model.Store
	GithubHost   string
	InsecureHTTP bool
	// API is what the readers' requests are answered by; LiveAPI in
	// the daemon.
	API http.RoundTripper
}

// wireSandboxNamespace fills ns's task://, memory://, deploy:// and
// config:// providers, and returns the backend of the proxy's /cli/
// issue and database reads.
func wireSandboxNamespace(ns *namespace.Server, cfg sandboxNamespaceConfig) *sandboxCLIBackend {
	client := inProcessAPIClient(cfg.API)
	ns.Tasks = mcp.NamespaceTasks{Reader: namespaceTaskReader{DaemonTasks: DaemonTasks{Client: client}, store: cfg.Store}}
	ns.Deploy = sandboxDeploy{client: client, stateDir: StateRepoDir(cfg.DataDir), secretsDir: SecretsDir(cfg.DataDir)}
	if cfg.Store != nil {
		ns.Memory = sandboxMemory{store: cfg.Store}
		ns.Config = sandboxRepoConfig{store: cfg.Store}
	}
	if cfg.DataDir != "" {
		// The state repository's working tree, held to what grain itself
		// exports there (staterepo.BrowsablePath): README.md,
		// schema-version, and memory/, release-notes/, scripts/,
		// settings/, tables/ and workflows/. Everything else in the
		// directory -- .git, whose objects keep whatever was ever
		// committed, the encrypted secrets file older builds kept there,
		// and anything a deployment has put beside them -- is withheld,
		// and a symlink is followed only to somewhere that list allows.
		ns.ConfigDir = StateRepoDir(cfg.DataDir)
		ns.ConfigAllow = staterepo.BrowsablePath
	}
	return &sandboxCLIBackend{cfg: cfg, client: client}
}

// namespaceTaskReader is DaemonTasks with the one read it lacks: an
// attempt's own prompt, which the API serves only for the latest
// attempt (GET /api/tasks/{id}/prompt) and the store keeps for each.
type namespaceTaskReader struct {
	DaemonTasks
	store *model.Store
}

var _ mcp.AttemptPromptReader = namespaceTaskReader{}

func (r namespaceTaskReader) AttemptPrompt(ctx context.Context, id string, attempt int) (string, error) {
	if r.store == nil {
		return "", nil
	}
	prompt, _, err := r.store.RunPrompt(ctx, id, attempt)
	return prompt, err
}

// sandboxDeploy is deploy://.
type sandboxDeploy struct {
	client               *ui.HTTPClient
	stateDir, secretsDir string
}

var (
	_ namespace.DeployProvider       = sandboxDeploy{}
	_ namespace.MetricsSectionLister = sandboxDeploy{}
)

// toolText is a tool's answer as a file's content, or its refusal as an
// error.
func toolText(res mcp.Result) (string, error) {
	if res.IsError {
		return "", errors.New(res.Text)
	}
	if !strings.HasSuffix(res.Text, "\n") {
		return res.Text + "\n", nil
	}
	return res.Text, nil
}

func (d sandboxDeploy) ReadMergeQueue(ctx context.Context) (string, error) {
	// No default-branch reader: the proxy has no run credential to ask
	// GitHub with, so a queued change whose task names no base is shown
	// with the branch the daemon knows (its batch's) or none.
	tool := mcp.NewMergeQueueTools(daemonMergeQueue{client: d.client})[0]
	return toolText(tool.Handler(ctx, map[string]any{"view": "full"}))
}

func (d sandboxDeploy) ReadMetrics(ctx context.Context, section string) (string, error) {
	if section == "all" {
		section = ""
	}
	if section != "" && !slices.Contains(mcp.MetricsSections, section) {
		return "", fmt.Errorf("%q is not a metrics section (%s)", section, strings.Join(mcp.MetricsSections, ", "))
	}
	text, err := DaemonMetrics{Client: d.client}.Metrics(ctx, mcp.MetricsQuery{Section: section})
	if err != nil {
		return "", err
	}
	return text, nil
}

func (d sandboxDeploy) ListMetricsSections() []string { return slices.Clone(mcp.MetricsSections) }

func (d sandboxDeploy) ReadPipelineChanges(ctx context.Context) (string, error) {
	tool := mcp.NewPipelineTools(DaemonPipeline{Client: d.client})[0]
	return toolText(tool.Handler(ctx, map[string]any{}))
}

// health is read_deployment_health and read_deployment_state, built per
// read so that a secret set since the daemon started is redacted by the
// next read (grainLogRedactor reads the store each time).
func (d sandboxDeploy) health() []mcp.Tool {
	var redactFn func(string) string
	if redactor := grainLogRedactor(d.secretsDir); redactor != nil {
		redactFn = func(s string) string { lines, _ := redactor.Redact([]string{s}); return lines[0] }
	}
	return deploymenthealth.Tools(deploymenthealth.Config{
		StateDir: d.stateDir, Reader: daemonDeploymentHealth{client: d.client}, Redact: redactFn,
	})
}

func (d sandboxDeploy) ReadHealth(ctx context.Context) (string, error) {
	return toolText(d.health()[0].Handler(ctx, map[string]any{}))
}

// ReadState is one table of the state repository (tables/<table>.json),
// or with table "" the list of them, as read_deployment_state answers.
func (d sandboxDeploy) ReadState(ctx context.Context, table string) (string, error) {
	args := map[string]any{}
	if table != "" {
		args["path"] = "tables/" + table + ".json"
	}
	return toolText(d.health()[1].Handler(ctx, args))
}

func (d sandboxDeploy) ListStateTables(ctx context.Context) ([]string, error) {
	text, err := toolText(d.health()[1].Handler(ctx, map[string]any{}))
	if err != nil {
		return nil, err
	}
	var listing struct {
		Files []string `json:"files"`
	}
	if err := json.Unmarshal([]byte(text), &listing); err != nil {
		return nil, fmt.Errorf("reading the state table list: %w", err)
	}
	tables := make([]string, 0, len(listing.Files))
	for _, f := range listing.Files {
		tables = append(tables, strings.TrimSuffix(filepath.Base(f), ".json"))
	}
	return tables, nil
}

func (d sandboxDeploy) ReadGrainLog(ctx context.Context, source string) (string, error) {
	tool := grainlog.Tools(grainlog.Config{Redactor: grainLogRedactor(d.secretsDir)})[0]
	args := map[string]any{}
	if source != "" {
		args["source"] = source
	}
	return toolText(tool.Handler(ctx, args))
}

func (d sandboxDeploy) ListGrainLogSources(context.Context) ([]string, error) {
	var names []string
	for _, s := range grainlog.DefaultSources() {
		names = append(names, s.Name)
	}
	return names, nil
}

// sandboxMemory is memory://, read from the store: the standing memory
// of each scope and the documents filed under it.
type sandboxMemory struct{ store *model.Store }

var _ namespace.MemoryProvider = sandboxMemory{}

func (m sandboxMemory) ListMemoryRepos(ctx context.Context) ([]string, error) {
	seen := map[string]bool{}
	memories, err := m.store.ListMemories(ctx)
	if err != nil {
		return nil, err
	}
	for _, mem := range memories {
		if mem.Repo != nil {
			seen[mem.Repo.String()] = true
		}
	}
	docs, err := m.store.ListMemoryDocuments(ctx)
	if err != nil {
		return nil, err
	}
	for _, doc := range docs {
		if doc.Repo != nil {
			seen[doc.Repo.String()] = true
		}
	}
	repos := make([]string, 0, len(seen))
	for r := range seen {
		repos = append(repos, r)
	}
	sort.Strings(repos)
	return repos, nil
}

func (m sandboxMemory) ReadMemoryLayer(ctx context.Context, scope string) (*namespace.MemoryLayerView, error) {
	var repo *model.RepoRef
	if scope != "global" {
		ref, err := model.ParseRepo(scope)
		if err != nil {
			return nil, fmt.Errorf("%w: memory scope %q: %v", namespace.ErrNotFound, scope, err)
		}
		repo = &ref
	}
	layer := &namespace.MemoryLayerView{Scope: scope}
	mem, err := m.store.GetMemory(ctx, repo)
	if err != nil {
		return nil, err
	}
	if mem != nil {
		layer.Text = mem.Text
	}
	docs, err := m.store.ListMemoryDocuments(ctx, model.MemoryScope{Repo: repo})
	if err != nil {
		return nil, err
	}
	for _, d := range docs {
		doc, err := m.store.GetMemoryDocument(ctx, repo, d.Name)
		if err != nil {
			return nil, err
		}
		if doc == nil {
			continue // forgotten between the listing and the read
		}
		layer.Docs = append(layer.Docs, namespace.MemoryDocView{Slug: doc.Name, Summary: doc.Summary, Text: doc.Text})
	}
	if mem == nil && len(layer.Docs) == 0 && repo != nil {
		return nil, fmt.Errorf("%w: no memory for %s", namespace.ErrNotFound, scope)
	}
	return layer, nil
}

// sandboxRepoConfig is config://repos/<owner>/<repo>: one repository's
// configuration row, as JSON.
type sandboxRepoConfig struct{ store *model.Store }

var _ namespace.ConfigProvider = sandboxRepoConfig{}

func (c sandboxRepoConfig) ListConfigRepos(ctx context.Context) ([]string, error) {
	seen := map[string]bool{}
	targets, err := c.store.TargetRepos(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range targets {
		seen[r] = true
	}
	configs, err := c.store.ListRepoConfigs(ctx)
	if err != nil {
		return nil, err
	}
	for _, rc := range configs {
		seen[rc.Repo.String()] = true
	}
	repos := make([]string, 0, len(seen))
	for r := range seen {
		repos = append(repos, r)
	}
	sort.Strings(repos)
	return repos, nil
}

func (c sandboxRepoConfig) ReadRepoConfig(ctx context.Context, repo string) (string, error) {
	ref, err := model.ParseRepo(repo)
	if err != nil {
		return "", err
	}
	rc, err := c.store.GetRepoConfig(ctx, ref)
	if err != nil {
		return "", err
	}
	if rc == nil {
		known, err := c.ListConfigRepos(ctx)
		if err != nil {
			return "", err
		}
		if !slices.ContainsFunc(known, func(r string) bool { return strings.EqualFold(r, repo) }) {
			return "", fmt.Errorf("no repository %s is configured", repo)
		}
		rc = &model.RepoConfig{Repo: ref}
	}
	out := *rc
	out.CloneURL = withoutUserinfo(out.CloneURL)
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b) + "\n", nil
}

// withoutUserinfo drops any credential written into a clone URL.
func withoutUserinfo(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = nil
	return u.String()
}

// sandboxCLIBackend is gitproxy.CLIBackend: list_issues, read_issue and
// query_grain_db, built for the run asking.
type sandboxCLIBackend struct {
	cfg    sandboxNamespaceConfig
	client *ui.HTTPClient
}

// IssueTools reads GitHub with the credential the run's own task pushes
// its repository with -- the credential `grain mcpserver` is handed as
// -pr-credential (orchestrator's runGitCredential) -- so the proxy reads
// as the account the tools would have.
func (b *sandboxCLIBackend) IssueTools(ctx context.Context, scope namespace.Scope) []mcp.Tool {
	credential := ""
	if b.cfg.Store != nil && scope.TaskIDStr != "" && scope.TaskRepo != "" {
		if ref, err := model.ParseRepo(scope.TaskRepo); err == nil && !model.IsConfigRepo(ref) {
			if name, err := b.cfg.Store.TaskGitCredentialOn(ctx, scope.TaskIDStr, ref.Host, ref.Owner, ref.Name); err == nil {
				credential = name
			}
		}
	}
	return issueTools(b.cfg.DataDir, b.cfg.GithubHost, b.cfg.InsecureHTTP, scope.TaskRepo, credential)
}

// DBQueryTool is query_grain_db, read-only: the API it calls checks the
// task's grant and live run again (ui.Server.handleTaskDBQuery).
func (b *sandboxCLIBackend) DBQueryTool(_ context.Context, scope namespace.Scope) mcp.Tool {
	return dbaccess.Tools(dbaccess.Config{
		TaskID:   scope.TaskIDStr,
		Executor: daemonDB{client: b.client, taskID: scope.TaskIDStr},
	})[0]
}
