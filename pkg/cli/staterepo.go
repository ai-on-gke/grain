package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/bwsalmon/grain/pkg/daemon"
	"github.com/bwsalmon/grain/pkg/secretsrepo"
	"github.com/bwsalmon/grain/pkg/staterepo"
)

const stateRepoUsage = `usage: grain state repo [-branch B] [-token-file T] [URL]
       grain state repo -none

With a URL, configures this deployment to read its configuration from an
external Git repository (read-only Infrastructure-as-Code mode) and writes
` + staterepo.SettingsFileName + ` in -data-dir. In external mode, grain pulls and applies
configuration from the external repository, does not host grain/config locally,
never exports or pushes daemon-generated commits, and makes UI settings
read-only.

With no arguments, reports the config repository mode in force. With -none,
switches back to the internal grain/config repository on this host.

That repository must not be this deployment's encrypted-secrets backup
repository (` + secretsrepo.SettingsFileName + `).

Flags:
  -branch B      branch to sync on (default: ` + staterepo.DefaultBranch + `)
  -token-file T  file holding the credential to fetch with, for a
                 repository the GitHub credential ladder under
                 <data-dir>/secrets/github does not cover
  -none          use the internal grain/config repository on this host
`

func stateRepoCommand(ctx context.Context, dataDir string, out io.Writer, args []string) error {
	fs := flag.NewFlagSet("grain state repo", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, stateRepoUsage) }
	branch := fs.String("branch", "", "branch to sync the config repository on")
	tokenFile := fs.String("token-file", "", "file holding the credential to fetch with")
	none := fs.Bool("none", false, "use the internal grain/config repository on this host")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	settings, err := staterepo.LoadSettings(dataDir)
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
		was := settings.Remote
		settings.Remote = ""
		settings.TokenFile = ""
		if err := staterepo.SaveSettings(dataDir, settings); err != nil {
			return err
		}
		reconcileLocalStateRepoIfPresent(ctx, dataDir)
		if was == "" {
			fmt.Fprintln(out, "this deployment already uses the internal grain/config repository on this host")
			return nil
		}
		fmt.Fprintf(out, "switched from external config repository %s to the internal grain/config repository.\n", was)
		fmt.Fprintf(out, "working tree in %s is kept; grain will host grain/config locally and export database changes to it.\n", daemon.StateRepoDir(dataDir))
		return nil
	case len(rest) == 1:
		remote := strings.TrimSpace(rest[0])
		if remote == "" {
			fs.Usage()
			return errors.New("a URL is required")
		}
		sec, err := secretsrepo.LoadSettings(dataDir)
		if err != nil {
			return err
		}
		if err := daemon.RefuseStateRepoAsSecretsRepo(sec.Remote, remote); err != nil {
			return err
		}
		was := settings.Remote
		settings.Remote = remote
		if branch := strings.TrimSpace(*branch); branch != "" {
			if err := staterepo.ValidateBranch(branch); err != nil {
				return err
			}
			settings.Branch = branch
		}
		switch {
		case *tokenFile != "":
			settings.TokenFile = *tokenFile
		case !daemon.SameRemote(remote, was):
			settings.TokenFile = ""
		}
		if err := staterepo.SaveSettings(dataDir, settings); err != nil {
			return err
		}
		reconcileLocalStateRepoIfPresent(ctx, dataDir)
		fmt.Fprintf(out, "using external config repository %s (read-only IaC mode; grain/config is not hosted locally).\n", remote)
		reportStateRepo(dataDir, out)
		return nil
	}
	reportStateRepo(dataDir, out)
	return nil
}

func reconcileLocalStateRepoIfPresent(ctx context.Context, dataDir string) {
	if _, err := os.Stat(filepath.Join(daemon.StateRepoDir(dataDir), ".git")); err != nil {
		return
	}
	repo, err := daemon.OpenStateRepo(ctx, dataDir)
	if err != nil {
		return
	}
	settings, err := staterepo.LoadSettings(dataDir)
	if err != nil {
		return
	}
	_ = repo.ConfigureMirror(ctx, settings.Remote, settings.Branch, daemon.RepoToken(dataDir, settings.Remote, settings.TokenFile))
}

func reportStateRepo(dataDir string, out io.Writer) {
	settings, err := staterepo.LoadSettings(dataDir)
	if err != nil {
		fmt.Fprintf(out, "mode:       unknown -- %v\n", err)
		return
	}
	if settings.Remote == "" {
		fmt.Fprintf(out, "mode:       internal -- grain/config is hosted on this host.\n")
		fmt.Fprintf(out, "            `grain state repo <git-url>` switches to an external config repository\n")
		fmt.Fprintf(out, "            (read-only IaC mode).\n")
		return
	}
	branch := settings.Branch
	if branch == "" {
		branch = staterepo.DefaultBranch
	}
	fmt.Fprintf(out, "mode:       external (read-only IaC) -- %s (branch %s)\n", settings.Remote, branch)
}
