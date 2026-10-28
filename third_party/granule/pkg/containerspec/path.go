package containerspec

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// maxLinks is how many symlinks one path may pass through before it is
// taken for a loop, the same ceiling the kernel's own path walk uses.
const maxLinks = 40

// ResolveIn follows symlinks the way the kernel will once the chroot has
// happened: a link to an absolute path means that path *inside* the
// wrapped filesystem, and ".." stops at the wrapped filesystem's root.
// The answer is the path inside the container; joined to rootfs it names
// a file on the guest that no symlink stands between.
//
// Nothing else gets this right from out here. Alpine's /bin/sh is a
// symlink to /bin/busybox, and a plain os.Stat of rootfs + "/bin/sh"
// follows it to the *guest's* /bin/busybox, which does not exist -- so
// the most ordinary image there is looks like an image whose entrypoint
// is missing.
//
// It is also what keeps granule-container, which runs as root on the
// guest before any chroot, inside the image it was handed. The image is
// somebody else's: a VOLUME of "../../etc", or a /data shipped as a
// symlink to /etc, is a path that filepath.Join plus os.MkdirAll and
// os.Chown would happily follow onto the guest's own /etc. Everything
// granule-container creates, writes or chowns on the image's behalf is
// resolved through here first.
func ResolveIn(rootfs, path string) (string, error) {
	resolved, _, err := walkIn(rootfs, path, false, 0)
	return resolved, err
}

// MkdirIn is os.MkdirAll for a path inside the wrapped filesystem,
// resolved the way ResolveIn resolves it, creating whatever is missing
// along the way. It returns the directory's resolved path inside the
// container and whether its last component had to be created.
func MkdirIn(rootfs, path string, perm fs.FileMode) (string, bool, error) {
	resolved, created, err := walkIn(rootfs, path, true, perm)
	if err != nil {
		return "", false, err
	}
	info, err := os.Lstat(filepath.Join(rootfs, resolved))
	if err != nil {
		return "", false, err
	}
	if !info.IsDir() {
		return "", false, fmt.Errorf("%s: %w", path, syscall.ENOTDIR)
	}
	return resolved, created, nil
}

// WriteFileIn writes data to name inside the wrapped filesystem, creating
// the directories above it. A symlink where the file itself should be is
// replaced by the file rather than written through: the file this is
// asked to write is the container's own copy of something the guest
// supplies (its resolver configuration), and an image's /etc/resolv.conf
// that points at /run/systemd/resolve/stub-resolv.conf -- an ordinary
// thing for one to do -- names a file on the guest, not in the image,
// until the chroot has happened.
func WriteFileIn(rootfs, name string, data []byte, perm fs.FileMode) error {
	dir, _, err := MkdirIn(rootfs, filepath.Dir(filepath.Clean("/"+name)), 0o755)
	if err != nil {
		return err
	}
	dest := filepath.Join(rootfs, dir, filepath.Base(name))
	if info, err := os.Lstat(dest); err == nil && info.Mode()&os.ModeSymlink != 0 {
		if err := os.Remove(dest); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// walkIn is ResolveIn, and with create set MkdirIn: a missing component
// is made as a directory rather than reported. The bool says whether the
// final component was one of those.
func walkIn(rootfs, path string, create bool, perm fs.FileMode) (string, bool, error) {
	remaining := split(path)
	resolved := "/"
	links := 0
	createdLast := false
	for len(remaining) > 0 {
		component := remaining[0]
		remaining = remaining[1:]
		switch component {
		case "", ".":
			continue
		case "..":
			resolved = filepath.Dir(resolved)
			continue
		}
		next := filepath.Join(resolved, component)
		info, err := os.Lstat(filepath.Join(rootfs, next))
		if create && errors.Is(err, fs.ErrNotExist) {
			if err := os.Mkdir(filepath.Join(rootfs, next), perm); err != nil {
				return "", false, err
			}
			resolved = next
			createdLast = len(remaining) == 0
			continue
		}
		if err != nil {
			return "", false, err
		}
		createdLast = false
		if info.Mode()&os.ModeSymlink == 0 {
			resolved = next
			continue
		}
		links++
		if links > maxLinks {
			return "", false, fmt.Errorf("%s is a chain of symlinks with no end to it", path)
		}
		target, err := os.Readlink(filepath.Join(rootfs, next))
		if err != nil {
			return "", false, err
		}
		if strings.HasPrefix(target, "/") {
			resolved = "/"
		}
		remaining = append(split(target), remaining...)
	}
	return resolved, createdLast, nil
}

func split(path string) []string {
	return strings.Split(strings.Trim(path, "/"), "/")
}
