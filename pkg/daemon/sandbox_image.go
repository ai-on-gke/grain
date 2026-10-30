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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"

	"github.com/ai-on-gke/grain/pkg/k8s"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/registryauth"
	"github.com/ai-on-gke/grain/pkg/sandboximage"
	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/ai-on-gke/grain/pkg/upgrade"
)

// granuleImageFlag is granule's own flag naming the image a VM's
// container runs -- which, since bwsalmon/granule#37, is also the guest
// that container boots.
const granuleImageFlag = "-kontur-image"

// sandboxImageSource is where the sandbox image in force came from, and
// so which of three different things this deployment has to say about it:
// nothing at all, a warning that a pin of its own is holding its
// sandboxes behind grain, or a note that a pin an older install left
// behind has been let go of.
type sandboxImageSource int

const (
	// sandboxImageStamped: nothing pinned one, so this build's own stamp
	// is in force. The ordinary case, and the one the stamp exists for.
	sandboxImageStamped sandboxImageSource = iota
	// sandboxImagePinnedHere: this deployment named an image on purpose
	// and it wins -- -sandbox-image, or a -granule-create-arg
	// -kontur-image naming an image from somewhere other than the
	// stamp's own repository (a mirror, a private copy).
	sandboxImagePinnedHere
	// sandboxImageAdopted: a -granule-create-arg -kontur-image naming
	// another tag of the stamp's *own* repository, which is what an
	// older scripts/setup.sh wrote unconditionally at install time. The
	// stamp is used instead -- see chooseSandboxImage.
	sandboxImageAdopted
)

// sandboxImageChoice is which sandbox image this start creates VMs from,
// where that came from, and the single line about it the journal gets.
type sandboxImageChoice struct {
	image  string
	source sandboxImageSource
	log    string
}

// chooseSandboxImage decides which sandbox container every VM is created
// from, out of the three places an answer can come from.
//
// The default is defaultSandboxImage, the reference stamped into this
// build at build time (sandboximage.go): the sandbox built from this very
// commit. The stamp being resolved *here*, on each start, rather than
// baked into grain-daemon.service by scripts/setup.sh is the whole point.
// An upgrade pulls the new grain image, asks it which sandbox image goes
// with it, pulls that too and restarts the service (pkg/upgrade's
// ImageConfig.SandboxImageArgs) -- but the ExecStart it restarts still
// carried the sandbox reference setup.sh resolved when the host was
// *installed*. So an upgraded deployment ran new grain against the guest
// of an old commit, indefinitely, with both pulls reported as successful:
// the symptom this was found through was every sandbox reporting nested
// virtualization as "denied" long after the guest image that grants the
// kvm group had been published and pulled.
//
// A deployment that names an image of its own keeps it, and -sandbox-image
// is where it says so. That covers a mirror, a private copy, a guest held
// at an older commit -- an operator's decision, which grain reports (the
// drift warning below) and does not overrule.
//
// The third case is the one this function was rewritten for. Units
// written before -sandbox-image existed carry -granule-create-arg
// -kontur-image *unconditionally*, because that is what setup.sh wrote
// for every granule host, pinned or not: on such a host the flag records
// what `grain sandbox-image` printed the day the host was installed, not
// a choice anybody made. Honouring it made the fix above unreachable for
// exactly the deployments that needed it -- on the one this was found on
// it overrode three consecutive upgrades identically, each of which had
// pulled the guest it then did not run. So a pin naming another tag of
// the stamp's own repository is read as that fossil and the stamp is
// adopted over it, loudly (adoptedSandboxImageNote, which names
// -sandbox-image for an operator who did mean it).
//
// Narrow on purpose: only the stamp's own repository. A pin naming
// anywhere else -- a mirror, a registry inside somebody's network, a
// private copy -- is kept, because it is not a shape setup.sh ever wrote
// by itself and because the stamp it would be replaced with may well be
// unpullable from that host, which would turn drift into every dispatch
// failing.
func chooseSandboxImage(cfg Config) sandboxImageChoice {
	if cfg.SandboxImage != "" {
		if cfg.SandboxImage == DefaultSandboxImage {
			return sandboxImageChoice{
				image:  cfg.SandboxImage,
				source: sandboxImagePinnedHere,
				log: fmt.Sprintf("grain daemon: sandbox image %s (named by -sandbox-image, and the same one this build is stamped for)",
					cfg.SandboxImage),
			}
		}
		return sandboxImageChoice{
			image:  cfg.SandboxImage,
			source: sandboxImagePinnedHere,
			log:    sandboxImageDriftWarning(cfg.SandboxImage, "-sandbox-image"),
		}
	}
	pinned, ok := pinnedSandboxImage(cfg.GranuleCreateArgs)
	if !ok {
		return sandboxImageChoice{
			image:  DefaultSandboxImage,
			source: sandboxImageStamped,
			log:    fmt.Sprintf("grain daemon: sandbox image %s (stamped into this build; nothing pinned one)", DefaultSandboxImage),
		}
	}
	switch {
	case pinned == DefaultSandboxImage:
		return sandboxImageChoice{
			image:  pinned,
			source: sandboxImagePinnedHere,
			log: fmt.Sprintf("grain daemon: sandbox image %s (named by -granule-create-arg, and the same one this build is stamped for)",
				pinned),
		}
	case pinned != "" && imageRepository(pinned) == imageRepository(DefaultSandboxImage):
		return sandboxImageChoice{
			image:  DefaultSandboxImage,
			source: sandboxImageAdopted,
			log:    adoptedSandboxImageNote(pinned),
		}
	default:
		return sandboxImageChoice{
			image:  pinned,
			source: sandboxImagePinnedHere,
			log:    sandboxImageDriftWarning(pinned, "-granule-create-arg"),
		}
	}
}

