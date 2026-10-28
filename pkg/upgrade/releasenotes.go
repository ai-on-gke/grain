package upgrade

// What an upgrade onto a target would bring, in that target's own words
// (grain/task-3092): the per-version release notes the target build
// carries (pkg/releasenotes), from the one after this deployment's
// version up to the target's own. The pane asks for them when an
// operator chooses a target, before anything is started.
//
// The target is asked the way an upgrade asks it everything else -- a
// `docker run --rm <image> release-notes --from <running> --json` on
// the container path, a pod on the Kubernetes one -- after the same
// pull and the same signature check an upgrade would make, so a build
// nobody could upgrade onto is never run to preview it. The source path
// builds nothing until it upgrades, so it reads the same files out of
// the fetched branch instead: they are what that build would embed.
//
// A target that cannot answer is not a failure. A build older than the
// subcommand refuses it, and a build cut before its own version's notes
// were written carries none for itself -- every candidate image, until
// promotion rebuilds it (#3064). Both are said as NotInThisBuild.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/releasenotes"
	"github.com/ai-on-gke/grain/pkg/version"
)

// NotInThisBuild is what the pane says for a target with no notes to
// show for what it brings.
const NotInThisBuild = "release notes are not in this build"

// ReleaseNotesPreview is GET /api/upgrade/release-notes's answer.
type ReleaseNotesPreview struct {
	Branch string `json:"branch"`
	// Ref is the image that was asked; empty on the source path.
	Ref string `json:"ref,omitempty"`
	// From is the version this deployment runs, whose notes and older
	// are left out; empty when this build carries no version stamp, and
	// then every note the target has is shown.
	From string `json:"from,omitempty"`
	// Version is the target build's own version, as it reported it;
	// empty for a build with no stamp (a merge's build, a branch).
	Version string `json:"version,omitempty"`
	// Notes is every version newer than From, up to the target's own,
	// newest first. Never nil.
	Notes []releasenotes.Note `json:"notes"`
	// Missing is the release the target is a build of when it carries
	// no notes for it -- "v1.9.0" for a v1.9.0-rc.2 image.
	Missing string `json:"missing,omitempty"`
	// NotInThisBuild is true when the target had nothing to say about
	// what it brings -- no subcommand, or no notes for its own version
	// -- and Message then says which.
	NotInThisBuild bool `json:"notInThisBuild,omitempty"`
	// Message is a sentence for the pane, when there is one to say.
	Message string `json:"message,omitempty"`
}

// releaseNotesStep names the preview's commands in the daemon log.
const releaseNotesStep = "release notes"

// releaseNotesCacheFor is how long a preview is reused. Choosing the
// same target twice should not pull and run it twice; a branch's image
// moves, though, so not for long.
const releaseNotesCacheFor = 5 * time.Minute

// releaseNotesPod is the pod a Kubernetes preview asks in -- apart from
// kubeAskPod, so a preview cannot delete the pod an upgrade is asking.
const releaseNotesPod = "grain-upgrade-release-notes"

type releaseNotesEntry struct {
	at      time.Time
	preview ReleaseNotesPreview
}

func (u *Upgrader) runningRelease() string {
	if u.cfg.RunningRelease != nil {
		return u.cfg.RunningRelease()
	}
	return version.Get().Release
}

// ReleaseNotes previews what an upgrade onto branch would bring. The
// error is for a branch this deployment would not upgrade onto, and for
// a target that could not be asked at all -- a pull that failed, a
// signature that did not verify; a target that was asked and had
// nothing to say comes back as a preview saying so.
func (u *Upgrader) ReleaseNotes(ctx context.Context, branch string) (ReleaseNotesPreview, error) {
	branch = strings.TrimSpace(branch)
	if err := u.checkBranch(branch); err != nil {
		return ReleaseNotesPreview{}, err
	}
	// One at a time: two previews of the same target would pull it
	// twice, and the Kubernetes one has a single pod name.
	u.notesMu.Lock()
	defer u.notesMu.Unlock()
	if e, ok := u.notes[branch]; ok && time.Since(e.at) < releaseNotesCacheFor {
		return e.preview, nil
	}

	from := u.runningRelease()
	preview := ReleaseNotesPreview{Branch: branch, From: from, Notes: []releasenotes.Note{}}
	if len(u.cfg.ReleaseNotesArgs) == 0 {
		preview.NotInThisBuild = true
		preview.Message = NotInThisBuild + ": this deployment does not ask builds for their release notes"
		return preview, nil
	}
	args := append([]string{}, u.cfg.ReleaseNotesArgs...)
	if from != "" {
		args = append(args, "--from", from)
	}
	p := &progress{prefix: "upgrade: " + branch + ": " + releaseNotesStep + ": ", current: -1}

	var report releasenotes.Report
	var refused error
	var err error
	switch u.Mode() {
	case ModeKube:
		preview.Ref, report, refused, err = u.kubeReleaseNotes(ctx, p, branch, args)
	case ModeImage:
		preview.Ref, report, refused, err = u.imageReleaseNotes(ctx, p, branch, args)
	default:
		report, err = u.sourceReleaseNotes(ctx, p, branch, from)
	}
	if err != nil {
		return ReleaseNotesPreview{}, err
	}
	if refused != nil {
		preview.NotInThisBuild = true
		preview.Message = NotInThisBuild + ": it does not answer `grain release-notes`, which a build older than the subcommand does"
		p.logf("%s refused the question: %v", branch, refused)
	} else {
		fillPreview(&preview, report)
	}
	if u.notes == nil {
		u.notes = map[string]releaseNotesEntry{}
	}
	u.notes[branch] = releaseNotesEntry{at: time.Now(), preview: preview}
	return preview, nil
}

