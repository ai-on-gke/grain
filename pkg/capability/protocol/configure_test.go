package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

var testConfigSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []any{"project", "token"},
	"properties": map[string]any{
		"project": map[string]any{"type": "string", "pattern": "^[a-z][a-z0-9-]+$"},
		"region":  map[string]any{"type": "string", "enum": []any{"eu", "us"}, "default": "eu"},
		"token":   map[string]any{"type": "string", "writeOnly": true, "minLength": 8},
	},
}

func configureTool(schema map[string]any) Tool {
	return Tool{Name: ConfigureTool, InputSchema: map[string]any{
		"type": "object", "required": []any{"config"},
		"properties": map[string]any{"config": schema},
	}}
}

func TestDiscoverConfigure(t *testing.T) {
	if got, err := DiscoverConfigure([]Tool{mintTool("a", nil)}); err != nil || got != nil {
		t.Fatalf("no configure tool: got %v, %v; want nil, nil", got, err)
	}
	got, err := DiscoverConfigure([]Tool{mintTool("a", nil), configureTool(testConfigSchema)})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"token"}; !reflect.DeepEqual(SecretFields(got), want) {
		t.Errorf("SecretFields = %v, want %v", SecretFields(got), want)
	}
	// format: password marks a secret as writeOnly does.
	pw, err := DiscoverConfigure([]Tool{configureTool(map[string]any{"type": "object", "properties": map[string]any{
		"key": map[string]any{"type": "string", "format": "password"},
	}})})
	if err != nil || !reflect.DeepEqual(SecretFields(pw), []string{"key"}) {
		t.Errorf("format: password: SecretFields = %v, %v", SecretFields(pw), err)
	}

	for name, tc := range map[string]struct {
		tools []Tool
		want  string
	}{
		"unenforced keyword is named": {
			tools: []Tool{configureTool(map[string]any{"type": "object", "patternProperties": map[string]any{}})},
			want:  "patternProperties",
		},
		"config not required": {
			tools: []Tool{{Name: ConfigureTool, InputSchema: map[string]any{"type": "object", "properties": map[string]any{"config": map[string]any{}}}}},
			want:  "requiring config",
		},
		"config not an object": {
			tools: []Tool{configureTool(map[string]any{"type": "string"})},
			want:  "type object",
		},
		"nested secret": {
			tools: []Tool{configureTool(map[string]any{"type": "object", "properties": map[string]any{
				"auth": map[string]any{"type": "object", "properties": map[string]any{"key": map[string]any{"type": "string", "writeOnly": true}}},
			}})},
			want: "top-level",
		},
		"non-string secret": {
			tools: []Tool{configureTool(map[string]any{"type": "object", "properties": map[string]any{
				"key": map[string]any{"type": "integer", "writeOnly": true},
			}})},
			want: "must be a string",
		},
		"advertised twice": {
			tools: []Tool{configureTool(testConfigSchema), configureTool(testConfigSchema)},
			want:  "twice",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := DiscoverConfigure(tc.tools)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func TestStoredConfigKeepsSecretsOnlyAsReferences(t *testing.T) {
	schema, err := DiscoverConfigure([]Tool{configureTool(testConfigSchema)})
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckStoredConfig(schema, map[string]any{"project": "p1", "token": "$secret:tok"}); err != nil {
		t.Fatalf("a referenced secret: %v", err)
	}
	for name, tc := range map[string]struct {
		config map[string]any
		want   string
	}{
		"secret in clear":            {map[string]any{"project": "p1", "token": "hunter2-hunter2"}, "is a secret field"},
		"reference in a plain field": {map[string]any{"project": "$secret:tok", "token": "$secret:tok"}, "is not a secret field"},
		"missing secret field":       {map[string]any{"project": "p1"}, "token"},
		"pattern":                    {map[string]any{"project": "P!", "token": "$secret:tok"}, "config.project"},
		"unknown field":              {map[string]any{"project": "p1", "token": "$secret:tok", "zone": "a"}, "zone"},
	} {
		t.Run(name, func(t *testing.T) {
			err := CheckStoredConfig(schema, tc.config)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "hunter2") {
				t.Fatalf("error quotes the secret: %v", err)
			}
		})
	}

	// A reference must be a whole top-level value, which ServerConfig
	// checks without the schema.
	bad := ServerConfig{Command: "grain", Config: map[string]any{"nested": map[string]any{"k": "$secret:tok"}}}
	if err := bad.Validate("https://capabilities.example.com/x"); err == nil || !strings.Contains(err.Error(), "whole value") {
		t.Fatalf("nested reference: Validate = %v", err)
	}
	good := ServerConfig{Command: "grain", Config: map[string]any{"project": "p1", "token": "$secret:tok"}}
	if err := good.Validate("https://capabilities.example.com/x"); err != nil {
		t.Fatal(err)
	}
	if got, want := good.SecretRefs(), []string{"tok"}; !reflect.DeepEqual(got, want) {
		t.Errorf("SecretRefs = %v, want %v", got, want)
	}
}

func TestResolveConfig(t *testing.T) {
	schema, _ := DiscoverConfigure([]Tool{configureTool(testConfigSchema)})
	stored := map[string]any{"project": "p1", "token": "$secret:tok"}
	lookup := func(v string) SecretLookup {
		return func(name string) (string, bool) { return v, name == "tok" && v != "" }
	}
	got, err := ResolveConfig(schema, stored, lookup("s3cret-value"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"project": "p1", "region": "eu", "token": "s3cret-value"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("resolved = %v, want %v", got, want)
	}
	if stored["token"] != "$secret:tok" {
		t.Errorf("stored config was modified: %v", stored)
	}
	if _, err := ResolveConfig(schema, stored, lookup("")); err == nil || !strings.Contains(err.Error(), "missing or empty: tok") {
		t.Errorf("missing secret: err = %v", err)
	}
	// A secret failing its own schema is reported by path, never quoted.
	_, err = ResolveConfig(schema, stored, lookup("short"))
	if err == nil || strings.Contains(err.Error(), "short") || !strings.Contains(err.Error(), "config.token") {
		t.Errorf("short secret: err = %v", err)
	}
}

// loopback is a Caller that answers from a CapabilityServer in-process,
// recording the order of what it was asked.
type loopback struct {
	srv   *CapabilityServer
	calls []string
}

func (l *loopback) rpc(method string, params any) (json.RawMessage, error) {
	p, _ := json.Marshal(params)
	line, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": json.RawMessage(p)})
	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  *RPCError       `json:"error"`
	}
	if err := json.Unmarshal(l.srv.handle(context.Background(), line), &resp); err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	return resp.Result, nil
}

func (l *loopback) ListTools(context.Context) ([]Tool, error) {
	l.calls = append(l.calls, "tools/list")
	raw, err := l.rpc("tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var res struct{ Tools []Tool }
	err = json.Unmarshal(raw, &res)
	return res.Tools, err
}

func (l *loopback) CallTool(_ context.Context, name string, args map[string]any) ([]byte, error) {
	l.calls = append(l.calls, name)
	raw, err := l.rpc("tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return nil, err
	}
	var res struct {
		Content []struct{ Text string }
		IsError bool
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	if res.IsError {
		return nil, errors.New(res.Content[0].Text)
	}
	return []byte(res.Content[0].Text), nil
}

func (l *loopback) Close() error { return nil }

// configuredServer is a CapabilityServer that advertises method "key" only
// once configured, with an args schema naming the configured project, as
// a server whose methods depend on its configuration does.
func configuredServer(t *testing.T, applied *[]map[string]any, fail error) *CapabilityServer {
	t.Helper()
	srv, err := NewCapabilityServer("cfg")
	if err != nil {
		t.Fatal(err)
	}
	err = srv.HandleConfigure(testConfigSchema, func(_ context.Context, cfg map[string]any) error {
		if fail != nil {
			return fail
		}
		*applied = append(*applied, cfg)
		m, err := NewMethod(MethodSpec{
			Name: "key",
			Args: map[string]any{"type": "object", "properties": map[string]any{
				"project": map[string]any{"const": cfg["project"]},
			}},
			Mint: func(context.Context, string, time.Time, map[string]any) (*MintResponse, error) {
				return &MintResponse{Status: StatusOK, Placements: []Placement{{Path: "/home/k", Content: "x"}}}, nil
			},
		})
		if err != nil {
			return err
		}
		return srv.SetMethods(m)
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func TestConfigureIsCalledBeforeAnyMintAndTheToolsReListed(t *testing.T) {
	var applied []map[string]any
	lb := &loopback{srv: configuredServer(t, &applied, nil)}
	ctx := context.Background()
	s, err := NewServer(ctx, "capabilities.example.com/cfg", lb)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Methods) != 0 || s.ConfigSchema == nil {
		t.Fatalf("before configure: methods %v, schema %v", s.Methods, s.ConfigSchema)
	}
	secrets := func(name string) (string, bool) { return "tok-value-1", name == "tok" }
	if err := s.ApplyConfig(ctx, map[string]any{"project": "p1", "token": "$secret:tok"}, secrets); err != nil {
		t.Fatal(err)
	}
	if s.Methods["key"] == nil {
		t.Fatalf("configure's methods were not re-listed: %v", s.Methods)
	}
	if _, err := s.Mint(ctx, "key", "a-1", time.Now().Add(time.Hour), map[string]any{"project": "p1"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"tools/list", ConfigureTool, "tools/list", "mint_key"}
	if !reflect.DeepEqual(lb.calls, want) {
		t.Errorf("calls = %v, want %v", lb.calls, want)
	}
	if len(applied) != 1 || applied[0]["token"] != "tok-value-1" || applied[0]["region"] != "eu" {
		t.Errorf("server was configured with %v", applied)
	}

	// A config with no configure tool to take it is refused, and an
	// empty one is nothing to do.
	plain := &Server{ID: "capabilities.example.com/plain", caller: lb}
	if err := plain.ApplyConfig(ctx, nil, nil); err != nil {
		t.Errorf("empty config, no configure tool: %v", err)
	}
	var ce *ConfigureError
	if err := plain.ApplyConfig(ctx, map[string]any{"a": "b"}, nil); !errors.As(err, &ce) {
		t.Errorf("config, no configure tool: err = %v, want a ConfigureError", err)
	}
}

func TestAFailingConfigureIsAConfigureErrorCarryingTheSchema(t *testing.T) {
	var applied []map[string]any
	lb := &loopback{srv: configuredServer(t, &applied, fmt.Errorf("project p1 does not exist"))}
	ctx := context.Background()
	s, err := NewServer(ctx, "capabilities.example.com/cfg", lb)
	if err != nil {
		t.Fatal(err)
	}
	secrets := func(name string) (string, bool) { return "tok-value-1", true }
	err = s.ApplyConfig(ctx, map[string]any{"project": "p1", "token": "$secret:tok"}, secrets)
	var ce *ConfigureError
	if !errors.As(err, &ce) || ce.Schema == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("err = %v, want a ConfigureError with the server's message and schema", err)
	}
	// The server validates the config too, and refuses what Grain would.
	if _, err := lb.CallTool(ctx, ConfigureTool, map[string]any{"config": map[string]any{"project": "p1"}}); err == nil {
		t.Errorf("configure accepted a config missing a required field")
	}
}

// A stdio server's configured dataPlaneToken is what Grain's proxy sends
// it, in place of the one its environment names.
func TestAConfiguredDataPlaneTokenIsTheDataPlanesBearer(t *testing.T) {
	schema := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
		DataPlaneTokenConfig: map[string]any{"type": "string", "writeOnly": true},
	}}
	srv, err := NewCapabilityServer("cfg")
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.HandleConfigure(schema, func(context.Context, map[string]any) error { return nil }); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s, err := NewServer(ctx, "capabilities.example.com/cfg", &loopback{srv: srv})
	if err != nil {
		t.Fatal(err)
	}
	entry := ServerConfig{Command: "grain", Args: []string{"capability", "self-debug", "-listen", "127.0.0.1:9730"},
		Env: map[string]string{DataPlaneTokenEnv: "from-env"}}
	if dp, err := s.dataPlaneOf(entry); err != nil || dp.Headers["Authorization"] != "Bearer from-env" {
		t.Errorf("unconfigured: %+v, %v; want the environment's token", dp, err)
	}
	secrets := func(name string) (string, bool) { return "from-config", name == "dp" }
	if err := s.ApplyConfig(ctx, map[string]any{DataPlaneTokenConfig: "$secret:dp"}, secrets); err != nil {
		t.Fatal(err)
	}
	if dp, err := s.dataPlaneOf(entry); err != nil || dp.Headers["Authorization"] != "Bearer from-config" {
		t.Errorf("configured: %+v, %v; want the configured token", dp, err)
	}
	// An HTTP server's data plane is behind its own headers whatever its
	// configuration says.
	if dp, err := s.dataPlaneOf(ServerConfig{ServerURL: "https://caps.example.com/mcp", Headers: map[string]string{"Authorization": "Bearer h"}}); err != nil || dp.Headers["Authorization"] != "Bearer h" {
		t.Errorf("http server: %+v, %v; want its headers", dp, err)
	}
}
