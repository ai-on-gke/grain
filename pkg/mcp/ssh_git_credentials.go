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
	"path"
	"strconv"
	"strings"

	"github.com/ai-on-gke/grain/pkg/kubectlgate"
	"github.com/bwsalmon/granule/pkg/sandbox"
)

type remoteBatchFilePlacer interface {
	PutFiles(ctx context.Context, files []sandbox.FileUpload) error
	EffectiveHomeDir() string
}

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
	baseIdentity := identity
	baseIdentity.SigningKey = ""
	credentials, gitconfig, err := gitCredentialFiles(remoteURL, token, baseIdentity)
	if err != nil {
		return err
	}
	netrc, _, err := CurlCredentialFiles(remoteURL, token, "")
	if err != nil {
		return err
	}
	if batch, ok := runner.(remoteBatchFilePlacer); ok {
		return configureGitCredentialsViaPutFiles(context.Background(), batch, remoteURL, credentials, gitconfig, netrc, identity)
	}
	stdin := credentials + gitconfig + netrc
	script := gitCredentialsScript(len(credentials), len(gitconfig), len(netrc))
	if identity.OrDefault().SigningKey != "" {
		pubKey, allowedSigners, signScript, err := GitSigningFiles(remoteURL, identity, "")
		if err != nil {
			return err
		}
		stdin += pubKey + allowedSigners + signScript
		script = gitCredentialsWithSigningScript(len(credentials), len(gitconfig), len(netrc), len(pubKey), len(allowedSigners), len(signScript))
	}
	// The script overwrites every file outright, so a session the
	// transport cut off part-way is run again rather than failing the
	// sandbox: a second run leaves exactly what one clean run would.
	ctx := context.Background()
	_, stderr, exitCode := kubectlgate.RetryIdempotent(ctx, func() (string, string, int) {
		return runner.Run(ctx, []string{"sh", "-c", script}, stdin)
	})
	if exitCode != 0 {
		return fmt.Errorf("gitcredentials: writing remote .git-credentials and .gitconfig: %s", strings.TrimSpace(stderr))
	}
	return nil
}

func configureGitCredentialsViaPutFiles(ctx context.Context, batch remoteBatchFilePlacer, remoteURL, credentials, gitconfig, netrc string, identity GitIdentity) error {
	home := batch.EffectiveHomeDir()
	netrcPath := path.Join(home, ".git-credentials.netrc")
	curlrc := fmt.Sprintf("netrc-optional\nnetrc-file = %q\n", netrcPath)

	var signingUploads []sandbox.FileUpload
	if identity.OrDefault().SigningKey != "" {
		pubKey, allowedSigners, signScript, err := GitSigningFiles(remoteURL, identity, "")
		if err != nil {
			return err
		}
		signProg := path.Join(home, ".git-ssh-sign")
		allowedPath := path.Join(home, ".git-allowed-signers")
		pubKeyPath := path.Join(home, ".git-signing-key.pub")
		gitconfig += fmt.Sprintf("[gpg]\n\tformat = ssh\n[gpg \"ssh\"]\n\tprogram = %s\n\tallowedSignersFile = %s\n[user]\n\tsigningkey = %s\n[commit]\n\tgpgsign = true\n[tag]\n\tgpgSign = true\n",
			signProg, allowedPath, pubKeyPath)
		signingUploads = []sandbox.FileUpload{
			{Path: pubKeyPath, Content: []byte(pubKey), Mode: 0o600},
			{Path: allowedPath, Content: []byte(allowedSigners), Mode: 0o600},
			{Path: signProg, Content: []byte(signScript), Mode: 0o700},
		}
	}

	uploads := []sandbox.FileUpload{
		{Path: path.Join(home, ".git-credentials"), Content: []byte(credentials), Mode: 0o600},
		{Path: path.Join(home, ".gitconfig"), Content: []byte(gitconfig), Mode: 0o600},
		{Path: netrcPath, Content: []byte(netrc), Mode: 0o600},
		{Path: path.Join(home, ".curlrc"), Content: []byte(curlrc), Mode: 0o600},
	}
	uploads = append(uploads, signingUploads...)
	if err := batch.PutFiles(ctx, uploads); err != nil {
		return fmt.Errorf("gitcredentials: writing remote .git-credentials and .gitconfig: %w", err)
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

func gitCredentialsWithSigningScript(credentialsLen, gitconfigLen, netrcLen, pubKeyLen, allowedSignersLen, signScriptLen int) string {
	return gitCredentialsScript(credentialsLen, gitconfigLen, netrcLen) + " && " +
		"dd of=.git-signing-key.pub bs=1 count=" + strconv.Itoa(pubKeyLen) + " status=none && " +
		"chmod -- 600 .git-signing-key.pub && " +
		"dd of=.git-allowed-signers bs=1 count=" + strconv.Itoa(allowedSignersLen) + " status=none && " +
		"chmod -- 600 .git-allowed-signers && " +
		"dd of=.git-ssh-sign bs=1 count=" + strconv.Itoa(signScriptLen) + " status=none && " +
		"chmod -- 700 .git-ssh-sign && " +
		`printf '[gpg]\n\tformat = ssh\n[gpg "ssh"]\n\tprogram = %s/.git-ssh-sign\n\tallowedSignersFile = %s/.git-allowed-signers\n[user]\n\tsigningkey = %s/.git-signing-key.pub\n[commit]\n\tgpgsign = true\n[tag]\n\tgpgSign = true\n' "$PWD" "$PWD" "$PWD" >> .gitconfig`
}