// fillPreview reads a target's answer into the preview.
func fillPreview(preview *ReleaseNotesPreview, report releasenotes.Report) {
	preview.Version = report.Version
	// The target answered for --from already; this is the same range
	// again, so a build that ignored the flag is not shown everything.
	preview.Notes = releasenotes.Between(report.Notes, preview.From, "")
	if preview.Notes == nil {
		preview.Notes = []releasenotes.Note{}
	}
	if own := releasenotes.Release(report.Version); own != "" {
		// Not newer comes first: every build is versioned now
		// (scripts/build-version.sh), so a main build running
		// v2.3.0-dev.114+<sha> asked about latest's v2.2.0 -- or about an
		// older main build -- is told it would go backwards, rather than
		// that v2.2.0's or v2.3.0's notes are missing.
		if preview.From != "" && releasenotes.Compare(report.Version, preview.From) <= 0 {
			preview.Message = fmt.Sprintf("%s is not newer than %s, which this deployment runs", report.Version, preview.From)
			return
		}
		has := false
		for _, n := range report.Notes {
			has = has || n.Version.Version == own || releasenotes.Release(n.Version.Version) == own
		}
		if !has && releasenotes.Compare(own, preview.From) > 0 {
			preview.Missing = own
			preview.NotInThisBuild = true
			preview.Message = fmt.Sprintf("%s for %s: an image is built before the release cut writes its version's notes", NotInThisBuild, own)
			return
		}
	}
	if len(preview.Notes) == 0 {
		preview.NotInThisBuild = true
		preview.Message = NotInThisBuild
		if preview.From != "" {
			preview.Message += " for anything newer than " + preview.From
		}
	}
}

// parseReport reads the subcommand's JSON. A pod's answer comes back
// through its logs, so anything a build logged before it is skipped.
func parseReport(out string) (releasenotes.Report, error) {
	var report releasenotes.Report
	start := strings.Index(out, "{")
	if start < 0 {
		return report, fmt.Errorf("no JSON in the answer: %q", truncate(out, 200))
	}
	if err := json.NewDecoder(strings.NewReader(out[start:])).Decode(&report); err != nil {
		return report, fmt.Errorf("reading the answer: %w", err)
	}
	return report, nil
}

// imageReleaseNotes pulls, pins and verifies the target exactly as
// stageImage does, then asks it.
func (u *Upgrader) imageReleaseNotes(ctx context.Context, p *progress, branch string, args []string) (ref string, report releasenotes.Report, refused, err error) {
	tag, err := u.imageRef(branch)
	if err != nil {
		return "", report, nil, err
	}
	if err := u.pullImage(ctx, p, stepPull, tag); err != nil {
		if registryHasNoSuchTag(err) {
			return "", report, nil, fmt.Errorf("the registry has no image %s", tag)
		}
		return "", report, nil, fmt.Errorf("pulling %s: %w", tag, err)
	}
	if ref, err = u.pinImage(ctx, p, stepPull, tag); err != nil {
		return "", report, nil, fmt.Errorf("pulling %s: %w", tag, err)
	}
	if len(u.cfg.Image.VerifyCmd) > 0 {
		if err := verifyPinned(ctx, p, stepVerify, u.cfg.Image.VerifyCmd, ref); err != nil {
			return ref, report, nil, fmt.Errorf("%w; not running it to read its release notes", err)
		}
	}
	out, err := runAsked(ctx, p, releaseNotesStep, imageRunTimeout, imageRunner{u: u, ref: ref}.run(nil, args))
	if err != nil {
		if ranAndFailed(err) {
			return ref, report, err, nil
		}
		return ref, report, nil, fmt.Errorf("asking %s for its release notes: %w", ref, err)
	}
	if report, err = parseReport(out); err != nil {
		// It ran, exited 0 and printed something else: not a build that
		// knows the question.
		return ref, report, err, nil
	}
	return ref, report, nil, nil
}

