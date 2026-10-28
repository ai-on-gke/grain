package orchestrator

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/capability/intree"
	"github.com/bwsalmon/grain/pkg/capability/portforward"
	"github.com/bwsalmon/grain/pkg/capability/protocol"
	"github.com/bwsalmon/grain/pkg/mcp"
)

// localPlacementSandbox runs every command it is given on this machine, with the
// placement script's target path moved under root -- so a pass applied
// to it lands in a directory the test can read back.
type localPlacementSandbox struct{ root string }

func (s *localPlacementSandbox) Name() string                              { return "run-1" }
func (s *localPlacementSandbox) Tools(context.Context) ([]mcp.Tool, error) { return nil, nil }
func (s *localPlacementSandbox) ConfigureGitCredentials(context.Context, string, string, mcp.GitIdentity) error {
	return nil
}
func (s *localPlacementSandbox) Release(context.Context) error { return nil }
func (s *localPlacementSandbox) RunInSandbox(ctx context.Context, argv []string, stdin string) (string, string, int) {
	if len(argv) < 5 || argv[2] != placeScript {
		return "", "unexpected command " + strings.Join(argv, " "), 1
	}
	argv = append([]string(nil), argv...)
	argv[4] = filepath.Join(s.root, argv[4])
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = strings.NewReader(stdin)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", stderr.String() + err.Error(), 1
	}
	return "", "", 0
}

// A two-file config map and a two-key secret map, configured on the real
// config-map server and granted to a task, land in the sandbox -- through
// the same placement script every backend runs -- with their modes.
func TestConfigMapGrantPlacesEveryFileOfItsMapsInTheSandbox(t *testing.T) {
	if _, err := exec.LookPath("base64"); err != nil {
		t.Skip("the placement script needs base64")
	}
	ctx := context.Background()
	s, err := intree.NewConfigMap(intree.ConfigMapConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(ctx) })
	const control = "walkthrough-control-token-0123456789abcdef"
	s.CapabilityServer.Token = control
	ts := httptest.NewServer(s.CapabilityServer)
	t.Cleanup(ts.Close)
	const tokenValue = "walkthrough-secret-token"
	stored := map[string]string{
		"control":    control,
		"cm-secrets": `{"deploy-creds/key.json":"{\"k\":1}","deploy-creds/token":"` + tokenValue + `"}`,
	}
	ps, err := protocol.Attach(ctx, "https://github.com/bwsalmon/grain/capabilities/config-map",
		protocol.ServerConfig{
			ServerURL: ts.URL + "/mcp",
			Headers:   map[string]string{"Authorization": "Bearer $secret:control"},
			Config: map[string]any{
				"config_maps": []any{map[string]any{
					"name": "my-tools",
					"data": map[string]any{
						"settings.toml": map[string]any{"content": "a = 1\n"},
						"run.sh":        map[string]any{"content": "#!/bin/sh\necho ok\n", "executable": true},
					},
				}},
				"secrets": []any{map[string]any{
					"name": "deploy-creds",
					"keys": map[string]any{
						"key.json": map[string]any{"mode": "0400"},
						"token":    map[string]any{},
					},
				}},
				"secret_values": "$secret:cm-secrets",
			},
		},
		func(name string) (string, bool) { v, ok := stored[name]; return v, ok },
		protocol.DialOptions{})
	if err != nil {
		t.Fatalf("attaching config-map: %v", err)
	}
	t.Cleanup(func() { ps.Close() })

	args, err := protocol.ValidateArgs(ps.Methods["config-map"].ArgsSchema, map[string]any{
		"config_maps": []any{"my-tools"},
		"secrets":     []any{"deploy-creds"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := ps.Mint(ctx, "config-map", "e1-1-0", time.Now().Add(time.Hour), args)
	if err != nil || resp.Status != protocol.StatusOK {
		t.Fatalf("mint = %+v, %v", resp, err)
	}
	pass := &protocol.Pass{Grants: []protocol.MintedGrant{{
		Grant:     protocol.Grant{Ref: protocol.Ref{CapabilityID: "https://github.com/bwsalmon/grain/capabilities/config-map", Method: "config-map"}},
		AttemptID: "e1-1-0",
		Response:  resp,
	}}}
	sb := &localPlacementSandbox{root: t.TempDir()}
	m, err := materializePass(ctx, Config{}, sb, pass, time.Now().Add(time.Hour),
		func(string) (portforward.Service, error) { return portforward.Service{}, nil })
	if err != nil {
		t.Fatalf("materializing the pass: %v", err)
	}
	defer m.Close()

	for path, want := range map[string]struct {
		mode    os.FileMode
		content string
	}{
		intree.ConfigMapDir + "/my-tools/settings.toml": {0o644, "a = 1\n"},
		intree.ConfigMapDir + "/my-tools/run.sh":        {0o755, "#!/bin/sh\necho ok\n"},
		intree.ConfigMapDir + "/deploy-creds/key.json":  {0o400, `{"k":1}`},
		intree.ConfigMapDir + "/deploy-creds/token":     {0o600, tokenValue},
	} {
		full := filepath.Join(sb.root, path)
		info, err := os.Stat(full)
		if err != nil {
			t.Errorf("%s was not placed: %v", path, err)
			continue
		}
		if info.Mode().Perm() != want.mode {
			t.Errorf("%s mode = %04o, want %04o", path, info.Mode().Perm(), want.mode)
		}
		if b, _ := os.ReadFile(full); string(b) != want.content {
			t.Errorf("%s content = %q, want %q", path, b, want.content)
		}
	}
	if strings.Contains(resp.PromptSection, tokenValue) {
		t.Errorf("prompt section leaked a secret value:\n%s", resp.PromptSection)
	}
	for _, want := range []string{"config map my-tools in " + intree.ConfigMapDir + "/my-tools", "secret map deploy-creds in " + intree.ConfigMapDir + "/deploy-creds: key.json (0400), token (0600)"} {
		if !strings.Contains(resp.PromptSection, want) {
			t.Errorf("prompt section missing %q:\n%s", want, resp.PromptSection)
		}
	}
}
