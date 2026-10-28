package upgrade

// The container-deployment half of this package (bwsalmon/agents#645).
//
// A deployment installed by scripts/setup.sh no longer builds a
// binary on the host it runs on: CI publishes one image per commit
// (../../.github/workflows/build-artifacts.yml), and the host pulls it.
// That leaves the checkout/build/install pipeline in upgrade.go with
// nothing to do on such a host -- there is no checkout to fetch into, no
// toolchain to build with, and the binary at InstallPath is not what the
// service runs -- so Config.Image switches Start onto the three steps
// that mean the same thing for an image:
//
//	pull the tag CI published for that branch, and pin what it pulled
//	to its digest
//	run it once, as a health check, before anything cuts over to it
//	point the deployment's image ref file at it, and restart
//
// It is strictly simpler than the binary path, and one difference is
// worth naming: there is no rollback. The binary path installs over the
// running binary and so has to be able to put the old one back if the
// new one fails its health check; here the health check runs against the
// pulled image *before* the ref file is touched at all, so a failure
// leaves the deployment pointing exactly where it already pointed, with
// nothing to undo.

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/installtrack"
)

// ImageConfig, set on Config.Image, makes an Upgrader upgrade a
// container deployment: pull, health-check and record an image tag
// instead of fetching, building and installing a binary. SrcDir,
// BuildCmd, BuiltBinary and InstallPath are all unused when it is set;
// HealthCheckArgs, Timeout, RestartCmd and StatusFile mean exactly what
// they do for the binary path.
type ImageConfig struct {
	// Repository is the image CI publishes a tag per branch to, with no
	// tag of its own -- "ghcr.io/bwsalmon/grain/grain". Required.
	Repository string
	// RefFile is the file naming the image the deployment's service
	// actually runs, which a successful upgrade rewrites: one
	// `GRAIN_IMAGE=<ref>` line, so that grain-daemon.service can read it
	// as an EnvironmentFile and interpolate it straight into its
	// ExecStart (scripts/setup.sh's write_systemd_units). The
	// indirection is the whole mechanism -- the unit names a *file*
	// rather than a tag, so an upgrade is a file write plus a restart
	// and never a re-write of the unit itself. Required.
	RefFile string
	// PullCmd fetches an image ref appended as its last argument;
	// {"docker", "pull"} when left nil. Overridable so a test can swap
	// in something that needs no docker daemon.
	PullCmd []string
	// RunCmd runs an image, with the ref and then HealthCheckArgs
	// appended; {"docker", "run", "--rm"} when left nil. The image's
	// own ENTRYPOINT is `grain` (Dockerfile), so appending
	// HealthCheckArgs -- {"schema-version"} on a real deployment -- runs
	// exactly the same subcommand the binary path's health check runs,
	// against the image about to be cut over to.
	RunCmd []string
	// ListCmd lists images for a repository, appended as its last argument,
	// one per line: the `repo:tag` cleanup would remove, optionally followed
	// by other names for the same image (its `repo@digest`), which are only
	// compared against what cleanOldImages keeps, and optionally ending in
	// the image's ID, which is what an image with neither a tag nor a
	// digest can only be removed by;
	// {"docker", "image", "ls", "--format", "{{.Repository}}:{{.Tag}} {{.Repository}}@{{.Digest}} {{.ID}}"}
	// when left nil. Overridable so a test can swap in something that needs
	// no docker daemon.
	ListCmd []string
	// ContainerImagesCmd prints the IDs of the images running containers
	// were started from, one per line; cleanOldImages never removes an
	// image by ID that one of them uses. Defaults to `docker ps` piped
	// into `docker container inspect --format {{.Image}}`. Overridable
	// so a test can swap in something that needs no docker daemon.
	ContainerImagesCmd []string
	// DigestCmd prints the repo digests of a pulled image ref, appended
	// as its last argument, one per line;
	// {"docker", "image", "inspect", "--format", "{{range .RepoDigests}}{{println .}}{{end}}"}
	// when left nil. It is what pins an upgrade to the image it pulled
	// rather than to the tag it pulled it by -- see pinImage. Overridable
	// so a test can swap in something that needs no docker daemon.
	DigestCmd []string
	// VerifyCmd checks who published an image: run with
	// `repo@sha256:...` appended -- the digest pinImage pinned, never a
	// tag -- and taken as verified only if it exits 0. On a real
	// deployment it is `cosign verify --key <release public key>`
	// (docs/image-signing.md). It runs after the pull and before the
	// health check, which is the first thing to run the image, and again
	// for the sandbox image the new build names before that is recorded
	// as pulled. A failure stops the upgrade with the ref file untouched.
	//
	// Nil skips the check, and the signature step says so on every
	// upgrade: see verify.go for why that warns rather than refuses.
	VerifyCmd []string
	// RemoveCmd removes an image ref, appended as its last argument;
	// {"docker", "image", "rm"} when left nil. Overridable so a test can
	// swap in something that needs no docker daemon. Never give it -f:
	// docker's refusal to remove an image a container uses, or an ID
	// several repositories name, is the cleanup's second line of defence.
	RemoveCmd []string
	// SandboxImageArgs, when non-empty, asks the newly pulled image
	// which *sandbox* container it expects -- {"sandbox-image"} on a
	// real deployment, pkg/cli/sandboximage.go's own subcommand -- and
	// pulls that too, before anything cuts over.
	//
	// A granule deployment runs two images from the same commit: grain,
	// and the sandbox container each task's VM runs inside. Upgrading
	// one without the other would leave a deployment whose next
	// dispatched task reaches for an image nothing ever fetched, so the
	// two move together or not at all -- a failure here stops the
	// upgrade with the ref file untouched, exactly like a failed health
	// check.
	//
	// Left nil by a deployment that dispatches into host directories:
	// there is no sandbox container in that shape to keep in step, and
	// pulling one it will never run would be a slow no-op with a real
	// failure mode of its own.
	SandboxImageArgs []string
	// SandboxImagePin is the sandbox image this deployment's own
	// configuration pins on purpose, when it pins one --
	// pkg/cli/daemon.go's chooseSandboxImage, which is also what
	// decides that a pin is deliberate rather than left over from an
	// older install. A deliberate one wins over whatever the newly pulled
	// image names.
	//
	// Set, and different from what SandboxImageArgs asked for, it makes
	// the sandbox step say so: the pull it has just paid for is not the
	// image this deployment's VMs will be created from. Without that the
	// step reports a successful pull of a guest nothing then runs, which
	// is exactly how a deployment ends up on a daemon from one commit and
	// sandboxes from another without anybody being told (grain/task-306).
	//
	// Empty for a deployment that pins nothing, which is the case the
	// stamp was built for and has nothing to report.
	SandboxImagePin string
	// UnitFlags are the names of the flags this deployment's own service
	// was started with -- "-data-dir", "-granule-sandboxes" -- which on a
	// host deployment is exactly what grain-daemon.service's ExecStart
	// passes, since this process is what that ExecStart started
	// (pkg/cli/daemon.go hands over the flags it saw, undefined ones
	// included).
	//
	// It is here because the unit and the image drift apart by design.
	// scripts/setup.sh writes the ExecStart once, at install time, and
	// an upgrade only rewrites RefFile above -- so the flags stay as the
	// installer of the day left them while the image behind them moves
	// with every upgrade and every rollback. A build that dropped a flag
	// meets a unit that still passes it, and a rollback meets a unit
	// written for something newer. That is the mismatch that crash-looped
	// this deployment 52 times on 2026-09-18 (grain/task-233).
	//
	// Checked, not enforced: see checkImageFlags for why a disagreement
	// does not stop the upgrade.
	//
	// Empty leaves the step out entirely, which is right for a
	// deployment whose flags are not written down on the host it runs on.
	UnitFlags []string
	// FlagNamesArgs asks a pulled image which flags its daemon accepts --
	// {"daemon", "-flag-names"} on a real deployment,
	// pkg/cli/daemon_unknown_flags.go's printDaemonFlagNames, one name
	// per line. Nil leaves the check out, the same way UnitFlags does.
	FlagNamesArgs []string
	// TargetRepository, when set, shifts the deployment from Repository to
	// TargetRepository on the next upgrade, provided CanTransitionRepository
	// permits the move (e.g. from LegacyGrainRepository to the post-split
	// Google or open-source repository, or an entry in AllowedRepositories).
	TargetRepository string
	// AllowedRepositories lists additional image repositories this
	// deployment is permitted to transition to from Repository.
	AllowedRepositories []string
	// Track classifies the deployment as a registered installtrack track or
	// installtrack.TrackOSS; when Repository is empty, DefaultRepository(Track)
	// supplies the default image repository.
	Track installtrack.Track
}

