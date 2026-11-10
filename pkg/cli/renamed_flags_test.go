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
	"flag"
	"io"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/hubenv"
)

func captureHubWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := hubenv.Log
	hubenv.Log = func() io.Writer { return &buf }
	hubenv.ResetForTest()
	t.Cleanup(func() {
		hubenv.Log = prev
		hubenv.ResetForTest()
	})
	return &buf
}

// A daemon unit written before the Grain Registry was renamed the Grain
// Hub still starts: every -registry-* flag is read as its -hub-* name,
// with one deprecation warning each.
func TestDaemonAcceptsThePreRenameRegistryFlags(t *testing.T) {
	warnings := captureHubWarnings(t)
	t.Setenv(daemon.HubURLEnv, "")
	t.Setenv(daemon.HubEntryEnv, "")
	t.Setenv("GRAIN_REGISTRY_URL", "")
	t.Setenv("GRAIN_REGISTRY_ENTRY", "")

	cfg, _, unknown, err := parseDaemonArgs([]string{
		"-data-dir", "/tmp/grain-data",
		"-disable-native-sandboxes",
		"-registry-url", "https://hub.example.com",
		"-registry-entry", "team-alpha",
		"-registry-host-kind", "kubernetes",
		"-registry-listed=false",
		"-registry-token-file", "/etc/grain/reg.token",
		"-ui-registry-issuer", "grain-registry",
		"-ui-registry-audience", "https://team-alpha.example.com",
		"-ui-registry-keys-url", "https://hub.example.com/.well-known/jwks.json",
	})
	if err != nil || len(unknown) != 0 {
		t.Fatalf("parseDaemonArgs = %v, unknown=%v", err, unknown)
	}
	if cfg.Hub.URL != "https://hub.example.com" || cfg.Hub.Entry != "team-alpha" ||
		cfg.Hub.HostKind != "kubernetes" || cfg.Hub.Listed ||
		cfg.HubTokenFile != "/etc/grain/reg.token" {
		t.Fatalf("parsed hub config = %+v (tokenFile=%q)", cfg.Hub, cfg.HubTokenFile)
	}
	if cfg.UiHubIssuer != "grain-registry" || cfg.UiHubAudience != "https://team-alpha.example.com" ||
		cfg.UiHubKeysURL != "https://hub.example.com/.well-known/jwks.json" {
		t.Fatalf("ui hub verifier = %q %q %q", cfg.UiHubIssuer, cfg.UiHubAudience, cfg.UiHubKeysURL)
	}
	for _, old := range []string{"-registry-url", "-registry-entry", "-ui-registry-issuer"} {
		if n := strings.Count(warnings.String(), old+" is deprecated"); n != 1 {
			t.Errorf("%s warned %d times, want once:\n%s", old, n, warnings)
		}
	}
	if !strings.Contains(warnings.String(), "use -hub-url") {
		t.Errorf("warning does not name the new flag:\n%s", warnings)
	}
}

// Given both spellings, the new one wins whichever comes first.
func TestNewHubFlagWinsOverItsOldName(t *testing.T) {
	captureHubWarnings(t)
	for _, args := range [][]string{
		{"-registry-url", "https://old.example.com", "-hub-url", "https://new.example.com"},
		{"-hub-url", "https://new.example.com", "-registry-url", "https://old.example.com"},
	} {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		url := fs.String("hub-url", "", "")
		r := defineRenamedFlags(fs, "hub-url")
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		if err := r.resolve(); err != nil {
			t.Fatal(err)
		}
		if *url != "https://new.example.com" {
			t.Errorf("%v: -hub-url = %q, want the new name's value", args, *url)
		}
	}
}

// The old environment variables are still read, after the new ones.
func TestDaemonReadsThePreRenameRegistryEnv(t *testing.T) {
	warnings := captureHubWarnings(t)
	t.Setenv(daemon.HubURLEnv, "")
	t.Setenv(daemon.HubEntryEnv, "")
	t.Setenv("GRAIN_REGISTRY_URL", "https://env-old.example.com")
	t.Setenv("GRAIN_REGISTRY_ENTRY", "env-grain")
	cfg, _, _, err := parseDaemonArgs([]string{"-data-dir", "/tmp/grain-data", "-disable-native-sandboxes"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hub.URL != "https://env-old.example.com" || cfg.Hub.Entry != "env-grain" {
		t.Fatalf("old env fallback = %+v", cfg.Hub)
	}
	if !strings.Contains(warnings.String(), "$GRAIN_REGISTRY_URL is deprecated") {
		t.Errorf("no deprecation warning for $GRAIN_REGISTRY_URL:\n%s", warnings)
	}

	t.Setenv(daemon.HubURLEnv, "https://env-new.example.com")
	cfg, _, _, err = parseDaemonArgs([]string{"-data-dir", "/tmp/grain-data", "-disable-native-sandboxes"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hub.URL != "https://env-new.example.com" {
		t.Fatalf("$GRAIN_HUB_URL should win over $GRAIN_REGISTRY_URL, got %q", cfg.Hub.URL)
	}
}

// `grain registry ...` is `grain hub ...`, and its -registry-url is -hub-url.
func TestGrainRegistryIsADeprecatedAliasForGrainHub(t *testing.T) {
	if got := canonicalCommand("registry"); got != "hub" {
		t.Fatalf("canonicalCommand(registry) = %q, want hub", got)
	}
	if !deprecatedCommandAliases["registry"] {
		t.Fatal("grain registry is not marked deprecated")
	}
	captureHubWarnings(t)
	fs := flag.NewFlagSet("grain hub list", flag.ContinueOnError)
	url := fs.String("hub-url", "", "")
	token := fs.String("hub-token", "", "")
	rest, err := parseArgs(fs, []string{"-registry-url", "https://hub.example.com", "entry", "-registry-token", "tok"})
	if err != nil {
		t.Fatal(err)
	}
	if *url != "https://hub.example.com" || *token != "tok" || len(rest) != 1 || rest[0] != "entry" {
		t.Fatalf("parseArgs: url=%q token=%q rest=%v", *url, *token, rest)
	}
}

// main rewrites a deprecated command word to its new spelling before
// anything dispatches on it, so the rewrite is where `grain registry ...`
// has to say it is deprecated: warning later, in dispatchCLI, saw "hub"
// and never fired.
func TestCanonicalizeCommandWordWarnsOnceForGrainRegistry(t *testing.T) {
	for _, tc := range []struct {
		typed, want string
		warn        bool
	}{
		{"registry", "hub", true},
		{"hub", "hub", false},
		{"hostd", "host", false},
		{"list", "list", false},
	} {
		var stderr bytes.Buffer
		args := []string{tc.typed, "list"}
		canonicalizeCommandWord(args, &stderr)
		if args[0] != tc.want {
			t.Errorf("grain %s: args[0] = %q, want %q", tc.typed, args[0], tc.want)
		}
		got := strings.Count(stderr.String(), "is deprecated")
		if tc.warn && (got != 1 || !strings.Contains(stderr.String(), "'grain registry' is deprecated") ||
			!strings.Contains(stderr.String(), "use 'grain hub'")) {
			t.Errorf("grain %s: want one warning naming 'grain hub', got %d:\n%s", tc.typed, got, stderr.String())
		}
		if !tc.warn && got != 0 {
			t.Errorf("grain %s: want no warning, got:\n%s", tc.typed, stderr.String())
		}
	}
	canonicalizeCommandWord(nil, io.Discard)
}
