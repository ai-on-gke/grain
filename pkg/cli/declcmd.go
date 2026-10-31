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

// declcmd.go is "grain declarations check": a repository's own `.grain`
// declarations (docs/declarations.md) read out of a working tree, before
// any of it is pushed, and either printed or refused.
//
// declarations.go beside this file is the other half of the same feature
// and is deliberately not this: that is the daemon's own source, where a
// repository is cloned with the credential this deployment holds and the
// rows are written to the store. This is a local verb -- it holds no
// store, asks no daemon, and takes no -server -- because the question it
// answers is asked from inside a checkout, usually by a CI job that has
// nothing but the tree.
//
// Until this existed the only way to find out whether a declaration file
// was well-formed was to merge it and read `repo_declaration_sync.
// last_error` off the repository's settings a poll later. That is the
// loop the wrong way round: a file that does not parse should fail the
// pull request that introduces it, and `.grain/workflows` can now say
// so in a step of its own --
//
//   - run: grain declarations check
//
// -- because this exits non-zero on any error and prints every verdict
// rather than the first (pkg/repodecl.Load joins them, for exactly this
// reason).
//
// Two things here are not in the tree and have to come from somewhere
// else, both because of what they decide:
//
//   - The repository. Every declared id is derived from it
//     (model.RepoDeclaredID) and it is the target of every task a
//     declaration files, which is why no file gets a say in it -- so
//     -repo, or the checkout's own `origin` remote, which is the same
//     answer without the typing.
//   - The zone a time of day is read against. That is the schedule's
//     explicit timezone (or UTC by default); -zone says which one to
//     preview against when no explicit timezone is declared.
//
// The firing preview is the other half of the point. "every: {hours: 6}"
// and `daily: {at: "09:00"}` are both easy to write and hard to picture,
// and a cadence is the one thing in these files whose mistake costs
// agent time rather than a parse error: -next says how many firings to
// work out, from now, through the same model.Recurrence.Next the
// reconciler fires by.
package cli

import (
	"context"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/repodecl"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// cmdDeclarations dispatches `grain declarations`. There is one verb,
// "check", and it is also what a bare `grain declarations` does: a
// second verb here would have to be something this command cannot do
// without a daemon, and `grain repo` already holds the ones that can.
//
// The verb is optional rather than required so that both spellings work,
// and it is matched exactly rather than "the first non-flag argument"
// the way `grain ci` matches its own: the argument after it is a
// directory, and a positional that is sometimes a verb and sometimes a
// path is a command that reads `check` in a directory called check as
// the verb. It is -- pass `./check` for the directory.
func cmdDeclarations(ctx context.Context, _ *ui.HTTPClient, out *printer, args []string) error {
	if len(args) > 0 && args[0] == "check" {
		args = args[1:]
	}
	return cmdDeclarationsCheck(ctx, out, args)
}

func cmdDeclarationsCheck(ctx context.Context, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain declarations check", flag.ContinueOnError)
	repoFlag := fs.String("repo", "", "the repository these declarations belong to, as owner/name "+
		"(default: the checkout's own \"origin\" remote)")
	next := fs.Int("next", 3, "how many upcoming firings to work out for each declared schedule (0 for none)")
	zone := fs.String("zone", "UTC", "IANA zone to preview schedule time of day against when no explicit time zone is declared")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := atMostArgs(fs, fs.Args(), 1); err != nil {
		return err
	}
	dir := fs.Arg(0)
	if dir == "" {
		dir = "."
	}
	// Stated here rather than left to repodecl.Load, which reads a
	// missing `.grain/templates` as "declares nothing" -- correct for a
	// repository, and exactly the wrong answer for a path that is a typo:
	// a check that reports "nothing declared, all well" for a directory
	// that does not exist is a green CI step that checked nothing.
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory -- point this at the root of a checkout, "+
			"the directory %s lives in", dir, model.DeclarationDir)
	}
	loc, err := time.LoadLocation(strings.TrimSpace(*zone))
	if err != nil {
		return fmt.Errorf("-zone %q: %w", *zone, err)
	}
	if *next < 0 {
		return fmt.Errorf("-next %d: a count of firings to print, not fewer than none", *next)
	}
	repo, source, err := declarationRepo(ctx, *repoFlag, dir)
	if err != nil {
		return err
	}

	view := declarationsView{
		Dir:        dir,
		Repo:       repo.String(),
		RepoSource: source,
		Zone:       loc.String(),
	}
	declared, loadErr := repodecl.Load(os.DirFS(dir), repo)
	if loadErr != nil {
		view.Errors = declarationProblems(loadErr)
		out.declarations(view)
		return fmt.Errorf("%s", declarationVerdict(len(view.Errors)))
	}
	view.OK = true
	view.fill(declared, time.Now(), loc, *next)
	out.declarations(view)
	return nil
}

