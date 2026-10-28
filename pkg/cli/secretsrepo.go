// secretsrepo.go is the daemon's side of pkg/secretsrepo: where the
// secrets repository lives under -data-dir, what authenticates its
// pushes, when it is synced, and how the git proxy keeps it inaccessible
// to every sandbox. This repository holds nothing but grain's ciphertext,
// so there is no version of "a task working in the secrets repository"
// that this deployment wants.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/secretsrepo"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

const secretsRepoUsage = `usage: grain secrets repo [-branch B] [-file F] [-token-file T] [URL]
       grain secrets repo -none

With a URL, names the repository this deployment's encrypted secrets file is
backed up to and writes ` + secretsrepo.SettingsFileName + ` in -data-dir. With no
arguments, reports the one in force. With -none, stops backing them up at all,
which leaves every secret on this host's disk and nowhere else.

That repository must not be this deployment's state repository: tasks are
dispatched at that one, and a sandbox that may clone a repository reads every
object in it. grain refuses the two being the same, and the git proxy refuses
the secrets repository to every sandbox whatever a task's scope says.

The private key is not pushed and never has been (` + "`grain state key path`" + ` names
it): a clone of the secrets repository is every secret grain has in a form
only that key can open.

Flags:
  -branch B      branch to keep it on (default: ` + secretsrepo.DefaultBranch + `)
  -file F        what to call the file in that repository; the default is
                 derived from this deployment's state repository, which is what
                 lets several grains share one secrets repository
  -token-file T  file holding the credential to push with, for a repository the
                 GitHub credential ladder under <data-dir>/secrets/github does
                 not cover
  -none          keep this deployment's secrets on this host alone
`

// secretsRepoCommand is `grain secrets repo`: the operator's handle on
// where the encrypted file is backed up.
//
// It writes the settings file and stops there. The clone, the first push
// and any restore are the daemon's, on the tick after this (stateManager's
// currentSecretsRepo), so this needs neither the network nor a daemon to
// be stopped.
func secretsRepoCommand(ctx context.Context, dataDir string, out io.Writer, args []string) error {
	fs := flag.NewFlagSet("grain secrets repo", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, secretsRepoUsage) }
	branch := fs.String("branch", "", "branch to keep the encrypted secrets file on")
	file := fs.String("file", "", "what to call that file in the repository")
	tokenFile := fs.String("token-file", "", "file holding the credential to push with")
	none := fs.Bool("none", false, "keep this deployment's secrets on this host alone")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	settings, err := secretsrepo.LoadSettings(dataDir)
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		fs.Usage()
		return errors.New("one URL at a time")
	}
	switch {
	case *none:
		if len(rest) > 0 {
			fs.Usage()
			return errors.New("-none takes no URL")
		}
		if settings.Remote == "" {
			fmt.Fprintln(out, "this deployment already keeps its encrypted secrets on this host alone")
			return nil
		}
		was := settings.Remote
		settings.Remote = ""
		if err := secretsrepo.SaveSettings(dataDir, settings); err != nil {
			return err
		}
		fmt.Fprintf(out, "no longer backing this deployment's encrypted secrets up to %s.\n", was)
		fmt.Fprintf(out, "what was already pushed is still there: this only stops grain adding to it.\n")
		return nil
	case len(rest) == 1:
		remote := strings.TrimSpace(rest[0])
		if remote == "" {
			fs.Usage()
			return errors.New("a URL is required")
		}
		state, err := staterepo.LoadSettings(dataDir)
		if err != nil {
			return err
		}
		if err := daemon.RefuseStateRepoAsSecretsRepo(remote, state.Remote); err != nil {
			return err
		}
		settings.Remote = remote
		if branch := strings.TrimSpace(*branch); branch != "" {
			if err := secretsrepo.ValidateBranch(branch); err != nil {
				return err
			}
			settings.Branch = branch
		}
		if *file != "" {
			settings.File = *file
		}
		if *tokenFile != "" {
			settings.TokenFile = *tokenFile
		}
		if err := secretsrepo.SaveSettings(dataDir, settings); err != nil {
			return err
		}
		fmt.Fprintf(out, "this deployment's encrypted secrets are backed up to %s.\n", remote)
		reportSecretsRepo(dataDir, out)
		fmt.Fprintf(out, "the daemon clones it and pushes on its next sync; the private key is not\n")
		fmt.Fprintf(out, "pushed and never has been, so a clone of that repository opens nothing.\n")
		return nil
	}
	reportSecretsRepo(dataDir, out)
	return nil
}

// reportSecretsRepo says where this deployment's encrypted secrets are
// backed up, in the two lines it takes, and says the other thing out
// loud when they are not backed up anywhere: a deployment whose secrets
// exist on one disk is one key failure away from losing every credential
// it has, and that is worth naming where an operator is already looking
// rather than leaving them to notice the absence of a line.
func reportSecretsRepo(dataDir string, out io.Writer) {
	settings, err := secretsrepo.LoadSettings(dataDir)
	if err != nil {
		fmt.Fprintf(out, "secrets backup: unknown -- %v\n", err)
		return
	}
	if settings.Remote == "" {
		fmt.Fprintf(out, "secrets backup: none -- the encrypted file above is on this host alone.\n")
		fmt.Fprintf(out, "            `grain secrets repo <git-url>` names a repository to keep a\n")
		fmt.Fprintf(out, "            copy of it in. The key that opens the file is never pushed.\n")
		return
	}
	state, serr := staterepo.LoadSettings(dataDir)
	if serr != nil {
		state = staterepo.Settings{}
	}
	branch := settings.Branch
	if branch == "" {
		branch = secretsrepo.DefaultBranch
	}
	fmt.Fprintf(out, "secrets backup: %s\n", settings.Remote)
	fmt.Fprintf(out, "            %s on %s, working tree %s\n",
		settings.FileNameFor(state.Remote), branch, daemon.SecretsRepoDir(dataDir))
}