// sandboxImageCreateArgs is the -granule-create-arg list a VM is really
// created with: this deployment's own, with chooseSandboxImage's answer
// written into it as granule's -kontur-image, and the one line about
// that answer logged where an operator can read it at startup.
//
// The flag is rewritten rather than only appended, since the answer can
// be a stamp adopted over a value the list already carries -- and left
// alone entirely when the list carries the flag with no value at all,
// which is granule's own to complain about rather than this function's
// to guess at.
func sandboxImageCreateArgs(cfg Config) []string {
	choice := chooseSandboxImage(cfg)
	log.Print(choice.log)
	if choice.image == "" {
		return cfg.GranuleCreateArgs
	}
	return withSandboxImageArg(cfg.GranuleCreateArgs, choice.image)
}

// withSandboxImageArg is args with -kontur-image set to image: the
// existing flag's value replaced in place, in whichever of granule's
// two spellings the list uses, or the flag appended when the list has
// none. The caller's slice is never written to -- cfg.granuleCreateArgs is
// read by more than this.
func withSandboxImageArg(args []string, image string) []string {
	out := append([]string{}, args...)
	for i, arg := range out {
		if strings.HasPrefix(arg, granuleImageFlag+"=") {
			out[i] = granuleImageFlag + "=" + image
			return out
		}
		if arg == granuleImageFlag {
			if i+1 < len(out) {
				out[i+1] = image
				return out
			}
			return append(out, image)
		}
	}
	return append(out, granuleImageFlag, image)
}

// imageRepository is an image reference without its tag or digest --
// "ghcr.io/bwsalmon/grain/guest" for both of that repository's sha- tags.
// It is how chooseSandboxImage tells "another tag of the image this build
// is stamped for" from "an image published somewhere else entirely".
//
// The last colon is only a tag separator when it comes after the last
// slash: a registry may carry a port ("127.0.0.1:5000/guest"), and a
// reference pinned by digest carries its own colon after an "@".
func imageRepository(ref string) string {
	if at := strings.LastIndex(ref, "@"); at >= 0 {
		ref = ref[:at]
	}
	if colon := strings.LastIndex(ref, ":"); colon > strings.LastIndex(ref, "/") {
		ref = ref[:colon]
	}
	return ref
}

