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

// Package repocreate creates a new repository on a forge, for an
// operator who would otherwise create it by hand and then connect it
// under Settings → Repos (POST /api/repos/create, docs/agent-api.md).
//
// Each forge host has its own Creator, registered the way ambient git
// credential sources are (gitproxy.RegisterAmbientSource): core
// registers github.com here, and a layer above core registers its own
// hosts (a glob such as "*.forge.example") from init(). A Creator
// only makes the repository and makes sure the deployment's credential
// can reach it; connecting it -- targetRepos and the repo_config row --
// is the caller's, the same for every host.
//
// A Creator reports what it did as Steps, and a step it could not do
// carries the exact manual fix (Step.Fix), because the usual reason a
// create cannot finish is a permission only a person can grant: the
// App's Administration permission, an installation on a personal
// account, or a user token grain was never given.
package repocreate

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
)

// Request is one repository to create.
type Request struct {
	// Host is the forge host, github.com when empty.
	Host  string
	Owner string
	Name  string
	// Private is the visibility; a public repository is Private false.
	Private     bool
	Description string
	// Init creates a first commit (a README) on the default branch, so
	// the repository has a branch grain can work against at once.
	Init bool
	// Template is an "owner/repo" to create the repository from, or empty.
	Template string
}

// Repo is Request's "owner/name".
func (r Request) Repo() string { return r.Owner + "/" + r.Name }

// Step statuses.
const (
	StepDone    = "done"
	StepSkipped = "skipped"
	StepFailed  = "failed"
)

// Step is one thing a create did, or could not do.
type Step struct {
	// Name is the step, stable for a caller to key on: "create",
	// "default-branch", "access", "connect", "repo-config".
	Name   string `json:"name"`
	Status string `json:"status"`
	// Detail is what happened, in a sentence.
	Detail string `json:"detail"`
	// Fix is what a person does to finish the step by hand, when it
	// failed or was skipped for want of something only they can grant.
	Fix string `json:"fix,omitempty"`
	// Command is a shell command that does Fix, when there is one.
	Command string `json:"command,omitempty"`
	// Link is the settings page Fix happens on, when there is one.
	Link string `json:"link,omitempty"`
}

// Outcome is what a Creator did.
type Outcome struct {
	// Repo is the repository as the forge spells it ("owner/name").
	Repo string
	// Created is true when this call made the repository.
	Created bool
	// Existed is true when the repository was there already: the call
	// is connect-only.
	Existed bool
	// DefaultBranch is the repository's default branch, when known.
	DefaultBranch string
	Steps         []Step
}

// Connectable is whether the repository exists, so the caller may go on
// to connect it.
func (o Outcome) Connectable() bool { return o.Created || o.Existed }

// Env is what a Creator may use from the deployment.
type Env struct {
	// Credentials is the deployment's git credential set.
	Credentials *gitproxy.CredentialSet
	// Credential is the name of the deployment's git connection for
	// Request's host and owner, or empty when it has none.
	Credential string
	// API is the forge's REST API, or nil for the creator's own default
	// for the host.
	API github.Transport
	// Secret resolves a secret from grain's secret store by name
	// ("secret" or "secret/key"); ok false when it is not set.
	Secret func(ctx context.Context, name string) (value string, ok bool)
	// Sleep waits between polls for the default branch; nil sleeps for
	// real, honouring ctx.
	Sleep func(ctx context.Context, d time.Duration) error
}

func (e Env) sleep(ctx context.Context, d time.Duration) error {
	if e.Sleep != nil {
		return e.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (e Env) secret(ctx context.Context, name string) (string, bool) {
	if e.Secret == nil {
		return "", false
	}
	v, ok := e.Secret(ctx, name)
	v = strings.TrimSpace(v)
	return v, ok && v != ""
}

// Creator creates repositories on the hosts it was registered for.
//
// Create returns an error only for a failure it has no step for (a bad
// request); a create the forge refused is an Outcome whose create step
// failed, with its Fix, so the caller can show it beside the rest.
type Creator interface {
	Create(ctx context.Context, env Env, req Request) (Outcome, error)
}

type registration struct {
	pattern string
	creator Creator
}

var (
	mu         sync.RWMutex
	registered []registration
)

// Register makes c the Creator for every host pattern matches -- a host
// ("github.com") or a path.Match glob ("*.forge.example") -- and
// returns what takes it away again (for a test). Later registrations
// win over earlier ones for a host both match. It panics on an empty
// pattern or a nil creator, or a pattern already registered: a
// registration is made once, from init().
func Register(pattern string, c Creator) (unregister func()) {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if pattern == "" || c == nil {
		panic("repocreate: Register needs a host pattern and a Creator")
	}
	if _, err := path.Match(pattern, "x"); err != nil {
		panic("repocreate: bad host pattern " + pattern)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, r := range registered {
		if r.pattern == pattern {
			panic("repocreate: host pattern " + pattern + " registered twice")
		}
	}
	reg := registration{pattern: pattern, creator: c}
	registered = append(registered, reg)
	return func() {
		mu.Lock()
		defer mu.Unlock()
		for i, r := range registered {
			if r.pattern == pattern {
				registered = append(registered[:i:i], registered[i+1:]...)
				return
			}
		}
	}
}

// For is the Creator registered for host.
func For(host string) (Creator, bool) {
	host = strings.ToLower(strings.TrimSpace(host))
	mu.RLock()
	defer mu.RUnlock()
	for i := len(registered) - 1; i >= 0; i-- {
		if ok, _ := path.Match(registered[i].pattern, host); ok {
			return registered[i].creator, true
		}
	}
	return nil, false
}

// Patterns is every registered host pattern, sorted.
func Patterns() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(registered))
	for _, r := range registered {
		out = append(out, r.pattern)
	}
	sort.Strings(out)
	return out
}

// Hosts is the hosts a create may name, for a host picker: every
// registered pattern that is a host itself, and every one of known (the
// hosts the deployment's git connections are on) a glob pattern
// matches. Sorted, without duplicates.
func Hosts(known []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(h string) {
		h = strings.ToLower(strings.TrimSpace(h))
		if h != "" && !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	for _, p := range Patterns() {
		if !strings.ContainsAny(p, "*?[") {
			add(p)
		}
	}
	for _, h := range known {
		if _, ok := For(h); ok {
			add(h)
		}
	}
	sort.Strings(out)
	return out
}

// ErrNoCreator is Create's error for a host nothing is registered for.
var ErrNoCreator = errors.New("no repository creator is registered for that host")

// Validate checks req's names, the way the forge would refuse them, so
// a bad request is refused before anything is asked of it.
func Validate(req Request) error {
	if req.Owner == "" || req.Name == "" {
		return errors.New("owner and name are required")
	}
	for _, part := range []string{req.Owner, req.Name} {
		if strings.ContainsAny(part, "/ \t\n") || part == "." || part == ".." {
			return fmt.Errorf("%q is not a valid repository owner or name", part)
		}
	}
	if req.Template != "" {
		owner, name, ok := strings.Cut(req.Template, "/")
		if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
			return fmt.Errorf("template %q must be owner/repo", req.Template)
		}
	}
	return nil
}
