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

package model

import (
	"fmt"
	"strings"
	"time"
)

// LifecycleDeclaration is how one repository says its tasks move
// (docs/task-lifecycle-sandbox.md).
//
// A repository's task lifecycle used to be the deployment's: a reducer
// pinned out of the *config* repository and run as a subprocess of the
// controller. This is the other half of that -- the definition declared
// in the repository whose tasks it governs, beside `.grain/workflows`,
// reviewed in the pull request that changes the thing it describes,
// branching with it, live when it merges.
//
// One file rather than a directory, unlike templates, suites and
// schedules: a repository has exactly one answer to "how do tasks here
// move", where it has as many checks as it likes. And `lifecycle.yml`
// rather than anything with `workflow` in it because `.grain/workflows`
// is already CI, and two directories a syllable apart -- one holding
// checks and one holding task policy -- is a naming collision somebody
// would pay for every time they read a stack trace.
//
// # What a lifecycle declaration may not say
//
// The same rule every other repository declaration is held to
// (repo_declaration.go, `.grain/README.md`): this file is written in a
// repository every agent grain dispatches there can rewrite, so it may
// say what work to do in this repository and when, and never what that
// work may touch. `capabilities`, `secrets`, a `network` on the serve
// sandbox and any `image` outside this repository's own package
// namespace are all refused by pkg/repodecl -- as reserved keys with
// their own reason, not as a quiet drop.
//
// The serve sandbox gets NetworkNone, no checkout and no credential, and
// that is not negotiable from a file: it is what makes it safe for the
// definition to live where an agent can edit it.
type LifecycleDeclaration struct {
	// Name is prose for a listing. Optional, and read by nothing.
	Name string
	// Image is the OCI image the definition is served from -- a name,
	// not a digest, resolved to the digest this repository's release job
	// last pushed. Empty means the stock guest with the repository
	// checked out at the pinned commit, which is the zero-config path a
	// first lifecycle should be written against: no registry, no release
	// job, no capability.
	Image string
	// Serve is how the definition is started and reached.
	Serve LifecycleServe
}

// Declared reports whether this is a real declaration rather than the
// zero value a repository with no `.grain/lifecycle.yml` produces.
func (d LifecycleDeclaration) Declared() bool { return d.Serve.Run != "" }

// LifecycleServe is the definition server: what starts it, where it
// listens, and how long grain waits on it.
type LifecycleServe struct {
	// Run is the command that starts the server, run through a shell
	// inside the serve sandbox.
	Run string
	// WorkingDirectory is where Run is run. Empty is the guest's default.
	WorkingDirectory string
	// Listen is the address the server binds: a unix socket path
	// (DefaultLifecycleListen), or "127.0.0.1:<port>" for a runtime that
	// cannot bind one. Grain dials either the same way -- by exec'ing a
	// forwarder in the guest -- because it has no route to a sandbox's
	// guest at all (docs/task-lifecycle-sandbox.md, "Transport").
	Listen string
	// Env is literal environment for the serve command. Literals only:
	// there is no secret interpolation here and there is not going to be.
	Env map[string]string
	// ReadyTimeout bounds how long GET /v1/health may take to answer
	// ready before the lifecycle is failed.
	ReadyTimeout time.Duration
	// RequestTimeout bounds one POST /v1/reduce. Exceeding it is a
	// reducer failure, and the connection is dropped under a handler
	// that is still running.
	RequestTimeout time.Duration
	// Sandbox is the shape of the serving VM.
	Sandbox LifecycleSandbox
}

// LifecycleSandbox is the serve sandbox's shape. Deliberately two fields:
// there is no `network` here, and no disk, because the serve sandbox has
// no network and writes nothing that outlives it.
type LifecycleSandbox struct {
	CPUs     int
	MemoryMB int
}

// Commitment is what a serving sandbox takes out of the host while it is
// held, which is what a deployment's budget admits against.
func (s LifecycleSandbox) Commitment() Commitment {
	return Commitment{CPUs: s.CPUs, MemoryMB: s.MemoryMB}
}

