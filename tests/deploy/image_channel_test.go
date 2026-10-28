package deploy

// scripts/setup.sh's choice of image tag (GRAIN_CHANNEL, grain/task-3689):
// a new deployment runs latest -- the last promoted release, not main's
// head -- an explicit GRAIN_IMAGE_TAG always wins, and a re-run keeps
// whatever channel (or pin) the deployment already runs, read from the
// unit's own image ref file, unless it is told otherwise.
//
// Run rather than read, like upgrade_allowlist_test.go: the block between
// the two markers is self-contained functions, sourced into a bash of its
// own with scratch ref files and asked what it decides.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/upgrade"
)

const (
	channelBegin = "# --- the image channel (GRAIN_CHANNEL)"
	channelEnd   = "# --- end of the image channel"
	channelImage = "ghcr.io/bwsalmon/grain/grain"
)

// channelShell runs script after sourcing setup.sh's channel functions,
// under setup.sh's own `set -euo pipefail`, with a clean slate for every
// variable they read and env added on top.
func channelShell(t *testing.T, env []string, script string, args ...string) (string, bool) {
	t.Helper()
	block := between(t, setupText(t), channelBegin, channelEnd)
	lib := filepath.Join(t.TempDir(), "channel.sh")
	if err := os.WriteFile(lib, []byte(block), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", append([]string{"-c", "set -euo pipefail\n" +
		"log() { echo \"==> $*\"; }\n" +
		"source \"$1\"\n" + script, "bash", lib}, args...)...)
	cmd.Env = append(append(os.Environ(),
		"GRAIN_IMAGE="+channelImage, "GRAIN_IMAGE_TAG=", "GRAIN_CHANNEL=", "GRAIN_REF="), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatalf("running bash: %v", err)
		}
	}
	return string(out), err == nil
}

// resolveTag runs resolve_image_tag over a unit ref file and a request
// ref file (paths that need not exist) and returns what it printed, the
// tag and the channel it settled on.
func resolveTag(t *testing.T, env []string, unitFile, requestFile string) (output, tag, channel string, ok bool) {
	t.Helper()
	out, ok := channelShell(t, env,
		"resolve_image_tag \"$2\" \"$3\" || exit 3\n"+
			"printf 'TAG=%s\\nCHANNEL=%s\\n' \"$GRAIN_IMAGE_TAG\" \"$GRAIN_CHANNEL\"\n", unitFile, requestFile)
	i := strings.LastIndex(out, "TAG=")
	if i < 0 {
		return out, "", "", ok
	}
	for _, line := range strings.Split(out[i:], "\n") {
		if v, found := strings.CutPrefix(line, "TAG="); found {
			tag = v
		}
		if v, found := strings.CutPrefix(line, "CHANNEL="); found {
			channel = v
		}
	}
	return out[:i], tag, channel, ok
}