// imageRefEnvKey is the variable name written into ImageConfig.RefFile,
// and the one grain-daemon.service's ExecStart interpolates.
const imageRefEnvKey = "GRAIN_IMAGE"

// imageRunTimeout bounds each of the short questions this path puts to
// the image it has just pulled: the health check, which sandbox image
// the new build expects, which daemon flags it defines, which schema it
// knows, and the `docker image ls` the cleanup reads.
//
// It used to be ten seconds, matched to the bound healthCheck puts on
// the binary path's equivalent run, and that match was the bug. On the
// binary path the subcommand *is* the command: `grain schema-version`
// touches no store and needs no config, so ten seconds bounds a binary
// that hangs instead of exiting and nothing else. Here the same
// subcommand is the small end of a whole container lifetime, and it is
// the container, not the answer, that overruns.
//
// grain/task-329 raised it to two minutes on that reasoning, and two
// minutes then reddened .grain/workflows/release.yml's pre-publish gate
// twice more. So this value is measured rather than argued, on a
// four-CPU sandbox of the shape that job runs in, against an image
// built out of this Dockerfile (1.66 GB, 11 layers):
//
//   - `grain schema-version` itself, exec'd into a container that is
//     already up: ~180ms. The subcommand is not slow and has not
//     regressed -- a bigger bound hides nothing here.
//   - the whole `docker run --rm <image> schema-version` on an idle
//     host: 1.2-2.2s.
//   - the same lifecycle on debian:bookworm-slim running /bin/true:
//     1.2-2.5s, the same again. So neither the gigabytes nor the layer
//     count is what this bounds -- it is docker's own per-container
//     cost, and the teardown `--rm` pays for is ~150ms of it. A cold
//     page cache adds nothing measurable either (~3s).
//   - the same run on a *contended* host: median ~40s, and one sample
//     in four over two minutes, the worst at 211s.
//
// That last line is the whole point. Contention does not slow this down
// by some factor you can budget for; it gives it a heavy tail while the
// median stays seconds. A bound of two minutes sat inside that tail, so
// it did not fail when the host was busy -- it failed when one run in
// some number drew the long straw, which is exactly the shape the gate
// flaked in: a different test of tests/container each time, every other
// test in the suite passing. release.yml builds three multi-gigabyte
// images and then drives that suite on the same machine, so the tail is
// where those runs live.
//
// Five minutes rather than ten, which is what parity with
// DefaultKubeHealthCheckTimeout would suggest, because that bound
// covers one question put to one pod while this one is charged four
// times over in a single staging -- the health check, the sandbox
// image, the flag names and the schema version -- before
// schemaCheckTimeout's rehearsal is added. Four times five plus that
// five is twenty-five minutes of Config.Timeout's forty-five, leaving
// twenty for the pull those all follow; four times ten would be the
// whole budget and leave the pull none.
// TestTheContainerPathsBoundLeavesRoomForThePull holds that sum down.
//
// Erring long is still right, on the same asymmetry restartTimeout
// reads: a bound that is too long delays a genuinely wedged image by
// minutes inside an upgrade that was going to fail anyway, and one that
// is too short is a failed release and a deployment left on its old
// build. A var, like restartTimeout and for the same reason: a test
// proving the bound exists cannot sit through it.
var imageRunTimeout = 5 * time.Minute

