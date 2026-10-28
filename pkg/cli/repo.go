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

package cli

// `grain repo` is the CLI's per-repo surface (grain/task-36): the
// allowlist a task's repo may name, and the default capability set a repo
// adds on top of the deployment's (grain/task-24, README's "The same set,
// per repo").
//
// Why this exists at all, when schedules, templates, suites and
// qualification plans are all still UI-only: those are *authored
// content*, written once in a form built for writing them, and their
// absence here is an open gap nobody has needed closed (docs/schedules.md
// says so in as many words). This is *deployment configuration*, which is
// the category the CLI already covers end to end -- `grain settings`,
// `grain secrets`, `grain config` -- because "why did this deployment do
// that" gets asked from a shell on the host at least as often as from a
// browser. A repo's own defaults were the one member of that category
// with no spelling here at all, and the asymmetry was visible: `grain
// settings` already *prints* them ("default in: owner/name",
// capabilityStatusLine) with no way to act on what it just showed.
//
// -target-repos stays on `grain settings` rather than moving here. The
// repos *pane* dropped its own copy of that field when
// bwsalmon/agents#473 moved add/remove onto the repo rows, but what it
// dropped was a comma-separated text box, which is a bad control for a
// human and a perfectly good flag for a script. The field itself is
// still deployment-wide configuration (model.Config.TargetRepos), and it
// is the whole-list form `grain sync`'s own "settings" section already
// speaks verbatim (ui.UpdateSettingsRequest.TargetRepos) -- so removing
// the flag would leave the CLI unable to say declaratively what a config
// file next to it can, and would break existing scripts to buy nothing.
// `grain repo add`/`remove` is the incremental form, the one a human at a
// shell wants and the one the pane has; both write the same field, and
// neither is a second source of truth for it.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// Flags precede the positional argument here, as they do on every other
// verb that takes both (`grain comment [-attach path]... <id>`): Go's
// flag package stops parsing at the first non-flag argument, so `grain
// repo capabilities acme/widgets -set x` would silently read instead of
// write. Spelled out in the usage line rather than worked around, since
// working around it would make this the one verb whose arguments may
// come in any order.
const repoUsage = `usage: grain repo <subcommand> [args]

  list                                  one line per repo this deployment knows about
  ci-capabilities [-set a,b] <owner/name> show, or replace, capabilities granted to CI jobs
  capabilities [-set a,b] <owner/name>  show, or replace, a repo's own default capability set
  prompt-extension [-set text] <owner/name>
                                        show, or replace, a repo's own standing instructions for a run
  setup-command [-set command] <owner/name>
                                        show, or replace, the shell grain runs in a fresh checkout of this repo
  unfixable-checks [-set a,b] <owner/name>
                                        show, or replace, the checks here no commit can turn green
  work-ownership [-set on|off|unset] [-item id] <owner/name>
                                        show, or set, whether this deployment runs this repo's standing work
  workflows <owner/name>                the grain CI workflows this deployment declares for this repo
  add <owner/name>                      add a repo to the allowlist a task's repo may name
  remove <owner/name>                   remove a repo from that allowlist
`

func cmdRepo(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, repoUsage)
		return errors.New("a repo subcommand is required")
	}
	sub, subArgs := args[0], args[1:]
	switch sub {
	case "list":
		return cmdRepoList(ctx, c, out, subArgs)
	case "ci-capabilities":
		return cmdRepoCICapabilities(ctx, c, out, args[1:])
	case "capabilities":
		return cmdRepoCapabilities(ctx, c, out, subArgs)
	case "prompt-extension":
		return cmdRepoPromptExtension(ctx, c, out, subArgs)
	case "setup-command":
		return cmdRepoSetupCommand(ctx, c, out, subArgs)
	case "unfixable-checks":
		return cmdRepoUnfixableChecks(ctx, c, out, subArgs)
	case "work-ownership":
		return cmdRepoWorkOwnership(ctx, c, out, subArgs)
	case "workflows":
		return cmdRepoWorkflows(ctx, c, out, subArgs)
	case "add":
		return cmdRepoAdd(ctx, c, out, subArgs)
	case "remove":
		return cmdRepoRemove(ctx, c, out, subArgs)
	default:
		fmt.Fprint(os.Stderr, repoUsage)
		return fmt.Errorf("unknown repo subcommand %q", sub)
	}
}

func cmdRepoList(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain repo list", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := noArgs(fs, fs.Args()); err != nil {
		return err
	}
	repos, err := c.ListRepos(ctx)
	if err != nil {
		return err
	}
	out.repos(repos)
	return nil
}

