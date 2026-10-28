package ui_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/intree"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/capability/servers"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
)

type fixedStatuses []servers.Status

func (f fixedStatuses) Statuses() []servers.Status { return f }

// own is list without the built-ins grain registers itself
// (intree.EnsureBuiltins), which every listing carries.
func own(list []ui.CapabilityServer) []ui.CapabilityServer {
	var out []ui.CapabilityServer
	for _, s := range list {
		if s.Builtin == nil {
			out = append(out, s)
		}
	}
	return out
}

func TestCapabilityServersAddBindEnableDisableRemove(t *testing.T) {
	srv, client := testServer(t)
	storeSettingsRow(t, client)
	store := secrets.New(t.TempDir())
	client.Config.Secrets = store
	const id = "https://github.com/bwsalmon/grain/capabilities/gcp"
	client.Config.CapabilityServers = fixedStatuses{{ID: id, State: servers.Failed, Error: "capability server " + id + ": references secrets that are missing or empty: gcp-minter"}}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	hc := serverAccessClient(srv, ts.URL)
	ctx := context.Background()

	if _, err := hc.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: "not a url", Config: protocol.ServerConfig{Command: "grain"}}); err == nil {
		t.Error("an entry keyed by something that is not a capability_id was saved")
	}
	if _, err := hc.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: id, Config: protocol.ServerConfig{Command: "grain", ServerURL: "https://x.example/mcp"}}); err == nil {
		t.Error("an entry mixing both transports was saved")
	}
	if _, err := hc.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: id, Config: protocol.ServerConfig{Command: "gcp-cap"}}); err != nil {
		t.Fatalf("SetCapabilityServer: %v", err)
	}

	list, err := hc.BindCapabilityServerSecret(ctx, ui.BindCapabilityServerSecretRequest{
		ID: id, Kind: "env", Name: "GCP_MINTER_KEY", Secret: "gcp-minter", Value: "super-secret-key-material",
	})
	if err != nil {
		t.Fatalf("BindCapabilityServerSecret: %v", err)
	}
	if len(own(list)) != 1 || own(list)[0].Config.Env["GCP_MINTER_KEY"] != "$secret:gcp-minter" {
		t.Fatalf("after bind: %+v", list)
	}
	if len(own(list)[0].Secrets) != 1 || !own(list)[0].Secrets[0].Present {
		t.Errorf("secrets = %+v, want gcp-minter present", own(list)[0].Secrets)
	}
	if own(list)[0].Status == nil || own(list)[0].Status.State != servers.Failed {
		t.Errorf("status = %+v, want the registry's", own(list)[0].Status)
	}
	if v, err := store.Resolve(ctx, "gcp-minter"); err != nil || v != "super-secret-key-material" {
		t.Errorf("the value was not stored: %q, %v", v, err)
	}
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), "super-secret") {
		t.Error("the listing carries the secret value")
	}

	// What reaches grain/config is the reference, never the value.
	cfg, err := client.Store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	text, err := model.EncodeMCPServers(cfg.MCPServers)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "super-secret") || !strings.Contains(text, "$secret:gcp-minter") {
		t.Errorf("stored mcp_servers = %s", text)
	}

	if list, err = hc.CapabilityServerAction(ctx, "disable", id); err != nil || !own(list)[0].Config.Disabled {
		t.Fatalf("disable: %+v, %v", list, err)
	}
	if list, err = hc.CapabilityServerAction(ctx, "enable", id); err != nil || own(list)[0].Config.Disabled {
		t.Fatalf("enable: %+v, %v", list, err)
	}
	if _, err := hc.CapabilityServerAction(ctx, "enable", "https://example.com/unknown"); err == nil {
		t.Error("enabling an unconfigured server succeeded")
	}
	if list, err = hc.CapabilityServerAction(ctx, "remove", id); err != nil || len(own(list)) != 0 {
		t.Fatalf("remove: %+v, %v", list, err)
	}
}

func TestSavingSettingsKeepsCapabilityServers(t *testing.T) {
	_, client := testServer(t)
	storeSettingsRow(t, client)
	ctx := context.Background()
	const id = "https://example.com/caps/x"
	if err := client.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: id, Config: protocol.ServerConfig{ServerURL: "https://cap.example.com/mcp"}}); err != nil {
		t.Fatal(err)
	}
	name := "renamed"
	if _, err := client.UpdateSettings(ctx, ui.UpdateSettingsRequest{EnvironmentName: &name}); err != nil {
		t.Fatal(err)
	}
	cfg, err := client.Store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.MCPServers[id]; !ok {
		t.Errorf("a Settings save dropped the capability servers: %+v", cfg.MCPServers)
	}
}