// sandboxImageDriftWarning is what a deployment whose pinned sandbox
// image is not this build's own stamp is told at every start, naming the
// flag the pin is written in.
//
// It is a WARNING rather than a neutral line, because the two tags being
// different is the exact drift sandboximage.go exists to prevent, and it
// is otherwise invisible: an upgrade *does* pull the sandbox image the
// new build names, says so, and then the daemon creates every VM from the
// pinned one instead. So the deployment runs a daemon from one commit
// against guests from another, every upgrade pays for a pull nothing then
// uses, and the only symptom reaching anybody is whatever the older guest
// is missing (on the deployment this was found on, "denied" nested
// virtualization from a guest built before the guest build's kvm
// grant).
//
// Both tags are named, because "the pinned one is old" is not a fact
// either half carries alone, and so is the one thing that fixes it: the
// pin lives in the host's own systemd unit, which is not in this
// repository and which grain cannot rewrite from here.
func sandboxImageDriftWarning(pinned, by string) string {
	return fmt.Sprintf("WARNING: sandbox image %s is pinned by %s, but this build of grain is stamped for %s. "+
		"Every VM is created from the pinned image, so this deployment's daemon and its sandboxes are from different commits, "+
		"and the sandbox image each upgrade pulls (the stamped one) is pulled and never run. "+
		"Drop that pin from this host's grain-daemon.service (scripts/setup.sh writes one only for "+
		"a deployment that set GRAIN_GRANULE_OCI_IMAGE) to let the sandbox move with grain.", orUnknownImage(pinned), by, DefaultSandboxImage)
}

// adoptedSandboxImageNote is what a deployment whose unit still carries
// the sandbox reference an older scripts/setup.sh resolved at install
// time is told, once per start, when this build's own stamp is used over
// it instead.
//
// It says which image was let go of as well as which one is in force,
// because the change is otherwise silent from the outside and because it
// is the pin an operator would go looking for after reading the previous
// releases' warning about it -- and it names -sandbox-image, which is how
// a deployment that did mean to hold that guest gets it back.
func adoptedSandboxImageNote(stale string) string {
	return fmt.Sprintf("grain daemon: sandbox image %s (stamped into this build, adopted over the %s this host's "+
		"-granule-create-arg still pins). That pin is another tag of the same image, which is what scripts/setup.sh "+
		"wrote for every granule host before -sandbox-image existed -- the guest this host was installed at rather "+
		"than one anybody chose -- and keeping it would hold every VM there while grain upgraded away from it, "+
		"pulling a guest nothing runs on each upgrade. Pass -sandbox-image %s to hold that guest on purpose.",
		DefaultSandboxImage, stale, stale)
}

// pinnedSandboxImage is the sandbox image this deployment's own create
// args name, and whether they name one at all. The value is empty for a
// flag whose value is missing, which is granule's problem to report
// rather than this one's -- but the flag being there is still a pin, and
// still wins over the stamp, so ok is true either way.
func pinnedSandboxImage(args []string) (string, bool) {
	for i, arg := range args {
		if value, ok := strings.CutPrefix(arg, granuleImageFlag+"="); ok {
			return value, true
		}
		if arg == granuleImageFlag {
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", true
		}
	}
	return "", false
}

// orUnknownImage is how a pin with no value reads in a log line.
func orUnknownImage(image string) string {
	if image == "" {
		return "?"
	}
	return image
}

// sandboxImageArgs is upgrade.ImageConfig.SandboxImageArgs: the
// subcommand an upgrade runs inside a newly pulled image to learn which
// sandbox container to pull alongside it, or nil for a deployment with
// no sandbox container to keep in step.
func sandboxImageArgs(granuleSandboxes bool) []string {
	if !granuleSandboxes {
		return nil
	}
	return []string{"sandbox-image"}
}