// registryHasNoSuchTag is whether a failed pull failed because the
// registry has no such tag -- the image-path twin of the source path's
// "origin has no branch": a version typed wrong is the failure the pane
// sees most, and docker's own words for it ("failed to resolve reference
// ...: not found", or a registry's "manifest unknown") read as an outage
// (the 10-15 install walkthrough).
func registryHasNoSuchTag(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "manifest unknown") ||
		strings.Contains(msg, "failed to resolve reference") && strings.Contains(msg, ": not found")
}

// kubeReleaseNotes verifies the target as stageKube does, then asks it
// in a pod under the Deployment's own security context.
func (u *Upgrader) kubeReleaseNotes(ctx context.Context, p *progress, branch string, args []string) (ref string, report releasenotes.Report, refused, err error) {
	ref, err = u.imageRefIn(u.cfg.Kube.Repository, branch)
	if err != nil {
		return "", report, nil, err
	}
	client := u.cfg.Kube.client()
	security, err := client.DeploymentSecurity(ctx, u.cfg.Kube.Deployment, u.cfg.Kube.container())
	if err != nil {
		return ref, report, nil, fmt.Errorf("reading the security context deployment/%s runs %s under: %w", u.cfg.Kube.Deployment, u.cfg.Kube.container(), err)
	}
	if len(u.cfg.Kube.VerifyCmd) > 0 {
		if ref, err = verifyTag(ctx, p, stepVerify, u.cfg.Kube.VerifyCmd, ref); err != nil {
			return ref, report, nil, fmt.Errorf("%w; not running it to read its release notes", err)
		}
	}
	out, err := kubeRunner{u: u, ref: ref, security: security, pod: releaseNotesPod}.ask(ctx, p, args)
	if err != nil {
		if errors.Is(err, errCouldNotAsk) {
			return ref, report, nil, fmt.Errorf("asking %s for its release notes: %w", ref, err)
		}
		return ref, report, err, nil
	}
	if report, err = parseReport(out); err != nil {
		return ref, report, err, nil
	}
	return ref, report, nil, nil
}

// sourceReleaseNotes reads the notes out of branch as origin has it.
// Fetched into a ref of its own rather than FETCH_HEAD or
// origin/<branch>, so a preview moves nothing the upgrade's own checkout
// reads; and read with git rather than out of the working tree, which
// stays on whatever this deployment last built.
func (u *Upgrader) sourceReleaseNotes(ctx context.Context, p *progress, branch, from string) (releasenotes.Report, error) {
	var report releasenotes.Report
	if u.cfg.SrcDir == "" {
		return report, errors.New("no source checkout configured")
	}
	const dir = "docs/releases"
	const previewRef = "refs/grain/release-notes-preview"
	ctx, cancel := context.WithTimeout(ctx, imageRunTimeout)
	defer cancel()
	git := func(args ...string) (string, error) {
		cmd := newCommand(ctx, "", "git", append([]string{"-C", u.cfg.SrcDir}, args...)...)
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return string(out), nil
	}
	remoteBranch := sourceBranch(branch)
	p.logf("fetching %s to read its release notes", remoteBranch)
	if _, err := git("fetch", "--quiet", "--no-tags", "--end-of-options", "origin", "+refs/heads/"+remoteBranch+":"+previewRef); err != nil {
		// A branch typed wrong is the failure the pane sees most, and
		// git's exit status alone does not say that is what it was.
		// Only that one answer is read out of git's stderr: the rest of
		// it can name the remote's URL, credentials and all.
		var exit *exec.ExitError
		if errors.As(err, &exit) && strings.Contains(string(exit.Stderr), "couldn't find remote ref") {
			return report, fmt.Errorf("origin has no branch %s", remoteBranch)
		}
		return report, fmt.Errorf("fetching %s: %w", remoteBranch, err)
	}
	listing, err := git("ls-tree", "--name-only", "--end-of-options", previewRef, dir+"/")
	if err != nil {
		return report, err
	}
	var all []releasenotes.Note
	for _, name := range strings.Split(strings.TrimSpace(listing), "\n") {
		base := path.Base(name)
		if !strings.HasPrefix(base, "v") || !strings.HasSuffix(base, ".md") {
			continue
		}
		if releasenotes.Release(strings.TrimSuffix(base, ".md")) == "" {
			continue
		}
		data, err := git("show", "--end-of-options", "refs/grain/release-notes-preview:docs/releases/"+base)
		if err != nil {
			return report, err
		}
		all = append(all, releasenotes.Parse(base, []byte(data)))
	}
	slices.SortStableFunc(all, func(a, b releasenotes.Note) int { return releasenotes.Compare(b.Version.Version, a.Version.Version) })
	report.Notes = releasenotes.Between(all, from, "")
	return report, nil
}
