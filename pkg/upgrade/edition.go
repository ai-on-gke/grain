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

// An upgrade never crosses editions by accident (grain/task-4153). A
// deployment on a registered install track runs that edition's build,
// and the edition links in things the open-source build does not have --
// capability servers, install-track defaults -- so an upgrade onto the
// open-source image (or the reverse) restarts the deployment as a
// different product: whatever the other build lacks is simply gone until
// somebody upgrades back, and every restart in between loses the runs in
// flight.
//
// Two checks hold that line, one on each side of the pull:
//
//   - the repository: an image in another edition's repository
//     (installtrack.RepositoryTrack) is not offered on the pane
//     (ImageTarget.Repositories), and is refused by Start, StartImage and
//     ValidateBranch;
//   - the build: the verify step asks the pulled image which edition it
//     is (`grain version --json`'s "edition", ImageConfig.EditionArgs)
//     before anything cuts over, which catches a repository this build
//     cannot classify.
//
// The one way across is StartImageWith with SwitchEdition: an explicit,
// labelled "switch edition" on the pane, behind a confirmation naming
// what is lost (EditionSwitchLosses), and recorded on the upgrade's
// Status (EditionSwitch) and in the daemon log.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/ai-on-gke/grain/pkg/installtrack"
)

// ErrCrossEdition is what an upgrade onto another edition's image is
// refused with when nobody asked to switch edition.
var ErrCrossEdition = errors.New("refusing an upgrade onto another edition's build")

// EditionSwitch records an upgrade the operator explicitly asked to cross
// editions. Editions are named as the pane shows them: "oss" for the
// open-source build, otherwise the install track's own name.
type EditionSwitch struct {
	// From is the edition this deployment was on.
	From string `json:"from"`
	// To is the edition it was asked to move to: the target repository's
	// (installtrack.RepositoryTrack), or the pulled image's own stamp
	// when the repository says nothing. Empty until either can say.
	To string `json:"to,omitempty"`
	// Repository is the repository the switch pulls from.
	Repository string `json:"repository"`
	// RequestedBy is who asked, as the request named them; empty for the
	// operator at the console.
	RequestedBy string `json:"requestedBy,omitempty"`
}

// ImageOptions are StartImageWith's knobs beyond the image itself.
type ImageOptions struct {
	// SwitchEdition allows the image to be another edition's build. It
	// is the explicit "switch edition" override and nothing else sets
	// it; the switch is recorded on Status.EditionSwitch.
	SwitchEdition bool
	// RequestedBy is recorded on Status.EditionSwitch.
	RequestedBy string
}

// EditionRepository is a repository ImageTarget lists only behind the
// pane's "switch edition" override, with the edition it carries.
type EditionRepository struct {
	Repository string `json:"repository"`
	Edition    string `json:"edition"`
}

// editionName is ed as the pane names it: "oss" for core.
func editionName(ed string) string {
	if ed = normalizeEdition(ed); ed != "" {
		return ed
	}
	return string(installtrack.TrackOSS)
}

// deploymentEdition is the edition this deployment's install track is
// (normalized: "" for open source), and false when it has no track to
// go by -- the source pipeline, or an image configuration that names
// none -- in which case nothing here is checked.
func (u *Upgrader) deploymentEdition() (string, bool) {
	if u.cfg.Image == nil || u.cfg.Image.Track == "" {
		return "", false
	}
	return normalizeEdition(string(u.cfg.Image.Track)), true
}

// repositoryEdition is the edition repo carries (normalized), and false
// when this build cannot say.
func repositoryEdition(repo string) (string, bool) {
	track, ok := installtrack.RepositoryTrack(repositoryOf(strings.TrimSpace(repo)))
	if !ok {
		return "", false
	}
	return normalizeEdition(string(track)), true
}

// otherEdition reports the edition repo carries when it is known and is
// not this deployment's own.
func (u *Upgrader) otherEdition(repo string) (string, bool) {
	mine, ok := u.deploymentEdition()
	if !ok {
		return "", false
	}
	theirs, ok := repositoryEdition(repo)
	if !ok || theirs == mine {
		return "", false
	}
	return theirs, true
}

// crossEditionError is the refusal for an upgrade onto repo, which
// carries edition other's builds, and how to get what was wanted.
func (u *Upgrader) crossEditionError(repo, other string) error {
	mine, _ := u.deploymentEdition()
	own := installtrack.DefaultRepository(u.cfg.Image.Track)
	return fmt.Errorf("%w: %s carries %s builds, and this deployment is on the %s install track. "+
		"Upgrade from a %s repository (this track's default is %s), or use \"switch edition\" on the upgrade pane to move to the %s edition on purpose",
		ErrCrossEdition, repo, editionName(other), editionName(mine), editionName(mine), own, editionName(other))
}

