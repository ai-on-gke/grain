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

package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// -ui-require-iap with no IAP audience would leave the UI reachable from
// the daemon's own host only, so the daemon refuses to start (task 2318).
func TestCheckConfigRefusesRequireIAPWithoutAnAudience(t *testing.T) {
	t.Setenv("GRAIN_IAP_AUDIENCE", "")
	t.Setenv("GRAIN_REQUIRE_IAP", "")
	t.Setenv("GRAIN_REQUIRE_VERIFIER", "")
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)
	cfg, _, _, err := parseDaemonArgs([]string{"-data-dir=/var/lib/grain/data", "-ui-require-iap"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.UiRequireIAP {
		t.Fatal("-ui-require-iap did not set UiRequireIAP")
	}
	start := startable()
	start.UiRequireIAP = true
	err = checkConfig(start)
	if err == nil || !strings.Contains(err.Error(), "-ui-require-iap needs an IAP audience") {
		t.Fatalf("checkConfig(-ui-require-iap, no audience) = %v, want a refusal naming the missing audience", err)
	}

	start.UiIAPAudience = "/projects/1/global/backendServices/2"
	if err := checkConfig(start); err != nil {
		t.Errorf("-ui-require-iap with -ui-iap-audience: %v", err)
	}
	start.UiIAPAudience = ""
	t.Setenv("GRAIN_IAP_AUDIENCE", "/projects/1/global/backendServices/2")
	if err := checkConfig(start); err != nil {
		t.Errorf("-ui-require-iap with $GRAIN_IAP_AUDIENCE: %v", err)
	}
	start.UiRequireIAP = false
	t.Setenv("GRAIN_IAP_AUDIENCE", "")
	if err := checkConfig(start); err != nil {
		t.Errorf("no -ui-require-iap, no audience: %v", err)
	}
}

func TestRequireIAPFromTheEnvironment(t *testing.T) {
	t.Setenv("GRAIN_REQUIRE_VERIFIER", "")
	t.Setenv("GRAIN_REQUIRE_IAP", "true")
	cfg, _, _, err := parseDaemonArgs([]string{"-data-dir=/var/lib/grain/data"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.UiRequireIAP {
		t.Error("GRAIN_REQUIRE_IAP=true did not set UiRequireIAP")
	}
	if cfg, _, _, _ := parseDaemonArgs([]string{"-data-dir=/var/lib/grain/data", "-ui-require-iap=false"}); cfg.UiRequireIAP {
		t.Error("-ui-require-iap=false did not override GRAIN_REQUIRE_IAP")
	}
	t.Setenv("GRAIN_REQUIRE_IAP", "maybe")
	if _, _, _, err := parseDaemonArgs([]string{"-data-dir=/var/lib/grain/data"}); err == nil {
		t.Error("GRAIN_REQUIRE_IAP=maybe was accepted")
	}
}

// `grain access-code` says when the server it would talk to takes the
// code over loopback only.
func TestAccessCodeNotesAServerThatRequiresIAP(t *testing.T) {
	requireIAP := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/access-code" {
			http.NotFound(w, r)
			return
		}
		if requireIAP {
			w.Write([]byte(`{"authenticated":false,"requireIAP":true,"accessCodeAccepted":true}`))
			return
		}
		w.Write([]byte(`{"authenticated":false}`))
	}))
	defer srv.Close()
	t.Setenv(serverEnvVar, srv.URL)
	dir := t.TempDir()
	t.Setenv(ui.LocalAPITokenFileEnv, "")
	t.Setenv(dataDirEnvVar, dir)
	code, err := ui.EnsureLocalAPIToken(ui.LocalAPITokenPath(dir))
	if err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if err := runAccessCode(nil, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != code {
		t.Errorf("stdout = %q, want only the code", out.String())
	}
	if !strings.Contains(errOut.String(), "not accepted over the network") {
		t.Errorf("stderr = %q, want the require-IAP note", errOut.String())
	}

	requireIAP = false
	out.Reset()
	errOut.Reset()
	if err := runAccessCode(nil, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q against a server that takes the code, want nothing", errOut.String())
	}
}

type stubIdentityVerifier struct {
	name string
}

func (s stubIdentityVerifier) Name() string      { return s.name }
func (s stubIdentityVerifier) Headers() []string { return []string{"X-Stub-Assertion"} }
func (s stubIdentityVerifier) Verify(*http.Request) (ui.VerifiedIdentity, bool, error) {
	return ui.VerifiedIdentity{}, false, nil
}

