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
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/upgrade"
)

// releaseHostAPIBase is the REST API root release assets are read from on
// credHost: its own API host and path prefix (api.github.com, or
// HOST/api/v3 on an enterprise host), over plain HTTP when the deployment
// was started with -github-insecure-http and credHost is its -github-host,
// or when the connection itself is recorded without TLS. Before this the
// upgrade client always built https://HOST/api/v3, so a -github-insecure-http
// forge answered every listing with "server gave HTTP response to HTTPS
// client" (docs/test-runs/2026-11-07-releases-upgrades-ci.md, Bug F1).
func releaseHostAPIBase(credHost gitproxy.CredentialHost, defHost string, insecureHTTP bool) (string, error) {
	apiHost, prefix, ok := credHost.APIBase()
	if !ok {
		return "", fmt.Errorf("is on %s, a plain git host with no forge API", credHost.Host)
	}
	scheme := "https"
	if (insecureHTTP && model.SameGitHost(credHost.Host, defHost, model.DefaultGitHost)) || !credHost.UseTLS() {
		scheme = "http"
	}
	return scheme + "://" + apiHost + prefix, nil
}

// daemonReleaseHostClient returns upgrade.ReleaseAssetsConfig.ClientForHost
// backed by this deployment's configured git connections and credential set.
// No new secret type or cloud registry login is introduced: github.com uses the
// deployment's GitHub token or App connection (or anonymous HTTP on public
// repositories with a hint naming Settings -> Connections if refused), and
// enterprise hosts use their configured token, App, or ambient CLI connection.
func daemonReleaseHostClient(cfg Config, store *model.Store, credentials *gitproxy.CredentialSet) func(ctx context.Context, host, owner, repo string) (*upgrade.ReleaseHostClient, error) {
	return func(ctx context.Context, host, owner, repo string) (*upgrade.ReleaseHostClient, error) {
		host = strings.ToLower(strings.TrimSpace(host))
		if host == "" {
			host = model.DefaultGitHost
		}
		defHost := restClientHost(cfg.GithubHost)
		credHost := gitproxy.CredentialHost{
			Host: host,
			Kind: model.GitHostGitHub,
		}
		if model.SameGitHost(host, model.DefaultGitHost, model.DefaultGitHost) {
			credHost.Host = model.DefaultGitHost
		} else if credentials != nil {
			h := credentials.HostOf(host)
			if h.Host != "" && model.SameGitHost(h.Host, host, defHost) {
				credHost = h
			}
		}

		var connName string
		if store != nil {
			lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			name, err := store.GitConnectionOn(lookupCtx, credHost.Host, owner, repo)
			cancel()
			if err != nil && !errors.Is(err, model.ErrNoTeamGitCredential) {
				return nil, fmt.Errorf("looking up git connection for %s/%s/%s: %w", credHost.Host, owner, repo, err)
			}
			connName = strings.TrimSpace(name)
		}

		if credentials != nil && connName != "" {
			h := credentials.HostOf(connName)
			if h.Host != "" && model.SameGitHost(h.Host, credHost.Host, defHost) {
				credHost = h
			}
		}
		if cfg.GithubInsecureHTTP && credHost.BaseURL == "" && model.SameGitHost(host, defHost, model.DefaultGitHost) {
			credHost.BaseURL = "http://" + host
		}

		apiBase, err := releaseHostAPIBase(credHost, defHost, cfg.GithubInsecureHTTP)
		if err != nil {
			return nil, fmt.Errorf("repository %s/%s %w", owner, repo, err)
		}

		missingConnHint := fmt.Sprintf("configure a git connection covering %s/%s/%s in Settings -> Connections", credHost.Host, owner, repo)
		if connName != "" {
			missingConnHint = fmt.Sprintf("check git connection %q for %s in Settings -> Connections", connName, credHost.Host)
		}

		if credentials != nil && connName != "" {
			if credentials.ForgeViaCommand(connName) {
				if cliClient := credentials.ForgeHTTPClient(connName, 0); cliClient != nil {
					return &upgrade.ReleaseHostClient{
						Client:      cliClient,
						APIBase:     apiBase,
						Connection:  connName,
						MissingHint: missingConnHint,
					}, nil
				}
			}

			cred, ok := credentials.GetForRepoOn(connName, credHost.Host, owner, repo)
			if ok && cred.SSH != nil {
				if apiCred, paired := credentials.APICredential(cred); paired {
					cred = apiCred
				} else {
					return &upgrade.ReleaseHostClient{
						Client:     credHost.HTTPClient(0),
						APIBase:    apiBase,
						Connection: connName,
						MissingHint: fmt.Sprintf(
							"git connection %q on %s is SSH-only and cannot read release assets via the REST API; pair it with a token or App in Settings -> Connections",
							connName, credHost.Host),
					}, nil
				}
			}
			if ok {
				if cred.Token != nil && *cred.Token != "" {
					return &upgrade.ReleaseHostClient{
						Client:      credHost.HTTPClient(0),
						APIBase:     apiBase,
						Token:       *cred.Token,
						Connection:  connName,
						MissingHint: missingConnHint,
					}, nil
				}
				if cred.MintFailure != nil {
					return nil, fmt.Errorf("git connection %q for %s/%s/%s cannot mint a token: %s (fix connection %q in Settings -> Connections)",
						connName, credHost.Host, owner, repo, cred.MintFailure.Describe(), connName)
				}
				if cred.Unusable != "" {
					return nil, fmt.Errorf("git connection %q for %s/%s/%s is unusable: %s (fix connection %q in Settings -> Connections)",
						connName, credHost.Host, owner, repo, cred.Unusable, connName)
				}
			}
		}

		// When no explicit connection matched on an enterprise host that has a
		// registered command-backed ambient transport, let the daemon's ambient
		// account try it directly.
		if !model.SameGitHost(credHost.Host, model.DefaultGitHost, model.DefaultGitHost) {
			if cliClient, ok := gitproxy.AmbientHostHTTPClient(credHost, "", 0); ok {
				return &upgrade.ReleaseHostClient{
					Client:      cliClient,
					APIBase:     apiBase,
					Connection:  connName,
					MissingHint: missingConnHint,
				}, nil
			}
		}

		return &upgrade.ReleaseHostClient{
			Client:      credHost.HTTPClient(0),
			APIBase:     apiBase,
			Connection:  connName,
			MissingHint: missingConnHint,
		}, nil
	}
}

