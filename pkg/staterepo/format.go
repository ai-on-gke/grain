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

package staterepo

// format.go lays out a state repository that does not have one yet, and
// writes the CI step that keeps changes to it honest.
//
// Everything else in this package answers "what does a running
// deployment do with a repository". This file answers the question that
// comes before that one: an operator has just created an empty
// repository on GitHub and has nothing in it, and the parts of a state
// repository that are not the dump -- the README that says what the
// tree is, the .gitignore that keeps a stray key out of it, and the
// workflow that runs `grain state check` against a proposed change --
// have until now existed only once a deployment had seeded it, and the
// workflow has not existed at all.
//
// Two things are deliberately not here.
//
// No dump. Formatting writes no tables/ and no schema-version stamp, so
// HasDump stays false and the repository stays the "empty repository
// grain seeds from what it has" of the bootstrap. Writing an empty dump
// would be easy -- an empty database at this build's schema exports one
// file per table -- and would quietly turn adopting into the *other*
// case, the one where the repository's contents replace the database.
// An operator who formatted a new repository and then adopted it would
// have emptied their deployment, and would have been told they were
// adopting an existing repository, which by then would have been true.
//
// No commit, and no push. Format writes files into a directory and
// stops, because it runs in a clone somebody made and has no business
// making commits there.
//
// installWorkflow, at the bottom of this file, is the other half of the
// same job and the one that runs on the deployment: Seed and every Sync
// call it, so a repository grain was pointed at ends up with the check
// whether or not anybody ran a command, and one whose workflow a merge
// dropped gets it back on the next tick. It carries the risk `format`
// was written to avoid -- a push that adds a file under
// .github/workflows is refused unless the credential making it may
// write workflows, and grain's own installation token need not be able
// to -- and it carries it in the one shape where that is survivable: the
// workflow is a commit of its own, pushed on its own, and undone in full
// whenever that push does not land -- for whatever reason, since a
// commit left behind is a commit the *export's* next push carries, which
// is where a refusal would take the export down with it. A deployment
// whose credential says no loses the CI step and nothing else, and is
// told in its journal how to install the file by hand.

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultCheckImage is the container the generated CI step runs
// `grain state check` out of when nothing names a better one.
//
// grain publishes no bare binaries -- one image per commit is the whole
// of what CI releases (../../.github/workflows/build-artifacts.yml), and
// its entrypoint is the grain binary, so `docker run <image> state check
// /state` is the command. `:latest` is main's, and public, which is what
// makes the generated workflow need no credential of any kind: a state
// repository's own GITHUB_TOKEN cannot read another repository's private
// packages, and a step that needed one would be a step an operator has
// to configure before it works.
//
// It is a default rather than a constant in the workflow because the
// check is only meaningful against a build that knows the same schema as
// the deployment (Check refuses any other), so a deployment pinned to an
// older tag wants that tag here. A deployment does not have to be told
// which one it is: cmd/grain stamps its own image reference in at link
// time and passes it as Config.CheckImage, exactly as it does for the
// sandbox (pkg/cli/grainimage.go), and this value is what an unstamped
// build -- a `make build` on a laptop, a `docker build` with no build arg
// -- falls back to. Naming the tag CI keeps pointed at main is a less
// precise answer than that stamp and a far better one than nothing.
const DefaultCheckImage = "ghcr.io/bwsalmon/grain/grain:latest"

// Formatted is what Format did, so its caller can print it: the files it
// wrote, and the files it found already there and left alone.
type Formatted struct {
	// Wrote names the paths, relative to the directory, that Format put
	// there or brought up to date.
	Wrote []string
	// Left names the paths Format found and did not touch. Only the
	// workflow can end up here: the README is grain's own file and is
	// rewritten on every sync anyway, and the .gitignore is merged into
	// rather than replaced.
	Left []string
}

