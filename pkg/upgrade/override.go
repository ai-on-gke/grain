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
	"github.com/ai-on-gke/grain/pkg/registryauth"
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
// ("docker.io/grainai/grain"), a tagged reference
// ("...grain:main"), a pinned one ("...grain@sha256:..."),
// or both ("...grain:main@sha256:...").
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
	// RepositoryWarning is RepositoryWarning(): set when Repository names
	// no registry host, so an upgrade would refuse to pull it.
	RepositoryWarning string
	// Edition is this deployment's edition as the pane names it ("oss",
	// or its install track's name); empty when it has no track to go by.
	Edition string
	// OtherEditionRepositories are repositories this deployment could
	// otherwise pull from that carry another edition's builds. They are
	// never in Repositories: the pane offers them only behind its
	// explicit "switch edition" override (edition.go).
	OtherEditionRepositories []EditionRepository
	// EditionSwitchLosses is what switching to another edition gives up
	// (EditionSwitchLosses), for that override's confirmation. Empty
	// when there is no other edition to switch to.
	EditionSwitchLosses []string
}

// ImageTarget reports where an image upgrade would pull from, and false
// on any other pipeline, where there is no image to choose.
func (u *Upgrader) ImageTarget() (ImageTarget, bool) {
	if u.Mode() != ModeImage {
		return ImageTarget{}, false
	}
	t := ImageTarget{
		Repository:        u.EffectiveRepository(),
		RepositoryWarning: u.RepositoryWarning(),
	}
	if u.cfg.Image.Track != "" {
		t.DefaultRepository = installtrack.DefaultRepository(u.cfg.Image.Track)
	}
	if mine, ok := u.deploymentEdition(); ok {
		t.Edition = editionName(mine)
	}
	for _, repo := range installtrack.KnownSuccessorRepositories(u.cfg.Image.AllowedRepositories...) {
		if u.validateRepository(repo) != nil {
			continue
		}
		// Another edition's repository is not a choice like the others:
		// picking it swaps this edition's build for another's
		// (grain/task-4153), so it is listed apart, for the override.
		if other, cross := u.otherEdition(repo); cross {
			t.OtherEditionRepositories = append(t.OtherEditionRepositories,
				EditionRepository{Repository: repo, Edition: editionName(other)})
			if t.EditionSwitchLosses == nil {
				t.EditionSwitchLosses = EditionSwitchLosses(u.cfg.Image.Track, other)
			}
			continue
		}
		t.Repositories = append(t.Repositories, repo)
	}
	return t, true
}

// validateRepository is whether this deployment may pull from repo.
func (u *Upgrader) validateRepository(repo string) error {
	if u.Mode() == ModeKube {
		base := strings.TrimSpace(u.cfg.Kube.Repository)
		if base == "" && u.cfg.Kube.Track != "" {
			base = installtrack.DefaultRepository(u.cfg.Kube.Track)
		}
		return installtrack.ValidateTargetRepository(base, repo)
	}
	base := strings.TrimSpace(u.cfg.Image.Repository)
	if base == "" && u.cfg.Image.Track != "" {
		base = installtrack.DefaultRepository(u.cfg.Image.Track)
	}
	return installtrack.ValidateTargetRepository(base, repo, u.cfg.Image.AllowedRepositories...)
}

// isRecordedRollbackImage reports whether image is the exact PreviousImage a
// completed upgrade recorded in StatusFile, so "Roll back to <previous>" works
// even when the previous build was pinned to a sha-... tag or digest alone.
func (u *Upgrader) isRecordedRollbackImage(image string) bool {
	if u.cfg.StatusFile == "" {
		return false
	}
	prev, err := readStatus(u.cfg.StatusFile)
	if err != nil || prev.PreviousImage == "" {
		return false
	}
	return strings.TrimSpace(image) == strings.TrimSpace(prev.PreviousImage)
}

// ValidateImage is the check StartImage makes of an image: it returns the
// branch the upgrade is recorded under and the reference it would pull.
// A tag in image names the branch (TagForBranch, inverted) and wins over
// branch; a digest pins the bytes, and branch -- the channel the request
// came from -- is then only the label.
//
// An image in another edition's repository is refused (ErrCrossEdition):
// ValidateImageWith's SwitchEdition is the only way to name one.
func (u *Upgrader) ValidateImage(branch, image string) (string, string, error) {
	branch, ref, _, err := u.validateImage(branch, image, ImageOptions{})
	return branch, ref, err
}