// upgradeSandboxImageArgs returns the subcommand an off-cluster ModeImage
// upgrade runs inside a candidate grain image to learn which sandbox guest
// image to pull (and optionally mirror) alongside it.
//
// Included for both local GranuleSandboxes and off-cluster K8sGranuleSandboxes
// (as well as any deployment with a sandbox image mirror configured), so a
// host deployment running kubernetes-granule pulls and mirrors the candidate
// build's guest image during stageImage before cutting over.
func upgradeSandboxImageArgs(cfg Config) []string {
	return sandboxImageArgs(cfg.GranuleSandboxes || cfg.K8sGranuleSandboxes || sandboxImageMirrorRepo(cfg) != "")
}

// sandboxImagePin is upgrade.ImageConfig.SandboxImagePin: the sandbox
// image this deployment's own -granule-create-arg names, which wins over
// whatever the newly pulled build asks for (sandboxImageCreateArgs), so
// that the upgrade's sandbox step can say the image it just pulled is
// not the one this deployment's VMs will run.
//
// uiSandboxImage is startUIServer's ui.Config.SandboxImage: the image
// every sandbox is really created from, beside the one this build is
// stamped for, so the sandbox health pane can say when they are not the
// same image (ui.SandboxImage's own doc comment).
//
// It asks chooseSandboxImage, the one place that decides, so the pane
// cannot disagree with what the daemon actually passed granule -- an
// adopted stamp reports as the stamp, unpinned, because that is what
// sandboxes are then built from and there is nothing left for an operator
// to go and change. nil for the host-directory backend, whose sandbox is
// a directory on this machine and has no image to name.
func uiSandboxImage(cfg Config) *ui.SandboxImage {
	switch {
	case cfg.DisableNativeSandboxes:
		return nil
	case cfg.GranuleSandboxes:
		choice := chooseSandboxImage(cfg)
		custom := customSandboxImageFor(cfg)
		return &ui.SandboxImage{
			Image:    firstNonEmpty(custom, orUnknownImage(choice.image)),
			Expected: DefaultSandboxImage,
			Pinned:   custom == "" && choice.source == sandboxImagePinnedHere,
		}
	case cfg.K8sGranuleSandboxes:
		expectedCfg := cfg
		expectedCfg.CustomSandboxImage = ""
		expectedCfg.K8sGranuleImage = ""
		return &ui.SandboxImage{
			Image:    activeSandboxImageFor(cfg),
			Expected: EffectiveK8sGranuleImage(expectedCfg),
		}
	case cfg.K8sSandboxes:
		return &ui.SandboxImage{
			Image:    activeSandboxImageFor(cfg),
			Expected: EffectiveK8sSandboxImage(Config{K8sSandboxes: true}),
		}
	case cfg.ContainerSandboxes:
		custom := customSandboxImageFor(cfg)
		stock := stockSandboxImageFor(cfg)
		return &ui.SandboxImage{
			Image:    firstNonEmpty(custom, stock),
			Expected: DefaultK8sSandboxImage,
			Pinned:   custom == "" && cfg.ContainerImage != "" && cfg.ContainerImage != DefaultK8sSandboxImage && cfg.ContainerImage != model.DefaultK8sSandboxImage,
		}
	default:
		return nil
	}
}

// sandboxGuestImage is orchestrator.Config.SandboxGuestImage: the same
// image uiSandboxImage reports, handed to the cycle so that it can work
// out which repository publishes the guest this deployment runs and keep
// that repository's guest cache following its base branch with releases
// switched off (model.GuestCacheFollowsBaseBranch).
//
// The same chooseSandboxImage for uiSandboxImage's reason -- one place
// decides, so what the cycle acts on and what the pane displays cannot
// disagree -- and "" for the host-directory backend, whose sandbox is a
// directory with no image to attribute to any repository.
func sandboxGuestImage(cfg Config) string {
	switch {
	case cfg.DisableNativeSandboxes:
		return ""
	case cfg.GranuleSandboxes, cfg.K8sGranuleSandboxes:
		return activeSandboxImageFor(cfg)
	default:
		return ""
	}
}