func refFile(t *testing.T, ref string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "image.env")
	if err := os.WriteFile(path, []byte("GRAIN_IMAGE="+ref+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSetupInstallsANewDeploymentOnLatest(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "image.env")
	out, tag, channel, ok := resolveTag(t, nil, missing, missing)
	if !ok {
		t.Fatalf("a fresh install was refused:\n%s", out)
	}
	if tag != "latest" || channel != "latest" {
		t.Errorf("a fresh install with nothing set runs tag %q on channel %q, want latest -- the last promoted release, not main's head:\n%s",
			tag, channel, out)
	}
	contains(t, out, "Image: "+channelImage+":latest")
	contains(t, out, "the default for a new deployment")

	// And the resolution happens before anything names the image.
	code := setupCode(t)
	before(t, code, `resolve_image_tag /etc/grain/image.env "$GRAIN_DATA_DIR/image.env"`,
		`GRAIN_IMAGE_REF="${GRAIN_IMAGE}:${GRAIN_IMAGE_TAG}"`,
		"the tag has to be settled before the image ref is built from it")
}

func TestSetupHonoursAnExplicitTag(t *testing.T) {
	installed := refFile(t, channelImage+":main@sha256:0123abcd")
	for name, tc := range map[string]struct {
		env              []string
		tag, channel     string
		wantOutputSubstr string
	}{
		"GRAIN_IMAGE_TAG pins one build": {[]string{"GRAIN_IMAGE_TAG=sha-1234567"}, "sha-1234567", "", "GRAIN_IMAGE_TAG=sha-1234567, as given"},
		"GRAIN_IMAGE_TAG pins a release": {[]string{"GRAIN_IMAGE_TAG=v1.9.0"}, "v1.9.0", "latest", "pinned to v1.9.0"},
		"GRAIN_IMAGE_TAG wins over GRAIN_CHANNEL": {[]string{"GRAIN_IMAGE_TAG=candidate", "GRAIN_CHANNEL=latest"},
			"candidate", "candidate", "GRAIN_IMAGE_TAG=candidate wins over GRAIN_CHANNEL=latest"},
		"GRAIN_CHANNEL switches the channel": {[]string{"GRAIN_CHANNEL=candidate"}, "candidate", "candidate", "GRAIN_CHANNEL=candidate, as given"},
		"GRAIN_CHANNEL=rc aliases candidate": {[]string{"GRAIN_CHANNEL=rc"}, "candidate", "candidate", "GRAIN_CHANNEL=rc, as given"},
		"GRAIN_REF names a branch":           {[]string{"GRAIN_REF=release/2.1"}, "release..2.1", "release/2.1", "GRAIN_REF=release/2.1, as given"},
		"GRAIN_CHANNEL wins over GRAIN_REF":  {[]string{"GRAIN_CHANNEL=latest", "GRAIN_REF=main"}, "latest", "latest", "GRAIN_CHANNEL=latest"},
	} {
		t.Run(name, func(t *testing.T) {
			out, tag, channel, ok := resolveTag(t, tc.env, installed, installed)
			if !ok {
				t.Fatalf("refused:\n%s", out)
			}
			if tag != tc.tag || channel != tc.channel {
				t.Errorf("settled on tag %q channel %q, want %q and %q -- not the installed main:\n%s", tag, channel, tc.tag, tc.channel, out)
			}
			contains(t, out, tc.wantOutputSubstr)
		})
	}

	// A channel that is not one stops the install, rather than pulling a
	// tag nobody publishes.
	missing := filepath.Join(t.TempDir(), "image.env")
	if out, _, _, ok := resolveTag(t, []string{"GRAIN_CHANNEL=stable"}, missing, missing); ok {
		t.Errorf("GRAIN_CHANNEL=stable was accepted:\n%s", out)
	} else {
		contains(t, out, "not a channel")
	}
}

func TestSetupKeepsTheInstalledChannelOnARerun(t *testing.T) {
	for _, tc := range []struct{ ref, tag, channel, say string }{
		// Every deployment installed before this change: it runs main,
		// and a plain re-run must not move it to latest behind its back.
		{channelImage + ":main@sha256:0123abcd", "main", "main", "kept"},
		{channelImage + ":candidate", "candidate", "candidate", "kept"},
		{channelImage + ":rc", "rc", "candidate", "kept"},
		{channelImage + ":latest@sha256:0123abcd", "latest", "latest", "kept"},
		{channelImage + ":release..2.1", "release..2.1", "release/2.1", "kept"},
		// A pin stays pinned: re-running is not asking to move off it.
		{channelImage + ":sha-1234567", "sha-1234567", "", "pinned to sha-1234567, which follows no channel"},
		{channelImage + ":v1.9.0@sha256:0123abcd", "v1.9.0", "latest", "pinned to v1.9.0, upgrades offered from latest"},
		{channelImage + ":v1.10.0-rc.1@sha256:0123abcd", "v1.10.0-rc.1", "candidate", "pinned to v1.10.0-rc.1, upgrades offered from candidate"},
	} {
		t.Run(tc.tag, func(t *testing.T) {
			unit := refFile(t, tc.ref)
			out, tag, channel, ok := resolveTag(t, nil, unit, filepath.Join(t.TempDir(), "absent"))
			if !ok {
				t.Fatalf("a re-run was refused:\n%s", out)
			}
			if tag != tc.tag || channel != tc.channel {
				t.Errorf("a re-run over %s settled on tag %q channel %q, want %q and %q:\n%s", tc.ref, tag, channel, tc.tag, tc.channel, out)
			}
			contains(t, out, tc.say)
			contains(t, out, unit+" already runs "+tc.ref)
		})
	}

	// With no unit file yet, the daemon's own request file is read.
	request := refFile(t, channelImage+":candidate")
	out, tag, _, _ := resolveTag(t, nil, filepath.Join(t.TempDir(), "absent"), request)
	if tag != "candidate" {
		t.Errorf("with only the request file, settled on %q, want its candidate:\n%s", tag, out)
	}

	// An image from some other repository is not a channel of this one.
	foreign := refFile(t, "registry.example/other:main")
	out, tag, _, _ = resolveTag(t, nil, foreign, foreign)
	if tag != "latest" {
		t.Errorf("a ref file naming another repository kept %q, want the new-deployment default:\n%s", tag, out)
	}
	contains(t, out, "no tag of "+channelImage)

	// The last assignment wins, as systemd reads an EnvironmentFile.
	both := filepath.Join(t.TempDir(), "image.env")
	if err := os.WriteFile(both, []byte("GRAIN_IMAGE="+channelImage+":main\n  GRAIN_IMAGE=\""+channelImage+":candidate\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, tag, _, _ = resolveTag(t, nil, both, both); tag != "candidate" {
		t.Errorf("a ref file assigning twice settled on %q, want the last, candidate", tag)
	}
}

// The shell's channel_for_tag is only worth having if it agrees with
// pkg/upgrade's ChannelForRef, which is what the daemon offers upgrades
// from: the installer and the Upgrade button must name the same channel.
func TestSetupChannelMatchesChannelForRef(t *testing.T) {
	for _, tag := range []string{"latest", "candidate", "rc", "main", "v1.9.0", "v2.0.0-google", "v2.0.0-oss", "v1.10.0-rc.1", "v1.10.0-rc.1-discarded", "v2.0.0-rc.1-google", "v2.0.0-rc.1-oss", "sha-1234567", "release..2.1", "grain..task-1"} {
		out, ok := channelShell(t, nil, "channel_for_tag \"$2\"", tag)
		if !ok {
			t.Fatalf("channel_for_tag %q failed:\n%s", tag, out)
		}
		if want := upgrade.ChannelForRef(channelImage + ":" + tag); out != want {
			t.Errorf("channel_for_tag %q = %q, but pkg/upgrade.ChannelForRef says %q", tag, out, want)
		}
	}
}

// An install says which commit the image it pulled was built from, and
// warns when a branch channel's image is behind the branch: main's tag
// moves only once a release of a newer commit publishes, and the owner
// who installed main to pick up a change merged fifteen minutes earlier
// got f09c277 and no word of it (grain/task-4432).
func TestTheInstallSaysWhichCommitItsImageWasBuiltFrom(t *testing.T) {
	const (
		built = "f09c2770000000000000000000000000000000aa"
		head  = "2e9a809a3000000000000000000000000000000b"
	)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "docker"),
		[]byte("#!/bin/sh\necho \"$STUB_REVISION\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := "PATH=" + bin + ":" + os.Getenv("PATH")
	// GitHub is asked through the image's own curl (image_run), which
	// stands in here for the image: the commits endpoint answers a bare
	// sha for the sha media type, and compare answers JSON with ahead_by.
	imageRun := `image_run() {
  for a in "$@"; do last="$a"; done
  [ -n "$STUB_HEAD" ] || return 22
  case "$last" in
    */commits/main) printf '%s' "$STUB_HEAD" ;;
    */compare/*) printf '{\n  "status": "behind",\n  "ahead_by": 4,\n  "behind_by": 0\n}\n' ;;
    *) return 22 ;;
  esac
}
`
	report := func(tag, revision, branchHead string) string {
		t.Helper()
		out, ok := channelShell(t, []string{path, "STUB_REVISION=" + revision, "STUB_HEAD=" + branchHead},
			imageRun+"report_image_revision \""+channelImage+":"+tag+"\" "+tag+"\n")
		if !ok {
			t.Fatalf("report_image_revision failed the install:\n%s", out)
		}
		return out
	}

	out := report("main", built, head)
	containsRun(t, out, "was built from commit f09c27700")
	containsRun(t, out, "WARNING: the main image is f09c27700, 4 commit(s) behind main (2e9a809a3)")
	containsRun(t, out, "may still be publishing")

	if out := report("main", head, head); strings.Contains(out, "WARNING") {
		t.Errorf("warned about an image built from main's head:\n%s", out)
	}
	// latest, candidate/rc, and pins have no branch head to be behind.
	for _, tag := range []string{"latest", "candidate", "rc", "v1.9.0", "v1.10.0-rc.1", "sha-f09c277"} {
		out := report(tag, built, head)
		containsRun(t, out, "was built from commit f09c27700")
		if strings.Contains(out, "WARNING") || strings.Contains(out, "GitHub") {
			t.Errorf("%s: compared a release or pin with a branch head:\n%s", tag, out)
		}
	}
	// What it cannot find out, it says, and the install goes on.
	containsRun(t, report("main", built, ""), "could not ask GitHub")
	containsRun(t, report("main", "", head), "carries no revision label")
}

func TestSetupDetectsGoogleAndOSSInstallTracks(t *testing.T) {
	dir := t.TempDir()
	missingUnit := filepath.Join(dir, "absent.service")
	emptyDataDir := filepath.Join(dir, "empty-data")
	missingGoogleBin := filepath.Join(dir, "no-google-bin")

	runDetect := func(env []string, unitFile, dataDir string) (track, signals string) {
		t.Helper()
		baseEnv := []string{
			"GRAIN_HOST_FQDN=devbox.example.com",
			"GRAIN_GOOGLE_BIN_DIR=" + missingGoogleBin,
			"GRAIN_INSTALL_TRACK=",
			"GRAIN_AGY_PATH=",
			"GRAIN_UI_ALLOWED_HOSTS=",
			"GRAIN_TARGET_REPOS=",
			"GRAIN_TARGET_REPO=",
		}
		out, ok := channelShell(t, append(baseEnv, env...),
			"printf 'TRACK=%s\\nSIGNALS=%s\\n' \"$(detect_install_track \"$2\" \"$3\")\" \"$(detect_install_track_signals \"$2\" \"$3\")\"\n",
			unitFile, dataDir)
		if !ok {
			t.Fatalf("detect_install_track failed:\n%s", out)
		}
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			if v, found := strings.CutPrefix(line, "TRACK="); found {
				track = v
			}
			if v, found := strings.CutPrefix(line, "SIGNALS="); found {
				signals = v
			}
		}
		return track, signals
	}

	if track, signals := runDetect(nil, missingUnit, emptyDataDir); track != "oss" || signals != "" {
		t.Errorf("clean install detected track=%q signals=%q, want oss and empty", track, signals)
	}

	if track, signals := runDetect([]string{"GRAIN_HOST_FQDN=alice.c.googlers.com"}, missingUnit, emptyDataDir); track != "google" || signals != "host:*.c.googlers.com" {
		t.Errorf("cloudtop hostname detected track=%q signals=%q, want google and host:*.c.googlers.com", track, signals)
	}

	googleBin := filepath.Join(dir, "google-bin")
	if err := os.MkdirAll(googleBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if track, signals := runDetect([]string{"GRAIN_GOOGLE_BIN_DIR=" + googleBin}, missingUnit, emptyDataDir); track != "google" || signals != "host:/google/bin" {
		t.Errorf("/google/bin detected track=%q signals=%q, want google and host:/google/bin", track, signals)
	}

	unitFile := filepath.Join(dir, "grain-daemon.service")
	if err := os.WriteFile(unitFile, []byte("[Service]\nExecStart=/usr/local/bin/grain daemon -agy-path /google/bin/releases/jetski-devs/tools/cli -ui-allowed-hosts *.proxy.googlers.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if track, signals := runDetect(nil, unitFile, emptyDataDir); track != "google" || signals != "unit:-agy-path=/google/bin,unit:-ui-allowed-hosts=*.proxy.googlers.com" {
		t.Errorf("unit signals detected track=%q signals=%q, want google and unit signals", track, signals)
	}

	dataDir := filepath.Join(dir, "google-data")
	if err := os.MkdirAll(filepath.Join(dataDir, "state-repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	stateCfg := `{
  "gitConnections": [{"host": "depot.code.corp.goog", "ambient": true, "ambientSource": "ggh"}],
  "agentBackends": [{"type": "antigravity", "authMethod": "ambient", "cliPath": "/google/bin/releases/jetski-devs/tools/cli"}],
  "capabilities": ["corp-research"]
}`
	if err := os.WriteFile(filepath.Join(dataDir, "state-repo", "config.json"), []byte(stateCfg), 0o644); err != nil {
		t.Fatal(err)
	}
	wantSignals := "store:git-code.corp.goog,store:git-ggh,store:antigravity-ambient,store:antigravity-google-bin,store:capability=corp-research"
	if track, signals := runDetect(nil, missingUnit, dataDir); track != "google" || signals != wantSignals {
		t.Errorf("store signals detected track=%q signals=%q, want google and %q", track, signals, wantSignals)
	}
}

func TestSetupResolvesRepositoryForInstallTrackAndShiftsCleanly(t *testing.T) {
	dir := t.TempDir()
	missingUnit := filepath.Join(dir, "absent.service")
	emptyDataDir := filepath.Join(dir, "empty-data")
	missingGoogleBin := filepath.Join(dir, "no-google-bin")

	baseEnv := []string{
		"GRAIN_HOST_FQDN=devbox.example.com",
		"GRAIN_GOOGLE_BIN_DIR=" + missingGoogleBin,
		"GRAIN_INSTALL_TRACK=",
		"GRAIN_IMAGE_EXPLICIT=",
	}

	resolveRepoAndTag := func(env []string, unitRefFile, requestRefFile string) (out, image, tag, track string, ok bool) {
		t.Helper()
		out, ok = channelShell(t, append(baseEnv, env...),
			"resolve_image_repository \"$2\" \"$3\"\n"+
				"resolve_image_tag \"$4\" \"$5\" || exit 3\n"+
				"printf 'IMAGE=%s\\nTAG=%s\\nTRACK=%s\\n' \"$GRAIN_IMAGE\" \"$GRAIN_IMAGE_TAG\" \"$GRAIN_INSTALL_TRACK\"\n",
			missingUnit, emptyDataDir, unitRefFile, requestRefFile)
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			if v, found := strings.CutPrefix(line, "IMAGE="); found {
				image = v
			}
			if v, found := strings.CutPrefix(line, "TAG="); found {
				tag = v
			}
			if v, found := strings.CutPrefix(line, "TRACK="); found {
				track = v
			}
		}
		return out, image, tag, track, ok
	}

	// Pre-v2.0 deployment on ghcr.io/bwsalmon/grain/grain:candidate shifting to the Google repository.
	legacyCandidate := refFile(t, "ghcr.io/bwsalmon/grain/grain:candidate")
	out, image, tag, track, ok := resolveRepoAndTag([]string{
		"GRAIN_HOST_FQDN=alice.c.googlers.com",
	}, legacyCandidate, legacyCandidate)
	if !ok {
		t.Fatalf("resolve failed:\n%s", out)
	}
	if track != "google" || image != "ghcr.io/bwsalmon/grain/google-grain" || tag != "candidate" {
		t.Errorf("Google repository shift settled on track=%q image=%q tag=%q, want google, ghcr.io/bwsalmon/grain/google-grain, candidate:\n%s",
			track, image, tag, out)
	}
	contains(t, out, "kept across repository shift (ghcr.io/bwsalmon/grain/grain -> ghcr.io/bwsalmon/grain/google-grain)")

	// A Google deployment that an earlier installer moved onto Artifact
	// Registry -- which is not populated -- shifts back onto
	// ghcr.io/bwsalmon/grain/google-grain and keeps its channel.
	artifactRegistryCandidate := refFile(t, "us-docker.pkg.dev/gke-adaptive-orchestration/grain/grain:candidate")
	out, image, tag, track, ok = resolveRepoAndTag([]string{
		"GRAIN_HOST_FQDN=alice.c.googlers.com",
	}, artifactRegistryCandidate, artifactRegistryCandidate)
	if !ok {
		t.Fatalf("resolve failed:\n%s", out)
	}
	if track != "google" || image != "ghcr.io/bwsalmon/grain/google-grain" || tag != "candidate" {
		t.Errorf("Artifact Registry repository shift settled on track=%q image=%q tag=%q, want google, ghcr.io/bwsalmon/grain/google-grain, candidate:\n%s",
			track, image, tag, out)
	}
	contains(t, out, "kept across repository shift (us-docker.pkg.dev/gke-adaptive-orchestration/grain/grain -> ghcr.io/bwsalmon/grain/google-grain)")

	// One that opted into Artifact Registry explicitly stays there.
	out, image, _, _, ok = resolveRepoAndTag([]string{
		"GRAIN_HOST_FQDN=alice.c.googlers.com",
		"GRAIN_GOOGLE_IMAGE_REPO=us-docker.pkg.dev/gke-adaptive-orchestration/grain/grain",
	}, artifactRegistryCandidate, artifactRegistryCandidate)
	if !ok {
		t.Fatalf("resolve failed:\n%s", out)
	}
	if image != "us-docker.pkg.dev/gke-adaptive-orchestration/grain/grain" {
		t.Errorf("GRAIN_GOOGLE_IMAGE_REPO=%s settled on image=%q:\n%s", "us-docker.pkg.dev/gke-adaptive-orchestration/grain/grain", image, out)
	}

	// grain/grain:main, which before the split was the Google build and is
	// now core grain: a Google-track deployment on it moves onto
	// ghcr.io/bwsalmon/grain/google-grain:main, and an OSS-track one stays on grain:main and gets
	// the core build (grain/task-4944).
	legacyMain := refFile(t, "ghcr.io/bwsalmon/grain/grain:main")
	for _, c := range []struct {
		fqdn, track, image string
	}{
		{"alice.c.googlers.com", "google", "ghcr.io/bwsalmon/grain/google-grain"},
		{"devbox.example.com", "oss", "ghcr.io/bwsalmon/grain/grain"},
	} {
		out, image, tag, track, ok := resolveRepoAndTag([]string{"GRAIN_HOST_FQDN=" + c.fqdn}, legacyMain, legacyMain)
		if !ok {
			t.Fatalf("resolve failed:\n%s", out)
		}
		if track != c.track || image != c.image || tag != "main" {
			t.Errorf("%s on grain/grain:main settled on track=%q image=%q tag=%q, want %s, %s, main:\n%s",
				c.fqdn, track, image, tag, c.track, c.image, out)
		}
	}

	// Pre-v2.0 deployment shifting to a configured new OSS repository.
	out, image, tag, track, ok = resolveRepoAndTag([]string{
		"GRAIN_OSS_IMAGE_REPO=ghcr.io/grain-org/grain/grain",
	}, legacyCandidate, legacyCandidate)
	if !ok {
		t.Fatalf("resolve failed:\n%s", out)
	}
	if track != "oss" || image != "ghcr.io/grain-org/grain/grain" || tag != "candidate" {
		t.Errorf("OSS repository shift settled on track=%q image=%q tag=%q, want oss, ghcr.io/grain-org/grain/grain, candidate:\n%s",
			track, image, tag, out)
	}
}

// The extractor grain hostd's binary comes from (write_host_extractor)
// runs as the container unit's ExecStartPre, so a UI upgrade's request is
// promoted into /etc/grain/image.env there too, and a re-run keeps it.
func TestHostdExtractorUpgradeUpdatesUnitImageRefAndSurvivesRerun(t *testing.T) {
	dir := t.TempDir()
	requestFile := filepath.Join(dir, "data", "image.env")
	unitDir := filepath.Join(dir, "etc", "grain")
	unitFile := filepath.Join(unitDir, "image.env")
	extractor := filepath.Join(dir, "lib", "extract-host-binaries.sh")
	extractedIDFile := filepath.Join(dir, "lib", ".extracted-image-id")
	hostGrainBin := filepath.Join(dir, "bin", "grain")
	hostdVersions := filepath.Join(dir, "lib", "hostd")
	dockerStub := filepath.Join(dir, "bin", "docker")

	for _, d := range []string{filepath.Dir(requestFile), unitDir, filepath.Dir(extractor), filepath.Dir(hostGrainBin)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	const initialRef = channelImage + ":main@sha256:0123abcd"
	const upgradedLatestRef = channelImage + ":latest@sha256:89abef01"
	const upgradedCandidateRef = channelImage + ":candidate@sha256:89abef01"

	if err := os.WriteFile(unitFile, []byte("GRAIN_IMAGE="+initialRef+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stubScript := `#!/usr/bin/env bash
set -euo pipefail
case "$1" in
  image)
    echo "${STUB_IMAGE_ID:-sha256:89abef01}"
    ;;
  create)
    if [ "${STUB_FAIL_CREATE:-}" = "1" ]; then
      exit 1
    fi
    echo "stub-cid"
    ;;
  cp)
    case "$2" in
      *:/usr/local/bin/grain|*:/usr/local/bin/granule)
        printf '#!/bin/sh\n' > "$3"
        ;;
      *:/usr/local/share/grain/src/.)
        mkdir -p "$3"
        printf 'module github.com/bwsalmon/grain\n' > "$3/go.mod"
        ;;
    esac
    ;;
  rm)
    ;;