// maxTagLen is the registry limit on a tag: 128 characters. A branch
// long enough to exceed it has no image published for it at all (CI
// would have failed to push the tag), so resolving one is reported here
// rather than left to a pull that would 404 with a less obvious reason.
const maxTagLen = 128

// TagForBranch is the tag CI publishes for a branch: the branch name
// with each '/' replaced by "..", since a docker tag may not contain a
// slash and grain's own branches routinely do (grain/issue-642,
// claude/some-task).
//
// ".." rather than the '-' this used to substitute, because the
// substitution has to be injective: with '-', claude/task-123 and
// claude-task-123 were one tag, so whoever could push the second branch
// could publish the image an upgrade onto the first one pulled (#2386).
// A '-' is an ordinary character in a branch name; ".." is the one
// sequence git refuses in one (git-check-ref-format), and a '/' can
// neither be doubled nor be followed by a '.', so in a tag made from a
// valid branch a run of one dot was '.', two were '/', three were "./",
// and no other run can occur. That only holds for names git would
// accept, which is why imageRefIn refuses the rest (validateBranch)
// before anything is pulled.
//
// ../../scripts/release-images.sh, ../../.github/workflows/build-artifacts.yml
// and ../../scripts/setup.sh make the identical substitution in shell;
// they are written down separately on purpose, since a script cannot
// call into this package. cmd/ghcr-prune calls this one.
func TagForBranch(branch string) string {
	return strings.ReplaceAll(branch, "/", "..")
}

// EffectiveRepository reports the image repository the next upgrade will pull
// from, accounting for TargetRepository, Track defaults, and any repository
// transition already recorded in RefFile.
func (u *Upgrader) EffectiveRepository() string {
	repo, _ := u.effectiveRepository()
	return repo
}

// SetTargetRepository configures the upgrader to shift from its current
// repository to repo on the next upgrade, returning an error if the transition
// is not permitted.
func (u *Upgrader) SetTargetRepository(repo string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.cfg.Image == nil {
		return fmt.Errorf("no image configuration")
	}
	repo = strings.TrimSpace(repo)
	base := strings.TrimSpace(u.cfg.Image.Repository)
	if base == "" && u.cfg.Image.Track != "" {
		base = installtrack.DefaultRepository(u.cfg.Image.Track)
	}
	if base != "" && !installtrack.CanTransitionRepository(base, repo, u.cfg.Image.AllowedRepositories...) {
		return fmt.Errorf("cannot transition image repository from %q to %q", base, repo)
	}
	u.cfg.Image.TargetRepository = repo
	return nil
}

func (u *Upgrader) effectiveRepository() (string, error) {
	if u.cfg.Image == nil {
		return "", fmt.Errorf("no image repository configured")
	}
	base := strings.TrimSpace(u.cfg.Image.Repository)
	if base == "" && u.cfg.Image.Track != "" {
		base = installtrack.DefaultRepository(u.cfg.Image.Track)
	}
	if target := strings.TrimSpace(u.cfg.Image.TargetRepository); target != "" {
		if base != "" && !installtrack.CanTransitionRepository(base, target, u.cfg.Image.AllowedRepositories...) {
			return "", fmt.Errorf("cannot transition image repository from %q to %q", base, target)
		}
		return target, nil
	}
	// If RefFile has already shifted away from the repository the systemd
	// unit passes on -upgrade-image -- a legacy repository the deployment
	// moved on from, or a repository an operator chose on the upgrade pane
	// (StartImage) -- stay on that shifted repository across subsequent
	// upgrades, provided the shift is still a permitted one. A re-run of
	// scripts/setup.sh rewrites both, so it is what moves a deployment back.
	if base != "" && u.cfg.Image.RefFile != "" {
		if prevRef, err := u.readImageRef(); err == nil {
			prevRepo := repositoryOf(prevRef)
			if prevRepo != "" && prevRepo != base &&
				installtrack.CanTransitionRepository(base, prevRepo, u.cfg.Image.AllowedRepositories...) {
				return installtrack.RepositoryForTrack(prevRepo, u.cfg.Image.Track), nil
			}
		}
	}
	if base == "" {
		return "", fmt.Errorf("no image repository configured")
	}
	// A Google-track deployment still naming the pre-split repository,
	// which is now the core build, upgrades onto the Google build it was
	// already running (installtrack.RepositoryForTrack).
	return installtrack.RepositoryForTrack(base, u.cfg.Image.Track), nil
}

