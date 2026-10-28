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

package ui

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/ai-on-gke/grain/pkg/model"
)

// CheckCLIPath decides whether path -- a connection's CLIPath -- names a
// binary the daemon can run: an absolute path to an executable file, or
// a bare name its own PATH resolves to one. It is asked by the process
// serving the UI, which is the daemon that execs it, so "executable"
// means by the daemon's own account and, in container mode, inside the
// daemon's own container: a path that exists only on the host is refused
// here exactly as a run would fail on it.
func CheckCLIPath(path string) error {
	if strings.IndexFunc(path, unicode.IsControl) >= 0 {
		return errors.New("must not contain control characters")
	}
	if !strings.ContainsRune(path, '/') {
		if _, err := exec.LookPath(path); err != nil {
			return fmt.Errorf("%q is not on the daemon's PATH (%v); give an absolute path instead", path, err)
		}
		return nil
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%q must be an absolute path or a bare command name resolved on the daemon's PATH", path)
	}
	// exec.LookPath on a path with a separator is the same check exec
	// makes before running it: a file, not a directory, that this
	// process's own account may execute.
	if _, err := exec.LookPath(path); err != nil {
		return fmt.Errorf("%s is not an executable file the daemon's account can run: %v", path, err)
	}
	return nil
}

// storedCLIPath is the CLIPath connection id was saved with, "" for one
// not saved yet.
func storedCLIPath(backends []model.AgentBackend, id string) string {
	for _, b := range backends {
		if b.ID == id {
			return b.CLIPath
		}
	}
	return ""
}

// auditCLIPathChanges records, in each connection's own team's audit
// trail, every CLIPath that differs between before and after -- a change
// to which binary the daemon runs as, with that connection's credential,
// so it is recorded the way a use of the credential is. Best effort: the
// save it follows has already landed.
func (c *Client) auditCLIPathChanges(ctx context.Context, before, after []model.AgentBackend, user string) {
	if c.Store == nil {
		return
	}
	if user = strings.TrimSpace(user); user == "" {
		user = c.actor(ctx).ID
	}
	for _, b := range after {
		was := storedCLIPath(before, b.ID)
		if was == b.CLIPath {
			continue
		}
		action := "set cli path"
		if b.CLIPath == "" {
			action = "clear cli path"
		}
		outcome := fmt.Sprintf("%q -> %q", was, b.CLIPath)
		if user != "" {
			outcome += " by " + user
		}
		log.Printf("grain ui: connection %s: %s %s", b.ID, action, outcome)
		if err := c.Store.RecordCredentialAccess(ctx, model.CredentialAccessEntry{
			Kind:       model.CredentialAccessConnection,
			Resource:   "connection/" + b.ID,
			Action:     action,
			Credential: b.ID,
			Outcome:    outcome,
		}); err != nil {
			log.Printf("grain ui: auditing connection %s's CLI path: %v", b.ID, err)
		}
	}
}