// sandboxImagePin is upgrade.ImageConfig.SandboxImagePin: the sandbox
// image this deployment pins on purpose, which wins over whatever the
// newly pulled build asks for (chooseSandboxImage), so that the upgrade's
// sandbox step can say the image it just pulled is not the one this
// deployment's VMs will run.
//
// Empty for a deployment that pins nothing -- there is nothing to
// disagree with then -- and empty too for one whose only pin is the
// install-time value chooseSandboxImage adopts the stamp over, since that
// upgrade's pull is exactly the image the restart will then run. Empty as
// well for a deployment that runs no sandbox containers at all, whose
// sandbox step is skipped anyway.
func sandboxImagePin(cfg Config) string {
	if !cfg.GranuleSandboxes {
		return ""
	}
	choice := chooseSandboxImage(cfg)
	if choice.source != sandboxImagePinnedHere {
		return ""
	}
	return choice.image
}

// stockSandboxImageFor is the image this process creates sandboxes from
// on cfg's active backend when no custom one is configured: the stamped
// (or flag-pinned) guest on granule, the backend's own image setting
// otherwise. "" for a backend with no image at all (host, none).
func stockSandboxImageFor(cfg Config) string {
	cfg.CustomSandboxImage = ""
	switch {
	case cfg.DisableNativeSandboxes:
		return ""
	case cfg.GranuleSandboxes:
		return chooseSandboxImage(cfg).image
	case cfg.ContainerSandboxes:
		return firstNonEmpty(cfg.ContainerImage, DefaultK8sSandboxImage)
	case cfg.K8sSandboxes:
		return EffectiveK8sSandboxImage(cfg)
	case cfg.K8sGranuleSandboxes:
		return EffectiveK8sGranuleImage(cfg)
	default:
		return ""
	}
}

// customSandboxImageFor is the custom sandbox image configured in
// Settings (model.Config.SandboxImage) that new sandboxes on cfg's active
// backend are created from instead of the stock one -- or "" when there
// is none, or when an explicit daemon flag names an image of its own,
// which wins: -sandbox-image or a -granule-create-arg -kontur-image pin
// on granule (chooseSandboxImage's sandboxImagePinnedHere), a
// -container-sandbox-image other than the stamped default on container.
// Kubernetes backends have no such flag; their k8s_* image settings are
// stored like this one is, and this one, being the more specific
// choice, wins.
func customSandboxImageFor(cfg Config) string {
	custom := strings.TrimSpace(cfg.CustomSandboxImage)
	if custom == "" {
		return ""
	}
	// Settings refuses such a value, but the state repository's import
	// does not go through Settings: one that would not reach a backend as
	// a single image reference is ignored here instead.
	if err := sandboximage.ValidateReference(custom); err != nil {
		return ""
	}
	switch {
	case cfg.DisableNativeSandboxes:
		return ""
	case cfg.GranuleSandboxes:
		if chooseSandboxImage(cfg).source == sandboxImagePinnedHere {
			return ""
		}
	case cfg.ContainerSandboxes:
		if cfg.ContainerImage != "" && cfg.ContainerImage != DefaultK8sSandboxImage && cfg.ContainerImage != model.DefaultK8sSandboxImage {
			return ""
		}
	case cfg.K8sSandboxes, cfg.K8sGranuleSandboxes:
		if mirrorRepo := sandboxImageMirrorRepo(cfg); mirrorRepo != "" && isMirroredGuestRef(custom, mirrorRepo) {
			return ""
		}
	default:
		return ""
	}
	return custom
}

// imageTag extracts the tag portion of ref (after the last ':' following the
// last '/', with any '@digest' stripped), or "" if ref has no tag.
func imageTag(ref string) string {
	ref = strings.TrimSpace(ref)
	if at := strings.LastIndex(ref, "@"); at >= 0 {
		ref = ref[:at]
	}
	if colon := strings.LastIndex(ref, ":"); colon > strings.LastIndex(ref, "/") {
		return ref[colon+1:]
	}
	return ""
}

