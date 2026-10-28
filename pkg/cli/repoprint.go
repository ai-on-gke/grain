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

// repoprint.go is how `grain repo` (repo.go) prints a deployment's
// repos and one repo's defaults, work ownership and workflows.
package cli

import (
	"fmt"
	"strings"

	"github.com/ai-on-gke/grain/pkg/ui"
)

func (p *printer) repos(repos []ui.RepoSummary) {
	if p.json {
		p.encode(repos)
		return
	}
	if len(repos) == 0 {
		fmt.Println("no repos yet -- add one with \"grain repo add owner/name\", or file a task against one")
		return
	}
	for _, r := range repos {
		fmt.Println(repoLine(r))
	}
}

// repoLine renders one repo as a line of `grain repo list`. "allowlisted"
// appears only on a repo Config.TargetRepos actually names, and nothing
// at all appears in its place otherwise: on an unrestricted deployment
// every row is unlisted by definition, and a column reading "not
// allowlisted" against all of them would look like a finding.
func repoLine(r ui.RepoSummary) string {
	allowlisted := ""
	if r.Configured {
		allowlisted = "allowlisted"
	}
	var notes []string
	if r.Tasks == 0 {
		notes = append(notes, "no tasks")
	} else {
		notes = append(notes, fmt.Sprintf("%d tasks (%s)", r.Tasks, repoStateCounts(r)))
	}
	if r.Blocked > 0 {
		notes = append(notes, fmt.Sprintf("%d blocked", r.Blocked))
	}
	if len(r.DefaultCapabilities) > 0 {
		notes = append(notes, "defaults: "+strings.Join(r.DefaultCapabilities, ", "))
	}
	// Named rather than quoted: a repo's standing instructions can be a
	// paragraph, and a line-per-repo listing is the wrong place for one
	// (ui.RepoSummary.PromptExtension). This is the pointer at "grain
	// repo prompt-extension owner/name", which prints the text.
	if r.PromptExtension {
		notes = append(notes, "prompt extension")
	}
	// Named, not printed, for the same reason: a setup command can be
	// several lines of shell, and "grain repo setup-command owner/name"
	// is what prints it.
	if r.SetupCommand {
		notes = append(notes, "setup command")
	}
	// Named for the same reason again, and here the reason is strongest:
	// a workflow is a whole YAML file and a repo may declare several.
	// "grain repo workflows owner/name" prints them.
	if r.CICapabilities {
		notes = append(notes, "CI capabilities")
	}
	if r.Workflows {
		notes = append(notes, "CI workflows")
	}
	return strings.TrimRight(fmt.Sprintf("%-30s %-12s %s", r.Repo, allowlisted, strings.Join(notes, "; ")), " ")
}

// repoStateCounts breaks a repo's task count down by state, in
// ui.RepoStateOrder -- the repos page's own order, so the two read the
// same way.
//
// Anything the count includes that that order does not name is reported
// as "other" rather than dropped: a breakdown printed beside a total it
// does not add up to is worse than one that admits it has a leftover,
// and a state added to model without being added to RepoStateOrder is
// exactly how that would otherwise happen.
func repoStateCounts(r ui.RepoSummary) string {
	var parts []string
	named := 0
	for _, state := range ui.RepoStateOrder {
		n := r.States[state]
		if n == 0 {
			continue
		}
		named += n
		parts = append(parts, fmt.Sprintf("%d %s", n, state))
	}
	if other := r.Tasks - named; other > 0 {
		parts = append(parts, fmt.Sprintf("%d other", other))
	}
	return strings.Join(parts, ", ")
}

func (p *printer) repoDefaults(d ui.RepoDefaults) {
	if p.json {
		p.encode(d)
		return
	}
	fmt.Println(d.Repo)
	// All three sets, because the editable one says nothing useful alone
	// (ui.RepoDefaults' own doc comment): a repo listing gcp-key looks
	// identical whether that is the only thing putting gcp-key on tasks
	// here or a restatement of what every task in the deployment already
	// gets.
	fmt.Printf("own defaults:        %s\n", capabilityList(d.DefaultCapabilities))
	fmt.Printf("deployment defaults: %s\n", capabilityList(d.DeploymentDefaultCapabilities))
	fmt.Printf("what a task gets:    %s\n", capabilityList(d.EffectiveDefaultCapabilities))
	fmt.Println("\n-set replaces own defaults only; the deployment-wide set is \"grain settings -default-capabilities\", and a repo can only add to it")
}

