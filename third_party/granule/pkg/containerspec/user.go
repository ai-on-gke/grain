package containerspec

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Credential is the user a container's process runs as, resolved against
// the wrapped filesystem.
//
// Set distinguishes "run as root" from "was not asked": an image with no
// USER runs as whatever granule-container itself is, which is root, and
// saying so by not setting credentials at all keeps the ordinary case
// free of a setuid/setgid that could only fail.
type Credential struct {
	UID, GID uint32
	Home     string
	Set      bool
}

// ResolveUser turns an image's USER into numbers, looking names up in
// the wrapped filesystem's own /etc/passwd and /etc/group.
//
// Those files are the only place a name means anything: the guest around
// the container has its own accounts, and "nginx" in an image is not the
// guest's "nginx". Reading them is also all this does -- they are parsed,
// never executed, and a malformed line is skipped rather than failing the
// boot.
//
// Supplementary groups are deliberately not resolved: a container's
// secondary groups come from the runtime's own /etc/group handling, and
// guessing at them here would be a security decision made on a file the
// wrapped image authored. A workload that needs one can be given it in
// its own entrypoint.
func ResolveUser(rootfs, user string) (Credential, error) {
	user = strings.TrimSpace(user)
	if user == "" {
		return Credential{}, nil
	}

	name, group, hasGroup := strings.Cut(user, ":")
	if name == "" {
		return Credential{}, fmt.Errorf("the user %q names no user", user)
	}

	var cred Credential
	cred.Set = true
	if uid, err := strconv.ParseUint(name, 10, 32); err == nil {
		cred.UID = uint32(uid)
		// A numeric user need not be in the image's passwd file at all --
		// a distroless image typically has no passwd file. Where there is
		// an entry, its group and home are the ones the image intends;
		// where there is not, the group is root's, which is what a
		// container runtime does with the same input.
		if entry, ok := lookupPasswdByUID(rootfs, uint32(uid)); ok {
			cred.GID, cred.Home = entry.gid, entry.home
		}
	} else {
		entry, ok := lookupPasswdByName(rootfs, name)
		if !ok {
			return Credential{}, fmt.Errorf("the image runs as user %q, which is not in its own /etc/passwd", name)
		}
		cred.UID, cred.GID, cred.Home = entry.uid, entry.gid, entry.home
	}

	if hasGroup {
		if group == "" {
			return Credential{}, fmt.Errorf("the user %q names no group after the colon", user)
		}
		if gid, err := strconv.ParseUint(group, 10, 32); err == nil {
			cred.GID = uint32(gid)
		} else {
			gid, ok := lookupGroup(rootfs, group)
			if !ok {
				return Credential{}, fmt.Errorf("the image runs as group %q, which is not in its own /etc/group", group)
			}
			cred.GID = gid
		}
	}
	return cred, nil
}

type passwdEntry struct {
	name string
	uid  uint32
	gid  uint32
	home string
}

func lookupPasswdByName(rootfs, name string) (passwdEntry, bool) {
	return findPasswd(rootfs, func(e passwdEntry) bool { return e.name == name })
}

func lookupPasswdByUID(rootfs string, uid uint32) (passwdEntry, bool) {
	return findPasswd(rootfs, func(e passwdEntry) bool { return e.uid == uid })
}

func findPasswd(rootfs string, match func(passwdEntry) bool) (passwdEntry, bool) {
	var found passwdEntry
	ok := false
	eachColonLine(rootfs, "/etc/passwd", func(fields []string) bool {
		if len(fields) < 6 {
			return true
		}
		uid, err := strconv.ParseUint(fields[2], 10, 32)
		if err != nil {
			return true
		}
		gid, err := strconv.ParseUint(fields[3], 10, 32)
		if err != nil {
			return true
		}
		entry := passwdEntry{name: fields[0], uid: uint32(uid), gid: uint32(gid), home: fields[5]}
		if match(entry) {
			found, ok = entry, true
			return false
		}
		return true
	})
	return found, ok
}

func lookupGroup(rootfs, name string) (uint32, bool) {
	var gid uint32
	ok := false
	eachColonLine(rootfs, "/etc/group", func(fields []string) bool {
		if len(fields) < 3 || fields[0] != name {
			return true
		}
		n, err := strconv.ParseUint(fields[2], 10, 32)
		if err != nil {
			return true
		}
		gid, ok = uint32(n), true
		return false
	})
	return gid, ok
}

// eachColonLine walks a passwd/group-shaped file, calling fn until it
// returns false. A file that is not there is not an error: an image
// without one simply has no names to resolve.
//
// The file is the image's, found the way the chroot will find it
// (ResolveIn): an /etc/passwd shipped as a symlink to /etc/passwd means
// the image's own, not the guest's.
func eachColonLine(rootfs, name string, fn func(fields []string) bool) {
	resolved, err := ResolveIn(rootfs, name)
	if err != nil {
		return
	}
	f, err := os.Open(filepath.Join(rootfs, resolved))
	if err != nil {
		return
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !fn(strings.Split(line, ":")) {
			return
		}
	}
}
