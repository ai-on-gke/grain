package intree

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	capmodelproxy "github.com/bwsalmon/grain/pkg/capability/modelproxy"
	"github.com/bwsalmon/grain/pkg/capability/protocol"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/modelproxy"
)

// DefaultModelProxyPort is the model-proxy server's first data-plane port
// (§6.3); each further vendor takes the next one unless -listen says
// otherwise.
const DefaultModelProxyPort = 9729

// ModelProxyKeyEnv is the environment variable the model-proxy server
// reads vendor's credential from, e.g. MODEL_PROXY_CLAUDE_KEY.
func ModelProxyKeyEnv(vendor string) string {
	return "MODEL_PROXY_" + strings.ToUpper(vendor) + "_KEY"
}

// ModelProxyVendor is one vendor the model-proxy server forwards to.
type ModelProxyVendor struct {
	Vendor modelproxy.Vendor
	// Listen is the loopback address of this vendor's data-plane
	// listener.
	Listen string
	// Credential is the key requests are forwarded with.
	Credential string
	// Upstream overrides the vendor's own origin, for tests.
	Upstream  string
	Transport http.RoundTripper
}

// ModelProxyConfig is the model-proxy server's configuration.
type ModelProxyConfig struct {
	Vendors []ModelProxyVendor
	Now     func() time.Time
	Log     *log.Logger
}

// NewModelProxy builds the model-proxy server: method model-proxy (§6.3),
// a per-attempt token on pkg/modelproxy's proxy for the vendor with:
// {vendor} names, placed exactly as the in-process <vendor>-proxy
// capability places it. Every vendor in cfg must have its credential.
func NewModelProxy(cfg ModelProxyConfig) (*Server, error) {
	if len(cfg.Vendors) == 0 {
		return nil, fmt.Errorf("model-proxy: at least one -vendor is required")
	}
	for _, v := range cfg.Vendors {
		if v.Credential == "" {
			return nil, modelProxyKeyMissing(v.Vendor)
		}
	}
	return newConfigurableModelProxy(cfg)
}

func modelProxyKeyMissing(v modelproxy.Vendor) error {
	return fmt.Errorf("model-proxy: %s is not set; bind it to the %s credential's secret in grain/config", ModelProxyKeyEnv(v.Name), v.Display)
}

// ModelProxyKeyConfig is the configure field holding vendor's credential,
// e.g. claudeKey.
func ModelProxyKeyConfig(vendor string) string { return vendor + "Key" }

// modelProxyConfigSchema is the model-proxy server's configure schema
// (§4.5): a secret field per vendor it proxies, each overriding that
// vendor's MODEL_PROXY_<VENDOR>_KEY, which stays the fallback for a
// server configured before configure existed. The vendors themselves
// stay flags: each binds its listener when the server starts.
func modelProxyConfigSchema(vendors []ModelProxyVendor) map[string]any {
	props := map[string]any{}
	for _, v := range vendors {
		props[ModelProxyKeyConfig(v.Vendor.Name)] = map[string]any{
			"type":        "string",
			"title":       v.Vendor.Display + " key",
			"description": "The credential requests to " + v.Vendor.Display + " are forwarded with.",
			"writeOnly":   true,
		}
	}
	return map[string]any{"type": "object", "additionalProperties": false, "properties": props}
}

// modelProxyServed is one vendor the server proxies: its provider, its
// listener's port, and the credential the configuration last applied
// gave it.
type modelProxyServed struct {
	vendor   modelproxy.Vendor
	provider *capmodelproxy.Provider
	port     int

	mu         sync.Mutex
	credential string
}

func (sv *modelProxyServed) setCredential(c string) {
	sv.mu.Lock()
	defer sv.mu.Unlock()
	sv.credential = c
}

func (sv *modelProxyServed) key(context.Context, modelproxy.Connection) (string, error) {
	sv.mu.Lock()
	defer sv.mu.Unlock()
	if sv.credential == "" {
		return "", fmt.Errorf("model-proxy: %s has no credential configured", sv.vendor.Display)
	}
	return sv.credential, nil
}