// sandboxImageMirrorRepo returns the OCI repository (without tag or digest)
// into which the build-stamped guest sandbox image should be mirrored for
// cluster pulls, or "" when mirroring is not configured.
//
// An explicit cfg.SandboxImageMirror (-sandbox-image-mirror /
// grain_config.sandbox_image_mirror) always wins. Otherwise, on a
// kubernetes-granule deployment whose k8s_granule_image or sandbox_image
// names a bare Artifact Registry / GCR repository in another GCP project
// (e.g. "us-docker.pkg.dev/<project>/grain/guest") or a "sha-" guest tag in
// another project's ".../guest" repository, that repository is derived as the
// mirror target so upgrades automatically advance the mirrored guest image
// instead of staying pinned to an older commit's digest.
func sandboxImageMirrorRepo(cfg Config) string {
	if mirror := strings.TrimSpace(cfg.SandboxImageMirror); mirror != "" {
		return imageRepository(mirror)
	}
	if !cfg.K8sGranuleSandboxes {
		return ""
	}
	stampedRepo := imageRepository(DefaultSandboxImage)
	for _, candidate := range []string{cfg.K8sGranuleImage, cfg.SandboxImage} {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		repo := imageRepository(candidate)
		_, project, _ := k8s.ImageRegistryProject(repo)
		if repo == "" || repo == stampedRepo || project == "" {
			continue
		}
		tag := imageTag(candidate)
		if candidate == repo || (strings.HasSuffix(repo, "/guest") && strings.HasPrefix(tag, "sha-")) {
			return repo
		}
	}
	return ""
}

// isMirroredGuestRef reports whether ref is the bare mirror repository or a
// mirrored guest tag ("sha-..." or matching DefaultSandboxImage's tag) inside
// mirrorRepo.
func isMirroredGuestRef(ref, mirrorRepo string) bool {
	ref = strings.TrimSpace(ref)
	mirrorRepo = imageRepository(strings.TrimSpace(mirrorRepo))
	if ref == "" || mirrorRepo == "" {
		return false
	}
	if imageRepository(ref) != mirrorRepo {
		return false
	}
	tag := imageTag(ref)
	return tag == "" || strings.HasPrefix(tag, "sha-") || tag == imageTag(DefaultSandboxImage)
}

var (
	mirroredSandboxImagesMu  sync.Mutex
	mirroredSandboxImages    = map[string]string{}
	mirroredSandboxWarned    = map[string]bool{}
	sandboxMirrorDockerCmd   = []string{"docker"}
	sandboxMirrorTokenSource = registryauth.Token
	mirrorSandboxImageFunc   = defaultMirrorSandboxImage
	// sandboxMirrorRegistryDigest is what the mirror registry answers
	// for a tag; tests replace it.
	sandboxMirrorRegistryDigest = upgrade.RegistryManifestDigest
)

// ResetMirroredSandboxImageCacheForTest clears the in-memory mirrored sandbox
// image cache between unit tests.
func ResetMirroredSandboxImageCacheForTest() {
	mirroredSandboxImagesMu.Lock()
	defer mirroredSandboxImagesMu.Unlock()
	clear(mirroredSandboxImages)
	clear(mirroredSandboxWarned)
}

// resolveOrMirrorSandboxImage resolves the mirrored guest image reference in
// mirrorRepo corresponding to sourceRef (pulling sourceRef, tagging and
// pushing it to mirrorRepo, and pinning to mirrorRepo's RepoDigest when local
// docker is available).
func resolveOrMirrorSandboxImage(ctx context.Context, sourceRef, mirrorRepo string) string {
	mirrorRepo = imageRepository(strings.TrimSpace(mirrorRepo))
	targetTagRef := upgrade.MirrorSandboxRef(sourceRef, mirrorRepo)
	if targetTagRef == "" {
		return sourceRef
	}
	cacheKey := sourceRef + "|" + mirrorRepo
	mirroredSandboxImagesMu.Lock()
	if cached, ok := mirroredSandboxImages[cacheKey]; ok {
		mirroredSandboxImagesMu.Unlock()
		return cached
	}
	mirroredSandboxImagesMu.Unlock()

	pinned, err := mirrorSandboxImageFunc(ctx, sourceRef, mirrorRepo, targetTagRef)
	if err != nil {
		mirroredSandboxImagesMu.Lock()
		alreadyWarned := mirroredSandboxWarned[cacheKey]
		mirroredSandboxWarned[cacheKey] = true
		mirroredSandboxImagesMu.Unlock()
		if !alreadyWarned {
			log.Printf("WARNING: mirroring sandbox image %s to %s failed (%v); using %s", sourceRef, mirrorRepo, err, targetTagRef)
		}
		return targetTagRef
	}
	if pinned == "" {
		pinned = targetTagRef
	}
	mirroredSandboxImagesMu.Lock()
	mirroredSandboxImages[cacheKey] = pinned
	mirroredSandboxImagesMu.Unlock()
	return pinned
}

