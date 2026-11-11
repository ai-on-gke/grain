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

package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/pkgcache"
)

// The Go build cache's writers.
//
// cmd/go runs whatever OutputID a GOCACHEPROG action entry names as if it
// had compiled it itself, so the action entries every sandbox reads may
// only be written by code nobody can slip anything into: a Grain CI job
// building a branch that only reviewed commits reach. The package cache
// enforces that by token (PackageCache.GoCacheJobs, pkgcache's
// SplitGoCacheTrust); this file is what hands a job one.
//
// A task sandbox is never given a token -- it presents none, reads the
// trusted namespace and writes nothing -- and neither is a CI job on a
// pull request, a task's branch or any other branch its author controls.

// WithCIGoCacheWriters gives service's CI job sandboxes the package
// cache's Go build-cache writer registry, and the rule for which refs may
// write it. It is set after BuildCIService, as ReleaseRefRefusal is,
// rather than widening that function's signature for every caller. A nil
// jobs leaves every job a reader.
func WithCIGoCacheWriters(service *ci.Service, jobs *pkgcache.JobTokenSet,
	refRefusal func(ctx context.Context, repo, ref string) (string, error)) {

	if service == nil || service.Engine == nil || jobs == nil {
		return
	}
	set := func(s CiSandboxes) CiSandboxes {
		s.GoCacheJobs, s.GoCacheRefRefusal = jobs, refRefusal
		return s
	}
	switch sb := service.Engine.Sandboxes.(type) {
	case CiSandboxes:
		service.Engine.Sandboxes = set(sb)
	case StateCheckSandboxes:
		if inner, ok := sb.Sandboxes.(CiSandboxes); ok {
			sb.Sandboxes = set(inner)
			service.Engine.Sandboxes = sb
		}
	}
}

// ciGoCacheWriteRefusal says why the job req describes may not write the
// shared Go build cache, and "" when it may.
//
// A job may write it only when everything it builds is reviewed code:
//   - its commit is vouched for (not SandboxRequest.Untrusted);
//   - its run is one only a merge or grain itself starts -- a push, a
//     trusted release, or a schedule grain fired -- never a pull request
//     or a manual run of whatever ref a caller names;
//   - its ref is one GoCacheRefRefusal accepts: for the daemon, the
//     repository's default branch, a paired branch or an open release's
//     branch, and never a task's branch (orchestrator.ReleaseBranchRefusal).
//
// Anything that cannot be decided -- no ref, no rule, an error asking --
// refuses.
func (s CiSandboxes) ciGoCacheWriteRefusal(ctx context.Context, req ci.SandboxRequest) string {
	if req.Untrusted {
		return "its commit is not one anything vouches for"
	}
	switch {
	case req.Event == ci.EventPush, req.Event == ci.EventRelease:
	case req.Event == ci.EventSchedule && req.TrustedSchedule:
	default:
		return fmt.Sprintf("a %q run can be started for code nobody reviewed", req.Event)
	}
	ref := strings.TrimPrefix(strings.TrimSpace(req.Ref), "refs/heads/")
	if ref == "" {
		return "it names no branch"
	}
	if s.GoCacheRefRefusal == nil {
		return "this deployment has no rule for which branches are reviewed"
	}
	refusal, err := s.GoCacheRefRefusal(ctx, req.Repo, ref)
	if err != nil {
		return fmt.Sprintf("could not tell whether %s is a reviewed branch: %v", ref, err)
	}
	return refusal
}

// grantGoCacheWrites makes the job req describes a trusted writer of the
// shared Go build cache, when it is one (ciGoCacheWriteRefusal): it mints
// a token, registers it in GoCacheJobs as Trusted, and repoints the
// sandbox's GOCACHEPROG at the cache with that token as basic auth. The
// answer revokes the token, and is nil when nothing was granted.
//
// Every failure leaves the job as it was, a reader: a job that cannot
// write the cache builds exactly as one on any other branch does.
func (s CiSandboxes) grantGoCacheWrites(ctx context.Context, req ci.SandboxRequest, backing orchestrator.Sandbox) func() {
	if s.GoCacheJobs == nil || s.PackageCache == nil || backing == nil {
		return nil
	}
	base := strings.TrimSpace(s.PackageCache(ctx))
	if base == "" {
		return nil
	}
	if refusal := s.ciGoCacheWriteRefusal(ctx, req); refusal != "" {
		return nil
	}
	token, err := newGoCacheJobToken()
	if err != nil {
		s.log("grain ci: %s: minting a Go build-cache token: %v -- it will read the cache and write none of it", backing.Name(), err)
		return nil
	}
	jobs := s.GoCacheJobs
	jobs.Register(token, pkgcache.JobAccess{Job: req.Name, Trusted: true})
	revoke := func() { jobs.Revoke(token) }
	configured, err := orchestrator.ConfigureGoCacheWriter(ctx, backing, base, token)
	if err != nil || !configured {
		revoke()
		if err != nil {
			s.log("grain ci: %s: %v -- it will read the Go build cache and write none of it", backing.Name(), err)
		}
		return nil
	}
	s.log("grain ci: %s: %s's %s is a reviewed branch, so this job writes the shared Go build cache", backing.Name(), req.Repo, req.Ref)
	return revoke
}

// newGoCacheJobToken is one job's Go build-cache credential: 256 random
// bits, hex so it needs no escaping in the GOCACHEPROG URL.
func newGoCacheJobToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
