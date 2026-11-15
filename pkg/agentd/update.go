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

package agentd

// `grain-agentd update`: the owner-run half of the self-update in
// design/design/agentd-stable-contract.design.md ("The safety valve").
//
//  1. Fetch the signed manifest and binary for this platform from the
//     controller's /api/hostd/agentd/{os}/{arch} routes (or -from DIR).
//  2. Verify the signature against the release key pinned at install
//     (~/.config/grain/agentd-release.pub, else the one in this binary)
//     -- never a key the controller supplies -- and the SHA-256; refuse a
//     downgrade unless the allowlist's update.pin names that version, and
//     anything outside the pin.
//  3. Stage it in ~/.local/lib/grain-agentd/versions/<v>/, run it with
//     -build and -check against the current allowlist, and only then move
//     the current link ~/.local/bin/grain-agentd resolves through. The
//     previous version stays.
//  4. Ask the running grain-agentd for an idle cutover (OpDrain with
//     Idle), as local hostd's grain-hostd-upgrade.path does; systemd
//     --user (Restart=always) starts the new build, which adopts the old
//     one's spools.
//  5. Roll back if the new build has not answered the controller's hello
//     within UpdateConfirmWindow: the unit's ExecStartPre guard (a shell
//     script, so a new build that cannot even start is still rolled back)
//     and the new build's own watchdog (updateWatchdog) both move current
//     back to the previous version.
//
// The daemon never starts this: `auto` (the controller asking for the
// update) is not implemented, and a root-installed `grain host` has no
// such command.

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"golang.org/x/mod/semver"

	"github.com/ai-on-gke/grain/pkg/agentd/release"
	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/version"
)

// UpdateConfirmWindow is how long a new build has, from its first start,
// to answer the controller's hello before it is rolled back.
var UpdateConfirmWindow = 5 * time.Minute

const (
	maxUpdateManifestBytes = 1 << 20
	maxUpdateBinaryBytes   = 512 << 20
)

