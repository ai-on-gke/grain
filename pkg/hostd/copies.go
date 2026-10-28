package hostd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The installer keeps each build of grain it has taken out of a daemon
// image at <CopiesDir>/<version>/grain (setup.sh's HOSTD_VERSIONS_DIR), and points <CopiesDir>/current at
// the one the daemon's image carries now (scripts/setup.sh,
// write_host_extractor). grain-hostd.service runs hostd through
// current/grain, so the copy a hostd runs and the one current names
// differ exactly while an upgrade waits on hostd's drain
// (docs/design/cli-run-location.md, "Upgrades and host runs").
const (
	CopiesDir   = "/usr/local/lib/grain/hostd"
	CurrentName = "current"
)

// CurrentLinkFor is the current link argv0 was started through, or "" for
// a hostd not started through one (a development hostd, `grain demo`).
func CurrentLinkFor(argv0 string) string {
	dir := filepath.Dir(argv0)
	if filepath.Base(dir) != CurrentName || !filepath.IsAbs(dir) {
		return ""
	}
	return dir
}

// RunningCopy is the copy this process's own binary is: the image-id
// directory it sits in under the copies directory, or "" when it is not
// one of the installer's copies.
func RunningCopy() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return copyOf(exe)
}

// copyOf is the image-id directory a resolved binary path is in, if it
// is <copies>/<id>/grain.
func copyOf(bin string) string {
	dir := filepath.Dir(bin)
	if filepath.Base(bin) != "grain" || filepath.Base(dir) == CurrentName {
		return ""
	}
	if filepath.Base(filepath.Dir(dir)) != filepath.Base(CopiesDir) {
		return ""
	}
	return filepath.Base(dir)
}

// currentCopy reads where link points now: the image-id directory, or ""
// when it is not a link.
func currentCopy(link string) string {
	target, err := os.Readlink(link)
	if err != nil {
		return ""
	}
	return filepath.Base(target)
}

// copyBuilds remembers each copy's build, which `grain hostd -build`
// prints: a copy's binary never changes under the same image ID.
var copyBuilds sync.Map

// buildOf is the build the binary in copy dir answers `hostd -build`
// with, or "" for one that cannot say (a build from before the flag).
func buildOf(ctx context.Context, copyDir, id string) string {
	if v, ok := copyBuilds.Load(id); ok {
		return v.(string)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(copyDir, id, "grain"), "hostd", "-build")
	// Printing a build needs nothing of hostd's environment, and is
	// handed none of it.
	cmd.Env = []string{}
	out, err := cmd.Output()
	build := ""
	if err == nil {
		build = strings.TrimSpace(string(out))
	}
	copyBuilds.Store(id, build)
	return build
}

// describeCopies fills h's Running, Current and CurrentBuild for a hostd
// started through link.
func describeCopies(ctx context.Context, h *Hello, link string) {
	h.Running = RunningCopy()
	if link == "" {
		return
	}
	h.Current = currentCopy(link)
	switch {
	case h.Current == "":
	case h.Current == h.Running:
		h.CurrentBuild = h.Build
	default:
		h.CurrentBuild = buildOf(ctx, filepath.Dir(link), h.Current)
	}
}