// imageRef is the fully qualified image an upgrade onto branch pulls.
func (u *Upgrader) imageRef(branch string) (string, error) {
	repo, err := u.effectiveRepository()
	if err != nil {
		return "", err
	}
	return u.imageRefIn(repo, branch)
}

// imageRefIn is imageRef with the repository named rather than read off
// Config.Image, because the Kubernetes path carries its own
// (KubeConfig.Repository) and resolves the identical question: which tag
// CI published for this branch, and is it one a registry will accept at
// all.
func (u *Upgrader) imageRefIn(repository, branch string) (string, error) {
	if repository == "" {
		return "", fmt.Errorf("no image repository configured")
	}
	// Start has already done this for any upgrade it runs; done again
	// here because injectivity is this function's promise to make, not
	// its callers' (TagForBranch).
	if err := validateBranch(branch); err != nil {
		return "", err
	}
	tag := TagForBranch(branch)
	if len(tag) > maxTagLen {
		return "", fmt.Errorf("branch %q makes a %d-character image tag, over the %d-character limit -- no image is published for it", branch, len(tag), maxTagLen)
	}
	return repository + ":" + tag, nil
}

// pullImage fetches ref, reporting what docker prints as it goes under
// step -- the same pull runs for the grain image and for the sandbox
// image that goes with it, and those are two different steps of the
// plan, so which one this is has to be passed in.
func (u *Upgrader) pullImage(ctx context.Context, p *progress, step, ref string) error {
	argv := u.cfg.Image.PullCmd
	if len(argv) == 0 {
		argv = []string{"docker", "pull"}
	}
	argv = append(append([]string{}, argv...), ref)
	return runLogged(ctx, p, step, "", argv)
}

// pinImage turns ref, a tag this host has just pulled, into a reference
// to exactly the image that pull fetched: `repo:tag@sha256:...`.
//
// A tag moves. CI pushes the branch's tag again on every commit, and any
// later `docker pull` on this host moves the local copy with it, so an
// upgrade that health-checked, rehearsed and recorded the *tag* would
// restart onto whatever the tag named at restart time -- and every
// restart after that onto whatever it names then -- which need not be
// the image any of those checks ran against (grain/task-1492). Every
// step after the pull therefore runs against the pinned ref, and the
// pinned ref is what RefFile records.
//
// The tag is kept in it, ahead of the digest, purely for people: docker
// resolves a reference carrying both by the digest alone and ignores the
// tag, but "grain:main@sha256:..." still says which branch a deployment
// is on to whoever reads the ref file or the upgrade pane. If the image
// is ever gone from this host, docker fetches it again by digest, which
// the registry has to answer with exactly those bytes.
//
// An image with no digest for ref's repository did not come from the
// registry the pull just asked -- a locally built image tagged to look
// like one, say -- and is refused rather than recorded by tag: there is
// then nothing to pin it to, and no way to say what a restart will run.
//
// A ref that is already pinned -- the sandbox reference a release stamps
// into the build, `repo:sha-<short>@sha256:...` (scripts/release-images.sh)
// -- comes back as it is, once this host is seen to hold that digest:
// the pull fetched it by digest, so there is nothing to choose between,
// only a pull to confirm.
func (u *Upgrader) pinImage(ctx context.Context, p *progress, step, ref string) (string, error) {
	argv := u.cfg.Image.DigestCmd
	if len(argv) == 0 {
		argv = []string{"docker", "image", "inspect", "--format", "{{range .RepoDigests}}{{println .}}{{end}}"}
	}
	argv = append(append([]string{}, argv...), ref)
	out, err := runAsked(ctx, p, step, imageInspectTimeout, argv)
	if err != nil {
		return "", fmt.Errorf("reading the digest of %s: %w", ref, err)
	}
	// RepoDigests lists one entry per repository the image was pulled
	// from or pushed to, so the one wanted is the one for ref's own.
	repo := repositoryOf(ref)
	_, pinned, isPinned := strings.Cut(ref, "@")
	for _, line := range strings.Split(out, "\n") {
		digest, ok := strings.CutPrefix(strings.TrimSpace(line), repo+"@")
		if !ok || !strings.HasPrefix(digest, "sha256:") {
			continue
		}
		if !isPinned {
			return ref + "@" + digest, nil
		}
		if digest == pinned {
			return ref, nil
		}
	}
	if isPinned {
		return "", fmt.Errorf("%s is not on this host by that digest after its pull, so what it names is not what is here (it printed %q)", ref, strings.TrimSpace(out))
	}
	return "", fmt.Errorf("%s has no digest from %s on this host, so there is nothing to pin the upgrade to (it printed %q)", ref, repo, strings.TrimSpace(out))
}

// imageInspectTimeout bounds pinImage's question. It reads the image
// store's metadata and starts no container, so it is nowhere near
// imageRunTimeout's tail.
const imageInspectTimeout = time.Minute

// healthCheckImage runs the just-pulled image with HealthCheckArgs, the
// image-mode counterpart of healthCheck's run of the just-installed
// binary -- and, unlike that one, it runs *before* anything cuts over,
// so a failure needs no rollback (see this file's own header).
//
// It is bounded by imageRunTimeout rather than by healthCheck's own
// ten seconds, because what it bounds is a container's whole life and
// not a process exec -- see that variable's comment.
func (u *Upgrader) healthCheckImage(ctx context.Context, p *progress, ref string) error {
	ctx, cancel := context.WithTimeout(ctx, imageRunTimeout)
	defer cancel()
	argv := u.cfg.Image.RunCmd
	if len(argv) == 0 {
		argv = []string{"docker", "run", "--rm"}
	}
	argv = append(append([]string{}, argv...), ref)
	argv = append(argv, u.cfg.HealthCheckArgs...)
	return runLogged(ctx, p, stepHealthCheck, "", argv)
}

