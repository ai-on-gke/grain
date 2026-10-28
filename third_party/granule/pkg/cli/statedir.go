package cli

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
)

// rootStateDir is where a root granule keeps VM state: a node's shared
// state, on the reasoning that every VM root creates belongs to the node
// rather than to whoever typed the command.
//
// A var rather than a const only so the tests can point the root case at
// a directory they are allowed to write to; nothing in granule assigns
// to it.
var rootStateDir = "/var/lib/granule/vms"

// geteuid is os.Geteuid, indirected so the tests can exercise the root
// branch of defaultStateDir without running as root.
var geteuid = os.Geteuid

// defaultStateDir is the directory granule saves VM specs in when
// -state-dir isn't given. It follows the caller rather than being one
// fixed path:
//
//   - root gets rootStateDir, which is what it has always been and what
//     a node's shared state should be;
//   - anyone else gets $XDG_STATE_HOME/granule/vms, or
//     ~/.local/state/granule/vms when XDG_STATE_HOME is unset (or set to
//     something relative, which the XDG spec says to ignore).
//
// The alternative was leaving it at rootStateDir for everyone, which an
// unprivileged caller cannot create: every such caller had to pass
// -state-dir on every command, which is why the README's flows all used
// to carry the flag. Nothing that works today changes -- a root caller
// gets the same directory it always did, and an unprivileged one used to
// get an error here rather than a working command.
//
// What it does mean is that "where is my VM?" depends on who is asking,
// so a "vm create" without sudo followed by a "vm list" with it looks
// like a VM that vanished. stateDirNote is what says so when a lookup in
// a default state directory comes up empty.
func defaultStateDir() string {
	if geteuid() == 0 {
		return rootStateDir
	}
	if dir := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(dir) {
		return filepath.Join(dir, "granule", "vms")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".local", "state", "granule", "vms")
	}
	// No home directory to put it under: fall back to the shared path,
	// which is the answer this had before it followed the caller. It
	// probably isn't writable, and ensureStateDir says so naming the
	// flag.
	return rootStateDir
}

// userStateDir is defaultStateDir's non-root answer for a named user's
// home directory, used to point a root caller at the state an
// unprivileged one saved.
func userStateDir(home string) string {
	return filepath.Join(home, ".local", "state", "granule", "vms")
}

// stateDirNote explains where the VMs a *differently privileged* caller
// created live, for a lookup that came up empty in the default state
// directory. It is the cost of defaultStateDir following the caller: a
// VM created without sudo has containers a "sudo granule vm list" can
// see and a spec it cannot, and the way that reads is "my VM is gone".
//
// Empty when -state-dir was given (the caller has already said where to
// look, so there is nothing to guess at), and empty for a root caller
// that wasn't invoked through sudo, since there is no particular user
// whose state to point at.
func stateDirNote(stateDir string) string {
	if stateDir != defaultStateDir() {
		return ""
	}
	if geteuid() != 0 {
		return fmt.Sprintf("a VM created as root (under sudo) has its state in %s instead -- pass -state-dir %s to look there", rootStateDir, rootStateDir)
	}
	name := os.Getenv("SUDO_USER")
	if name == "" {
		return ""
	}
	u, err := user.Lookup(name)
	if err != nil || u.HomeDir == "" {
		return ""
	}
	dir := userStateDir(u.HomeDir)
	return fmt.Sprintf("a VM %s created without sudo usually has its state in %s instead -- pass -state-dir %s to look there", name, dir, dir)
}

// stateDirNoteSuffix is stateDirNote punctuated for the end of a "VM not
// found" message.
func stateDirNoteSuffix(stateDir string) string {
	if note := stateDirNote(stateDir); note != "" {
		return "; " + note
	}
	return ""
}

// ensureStateDir creates stateDir if it isn't there and proves granule
// can write into it, so a directory that can't hold a VM's saved state is
// refused before anything is started.
//
// This used to be left to vmspec.Save, which runs *after* the VM's
// containers have been started -- so a state directory the caller cannot
// write to failed with a VM already running and nothing saved to find it
// by again. The default is now a directory its caller
// can create (see defaultStateDir), so this mostly catches an explicit
// -state-dir that points somewhere unwritable.
//
// MkdirAll succeeding is not enough on its own: an existing directory
// owned by someone else is created "successfully" and still cannot be
// written to, hence the probe file.
func ensureStateDir(stateDir string) error {
	const hint = "pass -state-dir to put it somewhere writable"
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return fmt.Errorf("state directory %s cannot be created (%w); %s", stateDir, err, hint)
	}
	probe, err := os.CreateTemp(stateDir, ".granule-writable-")
	if err != nil {
		return fmt.Errorf("state directory %s is not writable (%w); %s", stateDir, err, hint)
	}
	probe.Close()
	return os.Remove(probe.Name())
}
