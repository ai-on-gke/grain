package mcp

// PlaceFileOverSSH is the placement half of what ConfigureGitCredentials-
// OverSSH does for git: a controller-side write into a sandbox it has no
// filesystem route to. A capability's model.Placement (gcpkey's minted
// service-account key, geminikey's API key, githubsandbox's token) names
// an absolute path inside the sandbox, and for a local directory the
// executor just calls os.MkdirAll/os.WriteFile under that directory's
// root. A kontur VM has no such root -- SSH, over runner, is the only
// path to its filesystem at all -- so the same three steps run remotely
// instead, the same way ssh_tools.go's own handlers run `cat`/`dd`/
// `mkdir` rather than calling os.* directly.

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/ai-on-gke/grain/pkg/kubectlgate"
)

// PlaceFileOverSSH writes content to filePath on runner's remote host
// with mode (an octal string, model.Placement.EffectiveMode's own shape),
// creating filePath's parent directory first. filePath is used as given
// rather than resolved against any workspace, because a placement's path
// is already the absolute path it means inside the sandbox -- unlike
// read_file/write_file's own file_path, which is an agent's argument and
// is deliberately left relative to wherever a session starts.
//
// The mode is applied to an empty file BEFORE the content goes in, not
// chmod'd afterwards as ConfigureGitCredentialsOverSSH does: everything
// placed this way is credential material, and a `dd` that creates the
// file under the login user's umask leaves it world-readable for as long
// as it takes the next command to run. `install -m` collapses "create,
// empty, with the final mode" into the one step that has no such window,
// and truncates an existing file to the same end state -- so a re-place
// over a leftover file cannot inherit a wider mode either.
func PlaceFileOverSSH(ctx context.Context, runner remoteRunner, filePath, content, mode string) error {
	// Each of the three steps is idempotent -- mkdir -p, an install over
	// whatever is there, a dd that truncates -- so one the transport cut
	// off is run again rather than failing the sandbox's setup.
	run := func(argv []string, stdin string) (string, string, int) {
		return kubectlgate.RetryIdempotent(ctx, func() (string, string, int) { return runner.Run(ctx, argv, stdin) })
	}
	parent := path.Dir(filePath)
	if _, stderr, exitCode := run([]string{"mkdir", "-p", "--", parent}, ""); exitCode != 0 {
		return fmt.Errorf("creating remote %s: %s", parent, strings.TrimSpace(stderr))
	}
	if _, stderr, exitCode := run([]string{"install", "-m", mode, "--", "/dev/null", filePath}, ""); exitCode != 0 {
		return fmt.Errorf("creating remote %s with mode %s: %s", filePath, mode, strings.TrimSpace(stderr))
	}
	if _, stderr, exitCode := run([]string{"dd", "of=" + filePath, "status=none"}, content); exitCode != 0 {
		return fmt.Errorf("writing remote %s: Error writing %s: %s", filePath, filePath, strings.TrimSpace(stderr))
	}
	return nil
}