func upgradeReleaseAssetsConfig(cfg Config, store *model.Store, credentials *gitproxy.CredentialSet) upgrade.ReleaseAssetsConfig {
	stagingDir := ""
	if strings.TrimSpace(cfg.SandboxDir) != "" {
		stagingDir = strings.TrimSpace(cfg.SandboxDir) + "/.upgrade-staging"
	} else if strings.TrimSpace(cfg.DataDir) != "" {
		stagingDir = strings.TrimSpace(cfg.DataDir) + "/upgrade-staging"
	}
	ra := upgrade.ReleaseAssetsConfig{
		Repository:    strings.TrimSpace(cfg.UpgradeReleaseRepo),
		Asset:         strings.TrimSpace(cfg.UpgradeReleaseAsset),
		StagingDir:    stagingDir,
		ClientForHost: daemonReleaseHostClient(cfg, store, credentials),
	}
	if len(cfg.UpgradeSandboxReleaseAssets) > 0 {
		ra.SandboxAssets = make(map[string]string, len(cfg.UpgradeSandboxReleaseAssets))
		for k, v := range cfg.UpgradeSandboxReleaseAssets {
			ra.SandboxAssets[k] = v
		}
	}
	checksum := strings.TrimSpace(cfg.UpgradeReleaseChecksum)
	if strings.HasPrefix(checksum, ".") {
		ra.ChecksumSuffix = checksum
	} else if checksum != "" {
		ra.ChecksumAsset = checksum
	}
	return ra
}
