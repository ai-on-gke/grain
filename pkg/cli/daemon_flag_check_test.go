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

// daemon_flag_check_test.go covers checkConfig (daemon.go): the rules a
// `grain daemon` start is refused over, and -- the case this file was
// written for -- the one it is no longer refused over.
//
// A daemon used to exit 2 unless -gemini-api-key-file named a file, long
// after that stopped being where an agent credential comes from: the key
// is set in the UI (Settings -> Models) and read per dispatch,
// the flag's own help text says so ("With neither, a run driven by the
// gemini framework fails as setup-failed saying so, rather than the
// daemon refusing to start"), and buildAntigravityFramework is where the
// absence is actually reported. The guard outlived all of it, so a
// deployment that runs claude or codex, or one that keeps its key in the
// store, could not start at all without pointing the flag at some file
// -- and neither could anybody bringing a daemon up locally to look at
// the UI, which is exactly the screen the missing key gets pasted into.
package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres"
)

// startable is the minimum config a start is allowed on: a place to keep
// state, a sandbox backend, a framework name that exists, and a pace to
// reconcile at.
func startable() daemon.Config {
	return daemon.Config{
		DataDir:               "/var/lib/grain/data",
		SandboxDir:            "/var/lib/grain/data/scratches",
		ContainerSandboxes:    true,
		ContainerImage:        "example/guest",
		ContainerGitProxyHost: "172.17.0.1",
		MaxWorkers:            1,
		MaxMergers:            model.DefaultMaxMergers,
		MaxCIJobs:             model.DefaultMaxCIJobs,
		PollInterval:          30 * time.Second,
		AgentFramework:        model.AgentFrameworkAntigravity,
	}
}

func TestCheckConfigAllowsADaemonWithNoAgentCredential(t *testing.T) {
	t.Parallel()
	cfg := startable()
	cfg.GeminiAPIKeyFile = ""
	cfg.ClaudeOAuthTokenFile = ""
	cfg.OpenAIAPIKeyFile = ""
	cfg.CodexAuthFile = ""
	if err := checkConfig(cfg); err != nil {
		t.Fatalf("a daemon with no agent credential should start (and report the absence per dispatch): %v", err)
	}
}