// checkConfiguredEdition refuses an ordinary upgrade whose repository --
// the one effectiveRepository settles on -- carries another edition's
// builds: an -upgrade-image naming the other edition's repository. It
// is not a fallback to anything: the upgrade fails, and the error says
// what to set instead.
func (u *Upgrader) checkConfiguredEdition() error {
	if u.Mode() != ModeImage {
		return nil
	}
	repo, err := u.effectiveRepository()
	if err != nil || repo == "" {
		// Not this check's to report: the pull says what is wrong with
		// a repository that cannot be worked out.
		return nil
	}
	if other, cross := u.otherEdition(repo); cross {
		return u.crossEditionError(repo, other)
	}
	return nil
}

// EditionSwitchLosses is what a deployment on install track from gives
// up by switching to edition to, in the words the pane's confirmation
// shows: the track's own SwitchAwayLoses when it names them, then what
// every switch costs.
func EditionSwitchLosses(from installtrack.Track, to string) []string {
	fromName := editionName(string(from))
	toName := editionName(to)
	var losses []string
	if normalizeEdition(string(from)) != "" {
		if named := installtrack.SwitchAwayLoses(from); len(named) > 0 {
			losses = append(losses, named...)
		} else {
			losses = append(losses, fmt.Sprintf("the capability servers and other integrations the %s edition links in: the %s build does not have them, so tasks that rely on them fail until you switch back", fromName, toName))
		}
	}
	losses = append(losses,
		fmt.Sprintf("the %s install track's defaults: this host is still on the %s track, so the next ordinary upgrade pulls from %s again unless you reinstall it as %s",
			fromName, fromName, installtrack.DefaultRepository(from), toName),
		"every run in flight, lost to the restart (and to the one that switches back)")
	return losses
}

// imageEdition asks the pulled image ref which edition it is: the
// "edition" its `grain version --json` reports (ImageConfig.EditionArgs),
// normalized, "" for core. known is false when the image could not
// answer -- a build older than the question, or output that is not the
// JSON asked for -- which is not an error: an older build can still be
// rolled back to. err is this host failing to put the question at all.
func (u *Upgrader) imageEdition(ctx context.Context, p *progress, ref string) (edition string, known bool, err error) {
	argv := append(u.dockerRunArgs(), ref)
	argv = append(argv, u.cfg.Image.EditionArgs...)
	out, err := runAsked(ctx, p, stepVerify, imageRunTimeout, argv)
	if err != nil {
		if ranAndFailed(err) {
			p.logf("%s: %s did not say which edition it is (%v)", stepVerify, ref, err)
			return "", false, nil
		}
		return "", false, err
	}
	start := strings.IndexByte(out, '{')
	if start < 0 {
		p.logf("%s: %s did not say which edition it is: no JSON in %q", stepVerify, ref, strings.TrimSpace(out))
		return "", false, nil
	}
	var stamp struct {
		Edition string `json:"edition"`
	}
	if err := json.NewDecoder(bytes.NewReader([]byte(out[start:]))).Decode(&stamp); err != nil {
		p.logf("%s: %s did not say which edition it is: %v", stepVerify, ref, err)
		return "", false, nil
	}
	return normalizeEdition(stamp.Edition), true, nil
}

// checkImageEdition is the verify step's edition half: the pulled image
// ref must be this deployment's edition's build unless this upgrade is
// an explicit edition switch. It returns the note the step reports, or
// the refusal; the ref file is untouched either way, so a refusal
// leaves the deployment on the image it already runs.
func (u *Upgrader) checkImageEdition(ctx context.Context, p *progress, ref string) (string, error) {
	mine, ok := u.deploymentEdition()
	if !ok || len(u.cfg.Image.EditionArgs) == 0 {
		return "", nil
	}
	got, known, err := u.imageEdition(ctx, p, ref)
	if err != nil {
		return "", fmt.Errorf("asking %s which edition it is, so this deployment stays on the image it already runs: %w", ref, err)
	}
	if !known {
		return "its edition is unknown (the build did not say)", nil
	}
	sw := u.switchingEdition()
	if got == mine {
		return "the " + editionName(got) + " edition's build", nil
	}
	if sw == nil {
		return "", fmt.Errorf("%w: %s is the %s edition's build, and this deployment is on the %s install track; "+
			"leaving it on the image it already runs (choose a %s image, or use \"switch edition\" on the upgrade pane to move editions on purpose)",
			ErrCrossEdition, ref, editionName(got), editionName(mine), editionName(mine))
	}
	u.mu.Lock()
	if u.switching != nil && u.switching.To == "" {
		u.switching.To = editionName(got)
	}
	u.mu.Unlock()
	p.recordEditionSwitch(u.switchingEdition())
	log.Printf("upgrade: switching edition: %s is the %s edition's build; this deployment was on %s", ref, editionName(got), editionName(mine))
	return fmt.Sprintf("the %s edition's build: switching edition from %s, as asked", editionName(got), editionName(mine)), nil
}

// switchingEdition is a copy of the edition switch the upgrade in flight
// was asked to make, or nil.
func (u *Upgrader) switchingEdition() *EditionSwitch {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.switching == nil {
		return nil
	}
	sw := *u.switching
	return &sw
}