// versionDirRE is what a version may be to name a directory under
// versions/ (and a line of the pending file the guard script reads).
var versionDirRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+_-]{0,127}$`)

// updateLayout is where versions live, under ~/.local/lib/grain-agentd:
//
//	versions/<v>/grain-agentd   each staged build (0755)
//	current -> versions/<v>     the build ~/.local/bin/grain-agentd runs
//	pending                     "<previous>\n<new>\n" until the new build
//	                            reaches the controller
//	pending.started             touched by the guard at the new build's
//	                            first start: the confirm window's start
//	update-failed               the pending file of the last rollback
//	guard.sh                    the unit's ExecStartPre
type updateLayout struct {
	Lib, Versions, Current, Pending, Started, Failed, Guard string
}

func layoutFor(home string) updateLayout {
	lib := filepath.Join(home, ".local", "lib", Program)
	return updateLayout{
		Lib:      lib,
		Versions: filepath.Join(lib, "versions"),
		Current:  filepath.Join(lib, "current"),
		Pending:  filepath.Join(lib, "pending"),
		Started:  filepath.Join(lib, "pending.started"),
		Failed:   filepath.Join(lib, "update-failed"),
		Guard:    filepath.Join(lib, "guard.sh"),
	}
}

func (l updateLayout) binary(v string) string { return filepath.Join(l.Versions, v, Program) }

// currentVersion is the version directory current names, "" when there
// is none.
func (l updateLayout) currentVersion() string {
	target, err := os.Readlink(l.Current)
	if err != nil {
		return ""
	}
	return filepath.Base(target)
}

// updateDeps are update's seams for tests.
var (
	updateHTTPClient = &http.Client{Timeout: 2 * time.Minute}
	updatePlatform   = runtime.GOOS + "/" + runtime.GOARCH
	// runStaged runs a staged binary with args and returns its output.
	runStaged = func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		c := exec.CommandContext(ctx, bin, args...)
		var out bytes.Buffer
		c.Stdout, c.Stderr = &out, &out
		err := c.Run()
		return out.Bytes(), err
	}
)

func update(args []string, stdout, stderr io.Writer) int {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(stderr, Program+" update: "+err.Error())
		return 2
	}
	p := pathsFor(home)
	unitServer, unitIAP := unitDefaults(filepath.Join(p.UnitDir, UnitName))
	fs := flag.NewFlagSet(Program+" update", flag.ContinueOnError)
	fs.SetOutput(stderr)
	want := fs.String("version", "", "take exactly this release, and refuse whatever else the controller offers; needed for a build without a semantic version")
	server := fs.String("server", unitServer, "the grain daemon's URL to fetch the release from (default: the installed unit's -server)")
	iapAudience := fs.String("iap-audience", unitIAP, "the OAuth client ID of the IAP in front of the daemon (default: the installed unit's)")
	from := fs.String("from", "", "read the release from this directory (grain-agentd-<os>-<arch> and its .manifest.json) rather than the controller; verified the same way")
	allowlist := fs.String("allowlist", p.Allowlist, "the allowlist to read update: from and to -check the new build against")
	socket := fs.String("socket", defaultSocket(), "the running host agent's socket, asked for the cutover")
	noCutover := fs.Bool("no-cutover", false, "stage and switch the current link, but leave the running host agent alone: it moves to the new build at its next restart")
	guard := fs.Bool("guard", false, "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *guard {
		// What the guard script does, for a shell without one.
		if msg, err := guardRollback(layoutFor(home), time.Now()); err != nil {
			fmt.Fprintln(stderr, Program+" update -guard: "+err.Error())
			return 1
		} else if msg != "" {
			fmt.Fprintln(stderr, Program+": "+msg)
		}
		return 0
	}
	if geteuid() == 0 {
		fmt.Fprintln(stderr, Program+" update: run this as yourself, not root: "+Program+" belongs to the user it runs as")
		return 2
	}
	step := func(format string, a ...any) { fmt.Fprintf(stdout, "==> "+format+"\n", a...) }
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	u := updater{home: home, p: p, l: layoutFor(home), allowlist: *allowlist, step: step}
	if err := u.run(ctx, *want, *server, *iapAudience, *from); err != nil {
		if errors.Is(err, errUpToDate) {
			return 0
		}
		fmt.Fprintln(stderr, Program+" update: "+err.Error())
		return 1
	}
	if *noCutover {
		step("-no-cutover: the running host agent moves to %s at its next restart (systemctl --user restart %s)", u.staged, UnitName)
		return 0
	}
	if err := u.cutover(ctx, *socket); err != nil {
		fmt.Fprintln(stderr, Program+" update: "+err.Error())
		return 1
	}
	return 0
}

var errUpToDate = errors.New("up to date")

type updater struct {
	home      string
	p         paths
	l         updateLayout
	allowlist string
	step      func(format string, a ...any)

	staged string // the version staged and made current
}

func (u *updater) run(ctx context.Context, want, server, iapAudience, from string) error {
	a, err := hostd.LoadAllowlist(u.allowlist, owners())
	if err != nil {
		return fmt.Errorf("reading the allowlist: %w", err)
	}
	policy := a.Update
	if policy.EffectiveMode() == hostd.UpdateOff {
		return fmt.Errorf("%s sets update.mode: off; update by reinstalling, or change the mode", u.allowlist)
	}
	pinned, err := PinnedReleasePublicKey(u.home)
	if err != nil {
		return fmt.Errorf("the pinned release key: %w", err)
	}

	manifestRaw, fetchBinary, err := u.source(ctx, server, iapAudience, from)
	if err != nil {
		return err
	}
	m, err := release.ParseManifest(manifestRaw)
	if err != nil {
		return err
	}
	if m.Platform != updatePlatform {
		return fmt.Errorf("the release is for %s, not this machine's %s", m.Platform, updatePlatform)
	}
	signer, err := release.VerifyManifest(m, pinned)
	if err != nil {
		return fmt.Errorf("refusing the release: %w", err)
	}
	u.step("release %s for %s, signed by key %s", m.Version, m.Platform, release.KeyID(signer))

	running := u.installedBuild(ctx)
	if err := checkUpdateVersion(running, m.Version, want, policy); err != nil {
		if errors.Is(err, errUpToDate) {
			u.step("already on %s", running)
		}
		return err
	}
	if !versionDirRE.MatchString(m.Version) {
		return fmt.Errorf("release version %q cannot name a directory", m.Version)
	}

	binary, err := fetchBinary()
	if err != nil {
		return err
	}
	if err := release.VerifyBinaryHash(m, binary); err != nil {
		return fmt.Errorf("refusing the release: %w", err)
	}

	// Stage, then smoke-test, before anything the running unit uses moves.
	staged := u.l.binary(m.Version)
	if err := os.MkdirAll(filepath.Dir(staged), 0o755); err != nil {
		return err
	}
	if err := writeFileAtomic(staged, binary, 0o755); err != nil {
		return fmt.Errorf("staging %s: %w", staged, err)
	}
	u.step("staged %s", staged)
	out, err := runStaged(ctx, staged, "-build")
	if err != nil {
		return fmt.Errorf("%s -build: %v\n%s", staged, err, out)
	}
	if got := strings.TrimSpace(string(out)); got != m.Version {
		return fmt.Errorf("%s -build says %q, but its manifest says %q", staged, got, m.Version)
	}
	if out, err := runStaged(ctx, staged, "-check", "-allowlist", u.allowlist); err != nil {
		return fmt.Errorf("%s -check -allowlist %s: %v\n%s", staged, u.allowlist, err, out)
	}
	u.step("%s -build and -check pass", m.Version)

	previous, err := u.adoptLayout(ctx, running, m.Version)
	if err != nil {
		return err
	}
	if previous == m.Version {
		return fmt.Errorf("%s is already current, staged by an earlier update; start it with: systemctl --user restart %s", m.Version, UnitName)
	}
	if err := writeGuard(u.l); err != nil {
		return err
	}
	if err := writeFileAtomic(u.l.Pending, []byte(previous+"\n"+m.Version+"\n"), 0o644); err != nil {
		return err
	}
	_ = os.Remove(u.l.Started)
	if err := switchCurrent(u.l, m.Version); err != nil {
		_ = os.Remove(u.l.Pending)
		return err
	}
	u.staged = m.Version
	u.step("current is %s (previous %s kept for rollback)", m.Version, previous)

	// A rotated release key is pinned only once its release has verified.
	if !bytes.Equal(signer, pinned) {
		pem, err := release.FormatPublicKeyPEM(signer)
		if err == nil {
			err = writeFileAtomic(u.p.ReleasePub, pem, 0o644)
		}
		if err != nil {
			return fmt.Errorf("pinning the rotated release key: %w", err)
		}
		u.step("pinned rotated release key %s in %s", release.KeyID(signer), u.p.ReleasePub)
	}
	if err := u.ensureUnitGuard(); err != nil {
		u.step("note: %v; a new build that cannot start is not rolled back without it", err)
	}
	return nil
}

// installedBuild is the build ~/.local/bin/grain-agentd runs -- which the
// binary running update need not be, when the owner runs a newer one --
// or this binary's own when there is none.
func (u *updater) installedBuild(ctx context.Context) string {
	if out, err := runStaged(ctx, u.p.Bin, "-build"); err == nil {
		if v := strings.TrimSpace(string(out)); v != "" {
			return v
		}
	}
	return version.Get().Release
}

// source is the manifest and a func fetching the binary, from -from or
// the controller.
func (u *updater) source(ctx context.Context, server, iapAudience, from string) ([]byte, func() ([]byte, error), error) {
	binName, err := release.AssetName(updatePlatform)
	if err != nil {
		return nil, nil, err
	}
	if from != "" {
		manifestName, _ := release.ManifestAssetName(updatePlatform)
		raw, err := readBounded(filepath.Join(from, manifestName), maxUpdateManifestBytes)
		if err != nil {
			return nil, nil, err
		}
		return raw, func() ([]byte, error) { return readBounded(filepath.Join(from, binName), maxUpdateBinaryBytes) }, nil
	}
	su, err := parseServer(server)
	if err != nil {
		return nil, nil, fmt.Errorf("%v (or pass -from DIR)", err)
	}
	base := strings.TrimRight(su.String(), "/") + "/api/hostd/agentd/" + updatePlatform
	get := func(url string, limit int64) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		if iapAudience != "" {
			tok, err := hostd.NewIAPTokenProvider(iapAudience).Token(ctx)
			if err != nil {
				return nil, fmt.Errorf("IAP token: %w", err)
			}
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := updateHTTPClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			return nil, fmt.Errorf("GET %s: %s: %s", url, resp.Status, strings.TrimSpace(string(msg)))
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
		if err != nil {
			return nil, err
		}
		if int64(len(b)) > limit {
			return nil, fmt.Errorf("GET %s: more than %d bytes", url, limit)
		}
		return b, nil
	}
	raw, err := get(base+"/manifest.json", maxUpdateManifestBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("fetching the release manifest: %w", err)
	}
	return raw, func() ([]byte, error) { return get(base, maxUpdateBinaryBytes) }, nil
}

func readBounded(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err == nil && int64(len(b)) > limit {
		err = fmt.Errorf("%s is larger than %d bytes", path, limit)
	}
	return b, err
}

// checkUpdateVersion is whether running may move to target: inside the
// pin; never older unless the pin names target exactly; and, for a
// target that is not a semantic version (so cannot be ordered), only
// when the owner asked for it by name with -version.
func checkUpdateVersion(running, target, want string, policy hostd.UpdatePolicy) error {
	if want != "" && target != want {
		return fmt.Errorf("the controller offers %s, not the %s -version asks for", target, want)
	}
	if !policy.Allows(target) {
		return fmt.Errorf("%s is outside the allowlist's update.pin %q", target, policy.Pin)
	}
	if target == running {
		return errUpToDate
	}
	if !semver.IsValid(target) {
		if want == "" {
			return fmt.Errorf("%s is not a released version; take it only by name: -version %s", target, target)
		}
		return nil
	}
	if semver.IsValid(running) && semver.Compare(target, running) < 0 && policy.Pin != target {
		return fmt.Errorf("%s is older than this build's %s: refusing a downgrade (set update.pin: %s in the allowlist to ask for it)", target, running, target)
	}
	return nil
}

// adoptLayout makes ~/.local/bin/grain-agentd resolve through current,
// moving an installed binary into versions/ first, and returns the
// version current names now: the one a rollback returns to.
func (u *updater) adoptLayout(ctx context.Context, running, target string) (string, error) {
	if v := u.l.currentVersion(); v != "" {
		if _, err := os.Stat(u.l.binary(v)); err == nil {
			if err := linkBin(u.p.Bin, u.l); err != nil {
				return "", err
			}
			return v, nil
		}
	}
	// No current link yet: the binary install copied in is the previous
	// version.
	src := u.p.Bin
	if st, err := os.Lstat(src); err != nil || !st.Mode().IsRegular() {
		self, err := os.Executable()
		if err != nil {
			return "", err
		}
		src = self
	}
	prev := running
	if out, err := runStaged(ctx, src, "-build"); err == nil {
		prev = strings.TrimSpace(string(out))
	}
	if !versionDirRE.MatchString(prev) {
		prev = "previous"
	}
	if prev == target {
		prev += "-previous"
	}
	dest := u.l.binary(prev)
	if same, _ := sameFile(src, dest); !same {
		if err := copyExecutable(src, dest); err != nil {
			return "", fmt.Errorf("keeping the installed binary as %s: %w", dest, err)
		}
	}
	if err := switchCurrent(u.l, prev); err != nil {
		return "", err
	}
	if err := linkBin(u.p.Bin, u.l); err != nil {
		return "", err
	}
	u.step("kept the installed %s as %s", prev, dest)
	return prev, nil
}

// switchCurrent points current at versions/v, atomically.
func switchCurrent(l updateLayout, v string) error {
	if _, err := os.Stat(l.binary(v)); err != nil {
		return err
	}
	return replaceSymlink(filepath.Join("versions", v), l.Current)
}

// linkBin makes bin a symlink to current/grain-agentd.
func linkBin(bin string, l updateLayout) error {
	target := filepath.Join(l.Current, Program)
	if t, err := os.Readlink(bin); err == nil && t == target {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		return err
	}
	return replaceSymlink(target, bin)
}

func replaceSymlink(target, link string) error {
	tmp := fmt.Sprintf("%s.tmp-%d", link, os.Getpid())
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, link); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// guardScript is the unit's ExecStartPre. It is shell, not this binary,
// so that a new build that cannot start at all is rolled back too. At
// the first start after an update it starts the confirm window; at a
// later one past the window, with the update still unconfirmed, it moves
// current back to the previous version.
const guardScript = `#!/bin/sh
# grain-agentd update guard: the ExecStartPre of grain-agentd.service,
# written by ` + "`grain-agentd update`" + `. Rolls current back to the previous
# version when a new build has not reached the controller in time.
lib="$(dirname "$0")"
pending="$lib/pending"
[ -f "$pending" ] || exit 0
if [ ! -f "$lib/pending.started" ]; then
	: > "$lib/pending.started"
	exit 0