func cmdRepoCICapabilities(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	repo, set, err := parseRepoCapabilitySet("ci-capabilities", args)
	if err != nil {
		return err
	}
	var defaults ui.RepoDefaults
	if set == nil {
		defaults, err = c.RepoDefaults(ctx, repo)
	} else {
		defaults, err = c.SetRepoCICapabilities(ctx, repo, *set)
	}
	if err != nil {
		return err
	}
	if out.json {
		out.encode(defaults)
	} else {
		fmt.Println(defaults.Repo)
		fmt.Printf("CI capabilities: %s\n", capabilityList(defaults.CICapabilities))
	}
	return nil
}

// cmdRepoCapabilities shows a repo's own default capability set with no
// -set given, and replaces it with any -- including an empty one, which
// is how a repo goes back to adding nothing. See parseRepoCapabilities
// for why "not given" and "given empty" have to stay distinguishable.
func cmdRepoCapabilities(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	repo, set, err := parseRepoCapabilities(args)
	if err != nil {
		return err
	}
	if set != nil {
		defaults, err := c.SetRepoDefaultCapabilities(ctx, repo, *set)
		if err != nil {
			return err
		}
		out.repoDefaults(defaults)
		return nil
	}
	defaults, err := c.RepoDefaults(ctx, repo)
	if err != nil {
		return err
	}
	out.repoDefaults(defaults)
	return nil
}

// parseRepoCapabilities parses `grain repo capabilities`' own arguments:
// the repo, and the new set if one was named at all.
//
// A nil set means -set was not given and this is a read; a non-nil empty
// set means it was given empty, which is the only way to clear a repo's
// own defaults and must not be confused with not having asked to change
// them -- the same fs.Visit distinction cmdUpdate and cmdSettings make
// for every field they can set to a zero value. Split out from the
// command itself so that distinction is testable without a server to
// talk to.
func parseRepoCapabilities(args []string) (repo string, set *[]string, err error) {
	return parseRepoCapabilitySet("capabilities", args)
}

func parseRepoCapabilitySet(verb string, args []string) (repo string, set *[]string, err error) {
	fs := flag.NewFlagSet("grain repo "+verb, flag.ContinueOnError)
	ids := fs.String("set", "",
		"comma-separated capability IDs -- empty clears this set")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return "", nil, err
	}
	if len(rest) == 0 {
		return "", nil, fmt.Errorf("usage: grain repo %s [-set a,b] <owner/name>", verb)
	}
	if err := atMostArgs(fs, rest, 1); err != nil {
		return "", nil, err
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "set" {
			// splitRepoList, not strings.Split: "" has to mean the empty
			// set rather than [""], exactly as it does for
			// -target-repos and -default-capabilities -- and an empty
			// slice rather than a nil one, so that clearing the set
			// survives the trip over REST as `[]` (see splitRepoList).
			v := splitRepoList(*ids)
			set = &v
		}
	})
	return rest[0], set, nil
}

// cmdRepoPromptExtension shows a repo's own standing instructions with
// no -set given, and replaces them with any -- including an empty one,
// which is how a repo goes back to adding nothing of its own
// (grain/task-114). The deployment-wide layer these are appended to is
// `grain settings -prompt-extension`; a repo can only add to it.
func cmdRepoPromptExtension(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	repo, set, err := parseRepoPromptExtension(args)
	if err != nil {
		return err
	}
	if set != nil {
		defaults, err := c.SetRepoPromptExtension(ctx, repo, *set)
		if err != nil {
			return err
		}
		out.repoPromptExtension(defaults)
		return nil
	}
	defaults, err := c.RepoDefaults(ctx, repo)
	if err != nil {
		return err
	}
	out.repoPromptExtension(defaults)
	return nil
}

// parseRepoPromptExtension is parseRepoCapabilities' counterpart for the
// text, and makes the same distinction for the same reason: a nil set
// means -set was not given and this is a read, where a non-nil empty one
// was given empty and is the only way to clear what a repo says.
func parseRepoPromptExtension(args []string) (repo string, set *string, err error) {
	fs := flag.NewFlagSet("grain repo prompt-extension", flag.ContinueOnError)
	text := fs.String("set", "",
		"standing instructions appended to the deployment's for a run against this repo -- empty clears the repo's own")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return "", nil, err
	}
	if len(rest) == 0 {
		return "", nil, errors.New("usage: grain repo prompt-extension [-set text] <owner/name>")
	}
	if err := atMostArgs(fs, rest, 1); err != nil {
		return "", nil, err
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "set" {
			v := *text
			set = &v
		}
	})
	return rest[0], set, nil
}

