package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/capability/protocol"
)

// rowRecorder is a StatusReporter safe to read while the narrator's own
// goroutine writes to it, which can block every write on gate.
type rowRecorder struct {
	gate chan struct{}

	mu    sync.Mutex
	notes []string
	wrote chan string
}

func newRowRecorder() *rowRecorder { return &rowRecorder{wrote: make(chan string, 64)} }

func (r *rowRecorder) ReportStatus(ctx context.Context, note string) (StatusReport, error) {
	if r.gate != nil {
		select {
		case <-r.gate:
		case <-ctx.Done():
			return StatusReport{}, ctx.Err()
		}
	}
	r.mu.Lock()
	r.notes = append(r.notes, note)
	r.mu.Unlock()
	r.wrote <- note
	return StatusReport{Live: true}, nil
}

// next is the next note written, failing the test if none is.
func (r *rowRecorder) next(t *testing.T) string {
	t.Helper()
	select {
	case note := <-r.wrote:
		return note
	case <-time.After(5 * time.Second):
		t.Fatal("no activity was written")
		return ""
	}
}

const testDataPlaneToken = "test-data-plane-token-0123456789abcdef"

// proxiedServer is cs's attempt toolset behind a protocol.Proxy, wired the
// way orchestrator.wireEndpoints wires one: handshaken through the proxy,
// then observed by narrator under the server name the agent is given. It
// returns a client of the proxy, as the agent CLI is.
func proxiedServer(t *testing.T, upstream http.Handler, narrator *CapabilityNarrator, server string) *protocol.Client {
	t.Helper()
	srv := httptest.NewServer(upstream)
	t.Cleanup(srv.Close)
	proxy, err := protocol.NewProxy(protocol.DataPlane{
		Origin:  srv.URL,
		Headers: map[string]string{"Authorization": "Bearer " + testDataPlaneToken},
	}, "grain-task-1-1-0", nil)
	if err != nil {
		t.Fatal(err)
	}
	ep, err := protocol.ServeEndpoint(proxy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ep.Close() })
	roster, err := protocol.Handshake(context.Background(), ep.URL)
	if err != nil {
		t.Fatal(err)
	}
	ep.Proxy().ObserveToolCalls(narrator.Observer(server, roster.ServerName, roster.Labels))
	c, err := protocol.Dial(context.Background(), protocol.ServerConfig{ServerURL: ep.URL}, protocol.DialOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// wsServer is a capability server named workspace serving ws_read_file,
// standing in for a built-in server that registers its own phrases
// (RegisterCapabilityToolActivity).
func wsServer(t *testing.T) http.Handler {
	t.Helper()
	cs, err := protocol.NewCapabilityServer("workspace")
	if err != nil {
		t.Fatal(err)
	}
	cs.Token = testDataPlaneToken
	if err := cs.HandleTool("ws_read_file", "Read a file of the bound workspace's repo/.",
		map[string]any{"type": "object"},
		func(context.Context, map[string]any) ([]byte, error) { return []byte(`{"content":"package bar"}`), nil }); err != nil {
		t.Fatal(err)
	}
	return cs
}

// repoPath is a workspace path argument, relative to its repo/.
func repoPath(key string) NoteArg {
	return NoteArg{Key: key, Tail: true, Show: func(p string) string {
		p = strings.TrimPrefix(strings.Trim(p, "/"), "repo/")
		if p == "" || p == "." || p == "repo" {
			return "repo/"
		}
		return "repo/" + p
	}}
}

func init() {
	RegisterCapabilityToolActivity("workspace", map[string]CapabilityToolNote{
		"ws_read_file":  {Format: "reading %s", Args: []NoteArg{repoPath("path")}, Fallback: "reading a workspace file"},
		"ws_write_file": {Format: "writing %s", Args: []NoteArg{repoPath("path")}, Fallback: "writing a workspace file"},
		"ws_edit_file":  {Format: "editing %s", Args: []NoteArg{repoPath("path")}, Fallback: "editing a workspace file"},
		"ws_list":       {Format: "listing %s", Args: []NoteArg{repoPath("dir")}, Fallback: "listing repo/"},
		"ws_grep":       {Format: "searching %s for %s", Args: []NoteArg{repoPath("path"), QuotedArg("pattern")}, Fallback: "searching the workspace"},
		"ws_diff":       {Fallback: "reviewing the workspace diff"},
		"ws_mail":       {Format: "mailing %s to its reviewers", Args: []NoteArg{{Key: "change"}}, Fallback: "mailing a change to its reviewers"},
	})
}

// The operator's report: a run working in a workspace server left its row
// saying whatever it said last, because those calls go through the
// capability proxy rather than grain's own registry.
func TestAProxiedBuiltinCallNarratesTheRow(t *testing.T) {
	row := newRowRecorder()
	c := proxiedServer(t, wsServer(t), NewCapabilityNarrator(row), "workspace")
	out, err := c.CallTool(context.Background(), "ws_read_file", map[string]any{"path": "foo/bar.go"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "package bar") {
		t.Fatalf("the call answered %s, want the upstream's answer relayed", out)
	}
	if got := row.next(t); got != "workspace: reading repo/foo/bar.go" {
		t.Fatalf("row = %q", got)
	}
}

// customToolset is an attempt toolset of a server grain does not ship,
// whose tools/list offers the given tool entries as they are.
type customToolset struct {
	tools []map[string]any
}

func (f customToolset) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	body, _ := io.ReadAll(r.Body)
	json.Unmarshal(body, &req)
	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	var result any = map[string]any{"serverInfo": map[string]any{"name": "acme-deploy"}}
	switch req.Method {
	case "tools/list":
		result = map[string]any{"tools": f.tools}
	case "tools/call":
		result = map[string]any{"content": []map[string]any{{"type": "text", "text": "ok"}}}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
}

func TestACustomServersCallIsNarratedFromWhatItsToolSaysOfItself(t *testing.T) {
	schema := map[string]any{"type": "object"}
	toolset := customToolset{tools: []map[string]any{
		{"name": "roll_out", "inputSchema": schema, "description": "Roll out a build.",
			"annotations": map[string]any{"title": "Rolling out a build"}},
		{"name": "describe_only", "inputSchema": schema, "description": "Describe the\nrollout."},
		{"name": "bare", "inputSchema": schema},
	}}
	for _, tt := range []struct {
		tool, want string
	}{
		{"roll_out", "acme-deploy: Rolling out a build"},
		{"describe_only", "acme-deploy: Describe the rollout."},
		{"bare", "acme-deploy: bare"},
	} {
		t.Run(tt.tool, func(t *testing.T) {
			row := newRowRecorder()
			c := proxiedServer(t, toolset, NewCapabilityNarrator(row), "acme-deploy")
			if _, err := c.CallTool(context.Background(), tt.tool, map[string]any{}); err != nil {
				t.Fatal(err)
			}
			if got := row.next(t); got != tt.want {
				t.Fatalf("row = %q, want %q", got, tt.want)
			}
		})
	}
}

// The row is a courtesy and the call is the work: a store that has
// stopped answering must not hold up a single call made through the
// proxy, and the phrase still lands once it answers again.
func TestASlowRowDoesNotDelayTheCall(t *testing.T) {
	row := newRowRecorder()
	row.gate = make(chan struct{})
	c := proxiedServer(t, wsServer(t), NewCapabilityNarrator(row), "workspace")
	start := time.Now()
	for _, path := range []string{"a.go", "b.go", "c.go"} {
		if _, err := c.CallTool(context.Background(), "ws_read_file", map[string]any{"path": path}); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("three calls took %v behind a row that does not answer", elapsed)
	}
	close(row.gate)
	// Whatever was already being written lands, and of the phrases queued
	// behind it only the newest is kept: three calls, at most two writes,
	// ending on the last call's phrase.
	var got []string
	for len(got) == 0 || got[len(got)-1] != "workspace: reading repo/c.go" {
		got = append(got, row.next(t))
		if len(got) > 2 {
			t.Fatalf("writes = %q, want at most two ending on c.go's", got)
		}
	}
}

func TestANilNarratorNarratesNothing(t *testing.T) {
	if NewCapabilityNarrator(nil) != nil {
		t.Fatal("a narrator with no reporter is not nil")
	}
	var n *CapabilityNarrator
	n.Observer("workspace", "workspace", nil)("ws_diff", nil) // must not panic
}

func TestCapabilityActivityNotes(t *testing.T) {
	long := strings.Repeat("deep/", 30) + "file.go"
	for _, tt := range []struct {
		name, server, reported, tool string
		labels                       map[string]string
		args                         map[string]any
		want                         string
	}{
		{"edit", "workspace", "workspace", "ws_edit_file", nil, map[string]any{"path": "repo/foo/bar.go"}, "workspace: editing repo/foo/bar.go"},
		{"grep", "workspace", "workspace", "ws_grep", nil, map[string]any{"path": "x", "pattern": "func New"}, `workspace: searching repo/x for "func New"`},
		{"diff", "workspace", "workspace", "ws_diff", nil, nil, "workspace: reviewing the workspace diff"},
		{"list root", "workspace", "workspace", "ws_list", nil, map[string]any{"dir": "."}, "workspace: listing repo/"},
		{"number argument", "workspace", "workspace", "ws_mail", nil, map[string]any{"change": float64(12345)}, "workspace: mailing 12345 to its reviewers"},
		{"renamed server", "workspace-2", "workspace", "ws_read_file", nil, map[string]any{"path": "a.go"}, "workspace-2: reading repo/a.go"},
		{"gchat", "gchat-team", "gchat-team", "post_message", nil, map[string]any{"text": "hello"}, "gchat-team: posting to Google Chat"},
		{"missing argument", "workspace", "workspace", "ws_read_file", nil, nil, "workspace: reading a workspace file"},
		{"not a string", "workspace", "workspace", "ws_read_file", nil, map[string]any{"path": []any{"x"}}, "workspace: reading a workspace file"},
		{"newlines", "workspace", "workspace", "ws_grep", nil, map[string]any{"path": "x", "pattern": "a\nb\r\n\tc"}, `workspace: searching repo/x for "a b c"`},
		{"bidi and escapes", "workspace", "workspace", "ws_read_file", nil, map[string]any{"path": "a‮\x1b[31m.go"}, "workspace: reading repo/a [31m.go"},
		{"long path keeps its tail", "workspace", "workspace", "ws_read_file", nil, map[string]any{"path": long},
			"workspace: reading repo/…" + long[len(long)-(maxActivityArg-1):]},
		{"github token", "workspace", "workspace", "ws_grep", nil, map[string]any{"path": "x", "pattern": "ghp_abcdefghijklmnopqrstuvwxyz0123"}, "workspace: searching the workspace"},
		{"bearer header", "workspace", "workspace", "ws_write_file", nil, map[string]any{"path": "Bearer abcdefgh12345678"}, "workspace: writing a workspace file"},
		{"opaque key", "workspace", "workspace", "ws_read_file", nil, map[string]any{"path": "k/Zm9vYmFyYmF6cXV4MTIzNDU2Nzg5MGFiY2RlZg"}, "workspace: reading a workspace file"},
		{"custom label", "acme", "acme", "go", map[string]string{"go": "Deploying\nnow"}, nil, "acme: Deploying now"},
		{"secret label", "acme", "acme", "go", map[string]string{"go": "uses sk-abcdefghijklmnop1234"}, nil, "acme: go"},
		{"unknown workspace tool", "workspace", "workspace", "ws_new_thing", nil, nil, "workspace: ws_new_thing"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := capabilityActivityNote(tt.server, tt.reported, tt.labels, tt.tool, tt.args)
			if got != tt.want {
				t.Fatalf("note = %q, want %q", got, tt.want)
			}
		})
	}
}

// However long a label, the line fits the row, and nothing a capability
// server can say makes it read as one of grain's own declared waits.
func TestCapabilityActivityNotesFitTheRowAndAreNeverDeclaredWaits(t *testing.T) {
	note := capabilityActivityNote("acme", "acme", map[string]string{"t": strings.Repeat("x ", 200)}, "t", nil)
	if n := len([]rune(note)); n > MaxStatusLength {
		t.Fatalf("note is %d runes, over %d", n, MaxStatusLength)
	}
	for _, tool := range declaredWaitTools {
		phrase := toolActivityNote(tool, nil)
		note := capabilityActivityNote("acme", "acme", map[string]string{"t": phrase}, "t", nil)
		if IsDeclaredWait(note) {
			t.Fatalf("a server labelling its tool %q reads as a declared wait", phrase)
		}
	}
}
