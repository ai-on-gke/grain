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

package intree

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

var figmaTestDP = strings.Repeat("f", MinTokenBytes)

func TestParseFigmaURL(t *testing.T) {
	cases := []struct {
		url      string
		wantKey  string
		wantNode string
		wantErr  bool
	}{
		{
			url:      "https://www.figma.com/design/AbCdEf123456/Checkout-Flow?node-id=12-34&t=xyz",
			wantKey:  "AbCdEf123456",
			wantNode: "12:34",
		},
		{
			url:      "https://www.figma.com/design/RootKey99/branch/BranchKey77/Checkout-Flow?node-id=4-8",
			wantKey:  "BranchKey77",
			wantNode: "4:8",
		},
		{
			url:     "https://figma.com/file/FileKey01/Design-System",
			wantKey: "FileKey01",
		},
		{
			url:     "https://evil.example.com/design/AbCdEf123456/Fake",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		got, err := ParseFigmaURL(tc.url)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("ParseFigmaURL(%q) succeeded (%+v), want error", tc.url, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("ParseFigmaURL(%q): %v", tc.url, err)
		}
		if got.FileKey != tc.wantKey || got.NodeID != tc.wantNode {
			t.Fatalf("ParseFigmaURL(%q) = %+v, want key=%q node=%q", tc.url, got, tc.wantKey, tc.wantNode)
		}
	}
}

func TestFigmaOAuthAuthorizeAndCodeExchange(t *testing.T) {
	ctx := context.Background()
	var gotAuthHeader, gotCode, gotGrantType, gotRedirect string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/oauth/token" {
			http.NotFound(w, r)
			return
		}
		gotAuthHeader = r.Header.Get("Authorization")
		_ = r.ParseForm()
		gotCode = r.FormValue("code")
		gotGrantType = r.FormValue("grant_type")
		gotRedirect = r.FormValue("redirect_uri")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(FigmaOAuthTokenResponse{
			AccessToken:  "figu_short_lived_1",
			RefreshToken: "figr_refresh_1",
			ExpiresIn:    3600,
			UserIDString: "user-42",
		})
	}))
	defer fake.Close()

	authURL, err := BuildFigmaOAuthAuthorizeURL("", "client-123", "https://grain.example.com/api/capability-servers/figma-oauth/callback", "", "state-abc")
	if err != nil {
		t.Fatalf("BuildFigmaOAuthAuthorizeURL: %v", err)
	}
	if !strings.HasPrefix(authURL, "https://www.figma.com/oauth?") || !strings.Contains(authURL, "client_id=client-123") || !strings.Contains(authURL, "state=state-abc") {
		t.Fatalf("unexpected authorize URL: %s", authURL)
	}

	tok, err := ExchangeFigmaOAuthCode(ctx, fake.Client(), fake.URL, "client-123", "secret-xyz", "code-999", "https://grain.example.com/cb")
	if err != nil {
		t.Fatalf("ExchangeFigmaOAuthCode: %v", err)
	}
	wantBasic := "Basic " + base64.StdEncoding.EncodeToString([]byte("client-123:secret-xyz"))
	if gotAuthHeader != wantBasic || gotCode != "code-999" || gotGrantType != "authorization_code" || gotRedirect != "https://grain.example.com/cb" {
		t.Fatalf("unexpected OAuth exchange request: auth=%q code=%q grant=%q redirect=%q", gotAuthHeader, gotCode, gotGrantType, gotRedirect)
	}
	if tok.AccessToken != "figu_short_lived_1" || tok.RefreshToken != "figr_refresh_1" || tok.ExpiresIn != 3600 {
		t.Fatalf("unexpected token response: %+v", tok)
	}
}