func defaultMirrorSandboxImage(ctx context.Context, sourceRef, mirrorRepo, targetTagRef string) (string, error) {
	if LocalSandboxes != nil && slices.Equal(sandboxMirrorDockerCmd, []string{"docker"}) {
		return targetTagRef, nil
	}
	if len(sandboxMirrorDockerCmd) == 0 {
		return targetTagRef, nil
	}
	if _, err := exec.LookPath(sandboxMirrorDockerCmd[0]); err != nil {
		return targetTagRef, nil
	}

	// Fast path: if stageImage (or an earlier startup) already pushed
	// targetTagRef and local docker has its RepoDigest for mirrorRepo,
	// resolve the digest directly without re-pushing -- once the mirror
	// itself still answers that digest for the tag. A RepoDigest is only
	// docker's memory of a push; the repository may since have been
	// emptied or recreated, and the cluster pulls from the registry.
	for _, inspectRef := range []string{targetTagRef, sourceRef} {
		if pinned := inspectMirroredRepoDigest(ctx, inspectRef, mirrorRepo, targetTagRef); pinned != "" {
			_, digest, _ := strings.Cut(pinned, "@")
			if remote, err := sandboxMirrorRegistryDigest(ctx, targetTagRef); err == nil && remote == digest {
				return pinned, nil
			}
			break
		}
	}

	// Ensure sourceRef is present locally before tagging.
	if _, err := runSandboxMirrorDocker(ctx, nil, "image", "inspect", "--format", "{{.Id}}", sourceRef); err != nil {
		_ = sandboxMirrorLogin(ctx, sourceRef)
		if _, pullErr := runSandboxMirrorDocker(ctx, nil, "pull", sourceRef); pullErr != nil {
			return "", fmt.Errorf("docker pull %s: %w", sourceRef, pullErr)
		}
	}
	if _, err := runSandboxMirrorDocker(ctx, nil, "tag", sourceRef, targetTagRef); err != nil {
		return "", fmt.Errorf("docker tag %s %s: %w", sourceRef, targetTagRef, err)
	}
	_ = sandboxMirrorLogin(ctx, targetTagRef)
	if _, err := runSandboxMirrorDocker(ctx, nil, "push", targetTagRef); err != nil {
		return "", fmt.Errorf("docker push %s: %w", targetTagRef, err)
	}
	if pinned := inspectMirroredRepoDigest(ctx, targetTagRef, mirrorRepo, targetTagRef); pinned != "" {
		log.Printf("grain daemon: mirrored sandbox image %s -> %s", sourceRef, pinned)
		return pinned, nil
	}
	log.Printf("grain daemon: mirrored sandbox image %s -> %s", sourceRef, targetTagRef)
	return targetTagRef, nil
}

func inspectMirroredRepoDigest(ctx context.Context, inspectRef, mirrorRepo, targetTagRef string) string {
	out, err := runSandboxMirrorDocker(ctx, nil, "image", "inspect", "--format", "{{json .RepoDigests}}", inspectRef)
	if err != nil {
		return ""
	}
	var digests []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &digests); err != nil || len(digests) == 0 {
		return ""
	}
	// Only a RepoDigest docker recorded for mirrorRepo itself says the
	// image is there: one for the source repository is what a local
	// `docker tag` carries over before any push, and pinning to it after
	// a push that failed would hand the cluster a reference the mirror
	// does not have.
	prefix := mirrorRepo + "@"
	for _, d := range digests {
		if strings.HasPrefix(d, prefix) {
			if _, digest, ok := strings.Cut(d, "@"); ok && digest != "" {
				return targetTagRef + "@" + digest
			}
		}
	}
	return ""
}

