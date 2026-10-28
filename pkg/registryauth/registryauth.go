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

// Package registryauth finds the credential an image registry wants when
// the anonymous token exchange will not do: a Google OAuth2 access token
// for Artifact Registry (*-docker.pkg.dev) and Container Registry
// (gcr.io), whose private repositories answer an anonymous pull-scoped
// token with 403 rather than with the image.
//
// The token comes from where scripts/setup.sh looks for it: gcloud's own
// credential -- a service account key's first, when the host was given
// one -- and otherwise the GCE metadata server's token for the
// VM's attached service account. Neither is required; with no token the
// caller goes on anonymously, which is all a public image ever needed.
package registryauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// GoogleUser is the user name docker logs in to a Google registry as,
// with an access token as the password.
const GoogleUser = "oauth2accesstoken"

// IsGoogleRegistry is whether host ("us-docker.pkg.dev", "gcr.io") is a
// Google registry that takes an OAuth2 access token.
//
// host must be a bare DNS name: callers build a URL by pasting it after
// "https://", so a host carrying anything else -- "evil.test?x-docker.pkg.dev",
// "evil.test#.gcr.io", a port, user info -- would end in a Google suffix
// while the request, and the token with it, went somewhere else.
func IsGoogleRegistry(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	for _, c := range host {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '-') {
			return false
		}
	}
	return strings.HasSuffix(host, "-docker.pkg.dev") || host == "gcr.io" || strings.HasSuffix(host, ".gcr.io")
}

// HostOf is the registry host an image reference or repository
// ("us-docker.pkg.dev/p/r/grain:main") names, or "" for a Docker Hub
// short name, which carries no host.
func HostOf(ref string) string {
	host, _, ok := strings.Cut(strings.TrimSpace(ref), "/")
	if !ok || !strings.ContainsAny(host, ".:") && host != "localhost" {
		return ""
	}
	return host
}

// TokenSource is how Token resolves a fresh access token. Tests replace
// it; the default is gcloud, then the metadata server.
var TokenSource = defaultToken

// Token is a Google OAuth2 access token for pulling from a Google
// registry, or an error saying why there is none. A token is reused for
// a few minutes: one is good for an hour, and a Settings pane that
// probes several images per read should not run gcloud for each.
func Token(ctx context.Context) (string, error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if time.Now().Before(cache.expires) {
		return cache.token, cache.err
	}
	token, err := TokenSource(ctx)
	token = strings.TrimSpace(token)
	if err == nil && token == "" {
		err = errors.New("no Google access token available")
	}
	if err != nil {
		// Remembered too, for less long: a host with neither gcloud nor
		// a metadata server would otherwise wait out the metadata
		// server's timeout on every probe.
		cache.token, cache.err, cache.expires = "", err, time.Now().Add(failureTTL)
		return "", err
	}
	cache.token, cache.err, cache.expires = token, nil, time.Now().Add(tokenTTL)
	return token, nil
}

// ResetCache forgets the cached token (tests).
func ResetCache() {
	cache.mu.Lock()
	cache.token, cache.err, cache.expires = "", nil, time.Time{}
	cache.mu.Unlock()
}

// TokenFor is Token for an image on host, and "" -- with no error and
// nothing looked up -- for a host that is not a Google registry, or when
// no token can be had: the caller then goes on anonymously.
func TokenFor(ctx context.Context, host string) string {
	if !IsGoogleRegistry(host) {
		return ""
	}
	token, err := Token(ctx)
	if err != nil {
		return ""
	}
	return token
}

const (
	tokenTTL   = 10 * time.Minute
	failureTTL = time.Minute
)

var cache struct {
	mu      sync.Mutex
	token   string
	err     error
	expires time.Time
}

// serviceAccountKeyFiles are the key files gcloud is pointed at when
// one exists: the one the environment names, then the one a grain host
// is provisioned with.
func serviceAccountKeyFiles() []string {
	var files []string
	if f := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); f != "" {
		files = append(files, f)
	}
	return append(files, "/home/debian/.gcp-service-account.json")
}

// MetadataTokenURL is the metadata server's token endpoint, addressed by
// IP for the reason setup.sh gives: the metadata host name resolves
// through an /etc/hosts entry a container need not have.
var MetadataTokenURL = "http://169.254.169.254/computeMetadata/v1/instance/service-accounts/default/token"

func defaultToken(ctx context.Context) (string, error) {
	gcloudErr := errors.New("gcloud is not installed")
	if _, err := exec.LookPath("gcloud"); err == nil {
		var token string
		if token, gcloudErr = gcloudToken(ctx); gcloudErr == nil {
			return token, nil
		}
	}
	token, err := metadataToken(ctx)
	if err != nil {
		return "", fmt.Errorf("no Google access token: gcloud: %v; metadata server: %v", gcloudErr, err)
	}
	return token, nil
}

func gcloudToken(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	// A key file is used for this one call only, through gcloud's
	// credential override, rather than by activating it: activation
	// would switch the active account of whoever this daemon runs as
	// for every later gcloud command on the host, not just this one. A
	// key gcloud will not take falls through to the account it already
	// has.
	for _, keyFile := range serviceAccountKeyFiles() {
		if _, err := os.Stat(keyFile); err != nil {
			continue
		}
		if token, err := printAccessToken(ctx, "CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE="+keyFile); err == nil {
			return token, nil
		}
		break
	}
	return printAccessToken(ctx)
}

func printAccessToken(ctx context.Context, extraEnv ...string) (string, error) {
	printToken := exec.CommandContext(ctx, "gcloud", "auth", "print-access-token")
	printToken.Env = append(gcloudEnv(), extraEnv...)
	out, err := printToken.Output()
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(out))
	if token == "" {
		return "", errors.New("gcloud printed no access token")
	}
	return token, nil
}

// gcloudEnv is the whole of this process's environment, deliberately:
// gcloud is the credential being asked for, and finds its configuration
// and account through HOME, CLOUDSDK_CONFIG and
// GOOGLE_APPLICATION_CREDENTIALS.
func gcloudEnv() []string {
	return os.Environ()
}

func metadataToken(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, MetadataTokenURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Metadata-Flavor", "Google")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&body); err != nil {
		return "", err
	}
	if body.AccessToken == "" {
		return "", errors.New("the metadata server answered no access_token")
	}
	return body.AccessToken, nil
}