// scriptTester is a registry whose one server answers test_script.
type scriptTester struct {
	fixedStatuses
	calls []map[string]any
}

func (s *scriptTester) CallTool(_ context.Context, id, tool string, args map[string]any, _ time.Duration) ([]byte, error) {
	if id != "https://x.example/host-scripts" || tool != intree.HostScriptTestTool {
		return nil, fmt.Errorf("capability server %s advertises no tool %s", id, tool)
	}
	s.calls = append(s.calls, args)
	return json.Marshal(intree.HostScriptTestResult{Stdout: "<two words>\n", ExitCode: 3, DurationMS: 7})
}

func TestTestHostScriptCallsTheServersTestScriptTool(t *testing.T) {
	srv, client := testServer(t)
	tester := &scriptTester{}
	client.Config.CapabilityServers = tester
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	hc := serverAccessClient(srv, ts.URL)
	ctx := context.Background()

	res, err := hc.TestHostScript(ctx, ui.TestHostScriptRequest{ID: "https://x.example/host-scripts", Script: "echo-args", Argv: []string{"two words", "$(id)"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stdout != "<two words>\n" || res.ExitCode != 3 || res.DurationMS != 7 {
		t.Errorf("result = %+v", res)
	}
	if len(tester.calls) != 1 || tester.calls[0]["script"] != "echo-args" || fmt.Sprint(tester.calls[0]["argv"]) != "[two words $(id)]" {
		t.Errorf("test_script called with %v", tester.calls)
	}
	for _, req := range []ui.TestHostScriptRequest{
		{ID: "not a url", Script: "echo-args"},
		{ID: "https://x.example/host-scripts"},
		{ID: "https://x.example/other", Script: "echo-args"},
	} {
		if _, err := hc.TestHostScript(ctx, req); err == nil {
			t.Errorf("%+v was run", req)
		}
	}
	client.Config.CapabilityServers = fixedStatuses{}
	if _, err := hc.TestHostScript(ctx, ui.TestHostScriptRequest{ID: "https://x.example/host-scripts", Script: "echo-args"}); err == nil || !strings.Contains(err.Error(), "does not attach") {
		t.Errorf("without a registry: %v", err)
	}
}

// gchatTester is a registry whose one server answers gchat's test_post
// and usage tools.
type gchatTester struct {
	fixedStatuses
	calls []string
}

func (g *gchatTester) CallTool(_ context.Context, id, tool string, args map[string]any, _ time.Duration) ([]byte, error) {
	g.calls = append(g.calls, id+" "+tool)
	if id != intree.CanonicalID("gchat") {
		return nil, fmt.Errorf("capability server %s is not attached", id)
	}
	g.calls[len(g.calls)-1] = tool + " " + fmt.Sprint(args)
	switch tool {
	case intree.GChatTestTool:
		if args["space"] != "team-room" {
			return json.Marshal(intree.GChatTestResult{Space: fmt.Sprint(args["space"]), Error: "no space is configured by that name"})
		}
		return json.Marshal(intree.GChatTestResult{Space: "team-room", Message: "spaces/AAA/messages/1", Thread: "spaces/AAA/threads/1", Text: intree.GChatTestText})
	case intree.GChatUsageTool:
		return json.Marshal([]intree.GChatUsage{{Space: "team-room", Transport: "webhook", Posted: 2, Refused: 1, LastHour: 2, PerHour: 60}})
	}
	return nil, fmt.Errorf("capability server %s advertises no tool %s", id, tool)
}

func TestGChatTestPostAndUsageCallTheServersTools(t *testing.T) {
	srv, client := testServer(t)
	tester := &gchatTester{}
	client.Config.CapabilityServers = tester
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	hc := serverAccessClient(srv, ts.URL)
	ctx := context.Background()
	id := intree.CanonicalID("gchat")

	res, err := hc.TestGChatPost(ctx, ui.TestGChatPostRequest{ID: id, Space: "team-room"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Message != "spaces/AAA/messages/1" || res.Error != "" || res.Space != "team-room" {
		t.Errorf("result = %+v", res)
	}
	// A post the server could not make is a result, not a failed call.
	if res, err := hc.TestGChatPost(ctx, ui.TestGChatPostRequest{ID: id, Space: "elsewhere"}); err != nil || res.Error == "" {
		t.Errorf("posting to an unknown space = %+v, %v", res, err)
	}
	usage, err := hc.GChatUsage(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(usage) != 1 || usage[0].Space != "team-room" || usage[0].Posted != 2 || usage[0].Refused != 1 || usage[0].PerHour != 60 {
		t.Errorf("usage = %+v", usage)
	}
	if want := []string{"test_post map[space:team-room]", "test_post map[space:elsewhere]", "usage map[]"}; fmt.Sprint(tester.calls) != fmt.Sprint(want) {
		t.Errorf("calls = %q, want %q", tester.calls, want)
	}
	for _, req := range []ui.TestGChatPostRequest{
		{ID: "not a url", Space: "team-room"},
		{ID: id},
		{ID: "https://x.example/other", Space: "team-room"},
	} {
		if _, err := hc.TestGChatPost(ctx, req); err == nil {
			t.Errorf("%+v was posted", req)
		}
	}
	if _, err := hc.GChatUsage(ctx, ""); err == nil {
		t.Error("usage with no id was read")
	}
	// Only an entry running grain's gchat server is reached: another attached
	// server's own "usage" or "test_post" tool is never called, since
	// GET gchat-usage needs only capabilities:read.
	if err := client.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: "https://x.example/other", Config: protocol.ServerConfig{Command: "other-cap"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := hc.GChatUsage(ctx, "https://x.example/other"); err == nil || !strings.Contains(err.Error(), "does not run grain's gchat server") {
		t.Errorf("usage of another server: %v", err)
	}
	if want := []string{"test_post map[space:team-room]", "test_post map[space:elsewhere]", "usage map[]"}; fmt.Sprint(tester.calls) != fmt.Sprint(want) {
		t.Errorf("another server was called: calls = %q", tester.calls)
	}
	client.Config.CapabilityServers = fixedStatuses{}
	if _, err := hc.GChatUsage(ctx, id); err == nil || !strings.Contains(err.Error(), "does not attach") {
		t.Errorf("without a registry: %v", err)
	}
}

// A fresh deployment lists every built-in first, grants none of them to
// anything, refuses to remove one and keeps one disabled.
func TestBuiltinCapabilityServersAreListedDisableOnlyAndGrantNothing(t *testing.T) {
	srv, client := testServer(t)
	storeSettingsRow(t, client)
	prober := &fakeProber{}
	client.Config.CapabilityServers = prober
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	hc := serverAccessClient(srv, ts.URL)
	ctx := context.Background()
	gcp := intree.CanonicalID("gcp")
	if res, err := hc.TestCapabilityServer(ctx, ui.TestCapabilityServerRequest{ID: gcp}); err != nil || !res.OK || len(prober.probed) != 1 || prober.probed[0].Builtin != "gcp" {
		t.Errorf("testing a built-in before mcp_servers is stored = %+v, %v (probed %+v)", res, err, prober.probed)
	}
	if _, err := hc.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: "https://example.com/caps/own", Config: protocol.ServerConfig{Command: "own-cap"}}); err != nil {
		t.Fatal(err)
	}
	list, err := hc.ListCapabilityServers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	builtins := intree.Builtins()
	if len(list) != len(builtins)+1 || list[len(list)-1].ID != "https://example.com/caps/own" {
		t.Fatalf("list = %+v, want every built-in first, then the operator's own", list)
	}
	for i, b := range builtins {
		if list[i].ID != b.ID || list[i].Builtin == nil || list[i].Builtin.Name != b.Name {
			t.Errorf("row %d = %+v, want built-in %s", i, list[i], b.Name)
		}
		if intree.BuiltinConfigSchema(b.Name) != nil {
			if list[i].Status == nil || list[i].Status.State != servers.NeedsConfig || list[i].Status.ConfigSchema == nil {
				t.Errorf("configurable built-in %s status = %+v, want NeedsConfig with ConfigSchema", b.Name, list[i].Status)
			}
		}
	}
	if _, err := hc.CapabilityServerAction(ctx, "remove", gcp); err == nil || !strings.Contains(err.Error(), "disable") {
		t.Errorf("removing a built-in = %v, want a refusal pointing at disable", err)
	}
	if _, err := hc.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: "https://example.com/caps/renamed", RenameFrom: gcp, Config: protocol.ServerConfig{Command: "grain", Args: []string{"capability", "gcp"}}}); err == nil {
		t.Error("a built-in was renamed")
	}
	if _, err := hc.CapabilityServerAction(ctx, "disable", gcp); err != nil {
		t.Fatal(err)
	}
	cfg, err := client.Store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sc := cfg.MCPServers[gcp]; !sc.Disabled || sc.Builtin != "gcp" {
		t.Errorf("stored gcp = %+v, want a disabled built-in", sc)
	}
	if again, _ := intree.EnsureBuiltins(cfg.MCPServers); !again[gcp].Disabled {
		t.Error("registering the built-ins again enabled the disabled one")
	}
	usage, err := hc.CapabilityServerUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range usage {
		if len(u.Holders) != 0 {
			t.Errorf("%s has holders %+v from registration alone", u.ID, u.Holders)
		}
	}
}
