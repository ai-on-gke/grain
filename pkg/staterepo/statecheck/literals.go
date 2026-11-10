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

package statecheck

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// BaseFile reads one file, by its path from the state repository's root,
// as the branch a change is against holds it, and returns an error
// wrapping fs.ErrNotExist for a file that branch does not have.
type BaseFile func(path string) ([]byte, error)

// GitBase is the BaseFile of revision ref in the git checkout holding
// dir: `grain state check -base <ref>`. A ref git cannot resolve is an
// error here, rather than a base that seems to hold nothing.
func GitBase(dir, ref string) (BaseFile, error) {
	if out, err := exec.Command("git", "-C", dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}").CombinedOutput(); err != nil {
		return nil, fmt.Errorf("-base %s: not a revision in %s: %v %s", ref, dir, err, strings.TrimSpace(string(out)))
	}
	return func(path string) ([]byte, error) {
		// ./<path> is relative to dir, which need not be the
		// checkout's root.
		out, err := exec.Command("git", "-C", dir, "show", ref+":./"+path).Output()
		if err != nil {
			return nil, fmt.Errorf("%s:%s: %w", ref, path, fs.ErrNotExist)
		}
		return out, nil
	}, nil
}

// BaseMCPServers is grain_config.mcp_servers as base holds it (MCPServersIn
// over the files staterepo.GrainConfigFiles names). A base with no
// settings row holds none, which is an empty map, never nil.
func BaseMCPServers(base BaseFile) (map[string]protocol.ServerConfig, error) {
	dir, err := os.MkdirTemp("", "grain-state-check-base-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	for _, p := range staterepo.GrainConfigFiles() {
		data, err := base(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		dst := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(dst, data, 0o600); err != nil {
			return nil, err
		}
	}
	servers, err := staterepo.MCPServersIn(dir)
	if err != nil {
		return nil, err
	}
	if servers == nil {
		servers = map[string]protocol.ServerConfig{}
	}
	return servers, nil
}

// LiteralCredentials is the credential-exposure audit's CE-2 rule
// (protocol.ServerConfig.LiteralCredentials, which Client.SetCapabilityServer
// already applies to a save) asked of a pull request: a capability
// server's env, header, arg or URL holding a credential typed in rather
// than a $secret:<name> reference. Such a value is stored in
// grain_config.mcp_servers, exported in plain text to this repository and
// its history, and readable by anyone who may read settings.
//
// One the pull request adds or changes -- compared with base, the
// capability servers the branch it is against holds -- fails the check.
// One base already held is a warning: it is not this change's doing, and
// the daemon moves it into the secrets store at its next start
// (daemon's migrateLiteralCredentials). With no base (nil, as when
// `grain state check` is run without -base, or with -migrate, against a
// dump the running build has just exported) nothing can be told new from
// held, and every one is a warning. Neither message repeats a value.
func LiteralCredentials(ctx context.Context, db *sql.DB, base map[string]protocol.ServerConfig) ([]string, error) {
	cfg, err := model.New(db).GetConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the settings this dump carries: %w", err)
	}
	if cfg == nil {
		return nil, nil
	}
	ids := make([]string, 0, len(cfg.MCPServers))
	for id := range cfg.MCPServers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var warnings, refused []string
	for _, id := range ids {
		sc := cfg.MCPServers[id]
		all := sc.LiteralCredentials()
		if len(all) == 0 {
			continue
		}
		var added []string
		if base != nil {
			added = sc.NewLiteralCredentials(base[id])
		}
		for _, where := range all {
			if slices.Contains(added, where) {
				refused = append(refused, fmt.Sprintf("capability server %s: %s", id, where))
				continue
			}
			why := "as the branch this is against already does"
			if base == nil {
				why = "and with no base to compare with (-base) this check cannot tell whether this change added it"
			}
			warnings = append(warnings, fmt.Sprintf("the settings row's capability server %s carries a credential typed in at %s, %s -- "+
				"it is in plain text in this repository and its history: the daemon moves it into the secrets store as $secret:%s "+
				"at its next start, and the credential should then be rotated", id, where, why, protocol.LiteralSecretName(id, where)))
		}
	}
	if len(refused) > 0 {
		return warnings, fmt.Errorf("this change types %d credential(s) into capability servers, where they would be exported in plain text "+
			"to this repository and shown to anyone who may read settings -- store each in the secrets store "+
			"(Settings -> Secrets, or Bind a secret on the server) and write $secret:<name> in its place:\n  %s",
			len(refused), strings.Join(refused, "\n  "))
	}
	return warnings, nil
}
