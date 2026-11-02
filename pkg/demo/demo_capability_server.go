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

package demo

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/gcpsets/gcpfake"
	"github.com/ai-on-gke/grain/pkg/capability/intree"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/capability/servers"
	"github.com/ai-on-gke/grain/pkg/model"
)

// DemoCapabilityID is the demo's one capability server
// (design/spec/capabilities/capabilities.spec.md §3.1): a real protocol.CapabilityServer over
// Streamable HTTP on a loopback port, attached by a real servers.Registry,
// so the UI's grant forms, the task capabilities API and Settings →
// Capability servers all read a server the way a deployment's are read.
// What it mints is nothing; the demo never dispatches a run.
const DemoCapabilityID = "https://capabilities.demo.grain.example/cloud"

// DemoGCPCapabilityID is the demo's gcp server: grain's own in-tree gcp
// capability (admin accounts and permission sets), over gcpfake's
// in-memory IAM rather than GCP -- the one external boundary it stubs --
// so Settings can add admins and sets, Test them, and grant a set.
const DemoGCPCapabilityID = "https://capabilities.demo.grain.example/gcp"

// The demo's gchat server is grain's own built-in, registered under its
// canonical capability_id with nothing configured, so Settings shows it
// as a deployment's would: configure a webhook space, Test it, send a
// test message, grant gchat#post. Its HTTP client never leaves the
// machine: fakeChat answers every webhook and Chat API post itself.

// fakeChat is the Google Chat the demo's gchat server posts to: it takes
// any POST to /v1/spaces/<id>/messages -- a webhook URL on
// chat.googleapis.com, which is the only host a webhook may name -- and
// answers with the created message. Anything else is a 404. Nothing is
// sent anywhere.
type fakeChat struct {
	mu sync.Mutex
	n  int
}

var fakeChatPath = regexp.MustCompile(`^/v1/(spaces/[A-Za-z0-9_-]+)/messages$`)

func (f *fakeChat) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
		req.Body.Close()
	}
	m := fakeChatPath.FindStringSubmatch(req.URL.Path)
	status, body := http.StatusNotFound, `{"error":{"code":404,"message":"grain demo's Chat answers only message posts"}}`
	if req.Method == http.MethodPost && m != nil {
		f.mu.Lock()
		f.n++
		n := f.n
		f.mu.Unlock()
		status = http.StatusOK
		body = fmt.Sprintf(`{"name":"%s/messages/demo-%d","thread":{"name":"%s/threads/demo-%d"}}`, m[1], n, m[1], n)
	}
	return &http.Response{
		StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header: http.Header{"Content-Type": []string{"application/json"}},
		Body:   io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body)),
		Request: req, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
	}, nil
}

// demoGCPConfig is the demo gcp server's starting configuration: one
// permission set using the default ambient admin.
func demoGCPConfig() map[string]any {
	return map[string]any{
		"permission_sets": []any{map[string]any{
			"name": "sandbox-agent", "project": "demo-project", "roles": []any{"roles/logging.viewer"},
		}},
	}
}

// serveDemoCapability serves h on a loopback port until ctx ends and
// returns the entry that reaches it.
func serveDemoCapability(ctx context.Context, srv *protocol.CapabilityServer) (protocol.ServerConfig, error) {
	token := make([]byte, protocol.MinHTTPTokenBytes)
	if _, err := rand.Read(token); err != nil {
		return protocol.ServerConfig{}, err
	}
	srv.Token = hex.EncodeToString(token)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return protocol.ServerConfig{}, fmt.Errorf("listening for a demo capability server: %v", err)
	}
	go func() { _ = http.Serve(ln, srv) }()
	go func() { <-ctx.Done(); ln.Close() }()
	return protocol.ServerConfig{
		ServerURL: "http://" + ln.Addr().String() + "/mcp",
		Headers:   map[string]string{"Authorization": "Bearer " + srv.Token},
	}, nil
}

