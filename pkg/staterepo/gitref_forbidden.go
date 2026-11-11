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

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secretsrepo"
)

// ForbiddenGitRefWatch says why an enabled git_ref schedule may not watch
// watched on the deployment whose data directory is dataDir, or "" when it
// may. It is the state-repository half of the refusal POST/PATCH
// /api/schedules makes (#744): a retired per-team grain/config-<team> alias
// is never served; controller-local grain/config is disabled while an
// external configuration repository is configured; and the encrypted
// secrets backup repository is never read. dataDir "" judges only what needs
// no deployment (the retired alias).
func ForbiddenGitRefWatch(watched model.RepoRef, dataDir string) string {
	if model.IsRetiredConfigRepo(watched) {
		return model.RetiredConfigRepoReason(watched)
	}
	if dataDir == "" {
		return ""
	}
	if model.IsConfigRepo(watched) {
		if st, err := LoadSettings(dataDir); err == nil {
			if remote := strings.TrimSpace(st.Remote); remote != "" {
				return fmt.Sprintf("local %s is disabled when configuration is managed in external repository %s",
					model.ConfigRepo(), remote)
			}
		}
	}
	if sec, err := secretsrepo.LoadSettings(dataDir); err == nil {
		if remote := strings.TrimSpace(sec.Remote); remote != "" && RemoteMatchesRepo(remote, watched) {
			return "it is this deployment's encrypted secrets backup repository"
		}
	}
	return ""
}

// RemoteMatchesRepo reports whether a git remote URL (https://, ssh://,
// scp-style git@host:owner/name, or a bare host/owner/name) names repo.
// github.com and the default host (an empty RepoRef.Host) are the same.
func RemoteMatchesRepo(remote string, repo model.RepoRef) bool {
	r := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(remote), ".git"))
	for _, pfx := range []string{"https://", "http://", "ssh://git@", "ssh://"} {
		r = strings.TrimPrefix(r, pfx)
	}
	if strings.HasPrefix(r, "git@") {
		r = strings.Replace(strings.TrimPrefix(r, "git@"), ":", "/", 1)
	}
	parsed, err := model.ParseRepo(r)
	if err != nil {
		if after, ok := strings.CutPrefix(strings.ToLower(r), "github.com/"); ok {
			parsed, err = model.ParseRepo(after)
		}
	}
	if err != nil {
		return false
	}
	if parsed.Same(repo) {
		return true
	}
	if (strings.EqualFold(parsed.Host, "github.com") || parsed.Host == "") &&
		(strings.EqualFold(repo.Host, "github.com") || repo.Host == "") {
		return strings.EqualFold(parsed.Owner, repo.Owner) && strings.EqualFold(parsed.Name, repo.Name)
	}
	return false
}

// gitRefWatches is the enabled git_ref watch of every schedule in a state
// repository directory's settings/schedules.yml, by schedule id.
func gitRefWatches(dir string) (map[string]string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "settings", "schedules.yml"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	type trigger struct {
		Repo string `yaml:"repo"`
	}
	var doc struct {
		Schedules []struct {
			ID      string `yaml:"id"`
			Enabled *bool  `yaml:"enabled"`
			Trigger struct {
				GitRef     *trigger `yaml:"git_ref"`
				GitRefDash *trigger `yaml:"git-ref"`
			} `yaml:"trigger"`
		} `yaml:"schedules"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		// The import's own decoder says what is wrong with the file, and
		// says it better; this check has nothing to add to a file that
		// does not parse.
		return nil, nil
	}
	out := map[string]string{}
	for _, s := range doc.Schedules {
		if s.Enabled != nil && !*s.Enabled {
			continue
		}
		t := s.Trigger.GitRef
		if t == nil {
			t = s.Trigger.GitRefDash
		}
		if t == nil || strings.TrimSpace(t.Repo) == "" {
			continue
		}
		out[strings.TrimSpace(s.ID)] = strings.TrimSpace(t.Repo)
	}
	return out, nil
}

// forbiddenGitRefImport refuses settings arriving in dir that would start an
// enabled git_ref schedule watching a repository ForbiddenGitRefWatch
// refuses: one that is new, newly enabled, or newly pointed there relative
// to baseDir (the commit this host last agreed with). A schedule the
// database already held watching the same repository, enabled, is left to
// the operator to pause -- refusing every later import over it would wedge
// the state repository on a row nobody changed.
func forbiddenGitRefImport(baseDir, dir, dataDir string) error {
	head, err := gitRefWatches(dir)
	if err != nil {
		return fmt.Errorf("reading the git_ref schedules that just arrived: %w", err)
	}
	if len(head) == 0 {
		return nil
	}
	var base map[string]string
	if baseDir != "" {
		if base, err = gitRefWatches(baseDir); err != nil {
			return fmt.Errorf("reading the git_ref schedules this host last agreed with: %w", err)
		}
	}
	var bad []string
	for id, repo := range head {
		if prev, ok := base[id]; ok && strings.EqualFold(prev, repo) {
			continue
		}
		watched, err := model.ParseRepo(repo)
		if err != nil {
			// The import's decoder refuses a malformed repo itself.
			continue
		}
		if why := ForbiddenGitRefWatch(watched, dataDir); why != "" {
			bad = append(bad, fmt.Sprintf("schedule %s: git_ref repo %s cannot be watched: %s", id, watched, why))
		}
	}
	if len(bad) == 0 {
		return nil
	}
	slices.Sort(bad)
	return fmt.Errorf("settings/schedules.yml declares %d forbidden git_ref schedule(s):\n  %s",
		len(bad), strings.Join(bad, "\n  "))
}