const (
	// LifecycleDeclarationPath is the one file, beside the CI directory.
	LifecycleDeclarationPath = DeclarationDir + "/lifecycle.yml"
	// LifecycleDeclarationAltPath is the same declaration spelt the
	// other way people type YAML. Both are read, and declaring both is
	// an error rather than a coin toss.
	LifecycleDeclarationAltPath = DeclarationDir + "/lifecycle.yaml"

	// DefaultLifecycleListen is the socket a definition binds unless it
	// says otherwise. Inside the guest, so nothing outside it can reach
	// the reducer and nothing has to be assigned, discovered or
	// forwarded.
	DefaultLifecycleListen = "/run/grain/lifecycle.sock"
	// DefaultLifecycleReadyTimeout bounds readiness after start.
	DefaultLifecycleReadyTimeout = 30 * time.Second
	// DefaultLifecycleRequestTimeout bounds one reduce.
	DefaultLifecycleRequestTimeout = 5000 * time.Millisecond
	// MaxLifecycleRequestTimeout is the ceiling on request-timeout-ms. A
	// reducer is a pure function of state and one event; a minute of it
	// is a bug, and holding an instance's lock for a minute to find that
	// out is a worse one.
	MaxLifecycleRequestTimeout = 30 * time.Second

	// LifecycleImageEnv is the declaration's `image:`, supplied by grain
	// to a CI job in a repository that declares a lifecycle. It is the
	// only new thing the build needs: everything else about publishing a
	// lifecycle image is an ordinary release job
	// (docs/ci-capabilities.md).
	LifecycleImageEnv = "GRAIN_LIFECYCLE_IMAGE"
	// LifecycleTokenEnv carries the bearer token grain generates per
	// serving sandbox into the serve command's environment.
	LifecycleTokenEnv = "GRAIN_LIFECYCLE_TOKEN"
	// LifecycleListenEnv carries the resolved `listen:` into it, so a
	// server need not parse the declaration it was started from.
	LifecycleListenEnv = "GRAIN_LIFECYCLE_LISTEN"
)

// LifecycleState is where one repository's lifecycle is, as reported by
// GET /api/repos/{owner}/{name}/lifecycle.
type LifecycleState string

const (
	// LifecycleNone is no declaration: the deployment default applies,
	// which is what every repository gets today.
	LifecycleNone LifecycleState = "none"
	// LifecycleUnpublished is declared, but the tag has never resolved
	// -- no release run has published the image yet.
	LifecycleUnpublished LifecycleState = "unpublished"
	// LifecyclePulling is a resolved digest being fetched.
	LifecyclePulling LifecycleState = "pulling"
	// LifecycleReady is a pinned digest grain can serve from.
	LifecycleReady LifecycleState = "ready"
	// LifecycleRefused is an image grain will not serve: no guest disk
	// in it, or a name outside the repository's own namespace.
	LifecycleRefused LifecycleState = "refused"
	// LifecycleFailed is anything else that went wrong -- a declaration
	// that does not parse, a pull that will not complete, a server that
	// never becomes ready.
	LifecycleFailed LifecycleState = "failed"
)

// LifecycleStatus is one repository's lifecycle as an operator sees it.
//
// There is deliberately no build in here and no build log. A build is a
// release run, so it is already an object with a log and a live view;
// ReleaseRun points at it.
type LifecycleStatus struct {
	Declared   bool                  `json:"declared"`
	Name       string                `json:"name,omitempty"`
	Image      string                `json:"image,omitempty"`
	Version    string                `json:"version,omitempty"`
	State      LifecycleState        `json:"state"`
	ResolvedAt *time.Time            `json:"resolved_at,omitempty"`
	ReleaseRun string                `json:"release_run,omitempty"`
	Sandbox    *LifecycleSandboxInfo `json:"sandbox,omitempty"`
	Error      string                `json:"error"`
	Instances  LifecycleInstances    `json:"instances"`
}

// LifecycleSandboxInfo is the health of the sandbox serving a version.
type LifecycleSandboxInfo struct {
	State    string     `json:"state"`
	Since    *time.Time `json:"since,omitempty"`
	InFlight int        `json:"in_flight"`
}

// LifecycleInstances counts what is pinned where, which is the thing an
// operator wants after a release: how much of the repository has moved
// to the version that was just published.
type LifecycleInstances struct {
	PinnedHere      int `json:"pinned_here"`
	OnOlderVersions int `json:"on_older_versions"`
}