func TestCheckConfigRefusesWhatCannotBeRepairedWithoutARestart(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		with func(*daemon.Config)
		want string
	}{
		{"no data dir", func(c *daemon.Config) { c.DataDir = "" }, "-data-dir is required"},
		{"both sandbox backends", func(c *daemon.Config) {
			c.ContainerSandboxes, c.GranuleSandboxes = true, true
			c.ContainerImage, c.ContainerGitProxyHost = "example/guest", "172.17.0.1"
			c.GranuleSSHUser, c.GranuleWorkspace, c.GranuleGitProxyHost = "u", "/w", "172.17.0.1"
		}, "select different sandbox backends"},
		{"granule with no ssh user", func(c *daemon.Config) {
			c.ContainerSandboxes, c.GranuleSandboxes = false, true
			c.GranuleWorkspace, c.GranuleGitProxyHost = "/w", "172.17.0.1"
		}, "-granule-ssh-user is required"},
		{"granule with no workspace", func(c *daemon.Config) {
			c.ContainerSandboxes, c.GranuleSandboxes = false, true
			c.GranuleSSHUser, c.GranuleGitProxyHost = "u", "172.17.0.1"
		}, "-granule-workspace is required"},
		{"granule with no proxy host", func(c *daemon.Config) {
			c.ContainerSandboxes, c.GranuleSandboxes = false, true
			c.GranuleSSHUser, c.GranuleWorkspace = "u", "/w"
		}, "-granule-git-proxy-host is required"},
		{"upgrade from a source dir with nowhere to install it", func(c *daemon.Config) {
			c.UpgradeSrcDir = "/src"
		}, "-upgrade-install-path is required"},
		{"upgrade to an image with nowhere to record it", func(c *daemon.Config) {
			c.UpgradeImage = "ghcr.io/bwsalmon/grain:latest"
		}, "-upgrade-image-ref-file is required"},
		{"no workers", func(c *daemon.Config) { c.MaxWorkers = 0 }, "-max-workers must be at least 1"},
		// Not merely a silly number: it is the value loadConfig falls
		// back to when the stored one is unusable, and time.NewTicker
		// panics on it (grain/task-316).
		{"no interval to reconcile at", func(c *daemon.Config) { c.PollInterval = 0 }, "-poll-interval must be positive"},
		{"a negative interval", func(c *daemon.Config) { c.PollInterval = -time.Second }, "-poll-interval must be positive"},
		{"negative mergers", func(c *daemon.Config) { c.MaxMergers = -1 }, "-max-mergers cannot be negative"},
		{"backup-interval on postgres", func(c *daemon.Config) {
			c.DbDriver = "postgres"
			c.DbDSN = "postgres://localhost/grain"
			c.BackupInterval = time.Hour
		}, "-backup-interval requires SQLite"},
		// The CI cache's bounds: 0 in any of them is "grain's own
		// default" and is allowed (see below), so only a number that
		// cannot mean a bound at all is refused.
		{"negative CI cache age", func(c *daemon.Config) { c.CiCacheMaxAge = -time.Hour }, "-ci-cache-max-age cannot be negative"},
		{"negative CI cache size", func(c *daemon.Config) { c.CiCacheMaxBytes = -1 }, "-ci-cache-max-bytes cannot be negative"},
		{"negative CI cache entries", func(c *daemon.Config) { c.CiCacheMaxEntries = -1 }, "-ci-cache-max-entries cannot be negative"},
		{"negative CI cache entry size", func(c *daemon.Config) {
			c.CiCacheMaxEntryBytes = -1
		}, "-ci-cache-max-entry-bytes cannot be negative"},
		// Until GS-3 to GS-5 are built, a remote grit runs none of a
		// repository's workflows (task 1829).
		{"grit-url with no opt-in", func(c *daemon.Config) { c.GritURL = "http://127.0.0.1:4244" }, "-grit-url is experimental and refused without -grit-experimental"},
		{"unsupported db driver", func(c *daemon.Config) { c.DbDriver = "mysql" }, "-db-driver must be \"sqlite\" or \"postgres\""},
	}
	if !postgres.IsAvailable() {
		cases = append(cases, struct {
			name string
			with func(*daemon.Config)
			want string
		}{"postgres with no dsn", func(c *daemon.Config) { c.DbDriver = "postgres"; c.DbDSN = "" }, "-db-dsn is required when -db-driver is postgres"})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := startable()
			tc.with(&cfg)
			err := checkConfig(cfg)
			if err == nil {
				t.Fatalf("checkConfig = nil, want an error mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("checkConfig = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// A unit written for a host-sandbox deployment -- -sandbox-dir and nothing
// else -- has no backend left to fall back to, and says which flags would
// give it one rather than starting a daemon that can dispatch nothing.
func TestCheckSandboxBackendRefusesAConfigThatSelectsNone(t *testing.T) {
	t.Parallel()
	cfg := startable()
	cfg.ContainerSandboxes, cfg.ContainerImage, cfg.ContainerGitProxyHost = false, "", ""
	err := daemon.CheckSandboxBackend(cfg, false)
	if err == nil || !strings.Contains(err.Error(), "no sandbox backend selected") {
		t.Fatalf("checkSandboxBackend = %v, want it to refuse a config with no backend", err)
	}
	cfg.DisableNativeSandboxes = true
	if err := daemon.CheckSandboxBackend(cfg, false); err != nil {
		t.Fatalf("checkSandboxBackend with -disable-native-sandboxes = %v, want nil", err)
	}
}

func TestCheckConfigAllowsPostgresWithNoDSNWhenEmbeddedAvailable(t *testing.T) {
	t.Parallel()
	if !postgres.IsAvailable() {
		t.Skip("embedded postgres not available")
	}
	cfg := startable()
	cfg.DbDriver = "postgres"
	cfg.DbDSN = ""
	if err := checkConfig(cfg); err != nil {
		t.Fatalf("checkConfig should allow postgres with empty DSN when embedded postgres is available: %v", err)
	}
}

// A daemon started with -ci=false reserves nothing whatever the stored
// setting says (config.limits).
func TestCheckConfigIgnoresTheCIReservationWithNoCI(t *testing.T) {
	t.Parallel()
	cfg := startable()
	cfg.Ci, cfg.MaxCIJobs = false, 0
	if err := checkConfig(cfg); err != nil {
		t.Fatalf("a daemon running no CI should start with no CI capacity: %v", err)
	}
	cfg.MaxCIJobs = 99
	if err := checkConfig(cfg); err != nil {
		t.Fatalf("nor should a stored CI limit stop one: %v", err)
	}
}

// The default deployment starts cleanly with CI enabled.
func TestCheckConfigAllowsTheDefaultDeploymentWithCIOn(t *testing.T) {
	t.Parallel()
	cfg := startable()
	cfg.Ci, cfg.MaxCIJobs = true, model.DefaultMaxCIJobs
	if err := checkConfig(cfg); err != nil {
		t.Fatalf("checkConfig on the default deployment = %v", err)
	}
}

// A granule deployment names no -sandbox-dir, which is the whole point of
// that flag being conditional -- the check has to stay satisfied by the
// other backend rather than by that one directory always being there.
func TestCheckConfigAllowsAGranuleDeploymentWithNoSandboxDir(t *testing.T) {
	t.Parallel()
	cfg := startable()
	cfg.SandboxDir = ""
	cfg.ContainerSandboxes = false
	cfg.GranuleSandboxes = true
	cfg.GranuleSSHUser, cfg.GranuleWorkspace, cfg.GranuleGitProxyHost = "grain", "/home/grain/work", "172.17.0.1"
	if err := checkConfig(cfg); err != nil {
		t.Fatalf("checkConfig = %v, want a granule deployment to start", err)
	}
}

func TestCheckConfigAllowsDisabledNativeSandboxesWithNoSandboxDir(t *testing.T) {
	t.Parallel()
	cfg := startable()
	cfg.SandboxDir = ""
	cfg.ContainerSandboxes = false
	cfg.DisableNativeSandboxes = true
	if err := checkConfig(cfg); err != nil {
		t.Fatalf("checkConfig = %v, want disabled native sandboxes to start without sandboxDir", err)
	}
}

func TestCheckConfigRefusesDisabledNativeSandboxesWithOtherSandboxFlags(t *testing.T) {
	t.Parallel()
	cfg := startable()
	cfg.DisableNativeSandboxes = true
	if err := checkConfig(cfg); err == nil {
		t.Fatal("checkConfig = nil, want error when combining -disable-native-sandboxes and -container-sandboxes")
	} else if !strings.Contains(err.Error(), "select different sandbox backends") {
		t.Fatalf("checkConfig = %q, want it to mention 'select different sandbox backends'", err)
	}
}

// The legacy spelling reaches this check as itself on a deployment whose
// unit file still passes it: daemon() normalizes it into the new name
// for the config it builds, and model.ValidAgentFramework answers to
// both, so an old unit file starts rather than being refused by a check
// that only knows today's names.
func TestCheckConfigAllowsTheLegacyFrameworkName(t *testing.T) {
	t.Parallel()
	cfg := startable()
	cfg.AgentFramework = model.LegacyAgentFrameworkGemini
	if err := checkConfig(cfg); err != nil {
		t.Fatalf("checkConfig = %v, want the former spelling still accepted", err)
	}
}

// A run host's mcpserver reaches a container sandbox with `docker exec`,
// and a run host has no docker socket: every sandbox tool would fail
// (pkg/runhost's TestRunHostCannotReachAContainerSandbox makes the call),
// so the combination is refused before anything is dispatched into it.
// Run hosts with granule sandboxes, which they can reach, still start.
func TestCheckConfigRefusesRunHostsWithContainerSandboxes(t *testing.T) {
	t.Parallel()
	cfg := startable()
	cfg.RunHostImage = "example/grain"
	err := checkConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "-run-host-image") || !strings.Contains(err.Error(), "-container-sandboxes") {
		t.Fatalf("checkConfig(-run-host-image with -container-sandboxes) = %v, want a refusal naming both flags", err)
	}

	cfg.ContainerSandboxes = false
	cfg.GranuleSandboxes = true
	cfg.GranuleSSHUser, cfg.GranuleWorkspace, cfg.GranuleGitProxyHost = "root", "/home/debian", "172.18.0.1"
	if err := checkConfig(cfg); err != nil {
		t.Fatalf("checkConfig(-run-host-image with -granule-sandboxes) = %v, want it to start", err)
	}
}

// -host-sandboxes is a backend of its own again, for a single-user
// workstation: the flag parses, selects it, and starts on a loopback UI,
// and the guard refuses the default (every-interface) UI without the
// explicit override.
func TestCheckConfigAllowsHostSandboxesOnAWorkstation(t *testing.T) {
	t.Parallel()
	cfg, _, unknown, err := parseDaemonArgs([]string{
		"-data-dir", "/var/lib/grain/data", "-sandbox-dir", "/var/lib/grain/data/scratches",
		"-host-sandboxes", "-ui-addr", "127.0.0.1:4242",
	})
	if err != nil || len(unknown) != 0 {
		t.Fatalf("parseDaemonArgs = %v, unknown %v", err, unknown)
	}
	if !cfg.HostSandboxes || cfg.SandboxBackend() != model.SandboxBackendHost {
		t.Fatalf("-host-sandboxes selected %q", cfg.SandboxBackend())
	}
	if err := checkConfig(cfg); err != nil {
		t.Fatalf("checkConfig(-host-sandboxes on loopback) = %v, want it to start", err)
	}

	shared := startable()
	shared.ContainerSandboxes = false
	shared.HostSandboxes = true
	shared.UiAddr = "0.0.0.0:4242"
	if err := checkConfig(shared); err == nil || !strings.Contains(err.Error(), "-allow-unisolated-host-sandboxes") {
		t.Fatalf("checkConfig(-host-sandboxes on 0.0.0.0) = %v, want a refusal naming the override", err)
	}
	// Loopback is no trust of its own on this single-tenant build, so the
	// override is all it takes.
	shared.AllowUnisolatedHostSandboxes = true
	if err := checkConfig(shared); err != nil {
		t.Fatalf("checkConfig with -allow-unisolated-host-sandboxes = %v, want it to start", err)
	}

	both := startable()
	both.HostSandboxes = true
	both.UiAddr = "127.0.0.1:4242"
	if err := checkConfig(both); err == nil || !strings.Contains(err.Error(), "-host-sandboxes") {
		t.Fatalf("checkConfig(-container-sandboxes and -host-sandboxes) = %v, want both named", err)
	}
}

func TestParseDaemonArgsGritFlags(t *testing.T) {
	t.Parallel()
	// Default: neither -grit-url nor -grit-client-token is set.
	defCfg, _, unknown, err := parseDaemonArgs([]string{
		"-data-dir", "/var/lib/grain/data",
		"-disable-native-sandboxes",
	})
	if err != nil || len(unknown) != 0 {
		t.Fatalf("parseDaemonArgs default = %v, unknown %v", err, unknown)
	}
	if defCfg.GritURL != "" || defCfg.GritClientToken != "" {
		t.Fatalf("default GritURL=%q GritClientToken=%q, want both empty", defCfg.GritURL, defCfg.GritClientToken)
	}

	// With -grit-url and -grit-client-token set: parsed into daemon.Config.
	cfg, _, unknown, err := parseDaemonArgs([]string{
		"-data-dir", "/var/lib/grain/data",
		"-disable-native-sandboxes",
		"-grit-url", "http://127.0.0.1:4244",
		"-grit-client-token", "grain-main.secret123",
		"-grit-experimental",
	})
	if err != nil || len(unknown) != 0 {
		t.Fatalf("parseDaemonArgs with grit flags = %v, unknown %v", err, unknown)
	}
	if cfg.GritURL != "http://127.0.0.1:4244" || cfg.GritClientToken != "grain-main.secret123" || !cfg.GritExperimental {
		t.Fatalf("got GritURL=%q GritClientToken=%q GritExperimental=%v", cfg.GritURL, cfg.GritClientToken, cfg.GritExperimental)
	}
}

// -grit-experimental is what lets -grit-url through, for whoever is
// developing the split itself.
func TestCheckConfigAllowsGritURLWithTheExperimentalOptIn(t *testing.T) {
	t.Parallel()
	cfg := startable()
	cfg.GritURL, cfg.GritExperimental = "http://127.0.0.1:4244", true
	if err := checkConfig(cfg); err != nil {
		t.Fatalf("checkConfig with -grit-url -grit-experimental = %v, want nil", err)
	}
}