// Format writes the parts of a state repository that are not the dump
// into dir, creating it if it is not there: the README, the .gitignore,
// and the workflow that validates a pull request against this
// repository.
//
// image names the container the workflow runs the check from;
// DefaultCheckImage when empty. force replaces a workflow file that is
// already there, which is otherwise left as it is unless it is still
// grain's own rendering (EnsureWorkflow) -- an operator who has edited
// the runner, the trigger or a step has said something this command does
// not know better than.
//
// Safe to run twice: nothing here is destructive, and a directory that
// is already formatted comes out byte-identical.
func Format(dir, image string, force bool) (Formatted, error) {
	var out Formatted
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return out, fmt.Errorf("staterepo: preparing %s: %w", dir, err)
	}
	// Named from the dump if there is one under dir, since there is no
	// database here to ask: formatting a clone of a deployment's
	// repository has to leave its README saying whose state it is
	// (deploymentNameFromDump), and formatting an empty directory has
	// nothing to name until the deployment that adopts it seeds.
	if err := writeReadme(dir, deploymentNameFromDump(dir)); err != nil {
		return out, err
	}
	out.Wrote = append(out.Wrote, ReadmeFile)
	if err := EnsureIgnored(dir); err != nil {
		return out, err
	}
	out.Wrote = append(out.Wrote, IgnoreFile)
	wrote, err := EnsureWorkflow(dir, image, force)
	if err != nil {
		return out, err
	}
	if wrote {
		out.Wrote = append(out.Wrote, WorkflowFile)
	} else {
		out.Left = append(out.Left, WorkflowFile)
	}
	return out, nil
}

