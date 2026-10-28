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

package modelexec

import (
	"context"
	"errors"
	"fmt"
	"github.com/ai-on-gke/grain/pkg/authfix"
	"io"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/mcp"
)

// RemoteHost is one connected remote executor, seen as the host a Framework
// stages files on and starts a process on: it implements agent.Host by turning
// each of those calls into a request on that executor's connection.
type RemoteHost struct {
	conn *hostConn
}

var _ agent.Host = (*RemoteHost)(nil)
var _ agent.MCPArgsAuthorizer = (*RemoteHost)(nil)

// Unconfined implements agent.UnconfinedHost, indicating the remote VM is
// an unconfined environment where native agent tools are kept enabled.
func (h *RemoteHost) Unconfined() bool { return true }

// PlaceFile implements orchestrator.SandboxPlacer, injecting files and credentials
// directly into the remote executor VM.
func (h *RemoteHost) PlaceFile(ctx context.Context, path, content, mode string) error {
	m, err := strconv.ParseUint(mode, 8, 32)
	if err != nil {
		m = 0644
	}
	dir := filepath.Dir(path)
	if dir != "" && dir != "/" && dir != "." {
		if err := h.MkdirAll(ctx, dir, 0755); err != nil {
			return fmt.Errorf("modelexec: creating directory for %s: %w", path, err)
		}
	}
	return h.WriteFile(ctx, path, []byte(content), fs.FileMode(m))
}

// AuthorizeMCPArgs implements agent.MCPArgsAuthorizer: args is an mcpserver
// argv a Framework built for a run on this machine, and recording it is what
// lets the MCP tunnel stream that run's mcpserver opens back to grain be
// served. Nothing else is (Server.handleStream, mcpgrants.go).
func (h *RemoteHost) AuthorizeMCPArgs(args []string) {
	h.conn.mcpGrants.grant(args)
}

// Name is the remote machine's own name, as its daemon announced it.
func (h *RemoteHost) Name() string { return h.conn.name }

// ID returns the remote machine's unique instance ID.
func (h *RemoteHost) ID() string { return h.conn.id }

// BinaryPath returns where the named CLI lives on the remote host.
func (h *RemoteHost) BinaryPath(name string) (string, error) {
	path, ok := h.conn.binaries[name]
	if !ok {
		return "", fmt.Errorf("modelexec: %s offers no binary called %q (it offers %s)", h.conn.name, name, h.conn.offering())
	}
	return path, nil
}

// CodexAuthFile is the path on that machine of auth.json.
func (h *RemoteHost) CodexAuthFile() string { return h.conn.codexAuthFile }

// GitProxyURL returns the local Git proxy URL running on the remote executor.
func (h *RemoteHost) GitProxyURL() string { return h.conn.gitProxyURL }

// ConfigureGitCredentials writes .gitconfig and .git-credentials in dir on the remote host.
func (h *RemoteHost) ConfigureGitCredentials(ctx context.Context, dir, remoteURL, token string, identity mcp.GitIdentity) error {
	creds, gitconfig, err := mcp.GitCredentialFilesInDir(remoteURL, token, identity, dir)
	if err != nil {
		return err
	}
	if err := h.WriteFile(ctx, filepath.Join(dir, ".git-credentials"), []byte(creds), 0600); err != nil {
		return err
	}
	if err := h.WriteFile(ctx, filepath.Join(dir, ".gitconfig"), []byte(gitconfig), 0600); err != nil {
		return err
	}
	if identity.OrDefault().SigningKey != "" {
		netrc, curlrc, err := mcp.CurlCredentialFiles(remoteURL, token, dir)
		if err != nil {
			return err
		}
		pubKey, allowedSigners, signScript, err := mcp.GitSigningFiles(remoteURL, identity, dir)
		if err != nil {
			return err
		}
		if err := h.WriteFile(ctx, filepath.Join(dir, ".git-credentials.netrc"), []byte(netrc), 0600); err != nil {
			return err
		}
		if err := h.WriteFile(ctx, filepath.Join(dir, ".curlrc"), []byte(curlrc), 0600); err != nil {
			return err
		}
		if err := h.WriteFile(ctx, filepath.Join(dir, ".git-signing-key.pub"), []byte(pubKey), 0600); err != nil {
			return err
		}
		if err := h.WriteFile(ctx, filepath.Join(dir, ".git-allowed-signers"), []byte(allowedSigners), 0600); err != nil {
			return err
		}
		if err := h.WriteFile(ctx, filepath.Join(dir, ".git-ssh-sign"), []byte(signScript), 0700); err != nil {
			return err
		}
	}
	return nil
}