// ValidateLifecycleImage reports whether image is one repo may serve its
// lifecycle from, and says why when it is not.
//
// This is the load-bearing security check of the whole design, and it is
// made here -- when the declaration is read -- rather than when the
// image is pulled, so the error arrives in the pull request that wrote
// it.
//
// The rule is that the image is in the declaring repository's own
// package namespace: <registry>/<owner>/<name>[/...]. Its other half is
// `package-publish`, which resolves the deployment's credential for "the
// job's own repository, never one a workflow file names"
// (docs/ci-capabilities.md), so a release job can only push where this
// check will let a declaration point. Repository A cannot poison
// repository B's lifecycle even by naming its image, because it cannot
// push to it -- and cannot name it, because of this.
//
// A tag or a digest is refused too. `image:` is a name; the version is
// the digest grain resolves it to after a release, and a declaration
// that pinned its own tag would be a second, staler answer to "which
// version is live" sitting next to the real one.
func ValidateLifecycleImage(image string, repo RepoRef) error {
	name := strings.TrimSpace(image)
	if name == "" {
		return fmt.Errorf("image is empty -- omit it entirely to serve the definition from the stock guest")
	}
	if name != strings.ToLower(name) {
		return fmt.Errorf("image %q has an uppercase letter -- an OCI repository name is lowercase", image)
	}
	if strings.Contains(name, "@") {
		return fmt.Errorf("image %q names a digest -- `image:` is a name, and the version is "+
			"the digest grain resolves it to after this repository's release job publishes it", image)
	}
	parts := strings.Split(name, "/")
	if len(parts) < 3 {
		return fmt.Errorf("image %q is not in this repository's own package namespace -- it has to begin "+
			"%s/%s under a registry host, because that is the only namespace a release job here can push to",
			image, repo.Owner, repo.Name)
	}
	if !isRegistryHost(parts[0]) {
		return fmt.Errorf("image %q does not begin with a registry host -- write the registry out "+
			"(ghcr.io/%s/%s/...), since an unqualified name would be Docker Hub's library namespace",
			image, repo.Owner, repo.Name)
	}
	// A tag can only appear on the last component, and only after the
	// host's own optional :port has been accounted for by taking parts[0]
	// out of the reckoning.
	if strings.Contains(parts[len(parts)-1], ":") {
		return fmt.Errorf("image %q names a tag -- `image:` is a name, and the version is the digest "+
			"grain resolves it to after this repository's release job publishes it", image)
	}
	for _, part := range parts[1:] {
		if part == "" {
			return fmt.Errorf("image %q has an empty path component", image)
		}
	}
	if !strings.EqualFold(parts[1], repo.Owner) || !strings.EqualFold(parts[2], repo.Name) {
		return fmt.Errorf("image %q is %s/%s's namespace, not %s's -- a repository may only serve its "+
			"lifecycle from an image it can itself publish, which is %s/%s/... and nothing else",
			image, parts[1], parts[2], repo, parts[0], repo)
	}
	return nil
}

// isRegistryHost reports whether part looks like a registry host rather
// than the first component of a Docker Hub path. This is the
// distribution spec's own rule: a name's first component is the registry
// only if it has a dot or a port, or is exactly "localhost".
func isRegistryHost(part string) bool {
	return part == "localhost" || strings.ContainsAny(part, ".:")
}

// ValidateLifecycleListen reports whether listen is an address a
// definition server may bind.
//
// Two shapes, because there are two: a unix socket path, which is the
// default and what everything should use, and 127.0.0.1:<port> for a
// runtime that cannot bind one. A loopback address rather than any
// address, because the only thing that should ever reach this socket is
// the forwarder grain exec's into the guest.
func ValidateLifecycleListen(listen string) error {
	value := strings.TrimSpace(listen)
	switch {
	case value == "":
		return fmt.Errorf("listen is empty")
	case strings.HasPrefix(value, "/"):
		return nil
	case strings.HasPrefix(value, "127.0.0.1:"), strings.HasPrefix(value, "[::1]:"):
		port := value[strings.LastIndex(value, ":")+1:]
		if port == "" || strings.ContainsFunc(port, func(r rune) bool { return r < '0' || r > '9' }) {
			return fmt.Errorf("listen %q has no port number after the colon", listen)
		}
		return nil
	}
	return fmt.Errorf("listen %q is neither an absolute unix socket path nor 127.0.0.1:<port> -- "+
		"nothing outside the guest can reach this socket, so there is no address here worth binding "+
		"but its own", listen)
}