// ValidateImageWith is ValidateImage with StartImageWith's options: with
// SwitchEdition, an image in another edition's repository is accepted,
// and so is one in a repository this build cannot classify (the pulled
// image's own edition stamp then decides, at the verify step).
func (u *Upgrader) ValidateImageWith(branch, image string, o ImageOptions) (string, string, error) {
	branch, ref, _, err := u.validateImage(branch, image, o)
	return branch, ref, err
}

// validateImage is ValidateImageWith, also returning the edition switch
// the upgrade is to record (nil for none).
func (u *Upgrader) validateImage(branch, image string, opts ImageOptions) (string, string, *EditionSwitch, error) {
	branch = strings.TrimSpace(branch)
	if u.Mode() != ModeImage && u.Mode() != ModeKube {
		return "", "", nil, errors.New("an explicit image only applies to a deployment that upgrades by pulling an image (-upgrade-image)")
	}
	o, err := ParseImageOverride(image)
	if err != nil {
		return "", "", nil, err
	}
	isRollback := u.isRecordedRollbackImage(image)
	if !isRollback {
		if err := u.validateRepository(o.Repository); err != nil {
			return "", "", nil, err
		}
	} else if u.cfg.Refusal != "" {
		return "", "", nil, errors.New(u.cfg.Refusal)
	}
	effOpts := opts
	if isRollback {
		if _, cross := u.otherEdition(o.Repository); cross {
			effOpts.SwitchEdition = true
		}
	}
	sw, err := u.editionSwitchFor(o.Repository, effOpts)
	if err != nil {
		return "", "", nil, err
	}
	if o.Tag != "" {
		branch = branchForTag(o.Tag)
	} else if isRollback && branch == "" {
		if o.Digest != "" && len(o.Digest) >= len("sha256:")+12 {
			branch = "sha256-" + o.Digest[len("sha256:"):len("sha256:")+12]
		} else {
			branch = "rollback"
		}
	}
	if !isRollback {
		if err := u.checkBranch(branch); err != nil {
			return "", "", nil, err
		}
	} else if err := validateBranch(branch); err != nil {
		return "", "", nil, err
	}
	ref, err := u.imageRefIn(o.Repository, branch)
	if err != nil {
		return "", "", nil, err
	}
	if o.Digest != "" {
		if o.Tag == "" {
			ref = o.Repository
		}
		ref += "@" + o.Digest
	}
	return branch, ref, sw, nil
}

// StartImage is Start, pulling image rather than the branch's tag in the
// configured repository; an empty image is exactly Start. See
// ValidateImage for how image and branch combine.
func (u *Upgrader) StartImage(branch, image string) error {
	return u.StartImageWith(branch, image, ImageOptions{})
}

// StartImageWith is StartImage with options: SwitchEdition is the
// explicit "switch edition" override, the one way an upgrade may pull
// another edition's build, and it is recorded on Status.EditionSwitch
// and in the daemon log. It needs an image to switch to.
func (u *Upgrader) StartImageWith(branch, image string, opts ImageOptions) error {
	if strings.TrimSpace(image) == "" {
		if opts.SwitchEdition {
			return errors.New("switching edition needs the image to switch to: name another edition's repository or image")
		}
		return u.Start(branch)
	}
	branch, ref, sw, err := u.validateImage(branch, image, opts)
	if err != nil {
		log.Printf("upgrade: refusing an upgrade to image %q: %v", image, err)
		return err
	}
	return u.startWithOptions(branch, ref, sw, true)
}

// editionSwitchFor is the edition switch an upgrade onto repo records,
// nil when it crosses no edition, or the refusal: another edition's
// repository without opts.SwitchEdition, or a switch asked of this
// edition's own repository, where there is nothing to switch.
func (u *Upgrader) editionSwitchFor(repo string, opts ImageOptions) (*EditionSwitch, error) {
	other, cross := u.otherEdition(repo)
	if !opts.SwitchEdition {
		if cross {
			return nil, u.crossEditionError(repo, other)
		}
		return nil, nil
	}
	mine, ok := u.deploymentEdition()
	if !ok {
		return nil, errors.New("this deployment has no install track to switch edition from")
	}
	if theirs, known := repositoryEdition(repo); known && theirs == mine {
		return nil, fmt.Errorf("%s already carries the %s edition's builds, which this deployment is on: there is no edition to switch to, so leave \"switch edition\" off", repo, editionName(mine))
	}
	sw := &EditionSwitch{From: editionName(mine), Repository: repo, RequestedBy: strings.TrimSpace(opts.RequestedBy)}
	if cross {
		sw.To = editionName(other)
	}
	return sw, nil
}