fi
started=$(stat -c %%Y "$lib/pending.started" 2>/dev/null) || exit 0
now=$(date +%%s)
[ $((now - started)) -ge %d ] || exit 0
prev=$(sed -n 1p "$pending")
new=$(sed -n 2p "$pending")
case "$prev" in ""|*/*|.*) exit 0 ;; esac
[ -x "$lib/versions/$prev/grain-agentd" ] || exit 0
ln -sfn "versions/$prev" "$lib/current" || exit 0
mv -f "$pending" "$lib/update-failed"
rm -f "$lib/pending.started"
echo "grain-agentd: rolled back from $new to $prev: $new did not reach the controller within %d seconds" >&2
exit 0
`

func writeGuard(l updateLayout) error {
	secs := int(UpdateConfirmWindow / time.Second)
	return writeFileAtomic(l.Guard, []byte(fmt.Sprintf(guardScript, secs, secs)), 0o755)
}

// guardRollback is guardScript in Go: `grain-agentd update -guard`, and
// what the tests hold the script to.
func guardRollback(l updateLayout, now time.Time) (string, error) {
	raw, err := os.ReadFile(l.Pending)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	st, err := os.Stat(l.Started)
	if errors.Is(err, os.ErrNotExist) {
		return "", os.WriteFile(l.Started, nil, 0o644)
	} else if err != nil {
		return "", err
	}
	if now.Sub(st.ModTime()) < UpdateConfirmWindow {
		return "", nil
	}
	return rollback(l, raw)
}

// rollback moves current back to the pending file's previous version.
func rollback(l updateLayout, pending []byte) (string, error) {
	prev, next, _ := strings.Cut(strings.TrimSpace(string(pending)), "\n")
	if !versionDirRE.MatchString(prev) {
		return "", fmt.Errorf("%s names no previous version", l.Pending)
	}
	if err := switchCurrent(l, prev); err != nil {
		return "", err
	}
	if err := os.Rename(l.Pending, l.Failed); err != nil {
		return "", err
	}
	_ = os.Remove(l.Started)
	return fmt.Sprintf("rolled back from %s to %s: %s did not reach the controller within %s", strings.TrimSpace(next), prev, strings.TrimSpace(next), UpdateConfirmWindow), nil
}

// ensureUnitGuard adds the guard to an installed unit that has none (one
// written before this build), and reloads systemd --user.
func (u *updater) ensureUnitGuard() error {
	unitPath := filepath.Join(u.p.UnitDir, UnitName)
	b, err := os.ReadFile(unitPath)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("no %s", unitPath)
	} else if err != nil {
		return err
	}
	text, changed := withGuard(string(b), u.l.Guard)
	if !changed {
		return nil
	}
	if err := writeFileAtomic(unitPath, []byte(text), 0o644); err != nil {
		return err
	}
	u.step("added the rollback guard to %s", unitPath)
	if err := systemctl(io.Discard, "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl --user daemon-reload: %w", err)
	}
	return nil
}

// withGuard is unit with an ExecStartPre running guard, if it has none.
func withGuard(unit, guard string) (string, bool) {
	line := "ExecStartPre=-/bin/sh " + guard
	if strings.Contains(unit, line) {
		return unit, false
	}
	if i := strings.Index(unit, "\nExecStart="); i >= 0 {
		return unit[:i+1] + line + unit[i:], true
	}
	return unit, false
}

// unitDefaults reads -server and -iap-audience from the installed unit's
// ExecStart, so `grain-agentd update` needs no flags on a host install set
// up.
func unitDefaults(unitPath string) (server, iap string) {
	b, err := os.ReadFile(unitPath)
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		rest, ok := strings.CutPrefix(line, "ExecStart=")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		for i := 0; i+1 < len(f); i++ {
			switch f[i] {
			case "-server":
				server = f[i+1]
			case "-iap-audience":
				iap = f[i+1]
			}
		}
	}
	return server, iap
}

// cutover asks the running host agent to move to the new build at the
// first moment none of its agent CLIs runs.
func (u *updater) cutover(ctx context.Context, socket string) error {
	c := hostd.Client{Socket: socket, Build: version.Get().Release}
	hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := os.Stat(socket)
	var h hostd.Hello
	if err == nil {
		h, err = c.Hello(hctx)
	}
	if err != nil {
		u.step("no host agent answers on %s (%v); start the new build with: systemctl --user restart %s", socket, err, UnitName)
		return nil
	}
	if err := askDrain(hctx, c); err != nil {
		return fmt.Errorf("asking the host agent (%s) to cut over: %w", h.Build, err)
	}
	u.step("asked %s to cut over: it exits the first moment no agent CLI runs, and systemd --user starts %s, which adopts the detached runs", h.Build, u.staged)
	u.step("watch it with: journalctl --user -u %s -f; it rolls back to the previous version if %s has not reached the controller within %s", Program, u.staged, UpdateConfirmWindow)
	return nil
}

// updateWatchdog is a new build's half of the rollback: when the pending
// update names this build, it is confirmed the first time the controller
// asks for its hello, and otherwise rolled back -- current moved back and
// the host agent cut over, so systemd starts the previous version --
// UpdateConfirmWindow after it started. It returns the hello callback for
// hostd.Server.OnControllerHello, and a func that stops the watchdog.
func updateWatchdog(home, build string, logf func(string, ...any), cutover func()) (onHello func(), stop func()) {
	l := layoutFor(home)
	raw, err := os.ReadFile(l.Pending)
	if err != nil {
		return nil, func() {}
	}
	_, next, _ := strings.Cut(strings.TrimSpace(string(raw)), "\n")
	if strings.TrimSpace(next) != build || l.currentVersion() != build {
		return nil, func() {}
	}
	done := make(chan struct{})
	confirmed := make(chan struct{}, 1)
	go func() {
		t := time.NewTimer(UpdateConfirmWindow)
		defer t.Stop()
		select {
		case <-done:
		case <-confirmed:
			if os.Remove(l.Pending) == nil {
				_ = os.Remove(l.Started)
				logf("%s: update to %s confirmed: the controller has said hello", Program, build)
			}
		case <-t.C:
			raw, err := os.ReadFile(l.Pending)
			if err != nil {
				return
			}
			msg, err := rollback(l, raw)
			if err != nil {
				logf("%s: rolling back: %v", Program, err)
				return
			}
			logf("%s: %s; cutting over to it", Program, msg)
			cutover()
		}
	}()
	return func() {
			select {
			case confirmed <- struct{}{}:
			default:
			}
		}, func() {
			select {
			case <-done:
			default:
				close(done)
			}
		}
}
