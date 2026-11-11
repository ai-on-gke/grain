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

package hostd

// hostd's half of OpStage and OpListen (carry.go), for a daemon on
// another machine.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ai-on-gke/grain/pkg/tunnel/mux"
)

type sessionKey struct{}

// sessionMux is the session a request arrived over, or nil for one on
// the socket.
func sessionMux(ctx context.Context) *mux.Mux {
	m, _ := ctx.Value(sessionKey{}).(*mux.Mux)
	return m
}

// defaultStageDir is StageRoot's default under hostd's home: a
// directory of its own, not ~/.grain itself, which on a workstation is
// the local daemon's data directory (grain init workstation) -- its
// database and keys, which a daemon on another machine must not be able
// to read, overwrite or remove, nor hand a CLI as a path.
const defaultStageDir = ".grain/hostd-stage"

// stageRoot is StageRoot, or ~/.grain/hostd-stage, resolved through any
// symlink in it and created if it is not there.
func (s *Server) stageRoot() string {
	root := s.StageRoot
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		root = filepath.Join(home, filepath.FromSlash(defaultStageDir))
	}
	abs, err := filepath.Abs(expandHome(root))
	if err != nil {
		return ""
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return ""
	}
	return resolved
}

// describeRemote fills in what a daemon on another machine needs to know
// about this one (Hello.Remote).
func (s *Server) describeRemote(h *Hello, allow *Allowlist) {
	h.Remote = true
	h.StageRoot = s.stageRoot()
	h.Grain = s.CurrentLink
	if h.Grain == "" {
		if exe, err := os.Executable(); err == nil {
			h.Grain = exe
		}
	}
	targets := allow.loginTargets()
	h.LoginTargets = targets
	h.CodexAuth = targets[LoginCodex]
}

// withCLIRoot is a with root added to every CLI entry's paths_under and
// cwd_under: a copy, so the file's own entries are left as they are.
func (a *Allowlist) withCLIRoot(root string) *Allowlist {
	if root == "" {
		return a
	}
	out := *a
	out.CLIs = make(map[string]*Entry, len(a.CLIs))
	for name, e := range a.CLIs {
		c := *e
		c.PathsUnder = append(append([]string(nil), e.PathsUnder...), root)
		c.CwdUnder = append(append([]string(nil), e.CwdUnder...), root)
		out.CLIs[name] = &c
	}
	return &out
}

// stage runs one OpStage operation. Every path it touches, but a
// symlink's target, is under stageRoot, reached through no symlink.
func (s *Server) stage(op *StageOp) StageReply {
	var allow *Allowlist
	if s.Allowlist != "" {
		a, _, err := s.loadAllowlist()
		if err != nil {
			return StageReply{Error: err.Error()}
		}
		allow = a
	}
	return s.stageWithAllowlist(op, allow)
}

func (s *Server) stageWithAllowlist(op *StageOp, allow *Allowlist) StageReply {
	if op == nil {
		return StageReply{Error: "stage: no operation"}
	}
	root := s.stageRoot()
	if root == "" {
		return StageReply{Error: "stage: this hostd has no staging directory (no home directory?)"}
	}
	fail := func(err error) StageReply {
		return StageReply{Error: err.Error(), NotExist: errors.Is(err, fs.ErrNotExist)}
	}
	perm := fs.FileMode(op.Perm) & fs.ModePerm
	switch op.Op {
	case StageMkdirTemp:
		dir, err := stageTempDir(root)
		if err != nil {
			return fail(err)
		}
		path, err := os.MkdirTemp(dir, op.Pattern)
		if err != nil {
			return fail(err)
		}
		return StageReply{Path: path}
	case StageMkdirAll:
		path, err := confined(root, op.Path)
		if err != nil {
			return fail(err)
		}
		dirPerm := perm & 0o700
		if dirPerm == 0 {
			dirPerm = 0o700
		}
		if err := os.MkdirAll(path, dirPerm); err != nil {
			return fail(err)
		}
		// MkdirAll succeeds on an existing symlink to a directory,
		// leaving the link in place for the caller's next write or CLI.
		if _, err := confinedDir(root, path); err != nil {
			return fail(err)
		}
		return StageReply{Path: path}
	case StageWriteFile:
		path, err := confined(root, op.Path)
		if err != nil {
			return fail(err)
		}
		filePerm := perm & 0o600
		if filePerm == 0 {
			filePerm = 0o600
		}
		if err := writeRegularFile(path, op.Data, filePerm); err != nil {
			return fail(err)
		}
		return StageReply{Path: path}
	case StageCreateTemp:
		var (
			dir string
			err error
		)
		if op.Dir == "" {
			dir, err = stageTempDir(root)
		} else {
			dir, err = confinedDir(root, op.Dir)
		}
		if err != nil {
			return fail(err)
		}
		f, err := os.CreateTemp(dir, op.Pattern)
		if err != nil {
			return fail(err)
		}
		err = f.Chmod(0o600)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return fail(err)
		}
		return StageReply{Path: f.Name()}
	case StageSymlink:
		link, err := confined(root, op.Path)
		if err != nil {
			return fail(err)
		}
		// Links outside the staging root may only target regular files
		// named in the allowlist's logins: map (or the built-in table
		// when absent), resolved on this machine.
		target, ok := allow.resolveSymlinkTarget(op.Target)
		if !ok {
			return StageReply{Error: "stage: a symlink may only target a configured login file"}
		}
		if err := os.Symlink(target, link); err != nil {
			return fail(err)
		}
		return StageReply{Path: link}
	case StageReadTail:
		path, err := confined(root, op.Path)
		if err != nil {
			return fail(err)
		}
		limit := op.Limit
		if limit <= 0 || limit > MaxStageData {
			limit = MaxStageData
		}
		data, truncated, err := readTail(path, limit)
		if err != nil {
			return fail(err)
		}
		return StageReply{Path: path, Data: data, Truncated: truncated}
	case StageRemoveAll:
		path, err := confined(root, op.Path)
		if err != nil {
			return fail(err)
		}
		if path == root {
			return StageReply{Error: "stage: refusing to remove the staging root itself"}
		}
		if err := os.RemoveAll(path); err != nil {
			return fail(err)
		}
		return StageReply{Path: path}
	case StagePrune:
		path, err := confinedDir(root, op.Path)
		if err != nil {
			return fail(err)
		}
		if err := prune(path, op.MaxAge, int(op.Limit)); err != nil {
			return fail(err)
		}
		return StageReply{Path: path}
	}
	return StageReply{Error: fmt.Sprintf("stage: unknown operation %q", op.Op)}
}

