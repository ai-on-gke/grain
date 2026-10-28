package guestwrap

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// treeCopier copies a filesystem tree the way a docker build's COPY
// does, for WrapDir, which has no build to do it: modes (setuid and
// sticky bits included), ownership, symlinks as symlinks, hard links as
// links, device nodes, fifos, extended attributes and timestamps, so
// that `mke2fs -d` over the copy packs the filesystem the image carried.
//
// It only ever reads the source and only ever creates in the
// destination. Nothing in the source is followed: a symlink is copied as
// the link it is, so a link the wrapped image planted at /etc or at
// /usr/bin is a link inside the guest and never a way out of the tree on
// the host, and every file is opened O_NOFOLLOW.
type treeCopier struct {
	// owners preserves ownership and creates device nodes, both of which
	// need root. Without it every file belongs to whoever is copying and
	// device nodes are left out, which is a tree for inspection rather
	// than one a guest boots from -- see DirOptions.Unprivileged.
	owners bool

	links    map[fileID]string
	dirs     []copiedDir
	skipped  []string
	xattrBuf []byte
}

type fileID struct{ dev, ino uint64 }

// copiedDir is a directory whose mode and times are set after its
// contents are written: a 0555 directory cannot be copied into once its
// mode is, and writing a child moves its mtime.
type copiedDir struct {
	path string
	info fs.FileInfo
}

func newTreeCopier(owners bool) *treeCopier {
	return &treeCopier{owners: owners, links: map[fileID]string{}}
}

// copy copies the tree at src to dst, which must not exist yet.
func (c *treeCopier) copy(src, dst string) error {
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if err := c.entry(path, target, info); err != nil {
			return fmt.Errorf("copying /%s: %w", strings.TrimPrefix(filepath.ToSlash(rel), "."), err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Deepest first, so that setting a directory's times is not undone by
	// setting a child's mode.
	for i := len(c.dirs) - 1; i >= 0; i-- {
		dir := c.dirs[i]
		if err := os.Chmod(dir.path, dir.info.Mode()&(fs.ModePerm|fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky)); err != nil {
			return err
		}
		if err := setTimes(dir.path, dir.info); err != nil {
			return err
		}
	}
	c.dirs = nil
	return nil
}

func (c *treeCopier) entry(src, dst string, info fs.FileInfo) error {
	st, _ := info.Sys().(*syscall.Stat_t)
	mode := info.Mode()
	perm := mode & (fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky)

	if st != nil && !mode.IsDir() && st.Nlink > 1 {
		id := fileID{uint64(st.Dev), uint64(st.Ino)}
		if first, ok := c.links[id]; ok {
			return os.Link(first, dst)
		}
		c.links[id] = dst
	}

	switch mode.Type() {
	case fs.ModeDir:
		// Owner-writable until its contents are in; copy sets the real
		// mode afterwards.
		if err := os.Mkdir(dst, 0o700); err != nil {
			return err
		}
		c.dirs = append(c.dirs, copiedDir{dst, info})
	case 0:
		if err := copyFile(src, dst); err != nil {
			return err
		}
	case fs.ModeSymlink:
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		if err := os.Symlink(target, dst); err != nil {
			return err
		}
	case fs.ModeNamedPipe:
		if err := unix.Mkfifo(dst, 0o600); err != nil {
			return err
		}
	case fs.ModeDevice, fs.ModeDevice | fs.ModeCharDevice:
		if !c.owners || st == nil {
			c.skipped = append(c.skipped, src)
			return nil
		}
		kind := uint32(unix.S_IFBLK)
		if mode&fs.ModeCharDevice != 0 {
			kind = unix.S_IFCHR
		}
		if err := unix.Mknod(dst, kind|0o600, int(st.Rdev)); err != nil {
			return err
		}
	default:
		// A socket is something a process left behind, not part of an
		// image; docker's own export drops them too.
		c.skipped = append(c.skipped, src)
		return nil
	}

	if c.owners && st != nil {
		// Before the mode: chown clears setuid and setgid.
		if err := os.Lchown(dst, int(st.Uid), int(st.Gid)); err != nil {
			return err
		}
	}
	if err := c.copyXattrs(src, dst); err != nil {
		return err
	}
	if mode.Type() == fs.ModeSymlink || mode.IsDir() {
		if mode.IsDir() {
			return nil
		}
		return setTimes(dst, info)
	}
	if err := os.Chmod(dst, perm); err != nil {
		return err
	}
	return setTimes(dst, info)
}

// copyFile copies one regular file's bytes. Neither end follows a
// symlink: the source was a regular file when it was listed, and is
// refused if it has since become a link; the destination is created
// fresh.
func copyFile(src, dst string) error {
	in, err := os.OpenFile(src, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// copyXattrs copies extended attributes -- file capabilities, most
// often, which is how an image's ping works without setuid. A filesystem
// on either side that does not do xattrs, or an attribute this process
// may not set, leaves it out: unprivileged, the trusted.* and security.*
// namespaces are root's.
func (c *treeCopier) copyXattrs(src, dst string) error {
	names, err := c.listXattrs(src)
	if err != nil || len(names) == 0 {
		return nil
	}
	for _, name := range names {
		value, err := c.getXattr(src, name)
		if err != nil {
			continue
		}
		if err := unix.Lsetxattr(dst, name, value, 0); err != nil {
			if c.owners && !ignorableXattrError(err) {
				return fmt.Errorf("setting %s: %w", name, err)
			}
		}
	}
	return nil
}

func ignorableXattrError(err error) bool {
	return errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP)
}

func (c *treeCopier) listXattrs(path string) ([]string, error) {
	size, err := unix.Llistxattr(path, nil)
	if err != nil || size <= 0 {
		return nil, err
	}
	buf := make([]byte, size)
	size, err = unix.Llistxattr(path, buf)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, name := range strings.Split(string(buf[:size]), "\x00") {
		if name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

func (c *treeCopier) getXattr(path, name string) ([]byte, error) {
	size, err := unix.Lgetxattr(path, name, nil)
	if err != nil {
		return nil, err
	}
	value := make([]byte, size)
	size, err = unix.Lgetxattr(path, name, value)
	if err != nil {
		return nil, err
	}
	return value[:size], nil
}

// setTimes gives dst the source's access and modification times without
// following a symlink at dst.
func setTimes(dst string, info fs.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	times := []unix.Timespec{
		unix.NsecToTimespec(syscall.TimespecToNsec(st.Atim)),
		unix.NsecToTimespec(syscall.TimespecToNsec(st.Mtim)),
	}
	return unix.UtimesNanoAt(unix.AT_FDCWD, dst, times, unix.AT_SYMLINK_NOFOLLOW)
}