// declarationVerdict is the one-line error the command exits with, and
// so the line a CI log ends on. It says how many problems there were and
// nothing else: each of them has already been printed above it, in full,
// and repeating the first one here would make it look like the only one.
//
// Problems rather than files, because one bad file can be several: a
// template that does not parse is not declared, so the suite naming it
// and the schedule naming that suite are each refused for a template
// that is not there. The count is what was printed, not a claim about
// how many files to open.
func declarationVerdict(problems int) string {
	if problems == 1 {
		return "1 problem with this repository's declarations -- see above"
	}
	return fmt.Sprintf("%d problems with this repository's declarations -- see above", problems)
}

// declarationRepo decides which repository the tree is read as, and says
// where that came from so the output can too: an id derived from the
// wrong owner is a check that passed for a repository nobody is going to
// push to.
//
// The flag wins; otherwise the checkout's `origin` is asked, which is
// what a CI job in a cloned repository has and what a person in their own
// clone would have typed. Nothing else is guessed -- a directory that is
// not a git checkout, or a clone with no origin, is asked for -repo
// rather than defaulted to something.
func declarationRepo(ctx context.Context, flagValue, dir string) (model.RepoRef, string, error) {
	if trimmed := strings.TrimSpace(flagValue); trimmed != "" {
		repo, err := model.ParseRepo(trimmed)
		if err != nil {
			return model.RepoRef{}, "", err
		}
		return repo, "-repo", nil
	}
	out, err := daemon.CiRunGit(ctx, dir, nil, "remote", "get-url", "origin")
	if err != nil {
		return model.RepoRef{}, "", fmt.Errorf("no -repo, and %s has no \"origin\" remote to read one from "+
			"(%s): pass -repo owner/name -- the repository decides every declared id and is the repo "+
			"every declared task targets", dir, strings.TrimSpace(firstLine(out)))
	}
	remote := strings.TrimSpace(out)
	repo, err := model.ParseRepo(remote)
	if err != nil {
		return model.RepoRef{}, "", fmt.Errorf("the \"origin\" remote of %s is %q, which is not owner/name: "+
			"pass -repo owner/name", dir, remote)
	}
	return repo, "origin", nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// declarationProblems flattens what Load returned into one line per
// problem. Load joins its errors rather than returning the first
// (errors.Join), which is the whole reason this command can be one CI
// step: somebody who has just written three declaration files wants all
// three verdicts, not one per push.
func declarationProblems(err error) []string {
	var out []string
	var walk func(error)
	walk = func(e error) {
		switch joined := e.(type) {
		case interface{ Unwrap() []error }:
			for _, inner := range joined.Unwrap() {
				walk(inner)
			}
		default:
			out = append(out, e.Error())
		}
	}
	walk(err)
	sort.Strings(out)
	return out
}

// declarationsView is what the command prints, in both shapes. It is a
// view of its own rather than model.RepoDeclarations because -json is a
// promise to whatever parses it: the model types carry no JSON tags and
// are free to be rearranged, and half of what is interesting here (the
// file each thing came from, the stems a suite names, the firings) is
// either alongside the model value or computed.
type declarationsView struct {
	Dir  string `json:"dir"`
	Repo string `json:"repo"`
	// RepoSource is "-repo" or "origin" -- which of the two decided,
	// printed because an id derived from the wrong repository is the one
	// mistake this command could otherwise make silently.
	RepoSource string `json:"repoSource"`
	Zone       string `json:"zone"`
	// OK is the verdict, and is false whenever Errors is non-empty. A
	// tree with any error at all declares nothing (Load's own rule: a
	// suite naming a template that did not parse is not a suite anybody
	// meant to run), so the two are never both populated.
	OK        bool                   `json:"ok"`
	Errors    []string               `json:"errors,omitempty"`
	Settings  *declaredSettingsView  `json:"settings,omitempty"`
	Templates []declaredTemplateView `json:"templates,omitempty"`
	Suites    []declaredSuiteView    `json:"suites,omitempty"`
	Schedules []declaredScheduleView `json:"schedules,omitempty"`
	Triggers  []model.PathTrigger    `json:"triggers,omitempty"`
}

// declaredSettingsView is the two settings a repository may declare for
// itself, which is the whole of model.RepoSettings (grain/task-340).
// Both are a layer added to what an operator already set for this
// repository rather than a replacement of it, which is why `grain
// declarations check` calls them what it does.
type declaredSettingsView struct {
	Path            string `json:"path"`
	PromptExtension string `json:"promptExtension,omitempty"`
	SetupCommand    string `json:"setupCommand,omitempty"`
}

type declaredTemplateView struct {
	Path      string `json:"path"`
	Stem      string `json:"stem"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	Title     string `json:"title"`
	Body      string `json:"body,omitempty"`
	Base      string `json:"base,omitempty"`
	AutoMerge bool   `json:"autoMerge"`
	Network   string `json:"network,omitempty"`
	// Interactive is the template's interactive: a live chat.
	Interactive bool `json:"interactive,omitempty"`
	// KeepRunning is the template's keep-running: restart on exit.
	KeepRunning bool `json:"keepRunning,omitempty"`
	// NothingNeeded is the template's nothing-needed: "" for close.
	NothingNeeded string              `json:"nothingNeeded,omitempty"`
	CustomFields  map[string][]string `json:"customFields,omitempty"`
	// Push is the refs beyond their own branch this template's runs may
	// push here (grain/task-2536).
	Push model.RefPushSet `json:"push,omitempty"`
}

type declaredSuiteView struct {
	Path string `json:"path"`
	Stem string `json:"stem"`
	ID   string `json:"id"`
	Name string `json:"name"`
	// Templates is the stems the suite runs, in the order it lists them
	// -- what the file says, rather than the ids it was resolved into.
	Templates       []string `json:"templates"`
	Mode            string   `json:"mode"`
	Count           int      `json:"count,omitempty"`
	MaxPasses       int      `json:"maxPasses,omitempty"`
	RequireApproval bool     `json:"requireApproval"`
	AutoMerge       bool     `json:"autoMerge"`
}

type declaredScheduleView struct {
	Path string `json:"path"`
	Stem string `json:"stem"`
	ID   string `json:"id"`
	Name string `json:"name"`
	// Fires is "template <stem>" or "suite <stem>": what comes due.
	Fires      string      `json:"fires"`
	Cadence    string      `json:"cadence"`
	Base       string      `json:"base,omitempty"`
	Overlap    string      `json:"overlap"`
	Enabled    bool        `json:"enabled"`
	NextFiring []time.Time `json:"nextFiring,omitempty"`
}

// fill turns what Load produced into the view, working out each
// schedule's upcoming firings from now.
//
// From now, rather than from anything stored, because that is what a
// tree that has never been read means: a declared schedule's NextRunAt
// is set to Recurrence.Next(now) when grain first sees it
// (SyncRepoDeclarations), so "the first firing after this merges" is
// exactly what this computes. A schedule grain already holds keeps its
// own countdown instead, which is why this is a preview of a cadence and
// not a promise about a row.
func (v *declarationsView) fill(declared model.RepoDeclarations, now time.Time, loc *time.Location, next int) {
	if declared.Settings != nil {
		s := declared.Settings.Settings
		v.Settings = &declaredSettingsView{
			Path:            declared.Settings.Path,
			PromptExtension: s.PromptExtension,
			SetupCommand:    s.SetupCommand,
		}
	}

	// Stem by template id, so a suite prints the names its file actually
	// wrote rather than the ids they were resolved into.
	stems := map[string]string{}
	for _, t := range declared.Templates {
		stems[t.Template.ID] = model.DeclarationStem(t.Path)
	}
	for _, t := range declared.Templates {
		v.Templates = append(v.Templates, declaredTemplateView{
			Path:         t.Path,
			Stem:         model.DeclarationStem(t.Path),
			ID:           t.Template.ID,
			Name:         t.Template.Name,
			Title:        t.Template.Title,
			Body:         t.Template.Body,
			Base:         t.Template.Base,
			AutoMerge:    t.Template.AutoMerge,
			Network:      string(t.Template.NetworkAccess),
			Interactive:  t.Template.Interactive,
			KeepRunning:  t.Template.KeepRunning,
			CustomFields: t.Template.CustomFields,
			Push:         t.Push,

			NothingNeeded: string(t.Template.NothingNeededAction),
		})
	}
	for _, s := range declared.Suites {
		view := declaredSuiteView{
			Path:            s.Path,
			Stem:            model.DeclarationStem(s.Path),
			ID:              s.Suite.ID,
			Name:            s.Suite.Name,
			Mode:            string(s.Suite.Mode),
			RequireApproval: s.Suite.RequireApproval,
			AutoMerge:       s.Suite.AutoMerge,
		}
		switch s.Suite.Mode {
		case model.SuiteUntilClean:
			view.MaxPasses = s.Suite.MaxPasses
		default:
			view.Count = s.Suite.Count
		}
		for _, item := range s.Suite.Items {
			view.Templates = append(view.Templates, declaredName(stems, item.TemplateID))
		}
		v.Suites = append(v.Suites, view)
	}
	for _, s := range declared.Schedules {
		view := declaredScheduleView{
			Path: s.Path,
			// The identity stem off the id rather than off the path: the
			// two are the same for a `.grain/schedules` file, and for one
			// translated out of a workflow cron the id is the one that
			// says which schedule this is (`workflow.nightly`).
			Stem:    declaredStem(s.Schedule.ID),
			ID:      s.Schedule.ID,
			Name:    s.Schedule.Title,
			Fires:   declaredTrigger(stems, s.Schedule),
			Cadence: declaredCadence(s.Schedule.Recurrence),
			Base:    s.Schedule.Base,
			Overlap: overlapWording(s.Schedule.Overlap),
			Enabled: s.Schedule.Enabled,
		}
		at := now
		schedLoc := s.Schedule.Location()
		if schedLoc == time.UTC && loc != nil && loc != time.UTC {
			schedLoc = loc
		}
		for i := 0; i < next; i++ {
			at = s.Schedule.Recurrence.Next(at, schedLoc)
			view.NextFiring = append(view.NextFiring, at)
		}
		v.Schedules = append(v.Schedules, view)
	}
	v.Triggers = declared.Triggers
}

// declaredTrigger says what a schedule fires, by the stem its file
// names: "template nightly", "suite sweep".
func declaredTrigger(stems map[string]string, sched model.Schedule) string {
	if sched.TemplateID != nil {
		// A schedule translated out of a workflow's own `on: schedule`
		// cron fires one of grain's templates rather than one of this
		// repository's (model.WorkflowReviewTemplatePrefix), so naming a
		// stem the repository never wrote would read as a template it
		// could go and edit.
		if stem, ok := model.WorkflowReviewStem(*sched.TemplateID); ok {
			if sched.Base != "" {
				return "the " + stem + " workflow on " + sched.Base + ", and a review of the run"
			}
			return "the " + stem + " workflow, and a review of the run"
		}
		return "template " + declaredName(stems, *sched.TemplateID)
	}
	if sched.SuiteID != nil {
		return "suite " + declaredStem(*sched.SuiteID)
	}
	return ""
}

// declaredName is the stem an id was declared under: looked up where the
// caller has the map (templates), and otherwise read back off the id
// itself, which ends in the stem by construction (model.RepoDeclaredID).
func declaredName(stems map[string]string, id string) string {
	if stem, ok := stems[id]; ok {
		return stem
	}
	return declaredStem(id)
}

func declaredStem(id string) string {
	if i := strings.LastIndex(id, ":"); i >= 0 {
		return id[i+1:]
	}
	return id
}

// declaredCadence words a declared schedule's cadence in the one
// wording this CLI already has for one -- describeRecurrence, which is
// the Processes page's own (ui/src/processes.js), so a
// terminal and a browser call the same schedule the same thing.
//
// Through ui.Recurrence because that is the shape that function takes
// and pkg/ui's conversion into it (recurrenceFrom) is unexported. Three
// assignments is a cheaper price than a second spelling of "every 6h"
// that can drift from the first.
func declaredCadence(r model.Recurrence) string {
	wire := ui.Recurrence{Kind: string(r.Kind), EveryNHours: r.EveryNHours, EveryNMinutes: r.EveryNMinutes, DayOfMonth: r.DayOfMonth}
	switch r.Kind {
	case model.RecurrenceDaily, model.RecurrenceMonthly:
		wire.TimeOfDay = declaredTimeOfDay(r.TimeOfDay)
	case model.RecurrenceWeekly:
		wire.TimeOfDay = declaredTimeOfDay(r.TimeOfDay)
		wire.Weekday = strings.ToLower(r.Weekday.String())
	}
	return describeRecurrence(wire)
}

// describeRecurrence is the Go half of processes.js's function of
// the same name: the one wording the whole product has for a cadence, so
// a terminal and a browser call the same schedule the same thing.
func describeRecurrence(r ui.Recurrence) string {
	switch r.Kind {
	case "everyNHours":
		return fmt.Sprintf("every %dh", r.EveryNHours)
	case "everyNMinutes":
		return fmt.Sprintf("every %dm", r.EveryNMinutes)
	case "daily":
		return "daily at " + r.TimeOfDay
	case "weekly":
		return r.Weekday + "s at " + r.TimeOfDay
	case "monthly":
		return fmt.Sprintf("monthly on day %d at %s", r.DayOfMonth, r.TimeOfDay)
	default:
		return r.Kind
	}
}

// declaredTimeOfDay renders minutes since midnight as a wall clock reads
// it, which is also how the file wrote it (`at: "09:00"`).
func declaredTimeOfDay(minutes int) string {
	return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60)
}

// declarations prints one reading of a tree: every problem, or
// everything it declares.
func (p *printer) declarations(v declarationsView) {
	if p.json {
		p.encode(v)
		return
	}
	where := v.Dir
	if abs, err := filepath.Abs(v.Dir); err == nil {
		where = abs
	}
	fmt.Printf("%s, read as %s (from %s)\n", where, v.Repo, v.RepoSource)
	if len(v.Errors) > 0 {
		fmt.Println()
		for _, problem := range v.Errors {
			fmt.Println(problem)
		}
		// Said once, at the end, because it is the difference between
		// this output and a compiler's: nothing at all is declared while
		// any of it is wrong, so a repository whose one bad file is the
		// third of four has not half-landed the other three.
		fmt.Printf("\nNothing is declared while any of it is refused: grain keeps the declarations " +
			"it already had and records the reason (repo_declaration_sync.last_error).\n")
		return
	}
	if v.Settings == nil && len(v.Templates) == 0 && len(v.Suites) == 0 && len(v.Schedules) == 0 &&
		len(v.Triggers) == 0 {
		fmt.Printf("declares nothing: no %s, %s, %s or %s here. See docs/declarations.md for what goes in them.\n",
			model.SettingsDeclarationPath, model.TemplateDeclarationDir, model.SuiteDeclarationDir, model.ScheduleDeclarationDir)
		return
	}
	indentBlock := func(text string) string {
		lines := strings.Split(text, "\n")
		for i, line := range lines {
			lines[i] = "    " + line
		}
		return strings.Join(lines, "\n")
	}
	if v.Settings != nil {
		// Said as "adds", not as "sets": both of these are appended to
		// whatever an operator already wrote for this repository, and a
		// reader of this output who thinks a declaration replaces the
		// settings box beside it has the wrong idea of the whole feature
		// (model.RepoConfig.DeclaredPromptExtension).
		fmt.Printf("\nsettings  %s\n", v.Settings.Path)
		if v.Settings.PromptExtension != "" {
			fmt.Printf("  adds to this repository's prompt extension:\n%s\n",
				indentBlock(v.Settings.PromptExtension))
		}
		if v.Settings.SetupCommand != "" {
			fmt.Printf("  adds to this repository's setup command:\n%s\n",
				indentBlock(v.Settings.SetupCommand))
		}
	}
	for _, t := range v.Templates {
		fmt.Printf("\ntemplate %s  %s\n", t.Stem, t.Path)
		fmt.Printf("  id:         %s\n", t.ID)
		fmt.Printf("  name:       %s\n", t.Name)
		fmt.Printf("  title:      %s\n", t.Title)
		fmt.Printf("  base:       %s\n", baseOrDefault(t.Base))
		fmt.Printf("  auto-merge: %t\n", t.AutoMerge)
		for i, r := range t.Push {
			label := "              "
			if i == 0 {
				label = "  pushes:     "
			}
			fmt.Printf("%s%s\n", label, r)
		}
		if t.Network != "" {
			fmt.Printf("  network:    %s\n", t.Network)
		}
		if t.NothingNeeded != "" {
			fmt.Printf("  nothing-needed: %s\n", t.NothingNeeded)
		}
		if t.Interactive {
			fmt.Printf("  interactive: true\n")
		}
		if t.KeepRunning {
			fmt.Printf("  keep-running: true\n")
		}
		if len(t.CustomFields) > 0 {
			values := make([]string, 0, len(t.CustomFields))
			for _, name := range daemon.SortedKeys(t.CustomFields) {
				values = append(values, name+"="+strings.Join(t.CustomFields[name], ","))
			}
			// Not checked against this deployment's own declared fields:
			// that is a lookup in the store, and this command deliberately
			// has no daemon to ask. A field name no deployment declares is
			// dropped when the task is filed, not here.
			fmt.Printf("  fields:     %s\n", strings.Join(values, " "))
		}
	}
	for _, s := range v.Suites {
		fmt.Printf("\nsuite %s  %s\n", s.Stem, s.Path)
		fmt.Printf("  id:         %s\n", s.ID)
		fmt.Printf("  name:       %s\n", s.Name)
		fmt.Printf("  runs:       %s\n", strings.Join(s.Templates, ", "))
		if s.Mode == string(model.SuiteUntilClean) {
			fmt.Printf("  mode:       until clean, at most %d passes\n", s.MaxPasses)
		} else {
			fmt.Printf("  mode:       %d task(s) per template\n", s.Count)
		}
		fmt.Printf("  auto-merge: %t\n", s.AutoMerge)
		fmt.Printf("  approval:   %s\n", approvalWording(s.RequireApproval))
	}
	for _, s := range v.Schedules {
		fmt.Printf("\nschedule %s  %s\n", s.Stem, s.Path)
		fmt.Printf("  id:         %s\n", s.ID)
		fmt.Printf("  name:       %s\n", s.Name)
		fmt.Printf("  fires:      %s\n", s.Fires)
		fmt.Printf("  cadence:    %s\n", s.Cadence)
		fmt.Printf("  base:       %s\n", baseOrDefault(s.Base))
		fmt.Printf("  overlap:    %s\n", s.Overlap)
		if !s.Enabled {
			fmt.Printf("  enabled:    no -- declared, synced, and never comes due until the file says otherwise\n")
		}
		for i, at := range s.NextFiring {
			label := "  next:     "
			if i > 0 {
				label = "            "
			}
			fmt.Printf("%s  %s\n", label, at.Format("Mon 2006-01-02 15:04 MST"))
		}
	}
	for _, t := range v.Triggers {
		fmt.Printf("\ntrigger %s  %s\n", t.Name, t.Path)
		fmt.Printf("  on:         %s\n", t.On)
		if len(t.Branches) > 0 {
			fmt.Printf("  branches:   %s\n", strings.Join(t.Branches, ", "))
		} else {
			fmt.Printf("  branches:   %s\n", baseOrDefault(""))
		}
		fmt.Printf("  paths:      %s\n", strings.Join(t.Paths, ", "))
		if len(t.Ignore) > 0 {
			fmt.Printf("  ignore:     %s\n", strings.Join(t.Ignore, ", "))
		}
		fmt.Printf("  files:      %s, one task per %s\n", t.Template, t.Per)
		if t.Gate {
			fmt.Printf("  gate:       holds the merge; human: %s\n", t.Human)
		}
		fmt.Printf("  approval:   %s\n", approvalWording(!t.AutoApprove))
	}
	// The switch, last, because a tree that reads perfectly still
	// declares nothing at all until an operator turns this repository on
	// (RepoConfig.Declarations) -- and somebody running this for the
	// first time has no other way to find that out.
	fmt.Printf("\nWell-formed. Grain reads these once an operator turns declarations on for %s "+
		"(`grain repo`, or the repository's own settings page); until then they are just files.\n", v.Repo)
}

func baseOrDefault(base string) string {
	if strings.TrimSpace(base) == "" {
		return "(the repository's default branch)"
	}
	return base
}

// overlapWording spells out the empty overlap policy as the "skip" it
// means (model.OverlapPolicy's zero value), rather than printing a blank
// where the file left the key out.
func overlapWording(policy model.OverlapPolicy) string {
	if policy == "" {
		return string(model.OverlapSkip)
	}
	return string(policy)
}

func approvalWording(required bool) string {
	if required {
		return "each task waits for a human to approve it"
	}
	return "tasks are queued without waiting for approval"
}