// prune is StagePrune: dir's subdirectories older than maxAge removed,
// then all but the keep newest. A symlink in dir is left alone.
func prune(dir string, maxAge time.Duration, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	type sub struct {
		path string
		mod  time.Time
	}
	var kept []sub
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if maxAge > 0 && time.Since(info.ModTime()) > maxAge {
			_ = os.RemoveAll(path)
			continue
		}
		kept = append(kept, sub{path, info.ModTime()})
	}
	if keep > 0 && len(kept) > keep {
		sort.Slice(kept, func(i, j int) bool { return kept[i].mod.After(kept[j].mod) })
		for _, s := range kept[keep:] {
			_ = os.RemoveAll(s.path)
		}
	}
	return nil
}

// confined is path, "~/" expanded and cleaned, when it is root or under
// it and nothing between root and its last element is a symlink: a link
// planted inside the staging root cannot take a write outside it. The
// last element itself is left to the operation, which does not follow it.
func confined(root, path string) (string, error) {
	if path == "" {
		return "", errors.New("stage: no path")
	}
	path = filepath.Clean(expandHome(path))
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("stage: %s is not an absolute path", path)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("stage: %s is outside this hostd's staging directory %s", path, root)
	}
	if rel == "." {
		return root, nil
	}
	cur := root
	parts := strings.Split(rel, string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			break
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return "", fmt.Errorf("stage: %s passes through the symlink %s", path, cur)
		}
	}
	return path, nil
}

// confinedDir is confined for an operation that follows path itself --
// creating a file in it, pruning what is in it -- so path may not be a
// symlink either: one staged to the codex login, or planted by a CLI,
// would otherwise take the operation outside the staging root.
func confinedDir(root, path string) (string, error) {
	path, err := confined(root, path)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return "", fmt.Errorf("stage: %s is a symlink", path)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("stage: %s is not a directory", path)
	}
	return path, nil
}

func stageTempDir(root string) (string, error) {
	dir := filepath.Join(root, "tmp")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	// MkdirAll is content with a symlink to a directory, which would
	// take a new file or directory outside the root.
	return confinedDir(root, dir)
}

// writeRegularFile writes data to a regular file at path with owner-only
// mode perm, refusing a symlink, FIFO or special file without blocking on
// or truncating it, and tightening the mode of an existing file.
func writeRegularFile(path string, data []byte, perm fs.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, perm)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return fmt.Errorf("%s is not a regular file", path)
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if err := f.Truncate(0); err != nil {
		f.Close()
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// readTail is agent.LocalHost.ReadTail's: the last limit bytes of a
// regular file, refusing anything else rather than following it.
func readTail(path string, limit int64) ([]byte, bool, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%s is not a regular file", path)
	}
	start := max(int64(0), info.Size()-limit)
	data, err := io.ReadAll(io.NewSectionReader(f, start, limit))
	if err != nil {
		return nil, false, err
	}
	return data, start > 0, nil
}

// listenCarry serves OpListen: a listener on this machine's loopback
// whose every connection is carried back to the daemon over m, as a
// stream naming id, until the daemon closes conn.
func (s *Server) listenCarry(ctx context.Context, conn net.Conn, m *mux.Mux, id string) {
	if id == "" {
		s.refuse(conn, "listen: no id")
		return
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		s.refuse(conn, fmt.Sprintf("listen: %v", err))
		return
	}
	if err := writeJSON(conn, frameListening, Listening{Addr: ln.Addr().String()}); err != nil {
		ln.Close()
		return
	}
	var mu sync.Mutex
	open := map[net.Conn]bool{}
	done := make(chan struct{})
	go func() {
		// The daemon says nothing more on this stream; it closing it, or
		// the session ending, is the listener's end.
		_, _ = io.Copy(io.Discard, conn)
		close(done)
	}()
	go func() {
		select {
		case <-done:
		case <-ctx.Done():
		case <-m.Done():
		}
		ln.Close()
		mu.Lock()
		for c := range open {
			c.Close()
		}
		mu.Unlock()
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		mu.Lock()
		open[c] = true
		mu.Unlock()
		go func() {
			defer func() {
				mu.Lock()
				delete(open, c)
				mu.Unlock()
			}()
			st, err := m.OpenStream(ctx)
			if err != nil {
				c.Close()
				return
			}
			if err := writeJSON(st, frameRequest, Request{Op: OpCarry, ID: id}); err != nil {
				c.Close()
				st.Close()
				return
			}
			pipe(c, st)
		}()
	}
}
