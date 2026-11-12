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

package gcpsets

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"google.golang.org/api/option"
)

// TestEndpointsEnv is the environment variable a test-only gcp server
// reads its Endpoints from (ParseEndpoints), honoured only beside the
// server's -test-endpoints flag.
const TestEndpointsEnv = "GRAIN_GCP_TEST_ENDPOINTS"

// The services an Endpoints names, each by the googleapis.com host it
// stands in for.
const (
	EndpointIAM              = "iam"
	EndpointResourceManager  = "cloudresourcemanager"
	EndpointIAMCredentials   = "iamcredentials"
	EndpointStorage          = "storage"
	EndpointArtifactRegistry = "artifactregistry"
	EndpointOAuth2           = "oauth2"
	EndpointSTS              = "sts"
)

const testEndpointHostSuffix = ".googleapis.com"

// EndpointServices are the services an Endpoints must name, every one:
// a test pointed at a stub never reaches Google for the one it forgot.
var EndpointServices = []string{
	EndpointIAM, EndpointResourceManager, EndpointIAMCredentials, EndpointStorage,
	EndpointArtifactRegistry, EndpointOAuth2, EndpointSTS,
}

// Endpoints are, by service, the base URLs a test points GoogleConnector
// at instead of Google: a stub IAM, Resource Manager, IAM Credentials,
// Storage, Artifact Registry, OAuth2 token endpoint and STS. Nil is
// Google itself -- every production path.
type Endpoints map[string]string

// ParseEndpoints reads spec, "iam=http://127.0.0.1:8080/iam/,..." --
// every service in EndpointServices, each at a loopback http(s) base URL.
// A loopback host is the whole of what this seam allows: an admin's
// access token goes to whatever these name, so nothing off the machine is
// accepted.
func ParseEndpoints(spec string) (Endpoints, error) {
	e := Endpoints{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, raw, ok := strings.Cut(part, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return nil, fmt.Errorf("%s: %q is not service=base-url (service=base-url pairs, comma-separated)", TestEndpointsEnv, part)
		}
		if !knownEndpoint(name) {
			return nil, fmt.Errorf("%s: unknown service %q (want %s)", TestEndpointsEnv, name, strings.Join(EndpointServices, ", "))
		}
		if _, dup := e[name]; dup {
			return nil, fmt.Errorf("%s: %s is named twice", TestEndpointsEnv, name)
		}
		base, err := loopbackBase(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("%s: %s: %v", TestEndpointsEnv, name, err)
		}
		e[name] = base
	}
	var missing []string
	for _, s := range EndpointServices {
		if e[s] == "" {
			missing = append(missing, s)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%s: names no base URL for %s; a stub stands in for every service or none", TestEndpointsEnv, strings.Join(missing, ", "))
	}
	return e, nil
}

// String is e as ParseEndpoints reads it.
func (e Endpoints) String() string {
	names := make([]string, 0, len(e))
	for n := range e {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, n+"="+e[n])
	}
	return strings.Join(parts, ",")
}

func knownEndpoint(name string) bool {
	for _, s := range EndpointServices {
		if s == name {
			return true
		}
	}
	return false
}

// loopbackBase is raw as a base URL ending in "/", refused unless it is
// http or https on a loopback host.
func loopbackBase(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("%q is not an http(s) URL", raw)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("%q carries more than a scheme, host and path", raw)
	}
	host := u.Hostname()
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return "", fmt.Errorf("%q is not on a loopback address; a test endpoint is a stub on this machine", raw)
		}
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	return u.String(), nil
}

// clientOption points a Google API client for service at e's stub; nil
// when e names none, leaving the client on Google.
func (e Endpoints) clientOption(service string) []option.ClientOption {
	if base := e[service]; base != "" {
		return []option.ClientOption{option.WithEndpoint(base)}
	}
	return nil
}

// transport is base with requests to the OAuth2 and STS hosts sent to
// e's stubs instead -- the token fetches an admin's credentials make and
// the tokeninfo lookup Identity does. Nil e is base unchanged.
func (e Endpoints) transport(base http.RoundTripper) http.RoundTripper {
	if len(e) == 0 {
		return base
	}
	if base == nil {
		base = http.DefaultTransport
	}
	return &endpointTransport{e: e, base: base}
}

type endpointTransport struct {
	e    Endpoints
	base http.RoundTripper
}

func (t *endpointTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	service, ok := strings.CutSuffix(r.URL.Hostname(), testEndpointHostSuffix)
	if !ok || (service != EndpointOAuth2 && service != EndpointSTS) || t.e[service] == "" {
		return nil, fmt.Errorf("%s: %s is not a service the test endpoints stand in for; refusing to reach it", TestEndpointsEnv, r.URL.Host)
	}
	to, err := url.Parse(t.e[service])
	if err != nil {
		return nil, err
	}
	r2 := r.Clone(r.Context())
	r2.URL.Scheme, r2.URL.Host = to.Scheme, to.Host
	r2.URL.Path = strings.TrimSuffix(to.Path, "/") + r.URL.Path
	r2.URL.RawPath = ""
	r2.Host = to.Host
	return t.base.RoundTrip(r2)
}