// -ui-require-verifier validates that the named IdentityVerifier is active,
// treats -ui-require-iap as an alias for -ui-require-verifier=iap, and refuses
// conflicting settings (task 3192).
func TestCheckConfigRequireVerifier(t *testing.T) {
	t.Setenv("GRAIN_IAP_AUDIENCE", "")
	t.Setenv("GRAIN_REQUIRE_IAP", "")
	t.Setenv("GRAIN_REQUIRE_VERIFIER", "")
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)

	cfg, _, _, err := parseDaemonArgs([]string{"-data-dir=/var/lib/grain/data", "-ui-require-verifier=registry"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UiRequireVerifier != "registry" {
		t.Fatalf("UiRequireVerifier = %q, want registry", cfg.UiRequireVerifier)
	}

	// Unregistered verifier is refused at startup.
	start := startable()
	start.UiRequireVerifier = "registry"
	if err := checkConfig(start); err == nil || !strings.Contains(err.Error(), `-ui-require-verifier="registry" names no active IdentityVerifier`) {
		t.Fatalf("checkConfig(-ui-require-verifier=registry, unregistered) = %v, want missing-verifier error", err)
	}

	// Once registered, -ui-require-verifier=registry passes startup validation.
	unreg := ui.RegisterIdentityVerifier(stubIdentityVerifier{name: "registry"})
	t.Cleanup(unreg)
	if err := checkConfig(start); err != nil {
		t.Errorf("checkConfig(-ui-require-verifier=registry, registered): %v", err)
	}

	// -ui-require-verifier=iap requires an IAP audience (or registered "iap" verifier).
	start.UiRequireVerifier = "iap"
	if err := checkConfig(start); err == nil || !strings.Contains(err.Error(), "needs an IAP audience") {
		t.Fatalf("checkConfig(-ui-require-verifier=iap, no audience) = %v, want missing-audience error", err)
	}
	start.UiIAPAudience = "/projects/1/global/backendServices/2"
	if err := checkConfig(start); err != nil {
		t.Errorf("checkConfig(-ui-require-verifier=iap, with audience): %v", err)
	}

	// -ui-require-iap with -ui-require-verifier=iap is compatible, while
	// -ui-require-iap with -ui-require-verifier=registry conflicts.
	start.UiRequireIAP = true
	if err := checkConfig(start); err != nil {
		t.Errorf("checkConfig(-ui-require-iap + -ui-require-verifier=iap): %v", err)
	}
	start.UiRequireVerifier = "registry"
	if err := checkConfig(start); err == nil || !strings.Contains(err.Error(), "cannot set both -ui-require-iap and -ui-require-verifier") {
		t.Fatalf("checkConfig(-ui-require-iap + -ui-require-verifier=registry) = %v, want conflict error", err)
	}

	// grain demo validates -ui-require-verifier with the same rules.
	if _, err := parseDemoFlags([]string{"-ui-require-verifier=unregistered"}); err == nil || !strings.Contains(err.Error(), "names no active IdentityVerifier") {
		t.Errorf("parseDemoFlags(-ui-require-verifier=unregistered) = %v, want missing-verifier error", err)
	}
	demoOpts, err := parseDemoFlags([]string{"-ui-require-verifier=registry"})
	if err != nil || demoOpts.RequireVerifier != "registry" {
		t.Errorf("parseDemoFlags(-ui-require-verifier=registry) = (%+v, %v), want RequireVerifier=registry", demoOpts, err)
	}
}

func TestRequireVerifierFromTheEnvironment(t *testing.T) {
	t.Setenv("GRAIN_REQUIRE_IAP", "")
	t.Setenv("GRAIN_REQUIRE_VERIFIER", "registry")
	cfg, _, _, err := parseDaemonArgs([]string{"-data-dir=/var/lib/grain/data"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UiRequireVerifier != "registry" {
		t.Errorf("GRAIN_REQUIRE_VERIFIER=registry gave UiRequireVerifier=%q, want registry", cfg.UiRequireVerifier)
	}

	// Explicit empty flag overrides GRAIN_REQUIRE_VERIFIER.
	if cfg, _, _, err := parseDaemonArgs([]string{"-data-dir=/var/lib/grain/data", "-ui-require-verifier="}); err != nil || cfg.UiRequireVerifier != "" {
		t.Errorf("-ui-require-verifier= override = (%q, %v), want empty", cfg.UiRequireVerifier, err)
	}

	// Explicit -ui-require-verifier CLI flag overrides GRAIN_REQUIRE_IAP from the environment.
	t.Setenv("GRAIN_REQUIRE_IAP", "true")
	t.Setenv("GRAIN_REQUIRE_VERIFIER", "")
	if cfg, _, _, err := parseDaemonArgs([]string{"-data-dir=/var/lib/grain/data", "-ui-require-verifier=registry"}); err != nil || cfg.UiRequireVerifier != "registry" || cfg.UiRequireIAP {
		t.Errorf("-ui-require-verifier=registry with GRAIN_REQUIRE_IAP=true = (verifier=%q, iap=%v, err=%v), want verifier=registry, iap=false", cfg.UiRequireVerifier, cfg.UiRequireIAP, err)
	}

	// Conflicting environment variables (neither flag passed) surface as a checkConfig conflict.
	t.Setenv("GRAIN_REQUIRE_IAP", "true")
	t.Setenv("GRAIN_REQUIRE_VERIFIER", "registry")
	cfg, _, _, err = parseDaemonArgs([]string{"-data-dir=/var/lib/grain/data"})
	if err != nil {
		t.Fatal(err)
	}
	start := startable()
	start.UiRequireIAP = cfg.UiRequireIAP
	start.UiRequireVerifier = cfg.UiRequireVerifier
	if err := checkConfig(start); err == nil || !strings.Contains(err.Error(), "cannot set both") {
		t.Errorf("conflicting env GRAIN_REQUIRE_IAP + GRAIN_REQUIRE_VERIFIER: checkConfig = %v, want conflict error", err)
	}
}

func TestAccessCodeNotesAServerThatRequiresVerifier(t *testing.T) {
	verifierPayload := `"registry"`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/access-code" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"authenticated":false,"requireVerifier":` + verifierPayload + `,"accessCodeAccepted":true}`))
	}))
	defer srv.Close()
	t.Setenv(serverEnvVar, srv.URL)
	dir := t.TempDir()
	t.Setenv(ui.LocalAPITokenFileEnv, "")
	t.Setenv(dataDirEnvVar, dir)
	code, err := ui.EnsureLocalAPIToken(ui.LocalAPITokenPath(dir))
	if err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if err := runAccessCode(nil, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != code {
		t.Errorf("stdout = %q, want only the code", out.String())
	}
	if !strings.Contains(errOut.String(), "-ui-require-verifier=registry") || !strings.Contains(errOut.String(), "not accepted over the network") {
		t.Errorf("stderr = %q, want the require-verifier=registry note", errOut.String())
	}

	// Control/ANSI escape sequences from an untrusted server must never be
	// written to the operator's terminal alongside the printed access code.
	verifierPayload = `"\u001b]52;c;dGVzdA==\u0007"`
	out.Reset()
	errOut.Reset()
	if err := runAccessCode(nil, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(errOut.String(), "\x1b\x07") {
		t.Errorf("stderr leaked terminal escape characters: %q", errOut.String())
	}
	if !strings.Contains(errOut.String(), "(-ui-require-verifier)") || !strings.Contains(errOut.String(), "not accepted over the network") {
		t.Errorf("stderr = %q, want generic safe require-verifier note", errOut.String())
	}
}

func TestCheckConfigRegistryVerifierFlagsAndEnv(t *testing.T) {
	t.Setenv("GRAIN_REGISTRY_ISSUER", "")
	t.Setenv("GRAIN_REGISTRY_AUDIENCE", "")
	t.Setenv("GRAIN_REGISTRY_KEYS_URL", "")
	t.Setenv("GRAIN_REQUIRE_IAP", "")
	t.Setenv("GRAIN_REQUIRE_VERIFIER", "")
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)

	cfg, _, _, err := parseDaemonArgs([]string{
		"-data-dir=/var/lib/grain/data",
		"-ui-registry-issuer=https://grain.example.com",
		"-ui-registry-audience=https://my-grain.grain.example.com",
		"-ui-registry-keys-url=https://grain.example.com/.well-known/jwks.json",
		"-ui-require-verifier=registry",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UiRegistryIssuer != "https://grain.example.com" ||
		cfg.UiRegistryAudience != "https://my-grain.grain.example.com" ||
		cfg.UiRegistryKeysURL != "https://grain.example.com/.well-known/jwks.json" {
		t.Fatalf("parsed registry flags = (%q, %q, %q)", cfg.UiRegistryIssuer, cfg.UiRegistryAudience, cfg.UiRegistryKeysURL)
	}

	// All three configured -> -ui-require-verifier=registry passes without registering a stub verifier.
	start := startable()
	start.UiRegistryIssuer = cfg.UiRegistryIssuer
	start.UiRegistryAudience = cfg.UiRegistryAudience
	start.UiRegistryKeysURL = cfg.UiRegistryKeysURL
	start.UiRequireVerifier = "registry"
	if err := checkConfig(start); err != nil {
		t.Fatalf("checkConfig(valid registry verifier): %v", err)
	}

	// Incomplete registry configuration is rejected.
	partial := startable()
	partial.UiRegistryIssuer = "https://grain.example.com"
	partial.UiRegistryAudience = "https://my-grain.grain.example.com"
	if err := checkConfig(partial); err == nil || !strings.Contains(err.Error(), "incomplete registry verifier configuration") {
		t.Fatalf("checkConfig(missing keys URL) = %v, want incomplete registry verifier configuration error", err)
	}

	// Comma-separated audience or issuer is rejected (registry audience is a single URL).
	multiAud := start
	multiAud.UiRegistryAudience = "https://a.example.com,https://b.example.com"
	if err := checkConfig(multiAud); err == nil || !strings.Contains(err.Error(), "single URL") {
		t.Fatalf("checkConfig(comma-separated registry audience) = %v, want single URL error", err)
	}

	// Environment variables $GRAIN_REGISTRY_ISSUER, $GRAIN_REGISTRY_AUDIENCE, $GRAIN_REGISTRY_KEYS_URL also activate the registry verifier.
	envStart := startable()
	envStart.UiRequireVerifier = "registry"
	t.Setenv("GRAIN_REGISTRY_ISSUER", "https://grain.example.com")
	t.Setenv("GRAIN_REGISTRY_AUDIENCE", "https://my-grain.grain.example.com")
	t.Setenv("GRAIN_REGISTRY_KEYS_URL", "https://grain.example.com/.well-known/jwks.json")
	if err := checkConfig(envStart); err != nil {
		t.Fatalf("checkConfig(registry env vars): %v", err)
	}

	// grain demo also accepts -ui-registry-issuer, -ui-registry-audience, -ui-registry-keys-url with -ui-require-verifier=registry.
	t.Setenv("GRAIN_REGISTRY_ISSUER", "")
	t.Setenv("GRAIN_REGISTRY_AUDIENCE", "")
	t.Setenv("GRAIN_REGISTRY_KEYS_URL", "")
	demoOpts, err := parseDemoFlags([]string{
		"-ui-registry-issuer=https://grain.example.com",
		"-ui-registry-audience=https://my-grain.grain.example.com",
		"-ui-registry-keys-url=https://grain.example.com/.well-known/jwks.json",
		"-ui-require-verifier=registry",
	})
	if err != nil {
		t.Fatalf("parseDemoFlags(registry verifier): %v", err)
	}
	if demoOpts.RegistryIssuer != "https://grain.example.com" ||
		demoOpts.RegistryAudience != "https://my-grain.grain.example.com" ||
		demoOpts.RegistryKeysURL != "https://grain.example.com/.well-known/jwks.json" ||
		demoOpts.RequireVerifier != "registry" {
		t.Fatalf("parseDemoFlags = %+v", demoOpts)
	}
}

func TestDaemonDeploymentOwnerFlagAndEnv(t *testing.T) {
	t.Setenv("GRAIN_DEPLOYMENT_OWNER", "env-owner@example.com")
	cfg, _, _, err := parseDaemonArgs([]string{"-data-dir", t.TempDir()})
	if err != nil {
		t.Fatalf("parseDaemonArgs with GRAIN_DEPLOYMENT_OWNER: %v", err)
	}
	if cfg.DeploymentOwner != "env-owner@example.com" {
		t.Fatalf("cfg.DeploymentOwner = %q, want env-owner@example.com", cfg.DeploymentOwner)
	}

	cfgFlag, _, _, err := parseDaemonArgs([]string{"-data-dir", t.TempDir(), "-deployment-owner=flag-owner@example.com"})
	if err != nil {
		t.Fatalf("parseDaemonArgs with -deployment-owner: %v", err)
	}
	if cfgFlag.DeploymentOwner != "flag-owner@example.com" {
		t.Fatalf("cfgFlag.DeploymentOwner = %q, want flag-owner@example.com", cfgFlag.DeploymentOwner)
	}

	bad := startable()
	bad.DeploymentOwner = "not-an-email"
	if err := checkConfig(bad); err == nil || !strings.Contains(err.Error(), "-deployment-owner") {
		t.Fatalf("checkConfig(invalid -deployment-owner) = %v, want -deployment-owner error", err)
	}
}