// cmdRepoSetupCommand shows the shell grain runs in a fresh checkout of
// this repo -- before the agent's first turn, and again whenever a run
// rebuilds its sandbox -- with no -set given, and replaces it with any,
// including an empty one, which is how a repo goes back to needing no
// setup at all (grain/task-154).
func cmdRepoSetupCommand(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	repo, set, err := parseRepoSetupCommand(args)
	if err != nil {
		return err
	}
	if set != nil {
		defaults, err := c.SetRepoSetupCommand(ctx, repo, *set)
		if err != nil {
			return err
		}
		out.repoSetupCommand(defaults)
		return nil
	}
	defaults, err := c.RepoDefaults(ctx, repo)
	if err != nil {
		return err
	}
	out.repoSetupCommand(defaults)
	return nil
}

// cmdRepoUnfixableChecks shows the checks on this repo that will never
// pass for a pull request grain's agents author -- a CLA bot and its kind
// (model.RepoConfig.UnfixableChecks, grain/task-291) -- with no -set
// given, and replaces the whole list with any, including an empty one,
// which is how a repo goes back to having every check treated as one a
// commit could turn green.
//
// Here rather than UI-only for the reason the paragraph at the top of
// this file gives, and more strongly than for its siblings: this is
// answered in a terminal, by somebody who has just been told by a queue
// comment that grain spent a repair run on `cla/google`, and the answer
// is one line rather than a browser. The refusals are the server's
// (ui.SetRepoUnfixableChecks) rather than repeated here, so the CLI
// cannot come to accept a name the pane refuses.
func cmdRepoUnfixableChecks(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	repo, set, err := parseRepoUnfixableChecks(args)
	if err != nil {
		return err
	}
	var defaults ui.RepoDefaults
	if set != nil {
		defaults, err = c.SetRepoUnfixableChecks(ctx, repo, *set)
	} else {
		defaults, err = c.RepoDefaults(ctx, repo)
	}
	if err != nil {
		return err
	}
	out.repoUnfixableChecks(defaults)
	return nil
}

// parseRepoUnfixableChecks makes the same nil-versus-empty distinction
// its siblings above make, for the same reason: -set given empty is the
// only way to withdraw a repo's last declaration, and must not read as a
// request to print the list.
func parseRepoUnfixableChecks(args []string) (repo string, set *[]string, err error) {
	fs := flag.NewFlagSet("grain repo unfixable-checks", flag.ContinueOnError)
	checks := fs.String("set", "",
		"comma-separated check names nothing pushed to a branch here clears -- empty withdraws them all")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return "", nil, err
	}
	if len(rest) == 0 {
		return "", nil, errors.New("usage: grain repo unfixable-checks [-set a,b] <owner/name>")
	}
	if err := atMostArgs(fs, rest, 1); err != nil {
		return "", nil, err
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "set" {
			v := splitRepoList(*checks)
			set = &v
		}
	})
	return rest[0], set, nil
}

// cmdRepoWorkOwnership shows what this deployment says about a repo's
// standing work -- the repository default and every item's exception to
// it (model.WorkOwnership, grain/task-486) -- and with -set writes one
// of the two.
//
// Both levels through one verb rather than two, because they are one
// question asked at two scopes: -set alone is the repository's default,
// and -set with -item is that item's exception to it. The third value is
// spelled `unset` rather than left to an empty -set, which a shell makes
// awkward to type and impossible to read back in a script: "nobody has
// said" is the state this whole record exists to keep distinct from
// "somebody said no", so the word for it is worth having.
//
// Reading is the whole list, item by item, and it is the reason this is
// not UI-only: "is this deployment the one firing that nightly" is asked
// while something has not run, from a shell, and the answer used to live
// in two columns of an exported settings table.
func cmdRepoWorkOwnership(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	repo, item, set, err := parseRepoWorkOwnership(args)
	if err != nil {
		return err
	}
	var work ui.RepoWorkOwnership
	switch {
	case set != nil && item != "":
		work, err = c.SetRepoWorkItemOwnership(ctx, repo, item, *set)
	case set != nil:
		work, err = c.SetRepoWorkDefault(ctx, repo, *set)
	default:
		work, err = c.RepoWorkOwnership(ctx, repo)
	}
	if err != nil {
		return err
	}
	out.repoWorkOwnership(work)
	return nil
}