// overrideRef is the reference StartImage asked the running upgrade to
// pull, or "" for an ordinary one.
func (u *Upgrader) overrideRef() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.override
}

// start is Start after its branch has been checked, with ref the image
// to pull instead of the branch's tag ("" for none) and sw the edition
// switch the operator asked for (nil for none).
func (u *Upgrader) start(branch, ref string, sw *EditionSwitch) error {
	return u.startWithOptions(branch, ref, sw, true)
}

func (u *Upgrader) startWithOptions(branch, ref string, sw *EditionSwitch, manual bool) error {
	prevImg, prevRel, prevCommit := u.snapshotPrevious()

	u.mu.Lock()
	if u.done != nil {
		u.mu.Unlock()
		log.Printf("upgrade: %s: refused: %v", branch, ErrUpgradeInProgress)
		return ErrUpgradeInProgress
	}
	u.done = make(chan struct{})
	u.override = ref
	u.switching = sw
	u.cancelled = false
	u.cancelledBy = ""
	u.cancelReason = ""
	u.irreversible = false
	u.cancelFn = nil
	u.targetLabel = branch
	u.previousLabel = previousDisplayFromStatus(Status{
		Branch:          branch,
		PreviousImage:   prevImg,
		PreviousRelease: prevRel,
		PreviousCommit:  prevCommit,
	})
	u.mu.Unlock()

	now := time.Now().UTC()
	var graceUntil *time.Time
	graceWindow := time.Duration(0)
	if manual && u.GraceWindow() > 0 {
		graceWindow = u.GraceWindow()
		until := now.Add(graceWindow)
		graceUntil = &until
	}
	status := Status{
		Branch:          branch,
		Phase:           PhaseRunning,
		StartedAt:       &now,
		GraceUntil:      graceUntil,
		PreviousImage:   prevImg,
		PreviousRelease: prevRel,
		PreviousCommit:  prevCommit,
		Process:         processID,
	}
	if sw != nil {
		recorded := *sw
		status.EditionSwitch = &recorded
	}
	if err := writeStatus(u.cfg.StatusFile, status); err != nil {
		u.release()
		log.Printf("upgrade: %s: not started: recording upgrade status in %s: %v", branch, u.cfg.StatusFile, err)
		return fmt.Errorf("recording upgrade status: %w", err)
	}
	// Everything from here on says which branch it is about
	// (progress.logf); this one line is what ties the request that
	// arrived to the sequence of steps that follows it.
	switch {
	case sw != nil:
		to := sw.To
		if to == "" {
			to = "whichever edition the image says it is"
		}
		who := "the operator"
		if sw.RequestedBy != "" {
			who = sw.RequestedBy
		}
		log.Printf("upgrade: %s: requested, switching edition from %s to %s at %s's explicit request, pulling %s",
			branch, sw.From, to, who, ref)
	case ref != "":
		log.Printf("upgrade: %s: requested, pulling %s", branch, ref)
	default:
		log.Printf("upgrade: %s: requested", branch)
	}

	go u.run(branch, status, graceWindow)
	return nil
}

// registryHint is what a failed pull of ref adds to its error so an
// operator knows what to do about it: authenticate this host, or pick
// another repository, for a registry that refused the credentials; or
// pick another tag for one that has never published this one. Empty for
// any other failure, whose own words are already the best account.
//
// The repository it suggests instead is own, this deployment's own
// edition's default, and none when that is the repository that failed:
// it used to name the open-source repository to every deployment, which
// on another edition's track is an invitation to cross editions
// (grain/task-4153). Nothing is pulled from it: the upgrade has failed,
// and a hint is all this is.
func registryHint(ref, own string, err error) string {
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
		hint := fmt.Sprintf("the registry refused this host's credentials for %s: authenticate the host to %s (e.g. `docker login`, or gcloud auth configure-docker for Artifact Registry)",
			ref, registryHost(ref))
		if own = strings.TrimSpace(own); own != "" && own != repositoryOf(ref) && registryauth.HostOf(own) != "" {
			hint += fmt.Sprintf(", or choose %s on the upgrade pane", own)
		}
		return hint
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
