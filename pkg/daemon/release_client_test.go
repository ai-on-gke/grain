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

package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
)

func TestDaemonReleaseHostClientCredentialSelection(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	store, closeStore := openTestStore(t, dataDir)
	defer closeStore()

	creds, err := GitHubCredentialLadder(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := creds.SetPAT("gh-pat", "ghp_testtoken123"); err != nil {
		t.Fatal(err)
	}
	if err := creds.SetPAT("ghe-pat", "ghp_enterprisetoken456"); err != nil {
		t.Fatal(err)
	}
	if err := creds.SetHost("ghe-pat", gitproxy.CredentialHost{
		Host: "ghe.internal.example",
		Kind: model.GitHostGitHub,
	}); err != nil {
		t.Fatal(err)
	}

	if err := store.UpdateConfig(ctx, func(mc *model.Config) error {
		mc.GitConnections = []model.TeamGitConnection{
			{Host: "github.com", Credential: "gh-pat"},
			{Host: "ghe.internal.example", Credential: "ghe-pat"},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	creds.SetConnections(storeGitConnections(ctx, store))

	resolver := daemonReleaseHostClient(Config{DataDir: dataDir}, store, creds)

	// 1. github.com uses the configured GitHub connection's token.
	ghClient, err := resolver(ctx, "github.com", "ai-on-gke", "grain")
	if err != nil {
		t.Fatalf("github.com resolver error: %v", err)
	}
	if ghClient.Connection != "gh-pat" || ghClient.Token != "ghp_testtoken123" {
		t.Fatalf("github.com client = %+v, want connection gh-pat with token", ghClient)
	}

	// 2. Enterprise host uses its own host connection's token without leaking github.com's.
	gheClient, err := resolver(ctx, "ghe.internal.example", "platform", "grain")
	if err != nil {
		t.Fatalf("ghe resolver error: %v", err)
	}
	if gheClient.Connection != "ghe-pat" || gheClient.Token != "ghp_enterprisetoken456" {
		t.Fatalf("ghe client = %+v, want connection ghe-pat with token", gheClient)
	}

	// 3. Unconfigured host produces a clear MissingHint naming Settings -> Connections.
	unconfClient, err := resolver(ctx, "other.forge.example", "team", "grain")
	if err != nil {
		t.Fatalf("unconfigured host resolver error: %v", err)
	}
	if unconfClient.Token != "" {
		t.Fatalf("unconfigured host leaked token %q", unconfClient.Token)
	}
	if !strings.Contains(unconfClient.MissingHint, "other.forge.example/team/grain") ||
		!strings.Contains(unconfClient.MissingHint, "Settings -> Connections") {
		t.Fatalf("unexpected MissingHint: %q", unconfClient.MissingHint)
	}
}

// A deployment started with -github-host HOST -github-insecure-http reads
// release assets from that host over plain HTTP; it used to build https://HOST/api/v3 and fail every listing with "server gave
// HTTP response to HTTPS client"
// (docs/test-runs/2026-11-07-releases-upgrades-ci.md, Bug F1).
func TestDaemonReleaseHostClientHonoursInsecureGitHubHost(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	store, closeStore := openTestStore(t, dataDir)
	defer closeStore()
	creds, err := GitHubCredentialLadder(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	resolver := daemonReleaseHostClient(Config{DataDir: dataDir, GithubHost: "127.0.0.1:4280", GithubInsecureHTTP: true}, store, creds)
	hc, err := resolver(ctx, "127.0.0.1:4280", "ai-on-gke", "grain")
	if err != nil {
		t.Fatal(err)
	}
	if hc.APIBase != "http://127.0.0.1:4280/api/v3" {
		t.Errorf("APIBase = %q, want http://127.0.0.1:4280/api/v3", hc.APIBase)
	}
	// A release repository on github.com stays on github.com, over TLS:
	// -github-host names where this deployment's own work lives, not where
	// grain's releases are published.
	for _, host := range []string{"", "github.com"} {
		hc, err := resolver(ctx, host, "ai-on-gke", "grain")
		if err != nil {
			t.Fatalf("resolver(%q): %v", host, err)
		}
		if hc.APIBase != "https://api.github.com" {
			t.Errorf("resolver(%q).APIBase = %q, want https://api.github.com", host, hc.APIBase)
		}
	}

	// Without -github-insecure-http the same host is still read over TLS.
	secure := daemonReleaseHostClient(Config{DataDir: dataDir, GithubHost: "127.0.0.1:4280"}, store, creds)
	hc, err = secure(ctx, "127.0.0.1:4280", "ai-on-gke", "grain")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hc.APIBase, "https://127.0.0.1:4280") {
		t.Errorf("secure APIBase = %q, want https://127.0.0.1:4280...", hc.APIBase)
	}
}