// parseRepoWorkOwnership parses `grain repo work-ownership`'s own
// arguments: the repo, the item if one was named, and the state if one
// was given at all.
//
// A nil state means -set was not given and this is a read, the same
// distinction its siblings above make -- but here the empty string is a
// *value* rather than the absence of one, so it is reached by the word
// `unset` instead. -item with no -set is refused rather than quietly
// ignored: it reads as a request to act on one item, and printing the
// whole repository's list in answer to it would look like the item was
// the one thing that had been read.
func parseRepoWorkOwnership(args []string) (repo, item string, set *string, err error) {
	fs := flag.NewFlagSet("grain repo work-ownership", flag.ContinueOnError)
	state := fs.String("set", "",
		`"on", "off", or "unset" to go back to having said nothing -- which is not the same as "off"`)
	named := fs.String("item", "",
		"one work item to set instead of the repository default: a schedule's id, or \"release\"")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return "", "", nil, err
	}
	if len(rest) == 0 {
		return "", "", nil, errors.New("usage: grain repo work-ownership [-set on|off|unset] [-item id] <owner/name>")
	}
	if err := atMostArgs(fs, rest, 1); err != nil {
		return "", "", nil, err
	}
	var given bool
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "set" {
			given = true
		}
	})
	if given {
		// Parsed here rather than left to the server, because `-set 0`
		// is the spelling somebody reaches for and the answer to it is
		// the three words rather than a rejected HTTP request.
		v, err := parseOwnershipWord(*state)
		if err != nil {
			return "", "", nil, err
		}
		set = &v
	}
	if strings.TrimSpace(*named) != "" && set == nil {
		return "", "", nil, errors.New("-item names an item to set and needs -set; without it, " +
			"`grain repo work-ownership <owner/name>` prints every item")
	}
	return rest[0], strings.TrimSpace(*named), set, nil
}

// parseOwnershipWord turns what somebody types into what the API takes:
// the three words, and the empty string for the third of them. It is the
// CLI's own spelling rather than the model's parse
// (model.ParseOwnership) because `unset` is a word this end invents --
// over the wire the state is simply empty.
func parseOwnershipWord(s string) (string, error) {
	switch v := strings.ToLower(strings.TrimSpace(s)); v {
	case "on", "off":
		return v, nil
	case "unset", "none", "":
		return "", nil
	default:
		return "", fmt.Errorf("-set %q: the answers are \"on\", \"off\" and \"unset\"", s)
	}
}

// cmdRepoWorkflows prints the grain CI workflows this deployment
// declares for a repo (model.RepoWorkflow, grain/task-245) -- the ones
// that run for a repository grain does not control, where a
// `.grain/workflows` directory of its own is not on offer.
//
// Read-only, unlike its three siblings above, and deliberately: a
// workflow is a file, not a flag's worth of text, and the two places to
// write one are the repo's own page and a pull request against this
// deployment's state repository (repo_workflow is a settings table).
// Printing them is what this end is for -- seeing what a run against
// this repo would actually do, from a shell, without a browser.
func cmdRepoWorkflows(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain repo workflows", flag.ContinueOnError)
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return errors.New("usage: grain repo workflows <owner/name>")
	}
	if err := atMostArgs(fs, rest, 1); err != nil {
		return err
	}
	workflows, err := c.RepoWorkflows(ctx, rest[0])
	if err != nil {
		return err
	}
	out.repoWorkflows(workflows)
	return nil
}

// parseRepoSetupCommand makes the same nil-versus-empty distinction its
// two siblings above make, for the same reason: -set given empty is the
// only way to clear a repo's setup command, and must not read as a
// request to print it.
func parseRepoSetupCommand(args []string) (repo string, set *string, err error) {
	fs := flag.NewFlagSet("grain repo setup-command", flag.ContinueOnError)
	command := fs.String("set", "",
		"shell run in a fresh checkout of this repo before an agent's first turn -- empty clears it")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return "", nil, err
	}
	if len(rest) == 0 {
		return "", nil, errors.New("usage: grain repo setup-command [-set command] <owner/name>")
	}
	if err := atMostArgs(fs, rest, 1); err != nil {
		return "", nil, err
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "set" {
			v := *command
			set = &v
		}
	})
	return rest[0], set, nil
}

func cmdRepoAdd(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain repo add", flag.ContinueOnError)
	cloneURL := fs.String("clone-url", "", "non-GitHub clone URL; uses ambient sandbox credentials")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("usage: grain repo add [-clone-url URL] <owner/name>")
	}
	if err := atMostArgs(fs, fs.Args(), 1); err != nil {
		return err
	}
	settings, err := c.AddTargetRepoWithCloneURL(ctx, fs.Arg(0), *cloneURL)
	if err != nil {
		return err
	}
	out.targetRepos(settings)
	return nil
}

func cmdRepoRemove(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain repo remove", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("usage: grain repo remove <owner/name>")
	}
	if err := atMostArgs(fs, fs.Args(), 1); err != nil {
		return err
	}
	settings, err := c.RemoveTargetRepo(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	out.targetRepos(settings)
	return nil
}