// EnsureWorkflow writes the validation step into dir, reporting whether
// it wrote anything. A workflow that is already there is left alone
// unless force -- with the one exception below -- and reported as
// (false, nil) rather than as an error: "the CI step is already
// installed" is an answer, not a failure.
//
// The exception is a file that is byte for byte a rendering of grain's
// own, this build's template or an earlier one's: there is nothing of
// anybody else's in it to lose, so it is brought up to what this build
// would have written -- which for a current file is its image line and
// nothing else. See StaleWorkflow for why those are grain's and the rest
// of the file is not.
//
// Exported on its own because the two cases are different commands. A
// repository being formatted has no CI step yet; a repository a
// deployment has been pushing to for months has no CI step either, and
// nothing about adding one to that involves formatting anything.
func EnsureWorkflow(dir, image string, force bool) (bool, error) {
	if image == "" {
		image = DefaultCheckImage
	}
	path := filepath.Join(dir, filepath.FromSlash(WorkflowFile))
	if !force {
		switch body, err := os.ReadFile(path); {
		case err == nil:
			if !StaleWorkflow(body, image) {
				return false, nil
			}
		case !os.IsNotExist(err):
			return false, fmt.Errorf("staterepo: reading %s: %w", path, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, fmt.Errorf("staterepo: preparing %s: %w", filepath.Dir(path), err)
	}
	if err := writeFileIfChanged(path, Workflow(image)); err != nil {
		return false, err
	}
	return true, nil
}

// grainWorkflows is every template grain has ever rendered this file
// from, this build's first and the ones it has retired after it
// (workflow_history.go). RenderedImage tries them in order, so the
// answer for a file this build wrote costs one comparison and the rest
// are only reached by a file that is not it.
var grainWorkflows = append([]string{workflow}, earlierWorkflows...)

// RenderedImage reports the image a workflow file runs the check from,
// and whether that file is one grain rendered rather than one somebody
// has since made their own.
//
// The test is byte equality with one of grain's templates: everything
// before the image and everything after it has to be exactly what some
// build's Workflow would have written, and what is left in the middle is
// the image. Nothing is parsed as YAML, because the question is not
// "what does this workflow do" -- which grain has no business deciding
// for a file an operator is entitled to own -- but the far narrower "is
// this still grain's text, with only the one substitution in it".
//
// So a file with a runner, a trigger or a step of somebody's own in it
// answers false. A file an *earlier* grain rendered answers true, and
// deliberately: it is grain's own text word for word, nobody has touched
// it, and the alternative is that every repository stops being
// maintained the day this template's comment block is reworded. What
// happens to it is RenderedByThisBuild's half of the story -- it is
// rewritten to this build's template, once, rather than having its image
// line moved inside text that no longer says what grain does.
func RenderedImage(body []byte) (string, bool) {
	s := string(body)
	for _, template := range grainWorkflows {
		if image, ok := renderedFrom(s, template); ok {
			return image, true
		}
	}
	return "", false
}

// RenderedByThisBuild reports whether a workflow file is this build's own
// template rather than an earlier one's -- the difference between a file
// whose image line is all grain has to keep up to date and one whose
// whole text grain is about to replace with the current wording.
func RenderedByThisBuild(body []byte) bool {
	_, ok := renderedFrom(string(body), workflow)
	return ok
}

// renderedFrom reports the image in s if s is template with one
// substitution made in it, and nothing else.
func renderedFrom(s, template string) (string, bool) {
	prefix, suffix, ok := strings.Cut(template, workflowImagePlaceholder)
	if !ok {
		return "", false
	}
	if len(s) < len(prefix)+len(suffix) || !strings.HasPrefix(s, prefix) || !strings.HasSuffix(s, suffix) {
		return "", false
	}
	image := s[len(prefix) : len(s)-len(suffix)]
	// A rendered image is one token on one line. Anything else means the
	// file only looks like the template around an edit that happens to
	// sit where the image goes.
	if image == "" || strings.ContainsAny(image, "\n\"") {
		return "", false
	}
	return image, true
}

// StaleWorkflow reports whether a workflow file is grain's own rendering
// and is not what this build would write -- the whole of what grain is
// willing to change about a file that is already there.
//
// Which needs saying, because "grain never rewrites this file" is
// otherwise the rule, and it is the rule that keeps an operator's edit
// from fighting a timer. Two things are outside it, and both are facts
// about grain rather than about the repository.
//
// The image, which is the line that says which build's schema this dump
// was written by. It goes out of date on its own every time the
// deployment is upgraded, and a file written once with the tag of the
// day fails every pull request the moment the schemas part company, for
// a reason that has nothing to do with the change proposed in it -- so
// grain keeps that line in step with the image it is running.
//
// And the text around it, when the file is an earlier grain's rendering
// (workflow_history.go). That file is grain's own word for word, so
// there is nothing of anybody's to overwrite, and what it says about
// what grain does to it is out of date in exactly the way its image is.
// Bringing it up to this build's template is one commit, once, after
// which it is an ordinary current file.
//
// An operator who wants a different image says so where the deployment
// can read it, in state-repo.json's "checkImage": grain then writes and
// maintains *that* value, so the pin is stable rather than something a
// later sync takes back. Editing anything else in the file hands the
// whole of it back to whoever edited it, image included.
func StaleWorkflow(body []byte, want string) bool {
	if want == "" {
		want = DefaultCheckImage
	}
	if _, ok := RenderedImage(body); !ok {
		return false
	}
	return !bytes.Equal(body, Workflow(want))
}

// Workflow renders the GitHub Actions workflow that runs
// `grain state check` against a proposed change to a state repository.
//
// Rendered from a template with the image substituted in, rather than
// assembled out of a YAML library, because what this produces is a file
// a human is going to read and edit in their own repository: the
// comments, the order and the blank lines are most of what makes it
// reviewable, and a marshaller would keep none of them.
func Workflow(image string) []byte {
	if image == "" {
		image = DefaultCheckImage
	}
	return []byte(strings.ReplaceAll(workflow, workflowImagePlaceholder, image))
}

const workflowImagePlaceholder = "@IMAGE@"

const workflow = `# Written by grain. Checks that grain can still load this repository.
#
# grain writes this file whenever it is not here -- on the sync after a
# merge dropped it, or the first sync after a deployment adopted this
# repository -- and afterwards maintains it only for as long as it is
# still word for word grain's own: it keeps IMAGE below pointed at the
# build the deployment is actually running, and replaces the whole file
# when a newer grain words it differently. So an edit to it is safe:
# change the runner, add a step of your own, and grain leaves the file
# alone from then on, image included. To pin the image and keep the
# rest of this file grain's, set "checkImage" in the deployment's
# state-repo.json -- that is what IMAGE is written from. To stop grain
# offering the file at all, set "noWorkflow": true there instead.
#
# This repository is a grain deployment's database, written out as text
# (see README.md), so a pull request against it is a change to what that
# deployment runs. The one question a reviewer cannot answer by reading
# the diff is whether the result will load: a file that is not valid
# JSON, a row missing a column the schema insists on, or a link naming a
# row that is no longer in the dump all read as perfectly plausible text
# and fail at import time -- which, without this step, means after the
# merge, on the deployment, in front of whoever is on call rather than
# whoever wrote the change.
#
# ` + "`grain state check`" + ` is that import, run against a database it throws
# away afterwards: the same single transaction, the same constraints, the
# same rollback on any inconsistency. It needs no credential, no data
# directory and no daemon, only a checkout -- which is why this whole job
# is a checkout and one ` + "`docker run`" + `.
#
# On pull requests only, deliberately. grain commits to this repository
# itself, on a timer, straight to its branch, every time its database
# changes; validating those pushes would spend a CI run each time a task
# changed state, on a dump grain had just written out of the very
# database the check would import it back into.
#
# The image is grain's own published container -- its entrypoint is the
# grain binary, and the package is public, so this step needs no
# credential and no secret. It does have to be a build that knows the
# same schema as the deployment: ` + "`grain state check`" + ` refuses a dump
# stamped with any other, so a failure reading "repository is at schema
# N, this build knows M" means this tag and the deployment have drifted
# apart. The deployment writes its own image reference here to keep them
# together -- ` + "`grain image`" + ` on its host prints the tag it runs, and
# ` + "`grain state status`" + ` prints both schema numbers.
#
# The one failure this file cannot answer for is the one where nothing
# in it runs: a workflow run GitHub concludes before any job of it
# starts. It reads as every job of the run failing within seconds of the
# push, with no log under any of them -- a job whose only step is
# ` + "`echo`" + ` fails alongside the rest -- and on the run's own page on
# github.com those jobs are drawn with no steps under them at all,
# because no step of any of them was ever reached. "It has no steps"
# there describes a run that never started; it is not a fault in the
# file below, whose one job has two steps written out further down, and
# it is not GitHub failing to parse this file either -- an unparseable
# workflow is reported as unparseable, with the line it broke on. What
# that page does carry is GitHub's reason for concluding the run, which
# is the one place that reason is written down at all. No commit and no
# edit here changes it, because it is a fact about the repository rather
# than about the change: Actions disabled or restricted for this
# repository, a runner it may not use, or, for a private one, an account
# with no Actions minutes left or a spending limit it has reached. A
# check that has never once passed on any tree is that, and not a dump
# that will not load. Raise the account's limit; or point ` + "`runs-on`" + `
# below at a runner the repository may use, which costs a private
# repository no minutes if it is self-hosted; or set "noWorkflow": true
# in the deployment's state-repo.json to stop grain offering this check
# at all, which unblocks every pull request here at the cost of the
# validation.
name: grain state check

on:
  pull_request:
  # So it can be run by hand as well: after changing IMAGE below, or
  # after the deployment has been upgraded.
  workflow_dispatch:

permissions:
  contents: read

concurrency:
  group: ${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: true

jobs:
  check:
    name: grain can load this
    runs-on: ubuntu-latest

    steps:
      - uses: actions/checkout@v4

      - name: grain state check
        env:
          IMAGE: "` + workflowImagePlaceholder + `"
        run: |
          set -euo pipefail
          # A repository that has been formatted but that no deployment
          # has seeded yet holds no dump at all, and there is nothing to
          # validate until one arrives. ` + "`grain state check`" + ` calls that an
          # error, and for a CI step pointed at the wrong directory it is
          # one, so the empty case is answered here -- where a reader of
          # this file can see it -- rather than by making the check itself
          # laxer for everyone.
          if [ ! -d tables ]; then
            echo "No tables/ directory here: no grain deployment has written its database"
            echo "to this repository yet, so there is nothing to check."
            exit 0
          fi
          docker run --rm -v "$PWD:/state:ro" "$IMAGE" state check /state
`

// workflowCommitMessage is what the commit that installs the CI step
// says. Its own commit, and not folded into the export beside it, for
// the reason installWorkflow gives: a remote that refuses this file has
// to be able to refuse it without taking a database export down with it.
const workflowCommitMessage = "Add the check that runs on pull requests against this repository\n\n" +
	"Written by grain. `grain state check` imports this repository into a\n" +
	"throwaway database, so a change that would not load fails here rather\n" +
	"than on the deployment. Edit it freely -- grain writes this file only\n" +
	"when it is missing, and afterwards maintains nothing in it but the\n" +
	"image the check runs, which is the one line that is about the\n" +
	"deployment rather than about this repository."

// workflowImageCommitMessage is what the commit that repoints an
// already-installed check at this deployment's own image says.
//
// A reviewer of this repository sees a one-line diff arrive out of
// nowhere, so it has to say what moved and why a machine is allowed to
// move it: the check has to run a build that knows the same schema as
// the deployment, the deployment is the only thing that knows which
// build that is, and every other line in the file is still whoever
// edited it last's.
//
// Which is why the two images are named in the message rather than left
// to the diff. This is the commit an upgrade makes here, and "the image
// this deployment runs" is not a thing a reader of the log can look up
// afterwards -- it was true of one build on one day. from is the image
// the check was running (a file grain is willing to move the line in is
// by definition one it rendered, so there always is one, and "" is
// handled anyway rather than trusted), to is the image it runs now.
func workflowImageCommitMessage(from, to string) string {
	return "Point the check at the image this deployment runs\n\n" +
		"Written by grain, and only this one line of it:\n\n" +
		imageMoveLines(from, to) +
		"\n`grain state check` refuses a dump stamped with a schema it does not\n" +
		"know, so a check left pointing at the build of some earlier day fails\n" +
		"every pull request here for a reason that has nothing to do with the\n" +
		"change in it. Set \"checkImage\" in the deployment's state-repo.json to\n" +
		"choose the image yourself, or edit anything else in the file to have\n" +
		"grain leave the whole of it alone."
}

// imageMoveLines renders the two images a commit moved between, as an
// indented block a `git log` reads without wrapping -- an image
// reference is long enough that a sentence carrying two of them is one
// nobody finishes.
//
// An unknown or unchanged from is one line rather than an arrow from
// nowhere: what a reader needs in that case is which image the file
// points at now, and inventing a previous value it cannot check would be
// worse than saying less.
func imageMoveLines(from, to string) string {
	if from == "" || from == to {
		return "    " + to + "\n"
	}
	return "    from " + from + "\n" +
		"    to   " + to + "\n"
}

// workflowRefreshCommitMessage is what the commit that replaces an
// earlier grain's rendering of this file with the current one says.
//
// A reviewer sees the whole file rewritten by a machine, which is the
// diff this repository's rules say should never arrive, so it has to say
// why this one is allowed: the file it replaced was grain's own text
// word for word, down to the byte, and a file with anything of anybody
// else's in it would have been left exactly where it was.
// The images are named here for the same reason they are named above:
// this commit moves the image line too, inside a diff big enough to hide
// it, and which build the check runs is the fact an upgrade turns on.
func workflowRefreshCommitMessage(from, to string) string {
	return "Bring the check up to this grain's version of it\n\n" +
		"Written by grain, over a copy of this file that an earlier grain\n" +
		"wrote and that nothing had edited since -- byte for byte its own\n" +
		"text, which is the only kind of workflow grain will replace here.\n" +
		"The image the check runs comes with it:\n\n" +
		imageMoveLines(from, to) +
		"\nand that line is what this buys, because it is the one grain\n" +
		"maintains afterwards: `grain state check` refuses a dump stamped with\n" +
		"a schema it does not know, so the image this check runs has to follow\n" +
		"the deployment across an upgrade, and grain can only follow a file it\n" +
		"still recognises. Edit anything in it and grain leaves the whole of it\n" +
		"alone from then on, image included; set \"checkImage\" in the\n" +
		"deployment's state-repo.json to choose the image and keep the rest of\n" +
		"the file grain's."
}

// workflowRefusedFile records that this host's credential was refused a
// push carrying the workflow.
//
// Inside the git directory, beside grain-loaded-head and for the same
// reason: it is a fact about this
// host's credential rather than about the repository, so it must not be
// committed and must not travel to a clone on a machine whose credential
// may be a different one.
const workflowRefusedFile = "grain-workflow-refused"

// workflowRetryInterval is how long grain leaves a credential alone
// after it has refused the workflow once.
//
// Not "never again", because a permission is exactly the sort of thing
// an operator grants the day after they read the journal line below, and
// a grain that only ever tried once would need a restart to notice. Not
// every thirty seconds either: the attempt costs a commit and a rejected
// push, and repeating that on every tick would fill a deployment's
// journal with a refusal nobody has had a chance to act on yet.
const workflowRetryInterval = 24 * time.Hour

// installWorkflow makes sure the repository carries the CI step, and
// reports whether it committed one.
//
// This is what turns "grain state check exists" into "grain state check
// runs". Seed calls it once the repository has its first commit, and
// every Sync calls it after that, on the same terms the README is
// rewritten on: a repository an operator adopted, or one a merged pull
// request dropped the file out of, ends up with the workflow anyway
// rather than only if somebody remembered.
//
// It writes the file only when it is missing, and there the README and
// the workflow part company. The README is grain's own text and is
// rewritten wholesale; this file is one an operator is entitled to own
// -- running the check on their own infrastructure is the obvious case
// -- and a file grain rewrote on every tick would be a file whose editor
// is fighting a timer. So an edit made here, or merged in through a pull
// request, is final: grain notices there is a workflow that is not its
// own text and stops.
//
// A file that is still grain's rendering word for word is the exception:
// there is nothing of anybody else's in it, so grain brings it up to
// what this build would have written (StaleWorkflow). For a file this
// build's own template rendered that is the IMAGE line and nothing else
// -- a fact about the deployment rather than about the repository, and
// one that goes stale on its own, since a deployment upgraded since the
// file was written is a deployment whose check now runs an older schema
// than its dump, which fails every pull request against it. For a file
// an earlier grain rendered it is the whole text, once: its comment
// block describes a grain that is no longer running, and repointing an
// image inside it would leave the file explaining rules grain has since
// changed. Either way it costs one commit on the sync after an upgrade
// and nothing at all after that.
//
// Nothing at all happens without a remote. A workflow is GitHub's to
// run, so a local-only repository has no use for one; more to the point,
// a workflow commit sitting in a local-only history is a commit the
// *first* push after that repository is later published would carry, and
// that push either succeeds whole or is refused whole. Contained to a
// deployment that already has a remote, a refusal costs one commit that
// is undone on the spot.
func (r *Repo) installWorkflow(ctx context.Context) (bool, error) {
	if r.cfg.NoWorkflow || r.cfg.Remote == "" {
		return false, nil
	}
	// A repository with no commits is Seed's business, and the undo below
	// needs a commit to come back to. Seed calls this once it has made
	// one.
	if empty, _ := r.isEmpty(ctx); empty {
		return false, nil
	}
	image := r.cfg.CheckImage
	if image == "" {
		image = DefaultCheckImage
	}
	path := filepath.Join(r.cfg.Dir, filepath.FromSlash(WorkflowFile))
	// present says whether there is a file here already -- which decides
	// what this commit is for, and which the undo below needs too, since
	// a file that was already committed is restored rather than deleted.
	// refresh distinguishes the two commits that can be made to a file
	// that is: moving the image line of this build's own rendering, and
	// replacing an earlier grain's rendering with this build's wording.
	// was is the image that file runs today, which is what the commit
	// message names as the one being moved away from.
	var present, refresh bool
	var was string
	switch body, err := os.ReadFile(path); {
	case err == nil:
		if !StaleWorkflow(body, image) {
			// The file is here and points where it should -- grain wrote
			// it, somebody ran `grain state ci` and committed it, or a
			// merge brought it in -- so a refusal recorded earlier is a
			// refusal that has been dealt with, and the marker goes. It is
			// what WorkflowRefusedAt answers the State pane out of, and a
			// pane that went on saying the check is missing after somebody
			// installed it by hand would be telling an operator their own
			// fix did not work.
			//
			// Only on this branch, and not on the stale-image one below:
			// there the marker is still doing its other job, which is
			// holding the retry down to one attempt a day, and a sync that
			// cleared it on the way past would offer the same one-line
			// commit to the same credential on every tick.
			r.clearWorkflowRefused(ctx)
			return false, nil
		}
		present = true
		refresh = !RenderedByThisBuild(body)
		// Only a file StaleWorkflow called grain's own reaches here, so
		// this always answers; the unnamed case is left to
		// imageMoveLines rather than assumed away.
		was, _ = RenderedImage(body)
	case !os.IsNotExist(err):
		return false, fmt.Errorf("staterepo: looking for %s in %s: %w", WorkflowFile, r.cfg.Dir, err)
	}
	if !r.workflowDue(ctx, r.now()) {
		return false, nil
	}
	before, err := r.Head(ctx)
	if err != nil {
		return false, err
	}
	if _, err := EnsureWorkflow(r.cfg.Dir, image, false); err != nil {
		return false, err
	}
	message := workflowCommitMessage
	switch {
	case refresh:
		message = workflowRefreshCommitMessage(was, image)
	case present:
		message = workflowImageCommitMessage(was, image)
	}
	// Staged and committed by path, so this commit holds the workflow and
	// nothing but the workflow whatever else happens to be in the working
	// tree -- which is what makes undoing it a matter of dropping one
	// commit rather than a judgement about what else went with it.
	if _, err := r.git(ctx, "add", "--", WorkflowFile); err != nil {
		return false, err
	}
	if _, err := r.git(ctx, "commit", "--quiet", "-m", message, "--", WorkflowFile); err != nil {
		return false, err
	}
	// The loaded-head marker moves with HEAD, here as everywhere else
	// that commits. It records the commit this host's database is up to
	// date with, and this commit changes nothing the database holds -- so
	// leaving it behind would tell the next Apply that a pull request had
	// been merged, and have it import the repository's settings back over
	// every one changed since the last export.
	if err := r.recordLoadedHead(ctx); err != nil {
		return false, err
	}
	pushErr := r.Push(ctx)
	if pushErr == nil {
		return true, nil
	}
	// Undone whenever the push does not land, and not only when grain
	// recognises GitHub's refusal in it. What is at stake is the sentence
	// at the top of this file: a remote that will not take this file must
	// not be able to take a database export down with it.
	//
	// Keeping the commit is what broke that. "The next push that works
	// carries it" is true and is the problem: the next push is the
	// export's, so the commit rides along on it, and a remote that
	// refuses workflows refuses *that* push -- on a path with no undo, on
	// a tick where installWorkflow does nothing because the file is
	// already in the tree. The deployment's settings then stop reaching
	// its remote entirely, permanently, over a file worth one CI step. It
	// takes only two ordinary things in sequence: one push that failed
	// for its own reasons, and a credential that may not write workflows
	// -- which is the case this whole shape was built for.
	//
	// What the undo costs instead is one commit that never left this host
	// and is made again on a later tick. The one case it is not free is a
	// push the remote accepted and git reported as failed anyway, where
	// the remote now holds a commit this tree has reset away: the next
	// pull fast-forwards onto it, which is a merge arriving as far as
	// Apply is concerned, and the dump it imports is the one it exported.
	if err := r.undoWorkflowCommit(ctx, before, path, present); err != nil {
		return false, err
	}
	// Back where the marker was, for the reason it was moved above: HEAD
	// is the commit it names again.
	if err := r.recordLoadedHead(ctx); err != nil {
		return false, err
	}
	if !workflowRefused(pushErr) {
		// Not the permission, so not a day's wait: an unreachable remote
		// or an expired credential says nothing about whether this
		// credential may write workflows, and the next tick asks again.
		// Reported rather than returned, too. The export below is about
		// to push through the same remote and will say what it makes of
		// it in its own words; failing the cycle here would only mean
		// this tick's rows never got committed at all, over a step that
		// has already put everything back as it found it.
		log.Printf("staterepo: could not push %s to %s (%v). The commit that carried it has been "+
			"undone; grain offers it again on the next sync.", WorkflowFile, r.cfg.Remote, pushErr)
		return false, nil
	}
	// The same refusal, in the words of whichever commit it was: a
	// deployment that has no check at all, one whose check runs the wrong
	// build, and one whose check is an older grain's file read very
	// differently to whoever is holding the journal, and `grain state ci`
	// is the answer to all three -- with -force and -image for the last
	// two, since the file that is in the way there is grain's own.
	if refresh {
		log.Printf("staterepo: this deployment's credential may not push %s to %s, so the check that "+
			"runs on pull requests against its own state is still the one an earlier grain wrote, and "+
			"grain cannot bring it up to date or keep its image pointed at %s, the image this "+
			"deployment runs (%v). Until it does, that check may run an older build and fail every "+
			"pull request with a schema mismatch: run `grain state ci -force -image %s` in a clone "+
			"and commit the file with a credential that may. grain will try again in %s.",
			WorkflowFile, r.cfg.Remote, image, pushErr, image, workflowRetryInterval)
		return false, r.recordWorkflowRefused(ctx, r.now())
	}
	if present {
		log.Printf("staterepo: this deployment's credential may not push %s to %s, so grain cannot "+
			"point the check that runs on pull requests against its own state at %s, the image this "+
			"deployment runs (%v). Until it does, that check runs an older build and may fail every "+
			"pull request with a schema mismatch: run `grain state ci -force -image %s` in a clone "+
			"and commit the file with a credential that may. grain will try again in %s.",
			WorkflowFile, r.cfg.Remote, image, pushErr, image, workflowRetryInterval)
		return false, r.recordWorkflowRefused(ctx, r.now())
	}
	log.Printf("staterepo: this deployment's credential may not push %s to %s, so grain cannot install "+
		"the check that runs on pull requests against its own state (%v). Run `grain state ci` in a "+
		"clone and commit the file with a credential that may, or set \"noWorkflow\": true in %s to "+
		"stop grain offering it. grain will try again in %s.",
		WorkflowFile, r.cfg.Remote, pushErr, SettingsFileName, workflowRetryInterval)
	return false, r.recordWorkflowRefused(ctx, r.now())
}

// undoWorkflowCommit takes the working tree back to before the workflow
// was written, leaving no trace of a commit that can never be pushed.
//
// The branch moved back on its own -- what `reset --soft` does, written
// as update-ref because reset will not take --end-of-options -- and the
// path unstaged by hand afterwards, rather than a --hard reset: a --hard
// would also throw away whatever else is in the working tree, and the
// caller's own export may have written half a dump into it already. What
// is undone here is exactly the file this step wrote.
//
// present says the commit moved an image line rather than adding the
// file, and the working tree is put back accordingly: the version at
// `before` is checked back out instead of the file being deleted.
// Deleting it would turn a refused one-line change into the loss of a CI
// step the repository already had -- and, worse, into an offer to
// reinstall the whole file on the next tick, which is a second commit for
// the same credential to refuse.
func (r *Repo) undoWorkflowCommit(ctx context.Context, before, path string, present bool) error {
	if _, err := r.git(ctx, "update-ref", "--end-of-options", "HEAD", before); err != nil {
		return fmt.Errorf("staterepo: undoing the workflow commit in %s: %w", r.cfg.Dir, err)
	}
	// Resets the index entry to what `before` holds -- the old file, or
	// nothing at all when there was none.
	if _, err := r.git(ctx, "reset", "-q", "--", WorkflowFile); err != nil {
		return fmt.Errorf("staterepo: unstaging %s in %s: %w", WorkflowFile, r.cfg.Dir, err)
	}
	if present {
		if _, err := r.git(ctx, "checkout", "--", WorkflowFile); err != nil {
			return fmt.Errorf("staterepo: restoring %s in %s: %w", WorkflowFile, r.cfg.Dir, err)
		}
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("staterepo: removing %s: %w", path, err)
	}
	return nil
}

// workflowRefused reports whether a failed push was GitHub declining a
// change under .github/workflows for want of the permission to make one.
//
// Matched on the message because that is the only thing git gives us: a
// refusal is a non-zero exit and the remote's own text, and GitHub's
// text is stable and specific -- "refusing to allow a GitHub App / an
// OAuth App / a Personal Access Token to create or update workflow ...
// without `workflows` permission" (or "`workflow` scope"). Read too
// narrowly this costs a deployment one undone commit a day; read too
// widely it would undo a commit over some other push failure, which is
// why both halves have to be there.
func workflowRefused(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "refusing to allow") && strings.Contains(msg, "workflow")
}

// WorkflowRefusedAt reports whether this deployment is running without
// the CI step because its credential was refused it, and when it was
// last refused.
//
// This is the journal line above read back out, for the State pane. A
// deployment that cannot install the check looks, from that pane,
// exactly like one whose check runs on every pull request: it is syncing
// happily, its remote is up to date, nothing is in error -- because none
// of that is untrue, and the refusal is deliberately not an error, since
// a deployment must not stop syncing over a file worth one CI step. So
// the one place the fact appears is a line in the journal, seen by
// whoever was reading it that minute, and "the check is missing" is
// exactly the sort of thing nobody notices until a change that would not
// load is merged -- which is the failure the check exists to prevent.
//
// False whenever there is nothing to say, which is every ordinary
// deployment: one not offering the workflow at all (no remote, or
// noWorkflow set), one whose repository has the file -- installWorkflow
// clears the marker as soon as it sees one, however it arrived -- and
// one that has never been refused.
//
// The zero time with true is a marker grain wrote and cannot now read.
// The refusal it records still happened, so it is still reported; only
// the date is unknown, and the next sync's attempt writes a fresh one
// either way.
func (r *Repo) WorkflowRefusedAt(ctx context.Context) (time.Time, bool) {
	if r.cfg.NoWorkflow || r.cfg.Remote == "" {
		return time.Time{}, false
	}
	if _, err := os.Stat(filepath.Join(r.cfg.Dir, filepath.FromSlash(WorkflowFile))); err == nil {
		return time.Time{}, false
	}
	return r.readWorkflowRefused(ctx)
}

// readWorkflowRefused reads the marker: when this host's credential was
// last refused the workflow, and whether there is a marker at all. It
// answers about the marker alone -- WorkflowRefusedAt is the question an
// operator is actually asking, and adds the conditions under which a
// marker means nothing any more.
func (r *Repo) readWorkflowRefused(ctx context.Context) (time.Time, bool) {
	path, err := r.gitDirFile(ctx, workflowRefusedFile)
	if err != nil {
		return time.Time{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(data)))
	if err != nil {
		return time.Time{}, true
	}
	return at, true
}

// workflowDue reports whether grain should offer the workflow to this
// host's credential now. A marker that is missing, empty or unreadable
// means yes: the cost of trying is one commit that is undone again, and
// the cost of not trying is a repository with no CI step forever.
func (r *Repo) workflowDue(ctx context.Context, now time.Time) bool {
	at, refused := r.readWorkflowRefused(ctx)
	if !refused || at.IsZero() {
		return true
	}
	return !now.Before(at.Add(workflowRetryInterval))
}

func (r *Repo) recordWorkflowRefused(ctx context.Context, at time.Time) error {
	path, err := r.gitDirFile(ctx, workflowRefusedFile)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(at.UTC().Format(time.RFC3339Nano)+"\n"), 0o600); err != nil {
		return fmt.Errorf("staterepo: writing %s: %w", path, err)
	}
	return nil
}

// clearWorkflowRefused forgets a refusal the repository has outgrown.
//
// Best effort, and deliberately silent: the marker is a note to this
// host about a file that is now in the tree, and a sync that failed
// outright because it could not delete one would be a deployment that
// stopped exporting over bookkeeping. What a marker left behind costs is
// one stale sentence in the State pane until the next sync tries again.
func (r *Repo) clearWorkflowRefused(ctx context.Context) {
	path, err := r.gitDirFile(ctx, workflowRefusedFile)
	if err != nil {
		return
	}
	_ = os.Remove(path)
}