// repoPromptExtension prints the prompt-extension half of the same
// RepoDefaults document repoDefaults above prints the capability half
// of -- all three layers, for the same reason it prints all three sets:
// a repo's own text says nothing useful without what it is appended to,
// and what a run here is actually told is the composition rather than
// either one.
func (p *printer) repoPromptExtension(d ui.RepoDefaults) {
	if p.json {
		p.encode(d)
		return
	}
	fmt.Println(d.Repo)
	fmt.Print(promptExtensionBlock("own", d.PromptExtension))
	fmt.Print(promptExtensionBlock("deployment", d.DeploymentPromptExtension))
	fmt.Print(promptExtensionBlock("what a run here is told", d.EffectivePromptExtension))
	fmt.Println("\n-set replaces this repo's own text only; the deployment-wide one is \"grain settings -prompt-extension\", and a repo can only add to it. A task can override both (New task -> Advanced options)")
}

// repoSetupCommand prints the setup-command half of that same
// RepoDefaults document. One layer, not three: a setup command has no
// deployment-wide layer to compose with (ui.RepoDefaults.SetupCommand
// has why), so what this repo says is what a checkout of it gets.
func (p *printer) repoSetupCommand(d ui.RepoDefaults) {
	if p.json {
		p.encode(d)
		return
	}
	fmt.Println(d.Repo)
	fmt.Print(promptExtensionBlock("setup command", d.SetupCommand))
	fmt.Println("\nRun in the checkout after the clone and before the agent's first turn, and again whenever a run rebuilds its sandbox. Its exit status and the tail of its output are put in the run's prompt; a run is not failed for a setup that failed, but is told")
}

// repoUnfixableChecks prints the unfixable-checks half of that same
// RepoDefaults document. One layer again, and for a sharper reason than
// the setup command's: which check never passes is a fact about one
// repository's bots, so there is no deployment-wide list to compose with
// (ui.RepoDefaults.UnfixableChecks).
//
// The sentence under the list is the one thing a list of check names most
// invites being misread as, and it is printed whether or not the list has
// anything in it, because the reader of an empty list is usually somebody
// about to add to it: naming a check here does not exempt it. A pull
// request with one of these red still does not merge. What is saved is
// the base merge and the repair run grain would otherwise have spent
// reaching a green that is not reachable.
func (p *printer) repoUnfixableChecks(d ui.RepoDefaults) {
	if p.json {
		p.encode(d)
		return
	}
	fmt.Println(d.Repo)
	if len(d.UnfixableChecks) == 0 {
		fmt.Println("checks no commit here can clear: none -- every check this repo reports is treated as one a commit could turn green")
	} else {
		fmt.Printf("checks no commit here can clear: %s\n", strings.Join(d.UnfixableChecks, ", "))
	}
	fmt.Println("\nThis does not exempt them: a pull request with one of these red is still not merged, and a person clears it or nothing merges. What grain stops spending is the base merge and the repair run it would have made trying to reach a green that is not reachable -- it says so on the task's row and in one comment instead. Not for a job that is merely flaky")
	// Named rather than described, and read out of the response rather
	// than written here: the refusal is the server's
	// (ui.SetRepoUnfixableChecks), and a string this end hard-coded
	// would be the one that went stale if grain's own check were
	// renamed.
	fmt.Printf("Names are matched exactly and without case, never by part of a name; %s cannot be named, since it judges this exact commit and so is always the branch's own answer\n",
		unfixableGrainCIName(d))
}

// unfixableGrainCIName is what this build calls grain's own CI, as the
// response reported it. A build too old to report it -- or a response
// that arrived from somewhere else -- falls back to the phrase rather
// than to a name that might be wrong, because the sentence it appears in
// is about what cannot be named and is worth printing either way.
func unfixableGrainCIName(d ui.RepoDefaults) string {
	if d.GrainCICheckName == "" {
		return "grain's own CI"
	}
	return d.GrainCICheckName
}