// sandboxImage asks a pulled image which sandbox container it expects,
// by running the subcommand SandboxImageArgs names inside it. It returns
// the ref, or -- when the image named none -- an empty ref and a reason
// saying so in words, or an error.
//
// The three answers are the whole point of it. An image that *tells* us
// it has no sandbox to keep in step is not a failure: an older grain,
// pulled by a rollback, predates the subcommand and exits with a usage
// error rather than a ref, and failing an otherwise good rollback over
// that would be wrong. But "this host could not put the question" --
// a deadline that expired, a docker that could not start a container,
// a registry that had gone away mid-upgrade -- is not that answer, and
// used to be read as one: every failure here returned ("", nil), so an
// upgrade went on to cut over having pulled no guest image at all, and
// the next dispatched task reached for an image nothing had fetched
// (grain/task-338). ranAndFailed is what separates them, and the second
// case stops the upgrade with the ref file untouched, exactly like a
// failed health check.
//
// The reason is returned rather than only logged because it is what the
// step's own Detail says: "names no sandbox image" alone leaves an
// operator unable to tell a deliberate rollback to a build that predates
// the question from a deployment that has just silently stopped keeping
// its guest in step.
func (u *Upgrader) sandboxImage(ctx context.Context, p *progress, ref string) (sandbox, why string, err error) {
	argv := u.cfg.Image.RunCmd
	if len(argv) == 0 {
		argv = []string{"docker", "run", "--rm"}
	}
	argv = append(append([]string{}, argv...), ref)
	argv = append(argv, u.cfg.Image.SandboxImageArgs...)
	out, err := runAsked(ctx, p, stepSandbox, imageRunTimeout, argv)
	if err != nil {
		if !ranAndFailed(err) {
			return "", "", err
		}
		// It ran and refused: the shape a build older than the
		// subcommand has. The error is carried into the reason rather
		// than dropped, since the usage message on its stderr is the
		// evidence for that reading.
		refused := fmt.Sprintf("it ran and refused the question, which a build older than the subcommand does (%v)", err)
		p.logf("%s: %s names no sandbox image: %s -- nothing to keep in step", stepSandbox, ref, refused)
		return "", refused, nil
	}
	// One line, whatever else it printed: the subcommand prints exactly
	// the ref, and a stray trailing newline is not part of it.
	if named := strings.TrimSpace(strings.SplitN(out, "\n", 2)[0]); named != "" {
		return named, "", nil
	}
	// Answered, successfully, with nothing at all. Not a failure either
	// -- it is still the image saying it has no sandbox -- but worth
	// distinguishing from the refusal above, because an image that exits
	// 0 and prints no ref is a build whose stamp went missing rather
	// than a build too old to have one.
	return "", "it answered without naming one", nil
}

// checkImageFlags asks the newly pulled image which flags its daemon
// accepts and returns the ones this deployment's unit passes that it
// does not -- the flags that will be ignored from the restart onwards.
//
// It answers, before the cut-over, the question this deployment could
// only answer afterwards and at the cost of eight minutes down: does the
// image about to be started accept the command line the thing starting
// it will hand over?
//
// A disagreement is reported, not refused, and that is a decision rather
// than an omission. The upgrade that *removes* a flag is exactly the one
// every already-installed host needs -- refusing it would strand every
// deployment on the build before the removal until somebody re-ran
// scripts/setup.sh on each of them by hand, which is a worse outage than
// the setting being ignored. The daemon has stopped dying over it
// (pkg/cli/daemon_unknown_flags.go), so what is left is to make sure
// nobody is surprised: the step names the flags, and says where the unit
// that passes them lives.
//
// An image that cannot answer at all is not a disagreement either. An
// older grain, pulled by a rollback, predates -flag-names and fails with
// a usage error, which is the same "nothing to compare" as a deployment
// that configured this check away. The error comes back to the caller
// whole, though, and the step says which kind of failure it was
// (ranAndFailed): "too old to ask" and "this host could not run the
// image" are the same silence otherwise. Unlike sandboxImage, neither
// kind stops the upgrade -- nothing downstream needs this answer, and a
// check that could not run is no reason to refuse a build that has
// already proved it runs.
func (u *Upgrader) checkImageFlags(ctx context.Context, p *progress, ref string) ([]string, error) {
	argv := u.cfg.Image.RunCmd
	if len(argv) == 0 {
		argv = []string{"docker", "run", "--rm"}
	}
	argv = append(append([]string{}, argv...), ref)
	argv = append(argv, u.cfg.Image.FlagNamesArgs...)
	out, err := runAsked(ctx, p, stepFlags, imageRunTimeout, argv)
	if err != nil {
		if !ranAndFailed(err) {
			return nil, fmt.Errorf("%w: %w", errCouldNotAsk, err)
		}
		return nil, err
	}
	// Only the flag names are wanted here -- this side already knows
	// which of its own flags took values, because it parsed them
	// (flags.go, shared with the Kubernetes path).
	return flagsNotDefined(ref, out, u.cfg.Image.UnitFlags)
}

// imageFlagFix is what the image path tells an operator to do about a
// flag the new image will ignore: the command line is in a systemd unit
// on this host, which nothing in the image can rewrite.
const imageFlagFix = "Re-run scripts/setup.sh on this host to rewrite grain-daemon.service"