func sandboxMirrorLogin(ctx context.Context, imageRef string) error {
	host := registryauth.HostOf(imageRef)
	if !registryauth.IsGoogleRegistry(host) || sandboxMirrorTokenSource == nil {
		return nil
	}
	token, err := sandboxMirrorTokenSource(ctx)
	if err != nil || token == "" {
		return err
	}
	_, err = runSandboxMirrorDocker(ctx, strings.NewReader(token+"\n"),
		"login", "-u", "oauth2accesstoken", "--password-stdin", "https://"+host)
	return err
}

func runSandboxMirrorDocker(ctx context.Context, stdin *strings.Reader, args ...string) (string, error) {
	cmdline := append(slices.Clone(sandboxMirrorDockerCmd), args...)
	cmd := exec.CommandContext(ctx, cmdline[0], cmdline[1:]...)
	cmd.Env = append([]string(nil), os.Environ()...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if err != nil {
		// docker's own words -- "denied", "connection refused" -- are
		// what the mirror warning is read for; an exit status alone
		// says nothing about which.
		if msg := strings.TrimSpace(out.String()); msg != "" {
			err = fmt.Errorf("%w: %s", err, lastLine(msg))
		}
	}
	return out.String(), err
}

// lastLine is the last line of s, which is where docker puts the error
// a failed pull or push ended on, after its progress output.
func lastLine(s string) string {
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		return strings.TrimSpace(s[i+1:])
	}
	return s
}

func checkClusterSandboxImageConnectivity(ctx context.Context, cfg Config, imageRef string) error {
	if !cfg.K8sSandboxes && !cfg.K8sGranuleSandboxes {
		return nil
	}
	client := k8s.Client{
		Namespace:  firstNonEmpty(cfg.K8sNamespace, model.DefaultK8sNamespace),
		Kubectl:    cfg.K8sKubectl,
		Kubeconfig: EffectiveK8sKubeconfig(cfg),
	}
	return client.CheckImagePullConnectivity(ctx, imageRef)
}

// activeSandboxImageFor is the OCI image new sandboxes on cfg's active
// backend are created from: the custom image configured in Settings when
// one applies, else the backend's stock image, or "" when the active
// backend runs without an image (host, disabled).
func activeSandboxImageFor(cfg Config) string {
	return firstNonEmpty(customSandboxImageFor(cfg), stockSandboxImageFor(cfg))
}

// sandboxImageLabels is how the daemon reads a sandbox image's OCI
// labels: from the local docker daemon when it already has the image
// (granule and container hosts pull there), else from the image's
// registry (Kubernetes deployments, where the cluster pulls).
func sandboxImageLabels() sandboximage.LabelReader {
	return sandboximage.FirstLabels(sandboximage.DockerLabels(""), ui.FetchImageLabels)
}

// logCustomSandboxImage writes the contract verdict on a custom sandbox
// image to the journal -- the WARNING that replaces the commit-drift one
// for an image built FROM a grain base image, which is expected to be
// from another commit and is only wrong when its sandbox contract is.
// Run off the caller's goroutine: a registry round trip has no business
// holding up startup or a reconcile tick.
func logCustomSandboxImage(ctx context.Context, cfg Config, image, stock string, read sandboximage.LabelReader) {
	backend := string(cfg.SandboxBackend())
	base := sandboximage.BaseImageForBackend(backend, stock)
	labels, err := read(ctx, image)
	check := sandboximage.CheckCustomImage(image, backend, base, labels, err)
	if check.Warning != "" {
		log.Printf("WARNING: %s", check.Warning)
		return
	}
	log.Printf("grain daemon: custom sandbox image %s (Settings -> Sandboxes) in place of %s: %s",
		image, orUnknownImage(stock), check.Contract.Summary)
}
