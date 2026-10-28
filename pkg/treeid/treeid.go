// Package treeid names a tree by its content alone, as grain and grit
// both compute it (docs/design/grit-split.md, "Tree identity"):
//
//   - GitTree, the git tree object id, in the repository's own object
//     format; and
//   - SHA256, the sha256 of the tree's canonical archive, which is the
//     collision-resistant identity and an attestation's subject digest.
//
// The canonical archive is a tar stream of every entry in the tree,
// sorted by path bytes, with git's modes mapped to 0644, 0755, a
// symlink or a directory, mtime 0, uid and gid 0 and no user or group
// names. Entries are ustar, with pax headers only for what ustar cannot
// hold (a long path or link). A submodule is a zero-length regular entry
// whose link name is the gitlink commit, and is not expanded. `git
// archive` is not used, because it stamps the current time on a bare
// tree.
//
// It is a neutral package: it drives the git binary and imports nothing
// of grain's.
package treeid

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ID is a tree's identity.
type ID struct {
	SHA256  string `json:"sha256"`
	GitTree string `json:"gitTree"`
}

// ErrTooLarge is returned when a canonical archive passes its limit.
var ErrTooLarge = errors.New("treeid: canonical archive exceeds its size limit")

// Compute names treeish (a commit, a tree, or anything git resolves to
// a tree) in the repository at dir. max bounds the canonical archive in
// bytes, 0 meaning no bound; past it Compute answers ErrTooLarge.
func Compute(ctx context.Context, dir, treeish string, max int64) (ID, error) {
	tree, err := gitTree(ctx, dir, treeish)
	if err != nil {
		return ID{}, err
	}
	h := sha256.New()
	var w io.Writer = h
	var lw *limitWriter
	if max > 0 {
		lw = &limitWriter{w: h, left: max}
		w = lw
	}
	if err := Archive(ctx, dir, tree, w); err != nil {
		if lw != nil && lw.over {
			return ID{}, ErrTooLarge
		}
		return ID{}, err
	}
	return ID{SHA256: hex.EncodeToString(h.Sum(nil)), GitTree: tree}, nil
}

// Archive writes treeish's canonical archive to w.
func Archive(ctx context.Context, dir, treeish string, w io.Writer) error {
	tree, err := gitTree(ctx, dir, treeish)
	if err != nil {
		return err
	}
	entries, err := listTree(ctx, dir, tree)
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })

	cat := exec.CommandContext(ctx, "git", "-C", dir, "cat-file", "--batch")
	cat.Env = gitEnv()
	stdin, err := cat.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cat.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cat.Stderr = &stderr
	if err := cat.Start(); err != nil {
		return err
	}
	objects := bufio.NewReader(stdout)
	tw := tar.NewWriter(w)
	werr := func() error {
		for _, e := range entries {
			hdr := &tar.Header{Name: e.path, ModTime: time.Unix(0, 0), Uname: "", Gname: ""}
			switch e.mode {
			case "040000":
				hdr.Typeflag, hdr.Name, hdr.Mode = tar.TypeDir, e.path+"/", 0o755
				if err := tw.WriteHeader(hdr); err != nil {
					return err
				}
				continue
			case "160000":
				hdr.Typeflag, hdr.Mode, hdr.Linkname = tar.TypeReg, 0o644, e.oid
				if err := tw.WriteHeader(hdr); err != nil {
					return err
				}
				continue
			}
			body, err := readObject(stdin, objects, e.oid)
			if err != nil {
				return fmt.Errorf("treeid: %s: %w", e.path, err)
			}
			switch e.mode {
			case "100644":
				hdr.Typeflag, hdr.Mode = tar.TypeReg, 0o644
			case "100755":
				hdr.Typeflag, hdr.Mode = tar.TypeReg, 0o755
			case "120000":
				hdr.Typeflag, hdr.Mode, hdr.Linkname = tar.TypeSymlink, 0o777, string(body)
				body = nil
			default:
				return fmt.Errorf("treeid: %s has git mode %s", e.path, e.mode)
			}
			hdr.Size = int64(len(body))
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if _, err := tw.Write(body); err != nil {
				return err
			}
		}
		return tw.Close()
	}()
	stdin.Close()
	io.Copy(io.Discard, objects)
	if err := cat.Wait(); err != nil && werr == nil {
		return fmt.Errorf("treeid: git cat-file: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return werr
}

type entry struct{ mode, oid, path string }

func gitTree(ctx context.Context, dir, treeish string) (string, error) {
	if strings.HasPrefix(treeish, "-") {
		return "", fmt.Errorf("treeid: %q is not a tree-ish", treeish)
	}
	out, err := git(ctx, dir, "rev-parse", "--verify", "--end-of-options", treeish+"^{tree}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func listTree(ctx context.Context, dir, tree string) ([]entry, error) {
	out, err := git(ctx, dir, "ls-tree", "-r", "-t", "-z", "--full-tree", "--end-of-options", tree)
	if err != nil {
		return nil, err
	}
	var entries []entry
	for _, rec := range bytes.Split(out, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		meta, path, ok := bytes.Cut(rec, []byte{'\t'})
		f := strings.Fields(string(meta))
		if !ok || len(f) != 3 {
			return nil, fmt.Errorf("treeid: unexpected ls-tree record %q", rec)
		}
		entries = append(entries, entry{mode: f[0], oid: f[2], path: string(path)})
	}
	return entries, nil
}

// readObject asks cat-file --batch for oid and reads its body.
func readObject(stdin io.Writer, r *bufio.Reader, oid string) ([]byte, error) {
	if _, err := io.WriteString(stdin, oid+"\n"); err != nil {
		return nil, err
	}
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	f := strings.Fields(line)
	if len(f) != 3 || f[0] != oid || f[1] != "blob" {
		return nil, fmt.Errorf("cat-file answered %q for %s", strings.TrimSpace(line), oid)
	}
	size, err := strconv.ParseInt(f[2], 10, 64)
	if err != nil {
		return nil, err
	}
	body := make([]byte, size+1)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body[:size], nil
}

func git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = gitEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("treeid: git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// gitEnv is the environment git runs with here: a PATH and nothing else,
// so no caller's credentials reach it and no user or system config can
// change what it reads. Reading objects needs neither.
func gitEnv() []string {
	return []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
}

type limitWriter struct {
	w    io.Writer
	left int64
	over bool
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > l.left {
		l.over = true
		return 0, ErrTooLarge
	}
	l.left -= int64(len(p))
	return l.w.Write(p)
}