// demoCapabilityMethods are its methods: cloud-key, whose arguments use
// every kind of field the grant form renders -- a required enum, a
// pattern-checked string, an integer with bounds, a boolean, an array of
// unique strings, a nested object through $defs, and a oneOf -- and
// cloud-viewer, which takes none.
func demoCapabilityMethods() ([]*protocol.Method, error) {
	mint := func(context.Context, string, time.Time, map[string]any) (*protocol.MintResponse, error) {
		return &protocol.MintResponse{Status: protocol.StatusOK}, nil
	}
	key, err := protocol.NewMethod(protocol.MethodSpec{
		Name:        "cloud-key",
		Description: "Mint a short-lived cloud service-account key for one account and place it in the sandbox.",
		Args: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []any{"account", "project"},
			"properties": map[string]any{
				"account": map[string]any{
					"description": "Which account the key is for.",
					"enum":        []any{"staging", "prod", "dev"},
				},
				"project": map[string]any{
					"type":        "string",
					"description": "The project id: lowercase letters, digits and hyphens.",
					"pattern":     "^[a-z][a-z0-9-]{4,28}[a-z0-9]$",
				},
				"ttl_minutes": map[string]any{
					"type":        "integer",
					"description": "How long the key lives.",
					"minimum":     5,
					"maximum":     720,
					"default":     60,
				},
				"read_only": map[string]any{
					"type":        "boolean",
					"description": "Grant only viewer roles.",
					"default":     true,
				},
				"scopes": map[string]any{
					"type":        "array",
					"description": "OAuth scopes the key is limited to.",
					"items":       map[string]any{"type": "string", "minLength": 3},
					"minItems":    1,
					"maxItems":    4,
					"uniqueItems": true,
					"default":     []any{"cloud-platform"},
				},
				"labels": map[string]any{"$ref": "#/$defs/labels"},
			},
			"$defs": map[string]any{
				"labels": map[string]any{
					"type":                 "object",
					"description":          "Labels put on the key.",
					"additionalProperties": false,
					"properties": map[string]any{
						"team": map[string]any{"type": "string", "maxLength": 20},
					},
				},
			},
		},
		Mint: mint,
		Release: func(context.Context, string) error {
			return nil
		},
	})
	if err != nil {
		return nil, err
	}
	viewer, err := protocol.NewMethod(protocol.MethodSpec{
		Name:        "cloud-viewer",
		Description: "Read-only console access for the sandbox; takes no arguments.",
		Mint:        mint,
	})
	if err != nil {
		return nil, err
	}
	return []*protocol.Method{key, viewer}, nil
}

// demoCapabilityConfigSchema is the demo server's configuration
// (design/spec/capabilities/capabilities.spec.md §4.5): a pattern-checked project, an enum
// region, and apiToken, a secret field Settings shows write-only. None
// is required, so the server attaches before anyone has configured it.
var demoCapabilityConfigSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"properties": map[string]any{
		"project": map[string]any{
			"type":        "string",
			"description": "The project keys are minted in.",
			"pattern":     "^[a-z][a-z0-9-]{4,28}[a-z0-9]$",
		},
		"region": map[string]any{
			"description": "Where the minting service runs.",
			"enum":        []any{"eu", "us"},
		},
		"apiToken": map[string]any{
			"type":        "string",
			"description": "The minting service's own credential.",
			"writeOnly":   true,
		},
	},
}

// configureCapabilityServerDemo starts the demo's capability server on a
// loopback port, records it in stored's mcpServers, and returns the
// registry that attaches it, already running until ctx ends. lookup
// resolves the $secret: references a saved configuration holds.
func configureCapabilityServerDemo(ctx context.Context, stored *model.Config, lookup protocol.SecretLookup, identity servers.IdentitySource) (*servers.Registry, error) {
	methods, err := demoCapabilityMethods()
	if err != nil {
		return nil, err
	}
	srv, err := protocol.NewCapabilityServer("grain-demo-cloud", methods...)
	if err != nil {
		return nil, err
	}
	// Configuration is accepted as given: the demo mints nothing, so a
	// project or token is only ever checked against the schema.
	if err := srv.HandleConfigure(demoCapabilityConfigSchema, func(context.Context, map[string]any) error { return nil }); err != nil {
		return nil, err
	}
	entry, err := serveDemoCapability(ctx, srv)
	if err != nil {
		return nil, err
	}
	if stored.MCPServers == nil {
		stored.MCPServers = map[string]protocol.ServerConfig{}
	}
	stored.MCPServers[DemoCapabilityID] = entry

	cloud := gcpfake.New()
	cloud.DefaultIdentity = "you@demo.grain.example"
	gcp, err := intree.NewConfigurableGCP(intree.GCPConfig{Connector: cloud})
	if err != nil {
		return nil, err
	}
	gcpEntry, err := serveDemoCapability(ctx, gcp.CapabilityServer)
	if err != nil {
		return nil, err
	}
	gcpEntry.Config = demoGCPConfig()
	stored.MCPServers[DemoGCPCapabilityID] = gcpEntry

	// The data-plane token is the server's own, so configuring a space
	// is all Settings has to do; its listener is on a port of its own.
	token := make([]byte, intree.MinTokenBytes)
	if _, err := rand.Read(token); err != nil {
		return nil, err
	}
	gchat, err := intree.NewGChat(intree.GChatServerConfig{
		Listen: "127.0.0.1:0",
		Token:  hex.EncodeToString(token),
		Client: &http.Client{Transport: &fakeChat{}, Timeout: 10 * time.Second},
	})
	if err != nil {
		return nil, err
	}
	gchatEntry, err := serveDemoCapability(ctx, gchat.CapabilityServer)
	if err != nil {
		return nil, err
	}
	gchatEntry.Builtin = "gchat"
	stored.MCPServers[intree.CanonicalID("gchat")] = gchatEntry
	opts := servers.Options{Secrets: lookup}
	if identity != nil {
		opts.Identity = servers.StoreIdentity(identity, nil)
	}
	reg := servers.New(opts)
	reg.Update(stored.MCPServers)
	reg.Sync(ctx, stored.MCPServers)
	go reg.Run(ctx, 0)
	return reg, nil
}
