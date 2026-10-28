package mcp

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
)

// SandboxFileReader reads the file at p in a run's sandbox, whole and
// byte for byte, refusing one larger than limit bytes. A relative p is
// taken from where run_command starts, the sandbox's workspace.
//
// It exists for attachments by path (ResponseAttachments): the one way a
// run used to have of attaching a file was to put its bytes in the call
// as base64, which it could only have learned through a tool answer, and
// every tool answer is cut at maxResultTextBytes -- so any image past a
// few kilobytes could not be attached at all, against a 10 MB limit on
// the attachment itself (task 1193).
type SandboxFileReader func(ctx context.Context, p string, limit int) ([]byte, error)

// SandboxFileReaderOf is the reader behind tools' read_file, or nil when
// there is none to read through -- a roster with no sandbox, which then
// refuses attachments by path rather than reading from somewhere else.
func SandboxFileReaderOf(tools []Tool) SandboxFileReader {
	for _, t := range tools {
		if t.Name == "read_file" && t.ReadBytes != nil {
			return t.ReadBytes
		}
	}
	return nil
}

// errTooLargeToAttach is a file past the limit, in the words
// decodeResponseAttachment uses for one sent as content.
func errTooLargeToAttach(limit int) error {
	return fmt.Errorf("is over the %d byte limit", limit)
}

// hostReadBytes is read_file's raw read for a host-directory sandbox:
// confined to root exactly as read_file is (resolveForRead), since root
// is a directory on the controller and everything outside it is the
// controller's own.
func hostReadBytes(root string) SandboxFileReader {
	return func(_ context.Context, p string, limit int) ([]byte, error) {
		full, _, _, err := resolveForRead(root, p)
		if err != nil {
			return nil, err
		}
		data, err := readRegularFileUpTo(full, limit)
		if errors.Is(err, errOverLimit) {
			return nil, errTooLargeToAttach(limit)
		}
		return data, err
	}
}

// sshReadBytes is read_file's raw read for a remote sandbox. The whole
// remote host is the sandbox, so any path on it may be read, as read_file
// reads any; a relative one is taken from workspace, where run_command's
// shell starts.
func sshReadBytes(runner remoteRunner, workspace string) SandboxFileReader {
	return func(ctx context.Context, p string, limit int) ([]byte, error) {
		if !path.IsAbs(p) && workspace != "" {
			p = path.Join(workspace, p)
		}
		ctx, cancel := context.WithTimeout(ctx, sshReadTimeout)
		defer cancel()
		stdout, stderr, exitCode := runner.Run(ctx, sshReadArgvUpTo(p, limit), "")
		if exitCode != 0 {
			msg := strings.TrimSpace(stderr)
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				msg = fmt.Sprintf("the read did not finish within %s", sshReadTimeout)
			}
			return nil, errors.New(msg)
		}
		if len(stdout) > limit {
			return nil, errTooLargeToAttach(limit)
		}
		return []byte(stdout), nil
	}
}
