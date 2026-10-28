package mcp

// ConfigureGitCredentialsOverSSH is ConfigureGitCredentials for a
// GranuleSandboxes-style remote VM instead of a local directory: unlike
// NewSandboxTools' root (a plain directory HOME gets pinned to,
// runCommandTool), NewSSHSandboxTools has no filesystem of its own to
// write into -- SSH, over runner, is the only path to the remote host's
// filesystem at all, the same reason ssh_tools.go's own read/write/edit
// handlers run `cat`/`dd`/`mkdir` remotely instead of calling os.*
// directly. The two files are written with relative paths, the same way
// read_file/write_file/edit_file's own file_path already does (ssh_tools_
// test.go's own doc comment: "the directory a real SSH session starts in
// (normally the login user's $HOME)") -- so they land in whatever
// directory sshd itself chdirs a fresh session into, without this package
// needing to name or expand $HOME itself.

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/bwsalmon/grain/pkg/kubectlgate"
)

// ConfigureGitCredentialsOverSSH writes .gitconfig and .git-credentials
// into runner's remote SSH session's starting directory so that any git
// command a later run_command call executes there authenticates through
// remoteURL's host as sandbox, using token as the password half of HTTP
// Basic auth -- the same content ConfigureGitCredentials writes locally,
// just placed over runner instead of os.WriteFile.
//
// Both files go in one round trip rather than a write and a chmod for
// each: over `kubectl exec` every round trip is its own process and its
// own handshake with the API server, and four of them per sandbox is
// four chances for a busy control plane to fail the sandbox's setup.
// The two contents travel on stdin, one after the other, never in argv
// -- argv is visible to anyone who can list processes, on the
// controller as well as in the guest -- and the script splits them by
// the first file's byte length, which `dd bs=1 count=N` reads exactly
// (a bigger block size could read past the boundary on a pipe).
func ConfigureGitCredentialsOverSSH(runner remoteRunner, remoteURL, token string, identity GitIdentity) error {
	credentials, gitconfig, err := gitCredentialFiles(remoteURL, token, identity)
	if err != nil {
		return err
	}
	netrc, _, err := CurlCredentialFiles(remoteURL, token, "")
	if err != nil {
		return err
	}
	stdin := credentials + gitconfig + netrc
	// The script overwrites every file outright, so a session the
	// transport cut off part-way is run again rather than failing the
	// sandbox: a second run leaves exactly what one clean run would.
	ctx := context.Background()
	_, stderr, exitCode := kubectlgate.RetryIdempotent(ctx, func() (string, string, int) {
		return runner.Run(ctx, []string{"sh", "-c", gitCredentialsScript(len(credentials), len(gitconfig), len(netrc))}, stdin)
	})
	if exitCode != 0 {
		return fmt.Errorf("gitcredentials: writing remote .git-credentials and .gitconfig: %s", strings.TrimSpace(stderr))
	}
	return nil
}

// gitCredentialsScript is the guest-side half of ConfigureGitCredentials-
// OverSSH: the first credentialsLen bytes of stdin into .git-credentials,
// the next gitconfigLen bytes into .gitconfig, the remaining netrcLen bytes
// into .git-credentials.netrc, plus .curlrc pointing at it, each 0600.
//
// umask 077 makes a file the script creates owner-only from the moment
// it exists, so there is no window in which a token sits world-readable
// waiting for its chmod; the chmod is still there for a file left over
// from an earlier run, which keeps whatever mode it already had.
//
// `--` precedes the mode rather than following it, which looks
// backwards and is not: BSD's getopt stops parsing options at the first
// non-option argument, so in `chmod 600 -- f` the mode ends option
// parsing and the `--` that was meant to guard f becomes a filename of
// its own. GNU permutes its arguments and accepts either order, so
// `chmod -- 600 f` is the one spelling both userlands read the same way
// -- which matters for the test double (localExecRunner,
// ssh_tools_test.go) that runs this script on the host.
func gitCredentialsScript(credentialsLen, gitconfigLen, netrcLen int) string {
	return "umask 077 && " +
		"dd of=.git-credentials bs=1 count=" + strconv.Itoa(credentialsLen) + " status=none && " +
		"chmod -- 600 .git-credentials && " +
		"dd of=.gitconfig bs=1 count=" + strconv.Itoa(gitconfigLen) + " status=none && " +
		"chmod -- 600 .gitconfig && " +
		"dd of=.git-credentials.netrc bs=1 count=" + strconv.Itoa(netrcLen) + " status=none && " +
		"chmod -- 600 .git-credentials.netrc && " +
		`printf 'netrc-optional\nnetrc-file = "%s/.git-credentials.netrc"\n' "$PWD" > .curlrc && ` +
		"chmod -- 600 .curlrc"
}
