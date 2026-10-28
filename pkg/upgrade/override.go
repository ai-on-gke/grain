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

// An image upgrade normally pulls the tag CI publishes for a branch from
// the repository this deployment was installed with (imageRef). This
// file is the way round that for an operator who needs a different
// image: the repository the install picked is unpopulated, or needs
// registry credentials this host does not have, or one particular build
// is wanted rather than whatever a channel names now. StartImage takes
// either a repository, which the branch's tag is pulled from, or a full
// `repo:tag` / `repo@sha256:...` reference, and accepts it only from a
// repository installtrack.ValidateTargetRepository allows -- so the
// choice is between the images this deployment could already have been
// installed from, never an arbitrary one.

import (
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/installtrack"
)

// imageRefPattern is the character set an image reference may use: the
// same one scripts/setup.sh's unit guard accepts before it promotes a
// ref, so nothing this accepts is one the unit would then refuse.
var imageRefPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]*$`)

// digestPattern is a sha256 digest as a registry prints one.
var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// ImageOverride is an image reference an operator asked an upgrade to
// pull, taken apart: Repository always, and Tag, Digest or both when the
// reference named them.
type ImageOverride struct {
	Repository string
	Tag        string
	Digest     string
}

// ParseImageOverride takes apart image, which is a repository
// ("ghcr.io/bwsalmon/grain/google-grain"), a tagged reference
// ("...google-grain:main"), a pinned one ("...google-grain@sha256:..."),
// or both ("...google-grain:main@sha256:...").
func ParseImageOverride(image string) (ImageOverride, error) {
	image = strings.TrimSpace(image)
	if image == "" {
		return ImageOverride{}, errors.New("no image given")
	}
	if len(image) > 512 || !imageRefPattern.MatchString(image) {
		return ImageOverride{}, fmt.Errorf("%q is not an image reference: want repo/image, repo/image:tag or repo/image@sha256:<digest>", image)
	}
	var o ImageOverride
	named, digest, pinned := strings.Cut(image, "@")
	if pinned {
		if !digestPattern.MatchString(digest) {
			return ImageOverride{}, fmt.Errorf("%q has no valid sha256 digest after its '@'", image)
		}
		o.Digest = digest
	}
	o.Repository = repositoryOf(named)
	if o.Repository != named {
		o.Tag = named[len(o.Repository)+1:]
		if o.Tag == "" || len(o.Tag) > maxTagLen || strings.ContainsAny(o.Tag, "/@:") {
			return ImageOverride{}, fmt.Errorf("%q has no valid tag after its ':'", image)
		}
	}
	if o.Repository == "" || !strings.Contains(o.Repository, "/") {
		return ImageOverride{}, fmt.Errorf("%q names no registry repository (want e.g. %s)", image, installtrack.DefaultOSSGrainRepository)
	}
	return o, nil
}

// branchForTag inverts TagForBranch: ".." is the one sequence a branch
// name cannot contain, so every one in a tag was a '/'.
func branchForTag(tag string) string {
	return strings.ReplaceAll(tag, "..", "/")
}

// ImageTarget is what the upgrade pane shows before anything is pulled:
// the repository the next upgrade pulls from, the one this deployment's
// install track defaults to, and the repositories it may be pointed at
// instead.
type ImageTarget struct {
	Repository        string
	DefaultRepository string
	Repositories      []string
}

// ImageTarget reports where an image upgrade would pull from, and false
// on any other pipeline, where there is no image to choose.
func (u *Upgrader) ImageTarget() (ImageTarget, bool) {
	if u.Mode() != ModeImage {
		return ImageTarget{}, false
	}
	t := ImageTarget{Repository: u.EffectiveRepository()}
	if u.cfg.Image.Track != "" {
		t.DefaultRepository = installtrack.DefaultRepository(u.cfg.Image.Track)
	}
	for _, repo := range installtrack.KnownSuccessorRepositories(u.cfg.Image.AllowedRepositories...) {
		if u.validateRepository(repo) == nil {
			t.Repositories = append(t.Repositories, repo)
		}
	}
	return t, true
}

// validateRepository is whether this deployment may pull from repo.
func (u *Upgrader) validateRepository(repo string) error {
	base := strings.TrimSpace(u.cfg.Image.Repository)
	if base == "" && u.cfg.Image.Track != "" {
		base = installtrack.DefaultRepository(u.cfg.Image.Track)
	}
	return installtrack.ValidateTargetRepository(base, repo, u.cfg.Image.AllowedRepositories...)
}

// ValidateImage is the check StartImage makes of an image: it returns the
// branch the upgrade is recorded under and the reference it would pull.
// A tag in image names the branch (TagForBranch, inverted) and wins over
// branch; a digest pins the bytes, and branch -- the channel the request
// came from -- is then only the label.
func (u *Upgrader) ValidateImage(branch, image string) (string, string, error) {
	branch = strings.TrimSpace(branch)
	if u.Mode() != ModeImage {
		return "", "", errors.New("an explicit image only applies to a deployment that upgrades by pulling an image (-upgrade-image)")
	}
	o, err := ParseImageOverride(image)
	if err != nil {
		return "", "", err
	}
	if err := u.validateRepository(o.Repository); err != nil {
		return "", "", err
	}
	if o.Tag != "" {
		branch = branchForTag(o.Tag)
	}
	if err := u.checkBranch(branch); err != nil {
		return "", "", err
	}
	ref, err := u.imageRefIn(o.Repository, branch)
	if err != nil {
		return "", "", err
	}
	if o.Digest != "" {
		if o.Tag == "" {
			ref = o.Repository
		}
		ref += "@" + o.Digest
	}
	return branch, ref, nil
}

// StartImage is Start, pulling image rather than the branch's tag in the
// configured repository; an empty image is exactly Start. See
// ValidateImage for how image and branch combine.
func (u *Upgrader) StartImage(branch, image string) error {
	if strings.TrimSpace(image) == "" {
		return u.Start(branch)
	}
	branch, ref, err := u.ValidateImage(branch, image)
	if err != nil {
		log.Printf("upgrade: refusing an upgrade to image %q: %v", image, err)
		return err
	}
	return u.start(branch, ref)
}

// overrideRef is the reference StartImage asked the running upgrade to
// pull, or "" for an ordinary one.
func (u *Upgrader) overrideRef() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.override
}

// start is Start after its branch has been checked, with ref the image
// to pull instead of the branch's tag ("" for none).
func (u *Upgrader) start(branch, ref string) error {
	u.mu.Lock()
	if u.done != nil {
		u.mu.Unlock()
		log.Printf("upgrade: %s: refused: %v", branch, ErrUpgradeInProgress)
		return ErrUpgradeInProgress
	}
	u.done = make(chan struct{})
	u.override = ref
	u.mu.Unlock()

	now := time.Now().UTC()
	status := Status{Branch: branch, Phase: PhaseRunning, StartedAt: &now, Process: processID}
	if err := writeStatus(u.cfg.StatusFile, status); err != nil {
		u.release()
		log.Printf("upgrade: %s: not started: recording upgrade status in %s: %v", branch, u.cfg.StatusFile, err)
		return fmt.Errorf("recording upgrade status: %w", err)
	}
	// Everything from here on says which branch it is about
	// (progress.logf); this one line is what ties the request that
	// arrived to the sequence of steps that follows it.
	if ref != "" {
		log.Printf("upgrade: %s: requested, pulling %s", branch, ref)
	} else {
		log.Printf("upgrade: %s: requested", branch)
	}

	go u.run(branch, status)
	return nil
}

// registryHint is what a failed pull of ref adds to its error so an
// operator knows what to do about it: authenticate this host, or pick
// another repository, for a registry that refused the credentials; or
// pick another tag for one that has never published this one. Empty for
// any other failure, whose own words are already the best account.
func registryHint(ref string, err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "manifest unknown") || strings.Contains(msg, "manifest_unknown") ||
		statusCode404.MatchString(msg):
		return fmt.Sprintf("%s has not been published to %s yet (the registry has no such tag): choose another tag or repository on the upgrade pane",
			ref, repositoryOf(ref))
	case strings.Contains(msg, "unauthenticated") || strings.Contains(msg, "unauthorized") ||
		strings.Contains(msg, "denied") || statusCode40x.MatchString(msg):
		return fmt.Sprintf("the registry refused this host's credentials for %s: authenticate the host to %s (e.g. `docker login`, or gcloud auth configure-docker for Artifact Registry), or choose %s on the upgrade pane",
			ref, registryHost(ref), installtrack.DefaultOSSGrainRepository)
	}
	return ""
}

// statusCode404 and statusCode40x find a registry's HTTP status in a
// pull's output as a word of its own, so the same digits inside a digest
// are not read as one.
var (
	statusCode404 = regexp.MustCompile(`\b404\b`)
	statusCode40x = regexp.MustCompile(`\b40[13]\b`)
)

// registryHost is the registry part of ref.
func registryHost(ref string) string {
	host, _, _ := strings.Cut(ref, "/")
	return host
}
