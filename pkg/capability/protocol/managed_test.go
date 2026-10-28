package protocol

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// mapManaged is a ManagedSecrets over a map, counting what it generated.
type mapManaged struct {
	mu        sync.Mutex
	values    map[string]string
	generated int
}

func (m *mapManaged) Ensure(name string, gen func() (string, error)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.values[name]; ok {
		return nil
	}
	return m.set(name, gen)
}

func (m *mapManaged) Rotate(name string, gen func() (string, error)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.set(name, gen)
}

func (m *mapManaged) set(name string, gen func() (string, error)) error {
	v, err := gen()
	if err != nil {
		return err
	}
	if m.values == nil {
		m.values = map[string]string{}
	}
	m.values[name] = v
	m.generated++
	return nil
}

func (m *mapManaged) lookup(name string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.values[name]
	return v, ok
}

// tokenServer is a server whose schema declares DataPlaneTokenConfig,
// recording the token each configure gave it.
func tokenServer(t *testing.T, got *[]string) *CapabilityServer {
	t.Helper()
	schema := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
		DataPlaneTokenConfig: map[string]any{"type": "string", "writeOnly": true, "minLength": 32},
		"project":            map[string]any{"type": "string"},
	}}
	srv, err := NewCapabilityServer("cfg")
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.HandleConfigure(schema, func(_ context.Context, cfg map[string]any) error {
		tok, _ := cfg[DataPlaneTokenConfig].(string)
		*got = append(*got, tok)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return srv
}

var managedEntry = ServerConfig{Command: "grain", Args: []string{"capability", "workspace", "-listen", "127.0.0.1:9731"}}

func attachManaged(t *testing.T, id string, config map[string]any, lookup SecretLookup, store ManagedSecrets, got *[]string) *Server {
	t.Helper()
	s, err := AttachCallerManaged(context.Background(), id, ServerConfig{Config: config}, lookup, &loopback{srv: tokenServer(t, got)}, Identity{}, store)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A server declaring dataPlaneToken that nobody configured one for is
// sent a token grain generated and stored itself, found again (not made
// again) on the next attach -- a daemon restart -- and is what grain's
// proxy sends it.
func TestAManagedDataPlaneTokenIsGeneratedOnceAndKept(t *testing.T) {
	const id = "https://grain.dev/capabilities/workspace"
	store := &mapManaged{}
	var got []string
	s := attachManaged(t, id, nil, store.lookup, store, &got)
	name := ManagedSecretName(id, DataPlaneTokenConfig)
	tok, ok := store.lookup(name)
	if !ok || len(tok) < 32 {
		t.Fatalf("managed secret %s = %q, %v; want a generated value of at least 32 bytes", name, tok, ok)
	}
	if len(got) != 1 || got[0] != tok {
		t.Fatalf("configure was sent %q, want the generated token", got)
	}
	if dp, err := s.dataPlaneOf(managedEntry); err != nil || dp.Headers["Authorization"] != "Bearer "+tok {
		t.Errorf("data plane %+v, %v; want the generated token as its bearer", dp, err)
	}
	if !strings.HasPrefix(name, "capability-managed-") || !strings.HasSuffix(name, "/data-plane-token") || strings.Contains(name, "grain.dev") {
		t.Errorf("managed secret name %q", name)
	}

	// The next start finds the same value.
	attachManaged(t, id, nil, store.lookup, store, &got)
	if store.generated != 1 || got[1] != tok {
		t.Errorf("after a restart: generated %d time(s), configured %q; want the same token kept", store.generated, got[1])
	}

	// Another server gets a value of its own.
	other := &mapManaged{values: map[string]string{name: tok}}
	attachManaged(t, "https://grain.dev/capabilities/gchat", nil, other.lookup, other, &got)
	if got[2] == tok || len(got[2]) < 32 {
		t.Errorf("a second server was configured with %q", got[2])
	}
}

// An operator's own dataPlaneToken -- an override, or a deployment's
// token from before grain managed it -- wins, and nothing is generated.
func TestAConfiguredDataPlaneTokenOverridesTheManagedOne(t *testing.T) {
	const id = "https://grain.dev/capabilities/workspace"
	store := &mapManaged{}
	lookup := func(name string) (string, bool) {
		if name == "mine/token" {
			return strings.Repeat("o", 40), true
		}
		return store.lookup(name)
	}
	var got []string
	config := map[string]any{DataPlaneTokenConfig: "$secret:mine/token"}
	attachManaged(t, id, config, lookup, store, &got)
	if got[0] != strings.Repeat("o", 40) || store.generated != 0 {
		t.Errorf("configured %q, generated %d; want the operator's token and nothing generated", got[0], store.generated)
	}
	if !ManagedOverridden(id, DataPlaneTokenConfig, config) || ManagedOverridden(id, DataPlaneTokenConfig, nil) ||
		ManagedOverridden(id, DataPlaneTokenConfig, map[string]any{DataPlaneTokenConfig: SecretRef(ManagedSecretName(id, DataPlaneTokenConfig))}) {
		t.Errorf("ManagedOverridden")
	}
	if config[DataPlaneTokenConfig] != "$secret:mine/token" {
		t.Errorf("the stored config was changed: %v", config)
	}
}

// Rotating replaces the stored value, and the next configure sends the
// new one; the config grain stores is never given the value, only the
// attach's own copy a reference.
func TestRotatingAManagedTokenReplacesIt(t *testing.T) {
	const id = "https://grain.dev/capabilities/workspace"
	store := &mapManaged{}
	var got []string
	config := map[string]any{"project": "p"}
	attachManaged(t, id, config, store.lookup, store, &got)
	name := ManagedSecretName(id, DataPlaneTokenConfig)
	if err := store.Rotate(name, GenerateManagedValue); err != nil {
		t.Fatal(err)
	}
	s := attachManaged(t, id, config, store.lookup, store, &got)
	rotated, _ := store.lookup(name)
	if got[1] == got[0] || got[1] != rotated {
		t.Errorf("after rotate configured %q (before %q), want the new value", got[1], got[0])
	}
	if dp, _ := s.dataPlaneOf(managedEntry); dp.Headers["Authorization"] != "Bearer "+rotated {
		t.Errorf("data plane bearer not rotated")
	}
	if len(config) != 1 {
		t.Errorf("the entry's config was written to: %v", config)
	}
}

// Without a store nothing is generated: the server reads its
// environment's token, as before.
func TestNoManagedStoreManagesNothing(t *testing.T) {
	var got []string
	attachManaged(t, "https://grain.dev/capabilities/workspace", nil, nil, nil, &got)
	if got[0] != "" {
		t.Errorf("configured %q with no store", got[0])
	}
	if v, err := GenerateManagedValue(); err != nil || len(v) < 32 {
		t.Errorf("GenerateManagedValue = %q, %v", v, err)
	}
}