func TestFigmaCapabilityServerMCPAndTokenGeneration(t *testing.T) {
	ctx := context.Background()
	var refreshCalls atomic.Int32
	var imageSawAuth atomic.Bool

	pngBytes := []byte("\x89PNG\r\n\x1a\nmock-png-payload")
	var fakeURL string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/oauth/refresh":
			n := refreshCalls.Add(1)
			_ = r.ParseForm()
			if r.FormValue("refresh_token") != "figr_valid" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"bad refresh token"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(FigmaOAuthTokenResponse{
				AccessToken: "figu_minted_" + string(rune('0'+n)),
				ExpiresIn:   1800,
			})
		case "/v1/me":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer [redacted: bearer token]") && r.Header.Get("X-Figma-Token") != "figd_pat_1" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_, _ = w.Write([]byte(`{"id":"u1","handle":"Ada Designer","email":"ada@example.com"}`))
		case "/v1/files/AllowedKey1":
			_, _ = w.Write([]byte(`{
				"name": "Acme App UI",
				"lastModified": "2026-11-05T12:00:00Z",
				"version": "99",
				"editorType": "figma",
				"document": {
					"id": "0:0",
					"type": "DOCUMENT",
					"children": [{"id": "0:1", "name": "Checkout", "type": "CANVAS", "children": [{"id": "12:34", "name": "Pay Card", "type": "FRAME"}]}]
				},
				"components": {"12:99": {"name": "PrimaryButton", "description": "CTA button"}},
				"styles": {"S:1": {"name": "Brand/Primary", "styleType": "FILL"}}
			}`))
		case "/v1/files/AllowedKey1/nodes":
			_, _ = w.Write([]byte(`{
				"name": "Acme App UI",
				"lastModified": "2026-11-05T12:00:00Z",
				"nodes": {
					"12:34": {
						"document": {
							"id": "12:34",
							"name": "Pay Card",
							"type": "FRAME",
							"absoluteBoundingBox": {"x": 0, "y": 0, "width": 360, "height": 240},
							"layoutMode": "VERTICAL",
							"itemSpacing": 16,
							"paddingTop": 24, "paddingRight": 24, "paddingBottom": 24, "paddingLeft": 24,
							"cornerRadius": 12,
							"fills": [{"type": "SOLID", "color": {"r": 1, "g": 1, "b": 1, "a": 1}}],
							"effects": [{"type": "DROP_SHADOW", "offset": {"x": 0, "y": 4}, "radius": 12, "color": {"r": 0, "g": 0, "b": 0, "a": 0.15}}],
							"children": [
								{
									"id": "12:35",
									"name": "Heading",
									"type": "TEXT",
									"characters": "Complete Purchase",
									"style": {"fontFamily": "Inter", "fontSize": 20, "fontWeight": 600, "lineHeightPx": 28}
								}
							]
						}
					}
				}
			}`))
		case "/v1/images/AllowedKey1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"images": map[string]string{
					"12:34": fakeURL + "/cdn/frame-12-34.png",
				},
			})
		case "/cdn/frame-12-34.png":
			if r.Header.Get("Authorization") != "" || r.Header.Get("X-Figma-Token") != "" {
				imageSawAuth.Store(true)
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBytes)
		case "/v1/files/AllowedKey1/styles":
			_, _ = w.Write([]byte(`{"meta":{"styles":[{"node_id":"1:2","name":"Surface","style_type":"FILL"}]}}`))
		case "/v1/files/AllowedKey1/variables/local":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"Invalid scope"}`))
		case "/v1/files/AllowedKey1/comments":
			_, _ = w.Write([]byte(`{"comments":[{"id":"c1","message":"Keep 16px vertical gap","created_at":"2026-11-05T10:00:00Z","user":{"handle":"Ada"},"client_meta":{"node_id":"12:34"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer fake.Close()
	fakeURL = fake.URL

	srv, err := NewFigma(FigmaServerConfig{
		Listen:              "127.0.0.1:0",
		Token:               figmaTestDP,
		APIBase:             fake.URL,
		Client:              fake.Client(),
		AllowLoopbackImages: true,
		Now:                 func() time.Time { return time.Date(2026, 11, 5, 19, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("NewFigma: %v", err)
	}
	defer srv.Close(ctx)

	// Unconfigured server offers no methods.
	unconfSrv, err := NewFigma(FigmaServerConfig{Listen: "127.0.0.1:0", Token: figmaTestDP})
	if err != nil {
		t.Fatalf("NewFigma unconfigured: %v", err)
	}
	unconfPS, err := attachConfigured(t, unconfSrv, map[string]any{}, nil)
	if err != nil {
		t.Fatalf("attachConfigured empty: %v", err)
	}
	if len(unconfPS.Methods) != 0 {
		t.Fatalf("unconfigured methods = %d, want 0", len(unconfPS.Methods))
	}

	// Refuse allow_sandbox_token on PAT accounts.
	badPAT := map[string]any{
		protocol.DataPlaneTokenConfig: figmaTestDP,
		"accounts": []any{
			map[string]any{"name": "pat-acct", "auth_mode": FigmaAuthPAT, "allow_sandbox_token": true},
		},
	}
	if err := CheckBuiltinConfig(FigmaName, badPAT); err == nil {
		t.Fatal("CheckBuiltinConfig accepted allow_sandbox_token on PAT account")
	}

	// Configure OAuth account with allow_sandbox_token: true and allowed_file_keys.
	cfg := map[string]any{
		protocol.DataPlaneTokenConfig: "$secret:dp",
		"per_attempt":                 float64(4),
		"accounts": []any{
			map[string]any{
				"name":                "design",
				"auth_mode":           FigmaAuthOAuth,
				"client_id":           "cid-1",
				"allowed_file_keys":   []any{"AllowedKey1"},
				"allow_sandbox_token": true,
			},
		},
		"client_secrets": "$secret:csec",
		"refresh_tokens": "$secret:rtok",
	}
	ps, err := attachConfigured(t, srv, cfg, map[string]string{
		"dp":   figmaTestDP,
		"csec": `{"design":"csec-1"}`,
		"rtok": `{"design":"figr_valid"}`,
	})
	if err != nil {
		t.Fatalf("attachConfigured: %v", err)
	}
	if len(ps.Methods) != 2 {
		t.Fatalf("configured methods = %d, want 2 (mcp + token)", len(ps.Methods))
	}

	// Mint figma#token -> fresh short-lived OAuth token placed at /home/debian/.figma-token.
	tokResp := mint(t, ps, FigmaTokenMethod, "e1-10-1-0", map[string]any{})
	if tokResp.Status != protocol.StatusOK {
		t.Fatalf("tokMethod.Mint: resp=%+v", tokResp)
	}
	placed := placement(t, tokResp, FigmaTokenPath)
	if !strings.HasPrefix(placed.Content, "figu_minted_") {
		t.Fatalf("unexpected token placement: %+v", placed)
	}

	// Mint figma#mcp -> exercise MCP tools over the data plane.
	attemptID := "e1-11-1-0"
	mcpResp := mint(t, ps, FigmaMCPMethod, attemptID, map[string]any{})
	if mcpResp.Status != protocol.StatusOK {
		t.Fatalf("mcpMethod.Mint: resp=%+v", mcpResp)
	}
	dpURL := "http://" + srv.addr + mcpResp.MCPEndpoint

	callMCP := func(tool string, args map[string]any) (string, bool) {
		t.Helper()
		payload, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "tools/call",
			"params":  map[string]any{"name": tool, "arguments": args},
		})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, dpURL, bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+figmaTestDP)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var rpc struct {
			Result struct {
				IsError bool `json:"isError"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
		}
		if err := json.Unmarshal(raw, &rpc); err != nil {
			t.Fatalf("unmarshal MCP response %s: %v", string(raw), err)
		}
		var text string
		if len(rpc.Result.Content) > 0 {
			text = rpc.Result.Content[0].Text
		}
		return text, rpc.Result.IsError
	}

	// 1. get_design_context via full Figma URL.
	specJSON, isErr := callMCP("get_design_context", map[string]any{
		"url": "https://www.figma.com/design/AllowedKey1/Acme-App?node-id=12-34",
	})
	if isErr {
		t.Fatalf("get_design_context error: %s", specJSON)
	}
	if !strings.Contains(specJSON, `"Complete Purchase"`) || !strings.Contains(specJSON, `"#ffffff"`) || !strings.Contains(specJSON, `"vertical"`) {
		t.Fatalf("get_design_context missing expected design tokens: %s", specJSON)
	}

	// 2. export_images downloads PNG bytes without leaking auth headers.
	imgJSON, isErr := callMCP("export_images", map[string]any{
		"file_key": "AllowedKey1",
		"node_id":  "12-34",
	})
	if isErr {
		t.Fatalf("export_images error: %s", imgJSON)
	}
	if imageSawAuth.Load() {
		t.Fatal("export_images leaked Figma auth header to CDN download URL")
	}
	if !strings.Contains(imgJSON, base64.StdEncoding.EncodeToString(pngBytes)) {
		t.Fatalf("export_images did not return base64 PNG bytes: %s", imgJSON)
	}

	// 3. Disallowed file_key is refused by the grant's allowlist.
	refusedText, isErr := callMCP("get_file", map[string]any{"file_key": "OtherSecretFile"})
	if !isErr || !strings.Contains(refusedText, "not in this grant's allowed file keys") {
		t.Fatalf("expected file_key allowlist refusal, got isErr=%v text=%q", isErr, refusedText)
	}

	// 4. get_comments & get_styles_and_variables.
	commentsJSON, isErr := callMCP("get_comments", map[string]any{"file_key": "AllowedKey1"})
	if isErr || !strings.Contains(commentsJSON, "Keep 16px vertical gap") {
		t.Fatalf("get_comments unexpected result: isErr=%v text=%s", isErr, commentsJSON)
	}

	// 5. Release closes the endpoint and returns the audit trail.
	rel, err := ps.Release(ctx, FigmaMCPMethod, attemptID)
	if err != nil {
		t.Fatalf("ps.Release: %v", err)
	}
	if len(rel.Audit) < 3 {
		t.Fatalf("expected >= 3 audit records on release, got %d", len(rel.Audit))
	}
}

func TestFigmaExportImageBlocksSSRFHosts(t *testing.T) {
	srv, err := NewFigma(FigmaServerConfig{Listen: "127.0.0.1:0", Token: figmaTestDP})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close(context.Background())

	f := &figmaServer{cfg: FigmaServerConfig{ImageHostSuffixes: defaultFigmaImageHostSuffixes}}
	badURLs := []string{
		"http://figma-alpha-api.s3.us-west-2.amazonaws.com/img.png", // non-HTTPS
		"https://169.254.169.254/latest/meta-data/",                 // IP literal
		"https://127.0.0.1:8080/internal",                           // loopback without test flag
		"https://internal.example.org/secret.png",                   // non-allowlisted host
	}
	for _, u := range badURLs {
		if _, err := f.isAllowedImageURL(u); err == nil {
			t.Errorf("isAllowedImageURL(%q) unexpectedly succeeded", u)
		}
	}
	if _, err := f.isAllowedImageURL("https://figma-alpha-api.s3.us-west-2.amazonaws.com/images/abc.png"); err != nil {
		t.Errorf("valid S3 CDN URL rejected: %v", err)
	}
}