esac
`
	if err := os.WriteFile(dockerStub, []byte(stubScript), 0o755); err != nil {
		t.Fatal(err)
	}

	runExtractor := func(extraEnv ...string) {
		t.Helper()
		env := append([]string{
			"GRAIN_IMAGE=" + channelImage,
			"GRAIN_IMAGE_REF=" + initialRef,
			"GRAIN_USER=grain",
			"IMAGE_REF_FILE=" + requestFile,
			"UNIT_IMAGE_REF_DIR=" + unitDir,
			"UNIT_IMAGE_REF_FILE=" + unitFile,
			"HOST_EXTRACTOR=" + extractor,
			"EXTRACTED_ID_FILE=" + extractedIDFile,
			"HOSTD_EXTRACTED_ID_FILE=" + extractedIDFile,
			"HOST_GRAIN_BIN=" + hostGrainBin,
			"HOSTD_VERSIONS_DIR=" + hostdVersions,
			"DOCKER_BIN=" + dockerStub,
		}, extraEnv...)
		runBash(t, shellFunction(t, "image_ref_from")+shellFunction(t, "write_host_extractor")+
			"write_host_extractor\n\"$HOST_EXTRACTOR\"\n", env...)
	}

	// 1. UI upgrade switches from main to latest by writing $GRAIN_DATA_DIR/image.env,
	//    then grain-daemon.service restarts and its ExecStartPre runs extract-host-binaries.sh.
	if err := os.WriteFile(requestFile, []byte("GRAIN_IMAGE="+upgradedLatestRef+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runExtractor("STUB_IMAGE_ID=sha256:89abef01")

	// hostd's own copy is by image ID, behind the current link
	// grain-hostd.service runs and grain-hostd-upgrade.path watches.
	if got, err := os.Readlink(filepath.Join(hostdVersions, "current")); err != nil || got != "89abef01" {
		t.Fatalf("hostd's current link = %q (%v), want 89abef01", got, err)
	}
	if _, err := os.Stat(filepath.Join(hostdVersions, "89abef01", "grain")); err != nil {
		t.Fatal(err)
	}

	gotUnit, err := os.ReadFile(unitFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotUnit) != "GRAIN_IMAGE="+upgradedLatestRef+"\n" {
		t.Fatalf("/etc/grain/image.env after hostd upgrade = %q, want %q", gotUnit, "GRAIN_IMAGE="+upgradedLatestRef+"\n")
	}

	out, tag, channel, ok := resolveTag(t, nil, unitFile, requestFile)
	if !ok {
		t.Fatalf("resolve_image_tag failed after hostd upgrade:\n%s", out)
	}
	if tag != "latest" || channel != "latest" {
		t.Errorf("setup.sh re-run after UI upgrade settled on tag=%q channel=%q, want latest/latest (not reverting to main):\n%s",
			tag, channel, out)
	}

	// 2. Switching channel to candidate when candidate points to the same image ID
	//    (skipping extraction via .extracted-image-id fast path) still updates /etc/grain/image.env.
	if err := os.WriteFile(requestFile, []byte("GRAIN_IMAGE="+upgradedCandidateRef+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runExtractor("STUB_IMAGE_ID=sha256:89abef01", "STUB_FAIL_CREATE=1")

	gotUnit, err = os.ReadFile(unitFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotUnit) != "GRAIN_IMAGE="+upgradedCandidateRef+"\n" {
		t.Fatalf("/etc/grain/image.env on .extracted-image-id fast path = %q, want %q", gotUnit, "GRAIN_IMAGE="+upgradedCandidateRef+"\n")
	}

	out, tag, channel, ok = resolveTag(t, nil, unitFile, requestFile)
	if !ok {
		t.Fatalf("resolve_image_tag failed after fast-path channel switch:\n%s", out)
	}
	if tag != "candidate" || channel != "candidate" {
		t.Errorf("setup.sh re-run after fast-path channel switch settled on tag=%q channel=%q, want candidate/candidate:\n%s",
			tag, channel, out)
	}

	// 3. An untrusted/invalid image in $GRAIN_DATA_DIR/image.env is ignored and
	//    leaves /etc/grain/image.env untouched on candidate.
	if err := os.WriteFile(requestFile, []byte("GRAIN_IMAGE=evil.example/grain:main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runExtractor("STUB_IMAGE_ID=sha256:89abef01", "STUB_FAIL_CREATE=1")

	gotUnit, err = os.ReadFile(unitFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotUnit) != "GRAIN_IMAGE="+upgradedCandidateRef+"\n" {
		t.Fatalf("/etc/grain/image.env after untrusted request = %q, want %q preserved", gotUnit, "GRAIN_IMAGE="+upgradedCandidateRef+"\n")
	}

	// 4. Two more upgrades: each moves hostd's current link to a new
	//    directory, keeps the one it replaced (a draining hostd may still
	//    run it), and removes anything older.
	for _, id := range []string{"sha256:aaaa00000000ffff", "sha256:bbbb00000000ffff"} {
		if err := os.WriteFile(requestFile, []byte("GRAIN_IMAGE="+upgradedLatestRef+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runExtractor("STUB_IMAGE_ID=" + id)
	}
	if got, _ := os.Readlink(filepath.Join(hostdVersions, "current")); got != "bbbb00000000" {
		t.Errorf("hostd's current link after two upgrades = %q, want bbbb00000000", got)
	}
	entries, err := os.ReadDir(hostdVersions)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, " ") != "aaaa00000000 bbbb00000000 current" {
		t.Errorf("hostd's versions after two upgrades = %v, want the current one, the one it replaced, and the link", names)
	}
}