// MkdirTemp implements agent.Host.
func (h *RemoteHost) MkdirTemp(ctx context.Context, pattern string) (string, error) {
	res, err := h.conn.request(ctx, frame{Type: frameMkdirTemp, Pattern: pattern}, nil)
	if err != nil {
		return "", err
	}
	return res.Path, resultError(res)
}

// MkdirAll implements agent.Host.
func (h *RemoteHost) MkdirAll(ctx context.Context, path string, perm fs.FileMode) error {
	res, err := h.conn.request(ctx, frame{Type: frameMkdirAll, Path: path, Mode: uint32(perm)}, nil)
	if err != nil {
		return err
	}
	return resultError(res)
}

// WriteFile implements agent.Host.
func (h *RemoteHost) WriteFile(ctx context.Context, path string, data []byte, perm fs.FileMode) error {
	res, err := h.conn.request(ctx, frame{Type: frameWriteFile, Path: path, Data: data, Mode: uint32(perm)}, nil)
	if err != nil {
		return err
	}
	return resultError(res)
}

// CreateTemp implements agent.Host.
func (h *RemoteHost) CreateTemp(ctx context.Context, dir, pattern string) (string, error) {
	res, err := h.conn.request(ctx, frame{Type: frameCreate, Dir: dir, Pattern: pattern}, nil)
	if err != nil {
		return "", err
	}
	return res.Path, resultError(res)
}

// Symlink implements agent.Host.
func (h *RemoteHost) Symlink(ctx context.Context, target, link string) error {
	res, err := h.conn.request(ctx, frame{Type: frameSymlink, Target: target, Path: link}, nil)
	if err != nil {
		return err
	}
	return resultError(res)
}

// ReadTail implements agent.Host.
func (h *RemoteHost) ReadTail(ctx context.Context, path string, limit int64) ([]byte, bool, error) {
	res, err := h.conn.request(ctx, frame{Type: frameReadTail, Path: path, Limit: limit}, nil)
	if err != nil {
		return nil, false, err
	}
	return res.Data, res.Truncated, resultError(res)
}

// RemoveAll implements agent.Host.
func (h *RemoteHost) RemoveAll(ctx context.Context, path string) error {
	res, err := h.conn.request(ctx, frame{Type: frameRemoveAll, Path: path}, nil)
	if err != nil {
		return err
	}
	return resultError(res)
}

// Run implements agent.Host.
func (h *RemoteHost) Run(ctx context.Context, cmd agent.Command, tee io.Writer) (string, error) {
	out := &execOutput{tee: tee}
	h.conn.running.Add(1)
	defer func() {
		h.conn.running.Add(-1)
		h.conn.srv.notifyCapacity(h.conn)
	}()
	res, err := h.conn.request(ctx, frame{
		Type:  frameExec,
		Path:  cmd.Path,
		Args:  cmd.Args,
		Env:   cmd.Env,
		Dir:   cmd.Dir,
		Stdin: cmd.Stdin,
	}, out.write)
	if err != nil {
		return out.finish(), fmt.Errorf("modelexec: running %s on %s: %w", cmd.Path, h.conn.name, err)
	}
	if res.Error == "" {
		return out.finish(), nil
	}
	failure := errors.New(res.Error)
	if ctx.Err() != nil {
		failure = fmt.Errorf("%s: %w", res.Error, ctx.Err())
	}
	// The remote host's own exec error arrives as text, so ENOKEY is
	// recognised by its words (authfix.Expired) rather than errors.Is.
	failure = authfix.Wrap(failure, res.Stderr, cmd.Path)
	return out.finish(), fmt.Errorf("%w (stderr: %s)", failure, strings.TrimSpace(res.Stderr))
}

// Concurrency returns the maximum number of concurrent agents this remote host supports.
func (h *RemoteHost) Concurrency() int {
	return h.conn.concurrency()
}

type execOutput struct {
	tee io.Writer

	mu   sync.Mutex
	buf  strings.Builder
	done bool
}

func (o *execOutput) write(chunk []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.done {
		return
	}
	o.buf.Write(chunk)
	if o.tee != nil {
		o.tee.Write(chunk)
	}
}

func (o *execOutput) finish() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.done = true
	return o.buf.String()
}

func resultError(res frame) error {
	if res.Error == "" {
		return nil
	}
	return errors.New(res.Error)
}