// repoWorkOwnership prints both levels of what this deployment says
// about a repo's standing work, and prints each of them twice over: what
// was said, and what it resolves to.
//
// Both, on every line, because neither can be read off the other. "off
// here" and "follows a default that is off" are the same answer today
// and different decisions tomorrow -- the first survives the default
// being turned back on and the second does not -- and a listing that
// showed only the resolution would be a listing an operator could not
// act on. This is the whole of what the three-valued setting buys, and
// the reason a column of ticks was not what this got.
func (p *printer) repoWorkOwnership(w ui.RepoWorkOwnership) {
	if p.json {
		p.encode(w)
		return
	}
	fmt.Println(w.Repo)
	fmt.Printf("repository default: %s\n", repoWorkDefaultLine(w))
	if len(w.Items) == 0 {
		fmt.Println("\nno work items: this repository declares no schedules here and nothing has been said about one")
		return
	}
	fmt.Println()
	// Laid out by the widest id rather than at a fixed column: a
	// declared schedule's id carries the repository's own name, so a
	// guessed width would wrap every row on some deployments and waste
	// half the line on others.
	width := 0
	for _, item := range w.Items {
		if len(item.Item) > width {
			width = len(item.Item)
		}
	}
	for _, item := range w.Items {
		runs := "does not run"
		if item.Runs {
			runs = "runs"
		}
		fmt.Printf("  %-12s  %-*s  %s%s\n", runs, width, item.Item,
			repoWorkSaid(item.Override), repoWorkItemNote(item))
	}
	fmt.Println("\n-set writes the repository default, and -set with -item writes one item's exception to it. \"unset\" is not \"off\": an item that says nothing follows the default and moves with it, and one that says \"off\" stays off when the default is turned back on")
}

// repoWorkDefaultLine is the repository default in the same two halves
// every item line has -- what was said, and what it comes to -- with the
// empty case spelled out, since "nobody has said, so this deployment
// runs it" is the state very nearly every repository is in and the one
// most easily misread as "off".
func repoWorkDefaultLine(w ui.RepoWorkOwnership) string {
	runs := "does not run this repository's standing work"
	if w.DefaultRuns {
		runs = "runs this repository's standing work"
	}
	switch w.Default {
	case "on", "off":
		return fmt.Sprintf("%s -- this deployment %s unless an item below says otherwise", w.Default, runs)
	default:
		return fmt.Sprintf("not set -- nobody has said, so this deployment %s", runs)
	}
}

func repoWorkSaid(override string) string {
	switch override {
	case "on", "off":
		return override + " here"
	default:
		return "follows the default"
	}
}

// repoWorkItemNote is what else is worth saying about one row: the name
// it has, that it is paused on its own switch (which is not this
// setting and is not undone here), and the file a declared schedule
// comes from.
func repoWorkItemNote(item ui.RepoWorkItem) string {
	var parts []string
	if item.Title != "" {
		parts = append(parts, item.Title)
	}
	if item.Paused {
		parts = append(parts, "paused on its own row")
	}
	if item.Declaration != nil {
		parts = append(parts, item.Declaration.Path)
	}
	if item.Kind == ui.WorkItemKindUnknown {
		parts = append(parts, "nothing here has this id -- a leftover decision, clear it with -set unset")
	}
	if len(parts) == 0 {
		return ""
	}
	return "  (" + strings.Join(parts, ", ") + ")"
}

// repoWorkflows prints each declared workflow whole -- its path, the
// name the file gives itself, and the file.
//
// Whole, unlike the setup command's single line and unlike `grain repo
// list`'s pointer at this command: there is nowhere else to read one
// from a shell, and a workflow that is summarised is a workflow whose
// steps somebody has to open a browser to see. What the *repository*
// declares in its own tree is not here and cannot be -- reading that
// means cloning a branch, which is a run's job.
func (p *printer) repoWorkflows(w ui.RepoWorkflowsResponse) {
	if p.json {
		p.encode(w)
		return
	}
	fmt.Println(w.Repo)
	if len(w.Workflows) == 0 {
		fmt.Printf("grain declares no CI workflows for this repo. One declared here runs alongside "+
			"anything the repository itself declares in its %s, and replaces one it declares at the "+
			"same path\n", w.Dir)
		return
	}
	for _, workflow := range w.Workflows {
		label := workflow.Path
		if workflow.Name != "" && workflow.Name != workflow.Path {
			label += " (" + workflow.Name + ")"
		}
		fmt.Print("\n" + promptExtensionBlock(label, workflow.Text))
	}
}

func capabilityList(ids []string) string {
	if len(ids) == 0 {
		return "none"
	}
	return strings.Join(ids, ", ")
}

// targetRepos prints just the allowlist out of the settings `grain repo
// add`/`remove` get back, rather than the whole settings block: the
// deployment's poll interval is not what somebody who just added a repo
// asked about. -json still gets the whole response, which is what the
// endpoint actually returned and what a script would want to read
// another field out of.
func (p *printer) targetRepos(s ui.Settings) {
	if p.json {
		p.encode(s)
		return
	}
	fmt.Println("target repos updated")
}
