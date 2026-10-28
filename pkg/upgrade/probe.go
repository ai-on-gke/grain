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

package upgrade

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// What the daily automatic upgrade asks before it holds the queue
// (orchestrator's reconcileAutoUpgrade, grain/task-1211): would an
// upgrade onto this branch find the build already running? Staging
// answers that too -- sameImageVersion, sameKubeVersion,
// sameBinaryVersion -- but only after the drain the schedule waits for,
// and only by pulling, checking out or starting a pod. This asks the
// same question of the registry or the remote alone.

// probeTimeout bounds one AlreadyRunning call, whatever its caller's
// context allows.
const probeTimeout = 15 * time.Second

// AlreadyRunning reports whether branch's published build is the one
// this deployment runs now, so that an upgrade onto it would end
// unchanged (Status.Unchanged).
//
// true is a positive match only: the running build's digest (image and
// Kubernetes pipelines) or commit (source pipeline) equals the one the
// registry or origin names for branch now. Anything it cannot establish
// -- a running ref with no digest, a modified build, an image named
// outright over the branch's own (StartImage), an unreachable registry
// -- is false, with an error where something failed, and a caller is
// expected to read both the same way: go ahead and upgrade.
func (u *Upgrader) AlreadyRunning(ctx context.Context, branch string) (bool, error) {
	branch = strings.TrimSpace(branch)
	if err := u.checkBranch(branch); err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	switch u.Mode() {
	case ModeKube:
		current, err := u.cfg.Kube.client().DeploymentImage(ctx, u.cfg.Kube.Deployment, u.cfg.Kube.container())
		if err != nil {
			return false, fmt.Errorf("reading the image deployment/%s runs: %w", u.cfg.Kube.Deployment, err)
		}
		tag, err := u.imageRefIn(u.cfg.Kube.Repository, branch)
		if err != nil {
			return false, err
		}
		running, ok := kubeDigestRef(current)
		if !ok || normalizeRepository(repositoryOf(current)) != normalizeRepository(repositoryOf(tag)) {
			return false, nil
		}
		digest, err := u.manifestDigest(ctx, tag)
		if err != nil {
			return false, err
		}
		return running == normalizeRepository(repositoryOf(tag))+"@"+digest, nil
	case ModeImage:
		if u.overrideRef() != "" {
			return false, nil
		}
		current, err := u.readImageRef()
		if err != nil {
			return false, fmt.Errorf("reading the image this deployment runs: %w", err)
		}
		tag, err := u.imageRef(branch)
		if err != nil {
			return false, err
		}
		running, ok := digestRef(current)
		if !ok || repositoryOf(current) != repositoryOf(tag) {
			return false, nil
		}
		digest, err := u.manifestDigest(ctx, tag)
		if err != nil {
			return false, err
		}
		return running == repositoryOf(tag)+"@"+digest, nil
	}
	running, err := u.Running()
	if err != nil {
		return false, err
	}
	if running.Build.Modified || running.Build.Revision == "" {
		return false, nil
	}
	lsRemote := u.cfg.LsRemote
	if lsRemote == nil {
		lsRemote = gitLsRemote
	}
	head, err := lsRemote(ctx, u.cfg.SrcDir, sourceBranch(branch))
	if err != nil {
		return false, err
	}
	return head != "" && head == running.Build.Revision, nil
}

func (u *Upgrader) manifestDigest(ctx context.Context, ref string) (string, error) {
	lookup := u.cfg.ManifestDigest
	if lookup == nil {
		lookup = RegistryManifestDigest
	}
	digest, err := lookup(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("looking up %s in its registry: %w", ref, err)
	}
	if !strings.HasPrefix(digest, "sha256:") || len(digest) == len("sha256:") {
		return "", fmt.Errorf("the registry answered no digest for %s (%q)", ref, digest)
	}
	return digest, nil
}

// gitLsRemote is Config.LsRemote's default: the commit origin's branch
// is at, as the checkout step's fetch would find it.
func gitLsRemote(ctx context.Context, srcDir, branch string) (string, error) {
	if srcDir == "" {
		return "", errors.New("no source checkout configured")
	}
	ref := "refs/heads/" + branch
	out, err := newCommand(ctx, "", "git", "-C", srcDir, "ls-remote", "--end-of-options", "origin", ref).Output()
	if err != nil {
		return "", fmt.Errorf("git ls-remote origin %s: %w", ref, err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		sha, name, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if ok && name == ref {
			return sha, nil
		}
	}
	return "", fmt.Errorf("origin has no branch %s", branch)
}

// manifestAccept is every manifest type a grain image is published as:
// the multi-platform index CI pushes, and the single manifests a local
// build pushes. Asking for the index is what makes the registry answer
// the digest docker records as the image's RepoDigest, which is the one
// a pinned ref carries.
var manifestAccept = []string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.docker.distribution.manifest.v2+json",
}

// RegistryManifestDigest reads the digest a tagged ref
// ("ghcr.io/bwsalmon/grain/grain:main") names, with a HEAD request to the
// registry's v2 API and anonymously, as ListRegistryTags does: nothing
// is pulled.
func RegistryManifestDigest(ctx context.Context, ref string) (string, error) {
	repository := repositoryOf(ref)
	tag := strings.TrimPrefix(strings.TrimPrefix(ref, repository), ":")
	if tag == "" || strings.Contains(ref, "@") {
		return "", fmt.Errorf("%s names no tag to look up", ref)
	}
	host, name := registryLocation(repository)
	target := "https://" + host + "/v2/" + name + "/manifests/" + tag
	header := http.Header{"Accept": {strings.Join(manifestAccept, ", ")}}
	token := ""
	for attempt := 0; attempt < 2; attempt++ {
		resp, err := registryDo(ctx, http.MethodHead, target, token, header)
		if err != nil && attempt == 0 && errors.Is(err, http.ErrSchemeMismatch) && loopbackRegistry(host) {
			target = "http://" + strings.TrimPrefix(target, "https://")
			resp, err = registryDo(ctx, http.MethodHead, target, token, header)
		}
		if err != nil {
			return "", err
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized && token == "" {
			if token, err = registryToken(ctx, resp.Header.Get("WWW-Authenticate")); err != nil {
				return "", err
			}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("%s answered %s", resp.Request.URL.Host, resp.Status)
		}
		digest := strings.TrimSpace(resp.Header.Get("Docker-Content-Digest"))
		if digest == "" {
			return "", fmt.Errorf("%s answered no Docker-Content-Digest for %s", resp.Request.URL.Host, ref)
		}
		return digest, nil
	}
	return "", fmt.Errorf("%s refused the anonymous token for %s", host, ref)
}
