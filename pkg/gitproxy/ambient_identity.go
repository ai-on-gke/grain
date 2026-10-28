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

package gitproxy

// The commit identity that goes with an ambient credential.
//
// An ambient credential (ambient.go) pushes as this host's own GitHub
// login, so the commits it pushes ought to be attributed to that person
// too -- and the host already says who that is: the account's own
// `git config user.name` / `user.email`, which is what its owner commits
// as by hand, and failing that the login GitHub answered /user with.
// Without this a team on an ambient connection that names no commit
// author of its own pushed as mcp.DefaultGitIdentity, `grain agent
// <grain-agent@localhost>`, an address no GitHub account owns -- which
// is why a CLA check on such a pull request found nobody who had signed
// (bwsalmon/grain, the cla/google failure on #372).
//
// Every other kind of credential gets the same treatment from the
// account its token acts as instead (CredentialGitIdentity,
// credential_identity.go): a PAT's /user login, an App's bot account.
// Whatever a credential's identity leaves empty is filled from a team's
// legacy commit author (model.Team.AgentGitName/AgentGitEmail), then
// grain's own default (orchestrator.runGitIdentity).

import (
	"errors"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"
)

// CommitIdentity is a git user.name and user.email, either half of
// which may be empty: the shape AmbientGitIdentity answers in, kept
// apart from mcp.GitIdentity only so this package need not import mcp.
type CommitIdentity struct {
	Name  string
	Email string
}

// Empty reports whether neither half is set.
func (i CommitIdentity) Empty() bool { return i.Name == "" && i.Email == "" }

// String is the identity as git prints it, `Name <email>`, or whichever
// half there is.
func (i CommitIdentity) String() string {
	switch {
	case i.Name != "" && i.Email != "":
		return i.Name + " <" + i.Email + ">"
	case i.Email != "":
		return "<" + i.Email + ">"
	default:
		return i.Name
	}
}

// hostGitIdentityTTL is how long a host account's git identity is
// trusted before `git config` is asked again: it changes about as often
// as a login does, and a dispatch should not shell out for it twice per
// run.
const hostGitIdentityTTL = ambientTokenTTL

// hostGitIdentityEntry is HostGitIdentity's cache.
type hostGitIdentityEntry struct {
	id CommitIdentity
	at time.Time
}

// HostGitIdentity is the git identity this host's own account commits
// with by hand: its global `git config user.name` and `user.email`, read
// as the account whose login an ambient credential borrows (ambientEnv,
// so a host-mode daemon whose HOME points into its data directory still
// reads the account's real ~/.gitconfig). Either half the account never
// set is empty. It is only answered where an ambient credential is
// (AmbientAvailable): inside the deployment container there is no
// account of a person's to read.
//
// Cached on c for hostGitIdentityTTL, under its own key so a test that
// changes the fake host's answer can advance Now past it.
func (c *CredentialSet) HostGitIdentity() CommitIdentity {
	if AmbientAvailable() != nil {
		return CommitIdentity{}
	}
	c.mu.Lock()
	cached := c.hostIdentity
	c.mu.Unlock()
	now := c.now()
	if cached != nil && now.Sub(cached.at) < hostGitIdentityTTL {
		return cached.id
	}
	id := readHostGitIdentity()
	c.mu.Lock()
	c.hostIdentity = &hostGitIdentityEntry{id: id, at: now}
	c.mu.Unlock()
	return id
}

// readHostGitIdentity asks the host's git for the account's global
// identity. `--global` and not the daemon's working directory: the
// identity wanted is the person's, not whatever repository the daemon
// happens to be started in.
func readHostGitIdentity() CommitIdentity {
	if c := activeHostGit(); c != nil {
		id, err := c.identity()
		if err != nil {
			log.Printf("gitproxy: ambient: reading the host account's git identity through grain hostd: %v", err)
		}
		return id
	}
	path, err := exec.LookPath("git")
	if err != nil {
		return CommitIdentity{}
	}
	get := func(key string) string {
		stdout, _, err := runHostTool(ambientEnv(), "", path, "config", "--global", "--get", key)
		if err != nil {
			// Exit 1 is git's answer for a key that is not set, and a
			// timeout or a missing git is an identity the host does not
			// have either way.
			return ""
		}
		return oneLine(stdout)
	}
	return CommitIdentity{Name: get("user.name"), Email: get("user.email")}
}

// oneLine is s trimmed with any line break folded to a space, the shape
// a .gitconfig `name = <value>` line can carry (mcp.GitIdentity.OrDefault
// does the same on the way into the sandbox).
func oneLine(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' {
			return ' '
		}
		return r
	}, s))
}

// AmbientGitIdentity is the commit identity a run pushing through the
// ambient credential name commits under when its team has named none:
// what already exists on this host, in this order --
//
//  1. the host account's own git identity (HostGitIdentity), which is
//     what the person that login belongs to commits as by hand; and
//  2. for whichever half that leaves empty, the ambient login itself as
//     GitHub knows it (AmbientIdentityOf): its display name, or its
//     login when it has none, and its noreply address,
//     <id>+<login>@users.noreply.<host> (AmbientIdentity.NoreplyEmail),
//     which GitHub attributes to the account exactly as it does a
//     verified address. A plain git host has no /user to ask, so this
//     half is only the credential helper's username there.
//
// An error is a name that is not an ambient credential, or a login the
// host's tools could not produce when the host's git config was not
// enough on its own; the caller falls back to grain's own default
// identity then, as it would for any other credential. An identity with
// both halves empty is returned as such, with no error, when the host
// simply has none to offer.
func (c *CredentialSet) AmbientGitIdentity(name string) (CommitIdentity, error) {
	if !c.IsAmbient(name) {
		return CommitIdentity{}, fmt.Errorf("%q is not an ambient credential", name)
	}
	id := c.HostGitIdentity()
	if id.Name != "" && id.Email != "" {
		return id, nil
	}
	login, err := c.AmbientIdentityOf(name)
	if err != nil {
		if id.Empty() {
			return CommitIdentity{}, err
		}
		return id, nil
	}
	f, _ := c.AmbientOf(name)
	if id.Name == "" {
		id.Name = login.Name
		if id.Name == "" {
			id.Name = login.Login
		}
	}
	if id.Email == "" {
		id.Email = login.NoreplyEmail(f.Host)
	}
	if id.Empty() {
		return CommitIdentity{}, errors.New("the ambient login has no name or address to commit as")
	}
	return id, nil
}
