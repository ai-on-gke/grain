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

package staterepo

// The judgement forbiddenGitRefImport makes on a merged settings change
// (docs/test-runs/2026-11-07-releases-upgrades-ci.md, Bug F6): an enabled
// git_ref schedule the change introduces, re-points or re-enables onto a
// forbidden repository is refused; one the host already held is not.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secretsrepo"
)

func writeSchedulesYAML(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "settings"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings", "schedules.yml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func gitRefSchedule(id, repo string, enabled bool) string {
	en := "true"
	if !enabled {
		en = "false"
	}
	return "  - id: " + id + "\n    title: watch\n    trigger:\n      git_ref:\n        repo: " + repo +
		"\n        ref: refs/heads/main\n    enabled: " + en + "\n"
}

func TestForbiddenGitRefImport(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	if err := secretsrepo.SaveSettings(dataDir, secretsrepo.Settings{
		Remote: "git@github.com:acme/host-secrets.git", Branch: "main",
	}); err != nil {
		t.Fatal(err)
	}
	externalDataDir := t.TempDir()
	if err := SaveSettings(externalDataDir, Settings{Remote: "https://github.com/acme/config.git"}); err != nil {
		t.Fatal(err)
	}

	base := writeSchedulesYAML(t, "schedules:\n"+
		gitRefSchedule("sched-ok", "acme/widgets", true)+
		gitRefSchedule("sched-grandfathered", "acme/host-secrets", true)+
		gitRefSchedule("sched-paused", "grain/config-alpha", false))

	cases := []struct {
		name    string
		dataDir string
		head    string
		wantSub string // "" means accepted
	}{
		{"unchanged, grandfathered forbidden watch kept", dataDir, "schedules:\n" +
			gitRefSchedule("sched-ok", "acme/widgets", true) +
			gitRefSchedule("sched-grandfathered", "acme/host-secrets", true) +
			gitRefSchedule("sched-paused", "grain/config-alpha", false), ""},
		{"re-pointed onto a retired alias", dataDir, "schedules:\n" +
			gitRefSchedule("sched-ok", "grain/config-alpha", true), "grain/config-alpha not found: only grain/config exists"},
		{"new schedule watching the secrets backup repo (case-insensitive)", dataDir, "schedules:\n" +
			gitRefSchedule("sched-new", "ACME/Host-Secrets", true), "encrypted secrets backup repository"},
		{"paused forbidden schedule re-enabled", dataDir, "schedules:\n" +
			gitRefSchedule("sched-paused", "grain/config-alpha", true), "grain/config-alpha not found"},
		{"new disabled forbidden schedule allowed", dataDir, "schedules:\n" +
			gitRefSchedule("sched-new", "grain/config-beta", false), ""},
		{"git-ref spelling is held to the same rule", dataDir,
			"schedules:\n  - id: sched-dash\n    trigger:\n      git-ref:\n        repo: grain/config-gamma\n        ref: refs/heads/main\n",
			"grain/config-gamma not found"},
		{"local grain/config allowed without an external repo", dataDir, "schedules:\n" +
			gitRefSchedule("sched-cfg", "grain/config", true), ""},
		{"local grain/config refused with an external repo", externalDataDir, "schedules:\n" +
			gitRefSchedule("sched-cfg", "grain/config", true), "local grain/config is disabled"},
		{"secrets repo judged only with a data dir", "", "schedules:\n" +
			gitRefSchedule("sched-new", "acme/host-secrets", true), ""},
		{"malformed repo left to the decoder", dataDir, "schedules:\n" +
			gitRefSchedule("sched-bad", `"not a repo"`, true), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := forbiddenGitRefImport(base, writeSchedulesYAML(t, tc.head), tc.dataDir)
			if tc.wantSub == "" {
				if err != nil {
					t.Fatalf("forbiddenGitRefImport = %v, want accepted", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("forbiddenGitRefImport = %v, want an error containing %q", err, tc.wantSub)
			}
		})
	}
}

func TestRemoteMatchesRepo(t *testing.T) {
	t.Parallel()
	repo := model.RepoRef{Owner: "acme", Name: "host-secrets"}
	for remote, want := range map[string]bool{
		"git@github.com:acme/host-secrets.git":       true,
		"https://github.com/ACME/host-secrets":       true,
		"ssh://git@github.com/acme/host-secrets.git": true,
		"github.com/acme/host-secrets":               true,
		"https://github.com/acme/other":              false,
		"https://ghe.example.com/acme/host-secrets":  false,
		"": false,
	} {
		if got := RemoteMatchesRepo(remote, repo); got != want {
			t.Errorf("RemoteMatchesRepo(%q) = %v, want %v", remote, got, want)
		}
	}
}