// unusedSandboxNote is what the sandbox step adds when the image it has
// just pulled is not the one this deployment will create sandboxes from
// -- ImageConfig.SandboxImagePin naming a different tag. Empty when
// there is nothing to report: no pin, or a pin that is already what the
// new build names, which is the ordinary case and wants no commentary.
//
// It names the pinned tag rather than only saying "not used", because
// the pin is in the host's systemd unit and knowing which image is
// actually running is what an operator needs before deciding whether to
// keep it.
func (u *Upgrader) unusedSandboxNote(sandbox string) string {
	pin := u.cfg.Image.SandboxImagePin
	if pin == "" || pin == sandbox {
		return ""
	}
	return fmt.Sprintf(", which this deployment will not run: it pins %s", pin)
}

// imageRunner is schema.go's schemaRunner for the container path: both
// halves of it are a `docker run` of the image this upgrade has just
// pulled and has not yet cut over to.
type imageRunner struct {
	u   *Upgrader
	ref string
}

// run builds the argv for one such invocation. extra goes to docker
// itself, before the image ref; args go to the image, whose ENTRYPOINT
// is `grain` (Dockerfile).
func (r imageRunner) run(extra, args []string) []string {
	argv := r.u.cfg.Image.RunCmd
	if len(argv) == 0 {
		argv = []string{"docker", "run", "--rm"}
	}
	argv = append(append([]string{}, argv...), extra...)
	argv = append(argv, r.ref)
	return append(argv, args...)
}

func (r imageRunner) ask(ctx context.Context, p *progress, args []string) (string, error) {
	return runAsked(ctx, p, stepSchema, imageRunTimeout, r.run(nil, args))
}

// rehearse gives the container one directory of this host's filesystem,
// read-only, at the path it already has out here.
//
// The same path inside and out is what makes it work at all: the daemon
// asking for this is itself in a container, so the path it names is only
// meaningful to the docker daemon if the host has it too -- which
// -data-dir does, bind-mounted at its own path by
// scripts/setup.sh's docker_run_args, and the directory being mounted
// here is under it.
//
// Read-only, and it matters: what runs in there is `grain state check`,
// which imports into a throwaway database of its own, and a rehearsal
// that could write to this deployment's rows would not be one.
func (r imageRunner) rehearse(ctx context.Context, p *progress, dir string, args []string) error {
	ctx, cancel := context.WithTimeout(ctx, schemaCheckTimeout)
	defer cancel()
	return runLogged(ctx, p, stepSchema, "", r.run([]string{"--volume", dir + ":" + dir + ":ro"}, args))
}

// schemaCheckTimeout bounds the migration rehearsal. Longer than
// imageRunTimeout because it covers the same container lifetime *plus*
// the one thing this package runs that does real work -- a whole
// deployment's rows parsed and inserted into a database -- and short
// enough that an upgrade cannot sit on it for the whole of
// Config.Timeout.
const schemaCheckTimeout = 5 * time.Minute

// readImageRef reads back what writeImageRef last wrote: the image
// grain-daemon.service interpolates into its ExecStart, and so the one
// this deployment is on. The counterpart of the write, and the answer to
// "upgrading from what?" on every deployment scripts/setup.sh installs
// -- including the ones whose ref file was last written by that script
// rather than by an upgrade, which have no successful attempt on record
// to infer it from (grain/task-50).
//
// Parsed rather than read wholesale, and with the last assignment
// winning, because the file is a systemd EnvironmentFile: that is how
// systemd itself reads one, and an operator who pinned a deployment by
// hand may well have left the line they replaced above the one they
// wrote. Anything the shell would quote is stripped for the same reason,
// since an image reference never contains a quote of its own.
func (u *Upgrader) readImageRef() (string, error) {
	data, err := os.ReadFile(u.cfg.Image.RefFile)
	if err != nil {
		return "", err
	}
	ref := ""
	for _, line := range strings.Split(string(data), "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), imageRefEnvKey+"=")
		if !ok {
			continue
		}
		ref = strings.Trim(strings.TrimSpace(rest), `"'`)
	}
	if ref == "" {
		return "", fmt.Errorf("%s names no %s", u.cfg.Image.RefFile, imageRefEnvKey)
	}
	return ref, nil
}

// writeImageRef points the deployment at ref. Atomic for the same reason
// install and writeStatus are: grain-daemon.service reads this file on
// every start, and a start that raced a half-written one would come up
// with no image name at all.
//
// What it writes is a request, not the unit's environment: the file is
// this daemon's account's to write, so scripts/setup.sh's units read it
// as root only as data -- the last GRAIN_IMAGE= line, honoured only if
// it names an image in the -upgrade-image repository -- and copy that
// into a root-owned file of their own before a start (#2405). Nothing
// else this writes there reaches systemd or a root shell.
func (u *Upgrader) writeImageRef(ref string) error {
	line := fmt.Sprintf("%s=%s\n", imageRefEnvKey, ref)
	tmp := u.cfg.Image.RefFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(line), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, u.cfg.Image.RefFile)
}

// listedImage is one row of listImages: the `repo:tag` it would be
// removed by, then any other names ListCmd gave for the same image, and
// the image's ID if ListCmd printed one.
type listedImage struct {
	names []string
	id    string
}

// imageIDPattern matches an image ID as docker prints one: 12 hex digits from
// `docker image ls`, the whole `sha256:...` from inspect or --no-trunc.
var imageIDPattern = regexp.MustCompile(`^(sha256:)?[0-9a-f]{12,64}$`)