// newConfigurableModelProxy is the model-proxy server `grain capability
// model-proxy` serves: a listener per -vendor from the start, and
// model-proxy configured by the environment when every vendor's key is
// there, and by configure (modelProxyConfigSchema) over it either way.
// Until one of the two configures it, it advertises configure and no
// method, and its attach fails naming the key that is missing.
func newConfigurableModelProxy(cfg ModelProxyConfig) (*Server, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	s, err := newServer("model-proxy", cfg.Log)
	if err != nil {
		return nil, err
	}
	vendors := map[string]*modelProxyServed{}
	var names []string
	for _, v := range cfg.Vendors {
		name := v.Vendor.Name
		if _, dup := vendors[name]; dup {
			s.Close(context.Background())
			return nil, fmt.Errorf("model-proxy: vendor %s is configured twice", name)
		}
		endpoint := modelproxy.NewEndpoint()
		connection := modelproxy.Connection{ID: name}
		endpoint.SetConnections(func(context.Context, string) (modelproxy.Connection, error) { return connection, nil })
		sv := &modelProxyServed{vendor: v.Vendor, provider: capmodelproxy.New(v.Vendor, endpoint)}
		endpoint.SetCredential(sv.key)
		vendors[name] = sv
		names = append(names, name)
		proxy := &modelproxy.Proxy{Vendor: v.Vendor, Upstream: v.Upstream, Transport: v.Transport, Endpoint: endpoint}
		addr, err := s.listen(v.Listen, proxy)
		if err != nil {
			s.Close(context.Background())
			return nil, fmt.Errorf("model-proxy: %s: %w", name, err)
		}
		sv.port = addr.Port
		endpoint.SetURL("http://127.0.0.1:" + strconv.Itoa(addr.Port))
		s.Log.Printf("model-proxy: proxying %s on %s", v.Vendor.Display, addr)
	}
	fallback := map[string]string{}
	for _, v := range cfg.Vendors {
		fallback[v.Vendor.Name] = v.Credential
	}
	build := func(keys map[string]string) (*protocol.Method, error) {
		if len(names) == 0 {
			return nil, fmt.Errorf("model-proxy: at least one -vendor is required: pass -vendor (claude, openai, gemini) in Settings → Capabilities")
		}
		for _, name := range names {
			if keys[name] == "" {
				return nil, modelProxyKeyMissing(vendors[name].vendor)
			}
		}
		m, err := newModelProxyMethod(cfg.Now, names, vendors)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			vendors[name].setCredential(keys[name])
		}
		return m, nil
	}
	if m, err := build(fallback); err == nil {
		if err := s.SetMethods(m); err != nil {
			s.Close(context.Background())
			return nil, err
		}
		s.Methods = []*protocol.Method{m}
	}
	err = s.configurable(modelProxyConfigSchema(cfg.Vendors), func(_ context.Context, config map[string]any) ([]*protocol.Method, error) {
		keys := map[string]string{}
		for _, name := range names {
			keys[name] = fallback[name]
			if v, ok := config[ModelProxyKeyConfig(name)].(string); ok {
				keys[name] = v
			}
		}
		m, err := build(keys)
		if err != nil {
			return nil, err
		}
		return []*protocol.Method{m}, nil
	})
	if err != nil {
		s.Close(context.Background())
		return nil, err
	}
	return s, nil
}

// newModelProxyMethod is the model-proxy method minting tokens on
// vendors' providers.
func newModelProxyMethod(now func() time.Time, names []string, vendors map[string]*modelProxyServed) (*protocol.Method, error) {
	enum := make([]any, 0, len(names))
	for _, n := range names {
		enum = append(enum, n)
	}
	var mu sync.Mutex
	leases := map[string]struct {
		vendor string
		lease  model.Lease
	}{}
	return protocol.NewMethod(protocol.MethodSpec{
		Name:        "model-proxy",
		Description: "A per-attempt token for this server's proxy to one model vendor's inference API, revoked at release or at the grant's deadline.",
		Args: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []any{"vendor"},
			"properties": map[string]any{
				"vendor": map[string]any{"type": "string", "enum": enum},
			},
		},
		Now: now,
		Mint: func(ctx context.Context, attemptID string, deadline time.Time, args map[string]any) (*protocol.MintResponse, error) {
			name, _ := args["vendor"].(string)
			sv := vendors[name]
			if sv == nil {
				return refusedResponse("unknown vendor " + name), nil
			}
			cc := model.CapabilityContext{
				Run:      model.Run{ID: attemptID},
				Now:      now(),
				Deadline: deadline.Add(protocol.DefaultGrace),
			}
			mat, err := sv.provider.Materialize(ctx, cc)
			if err != nil {
				return nil, err
			}
			prompt, err := sv.provider.PromptSection(ctx, cc, mat.Placements)
			if err != nil {
				sv.provider.Revoke(ctx, cc, *mat.Lease)
				return nil, err
			}
			resp := &protocol.MintResponse{
				Status:        protocol.StatusOK,
				Ports:         []protocol.Port{{HostPort: sv.port}},
				PromptSection: prompt,
			}
			for _, pl := range mat.Placements {
				resp.Placements = append(resp.Placements, protocol.Placement{Path: pl.Path, Mode: "0600", Content: pl.Content})
			}
			mu.Lock()
			leases[attemptID] = struct {
				vendor string
				lease  model.Lease
			}{name, *mat.Lease}
			mu.Unlock()
			return resp, nil
		},
		Release: func(ctx context.Context, attemptID string) error {
			mu.Lock()
			l, ok := leases[attemptID]
			delete(leases, attemptID)
			mu.Unlock()
			if ok {
				return vendors[l.vendor].provider.Revoke(ctx, model.CapabilityContext{Now: now()}, l.lease)
			}
			return nil
		},
	})
}

func vendorByName(name string) (modelproxy.Vendor, bool) {
	for _, v := range modelproxy.Vendors() {
		if v.Name == name {
			return v, true
		}
	}
	return modelproxy.Vendor{}, false
}

func init() {
	register(Builder{
		Name:    "model-proxy",
		Summary: "per-attempt tokens on a model vendor's inference proxy (mint_model-proxy)",
		Build: func(ctx context.Context, args []string, env Env, logger *log.Logger) (*Server, error) {
			fs := newFlags("model-proxy", logOutput(logger))
			var vendors listFlag
			fs.Var(&vendors, "vendor", "a vendor to proxy as name[=loopback-address], repeatable (claude, openai, gemini)")
			if err := fs.Parse(args); err != nil {
				return nil, err
			}
			cfg := ModelProxyConfig{Log: logger}
			for i, v := range vendors {
				name, listen, ok := strings.Cut(v, "=")
				if !ok {
					listen = "127.0.0.1:" + strconv.Itoa(DefaultModelProxyPort+i)
				}
				vendor, known := vendorByName(name)
				if !known {
					return nil, fmt.Errorf("model-proxy: unknown vendor %q", name)
				}
				cfg.Vendors = append(cfg.Vendors, ModelProxyVendor{Vendor: vendor, Listen: listenAddr(env, listen), Credential: env(ModelProxyKeyEnv(name))})
			}
			return newConfigurableModelProxy(cfg)
		},
	})
}
