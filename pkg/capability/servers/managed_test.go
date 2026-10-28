package servers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/bwsalmon/grain/pkg/capability/protocol"
	"github.com/bwsalmon/grain/pkg/secrets"
)

// tokenCaller is a server whose configure schema declares the
// data-plane token, recording the token each configure sent it.
type tokenCaller struct {
	fakeCaller
	mu     sync.Mutex
	tokens []string
}

func (c *tokenCaller) ListTools(ctx context.Context) ([]protocol.Tool, error) {
	tools, err := c.fakeCaller.ListTools(ctx)
	return append(tools, protocol.Tool{Name: protocol.ConfigureTool, InputSchema: map[string]any{
		"type": "object", "required": []any{"config"},
		"properties": map[string]any{"config": map[string]any{
			"type": "object",
			"properties": map[string]any{
				protocol.DataPlaneTokenConfig: map[string]any{"type": "string", "writeOnly": true, "minLength": 32},
			},
		}},
	}}), err
}

func (c *tokenCaller) CallTool(_ context.Context, name string, args map[string]any) ([]byte, error) {
	if name != protocol.ConfigureTool {
		return nil, errors.New("not used")
	}
	cfg, _ := args["config"].(map[string]any)
	tok, _ := cfg[protocol.DataPlaneTokenConfig].(string)
	c.mu.Lock()
	c.tokens = append(c.tokens, tok)
	c.mu.Unlock()
	return []byte(`{"status":"ok"}`), nil
}

type tokenHarness struct {
	mu     sync.Mutex
	tokens []string
}

func (h *tokenHarness) attach(ctx context.Context, id string, cfg protocol.ServerConfig, lookup protocol.SecretLookup, opts protocol.DialOptions) (*protocol.Server, error) {
	c := &tokenCaller{fakeCaller: fakeCaller{methods: []string{"workspace"}}}
	s, err := protocol.AttachCallerManaged(ctx, id, cfg, lookup, c, opts.Identity, opts.Managed)
	h.mu.Lock()
	h.tokens = append(h.tokens, c.tokens...)
	h.mu.Unlock()
	return s, err
}

func (h *tokenHarness) last() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.tokens) == 0 {
		return ""
	}
	return h.tokens[len(h.tokens)-1]
}

func managedRegistry(store *secrets.Store, h *tokenHarness, logs *[]string) *Registry {
	lookup := func(name string) (string, bool) {
		v, err := store.Resolve(context.Background(), name)
		return v, err == nil
	}
	return New(Options{Secrets: lookup, Managed: store, Attach: h.attach, Logf: func(f string, a ...any) {
		*logs = append(*logs, fmt.Sprintf(f, a...))
	}})
}

// A server declaring dataPlaneToken is attached with a token grain
// generated into its secrets store, kept across a restart and replaced
// by RotateManaged; the value never reaches the stored configuration,
// the statuses Settings reads, or the log.
func TestTheRegistryManagesDataPlaneTokens(t *testing.T) {
	const id = "https://grain.dev/capabilities/workspace"
	dir := t.TempDir()
	store := secrets.New(dir)
	h := &tokenHarness{}
	var logs []string
	ctx := context.Background()
	cfg := map[string]protocol.ServerConfig{id: {Command: "grain", Args: []string{"capability", "workspace"}}}

	r := managedRegistry(store, h, &logs)
	r.Sync(ctx, cfg)
	if st := r.Statuses(); st[0].State != Attached {
		t.Fatalf("status %+v", st[0])
	}
	name := protocol.ManagedSecretName(id, protocol.DataPlaneTokenConfig)
	tok, err := store.Resolve(ctx, name)
	if err != nil || len(tok) < 32 || h.last() != tok {
		t.Fatalf("stored %q (%v), configured %q; want one generated token", tok, err, h.last())
	}
	noLeak := func(when string, values ...string) {
		t.Helper()
		statuses, _ := json.Marshal(r.Statuses())
		applied, _ := json.Marshal(r.applied)
		for _, v := range values {
			for what, s := range map[string]string{"statuses": string(statuses), "applied config": string(applied), "log": strings.Join(logs, "\n")} {
				if strings.Contains(s, v) {
					t.Errorf("%s: the token appears in the %s", when, what)
				}
			}
		}
	}
	noLeak("attached", tok)
	if cfg[id].Config != nil {
		t.Errorf("the configuration handed in was written to: %v", cfg[id].Config)
	}
	r.Close()

	// A restart -- a new registry over the same store -- keeps it.
	r = managedRegistry(secrets.New(dir), h, &logs)
	r.Sync(ctx, cfg)
	if h.last() != tok {
		t.Errorf("after a restart configured %q, want the kept token", h.last())
	}

	// Rotate replaces it and attaches the server again.
	rotated, err := r.RotateManaged(id)
	if err != nil || len(rotated) != 1 || rotated[0] != protocol.DataPlaneTokenConfig {
		t.Fatalf("RotateManaged = %v, %v", rotated, err)
	}
	r.Sync(ctx, cfg)
	next, _ := store.Resolve(ctx, name)
	if next == tok || h.last() != next {
		t.Errorf("after rotate stored %q, configured %q; want a new token sent", next, h.last())
	}
	noLeak("rotated", tok, next)

	// An operator's override wins, and there is nothing to rotate.
	if err := store.Set("mine", "token", []byte(strings.Repeat("o", 40))); err != nil {
		t.Fatal(err)
	}
	over := map[string]protocol.ServerConfig{id: {Command: "grain", Args: []string{"capability", "workspace"},
		Config: map[string]any{protocol.DataPlaneTokenConfig: "$secret:mine/token"}}}
	r.Sync(ctx, over)
	if h.last() != strings.Repeat("o", 40) {
		t.Errorf("override: configured %q", h.last())
	}
	if _, err := r.RotateManaged(id); err == nil {
		t.Errorf("RotateManaged of an overridden token succeeded")
	}
	r.Close()
}