// sameImageID reports whether a and b name the same image, either of
// them possibly the 12-digit short form of the other.
func sameImageID(a, b string) bool {
	a, b = strings.TrimPrefix(a, "sha256:"), strings.TrimPrefix(b, "sha256:")
	if len(a) < 12 || len(b) < 12 {
		return false
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	return strings.HasPrefix(b, a)
}

func containsImageID(ids []string, id string) bool {
	for _, other := range ids {
		if sameImageID(other, id) {
			return true
		}
	}
	return false
}

// listImages lists the images on this host for repository.
func (u *Upgrader) listImages(ctx context.Context, p *progress, repository string) ([]listedImage, error) {
	argv := u.cfg.Image.ListCmd
	if len(argv) == 0 {
		argv = []string{"docker", "image", "ls", "--format", "{{.Repository}}:{{.Tag}} {{.Repository}}@{{.Digest}} {{.ID}}"}
	}
	argv = append(append([]string{}, argv...), repository)
	out, err := runAsked(ctx, p, stepRecord, imageRunTimeout, argv)
	if err != nil {
		return nil, err
	}
	var images []listedImage
	for _, line := range strings.Split(out, "\n") {
		names := strings.Fields(line)
		if len(names) == 0 {
			continue
		}
		img := listedImage{names: names}
		if last := names[len(names)-1]; len(names) > 1 && imageIDPattern.MatchString(last) {
			img.names, img.id = names[:len(names)-1], last
		}
		images = append(images, img)
	}
	return images, nil
}

// containerImageIDs lists the IDs of the images running containers use.
func (u *Upgrader) containerImageIDs(ctx context.Context, p *progress) ([]string, error) {
	argv := u.cfg.Image.ContainerImagesCmd
	if len(argv) == 0 {
		argv = []string{"sh", "-c", `ids=$(docker ps -q --no-trunc) || exit 1; [ -z "$ids" ] || exec docker container inspect --format '{{.Image}}' $ids`}
	}
	out, err := runAsked(ctx, p, stepRecord, imageInspectTimeout, argv)
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

// removeImage removes an old image reference that is no longer needed.
func (u *Upgrader) removeImage(ctx context.Context, p *progress, ref string) error {
	argv := u.cfg.Image.RemoveCmd
	if len(argv) == 0 {
		argv = []string{"docker", "image", "rm"}
	}
	argv = append(append([]string{}, argv...), ref)
	return runLogged(ctx, p, stepRecord, "", argv)
}

// repositoryOf returns the repository part of an image reference, stripping
// the tag, the digest, or both (`repo:tag@sha256:...`, which is what
// pinImage makes). The digest goes first: its own "sha256:" would
// otherwise read as a tag separator.
func repositoryOf(ref string) string {
	ref, _, _ = strings.Cut(ref, "@")
	if idx := strings.LastIndex(ref, ":"); idx != -1 {
		slash := strings.LastIndex(ref, "/")
		if idx > slash {
			return ref[:idx]
		}
	}
	return ref
}

// imageNames is every name ref can be matched by in an image listing:
// itself, and for a pinned `repo:tag@sha256:...` also `repo@sha256:...`
// (the image itself, however it is tagged now) and `repo:tag`.
func imageNames(ref string) []string {
	names := []string{ref}
	if named, digest, ok := strings.Cut(ref, "@"); ok {
		names = append(names, repositoryOf(ref)+"@"+digest, named)
	}
	return names
}

// cleanOldImages cleans up container images from versions older than the previous
// version to avoid filling up the host's root disk (grain/task-10).
//
// It keeps the newly installed version and the immediately previous version (so
// rollback or fallback remains possible), plus any pinned sandbox image. Any
// older versions in the grain or sandbox repositories are removed with RemoveCmd.
//
// Best effort: a refusal (e.g. from an image still used by a running container)
// is logged to the journal rather than failing an upgrade that has already succeeded.
func (u *Upgrader) cleanOldImages(ctx context.Context, p *progress, ref, prevRef, sandbox, prevSandbox string) {
	keep := make(map[string]bool)
	for _, r := range []string{ref, prevRef, sandbox, prevSandbox, u.cfg.Image.SandboxImagePin} {
		if r == "" {
			continue
		}
		for _, name := range imageNames(r) {
			keep[name] = true
		}
	}

	repos := make(map[string]bool)
	if u.cfg.Image.Repository != "" {
		repos[u.cfg.Image.Repository] = true
	}
	if ref != "" {
		repos[repositoryOf(ref)] = true
	}
	if prevRef != "" {
		repos[repositoryOf(prevRef)] = true
	}
	if sandbox != "" {
		repos[repositoryOf(sandbox)] = true
	}
	if prevSandbox != "" {
		repos[repositoryOf(prevSandbox)] = true
	}
	if u.cfg.Image.SandboxImagePin != "" {
		repos[repositoryOf(u.cfg.Image.SandboxImagePin)] = true
	}

	for repo := range repos {
		keep[repo+":previous"] = true
	}
	// By any of its names: a pinned ref is kept by its digest, which is
	// what still finds it after its tag has moved on.
	kept := func(img listedImage) bool {
		for _, name := range img.names {
			if keep[name] {
				return true
			}
		}
		return false
	}

	// Every repository is listed before anything is removed, so that the
	// IDs of the kept images are all known first: the same image can be
	// listed under a kept tag and again as a row with no usable name, and
	// removing that row by ID would delete the kept image with it.
	listed := make(map[string][]listedImage)
	var keptIDs []string
	for repo := range repos {
		images, err := u.listImages(ctx, p, repo)
		if err != nil {
			p.logf("%s: listing older images for %s to remove: %v", stepRecord, repo, err)
			continue
		}
		listed[repo] = images
		for _, img := range images {
			if img.id != "" && kept(img) {
				keptIDs = append(keptIDs, img.id)
			}
		}
	}

	var running []string
	runningListed, runningErr := false, error(nil)
	var removedIDs []string
	for repo, images := range listed {
		for _, img := range images {
			if kept(img) {
				continue
			}
			ref := removableImage(repo, img.names)
			byID := false
			if ref == "" {
				// No tag and no digest to name it by -- an untagged or
				// locally built image -- so only its ID will do, and only
				// if nothing kept or running is that same image.
				if img.id == "" || len(img.names) == 0 || img.names[0] != repo+":<none>" ||
					containsImageID(keptIDs, img.id) || containsImageID(removedIDs, img.id) {
					continue
				}
				if !runningListed {
					running, runningErr = u.containerImageIDs(ctx, p)
					runningListed = true
					if runningErr != nil {
						p.logf("%s: not removing untagged images by ID, since the images running containers use could not be listed: %v", stepRecord, runningErr)
					}
				}
				if runningErr != nil || containsImageID(running, img.id) {
					continue
				}
				ref, byID = img.id, true
				removedIDs = append(removedIDs, img.id)
			}
			what := ref
			if byID {
				what = fmt.Sprintf("untagged %s image %s", repo, ref)
			}
			if err := u.removeImage(ctx, p, ref); err != nil {
				// Docker refuses, without -f, an image a container uses
				// (stopped ones included) or an ID several repositories
				// name; only the first is worth guessing at.
				why := ""
				if strings.Contains(err.Error(), "container") {
					why = " (a container may still be using it)"
				}
				p.logf("%s: leaving %s in place%s: %v", stepRecord, what, why, err)
				continue
			}
			p.logf("%s: removed %s, which is older than the previous version", stepRecord, what)
		}
	}
}

// removableImage returns the image reference (tag or digest) to pass to
// removeImage for a repository entry from listImages, or "" if the entry does
// not belong to repo or has neither a valid tag nor a valid digest -- in
// which case cleanOldImages can only remove it by its ID.
//
// A digest of "<none>", or an empty one (`repo@`, which is what docker's
// `{{.Digest}}` leaves for an image built here or untagged in some image
// stores), is never a usable reference: `docker image rm repo@` is an
// invalid reference format, not a removal.
func removableImage(repo string, names []string) string {
	if len(names) == 0 {
		return ""
	}
	usableDigest := func(name string) bool {
		return strings.HasPrefix(name, repo+"@") && name != repo+"@" && !strings.HasSuffix(name, "@<none>")
	}
	first := names[0]
	if strings.HasPrefix(first, repo+":") && !strings.HasSuffix(first, ":<none>") {
		return first
	}
	if first == repo+":<none>" || strings.HasPrefix(first, repo+"@") {
		for _, name := range names {
			if usableDigest(name) {
				return name
			}
		}
		for _, name := range names[1:] {
			if name != "" && !strings.Contains(name, "<none>") && !strings.HasSuffix(name, "@") && !imageIDPattern.MatchString(name) {
				return name
			}
		}
	}
	return ""
}

// sameImageVersion reports whether the pulled and pinned image ref for
// branch is already what this deployment runs.
//
// An unpinned moving branch tag in the ref file (`repo:main` with no
// `@sha256:`) is not treated as already running ref unless Config.RunningRelease
// (or a cached release-notes preview) confirms the version matches: a legacy
// ref file written before pinning existed still needs to be rewritten to
// record the digest it runs.
func (u *Upgrader) sameImageVersion(prevRef, branch, tag, ref string) bool {
	if prevDigest, ok := digestRef(prevRef); ok {
		if newDigest, ok := digestRef(ref); ok && prevDigest == newDigest {
			return true
		}
	}
	if prevRef != "" && !strings.Contains(prevRef, "@") && prevRef == tag &&
		(versionTag.MatchString(branch) || candidateTag.MatchString(branch) || strings.HasPrefix(branch, "sha-")) {
		return true
	}
	if rel := u.runningRelease(); rel != "" && (prevRef == "" || repositoryOf(prevRef) == repositoryOf(ref)) {
		if branch == rel {
			return true
		}
		u.notesMu.Lock()
		entry, cached := u.notes[branch]
		if !cached && (branch == "rc" || branch == "candidate") {
			entry, cached = u.notes[sourceBranch(branch)]
		}
		u.notesMu.Unlock()
		if cached && entry.preview.From != "" && entry.preview.From == entry.preview.Version {
			return true
		}
	}
	return false
}

// discardedRCUnchangedDetail returns the unchanged-upgrade detail message when
// an upgrade onto candidate or rc finds the deployment's running RC discarded
// with no replacement RC published yet, or "" otherwise.
func (u *Upgrader) discardedRCUnchangedDetail(ctx context.Context, branch string) string {
	if branch != "candidate" && branch != "rc" {
		return ""
	}
	if d := u.CachedDiscardedRC(ctx); d != nil && d.ReplacementRC == "" {
		return fmt.Sprintf("%s was discarded and no newer release candidate has been published yet; staying on the current build", d.Running)
	}
	return ""
}
