package model

import (
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
	"time"
)

// A repository can declare its own standing work: the templates, suites
// and schedules that belong to it, written as files in its own `.grain`
// directory and read back out of it by grain (grain/task-60).
//
// This is the same argument pkg/ci already makes for `.grain/workflows`,
// applied one layer up. A repository's checks live in the repository
// because they describe that repository and change when it changes; its
// nightly chore, its release sweep and the template they both file from
// describe it just as closely, and until now they could only be typed
// into grain's own settings -- a deployment away from the code, invisible
// in a pull request that changes the thing being scheduled, and impossible
// to review alongside it. A file under `.grain` is reviewed where the code
// is reviewed, branches with it, and arrives the moment it merges.
//
// # What a declaration is, and what it is emphatically not
//
// RepoWorkflow's doc comment draws the line this one has to hold to:
// "A `.grain/workflows` file in a target repository is a file every
// agent grain dispatches can rewrite; a row here is written through
// grain's own settings surface". That is just as true of these files,
// and it is why a declaration may only say *what work to do in this
// repository, and when* -- never what that work may touch:
//
//   - The target is always the declaring repository. A declared template
//     cannot file a task against somebody else's repo. It may name extra
//     repositories to read and push only as `repo-read:` and
//     `repo-write:` capabilities (grain/task-1780, as a `.grain/workflows`
//     job may since grain/task-1654), and those reach no further than
//     the filing team's own GitHub credential does: the git proxy fetches
//     and pushes a task's work with that credential and no other
//     (Store.TaskGitCredential), and Config.TargetRepos must allow
//     each one (checkDeclaredTargets).
//   - A declaration carries no grants. Capabilities are the deployment's
//     to hand out (Config.DefaultCapabilities, RepoConfig.
//     DefaultCapabilities), and they stay that way: a task filed from a
//     declared template holds exactly what the deployment already gives
//     tasks in that repository, and a file that asks for any capability
//     but repository access is refused rather than quietly ignored.
//   - Nothing is read from a repository at all until an operator turns
//     it on for that repository (RepoConfig.Declarations). Compute is
//     the one thing a declaration really does spend, and an unattended
//     schedule that files a task every hour is worth somebody deciding
//     on once.
//
// # Identity
//
// A declared row lives in the ordinary `template`, `suite` and
// `schedule` tables -- everything that fires one, resolves one or shows
// one in a list goes on working with no idea where it came from -- and
// is told apart by its id, which is derived from the repository and the
// file rather than allocated from a sequence (RepoDeclaredID). That is
// what makes syncing idempotent: reading the same tree twice writes the
// same rows twice, a file that is edited updates the row it already had,
// and a file that is deleted leaves a row this package can identify as
// no longer declared and remove. It is also why the `repo_declaration`
// table exists: the id says *that* a row was declared, and that table
// says by which repository and in which file, which is what an error
// message and grain's refusal to edit one have to be able to say.
const (
	// DeclarationDir is the directory a repository declares in.
	DeclarationDir = ".grain"
	// SettingsDeclarationPath is the primary path for declared repository settings.
	SettingsDeclarationPath = DeclarationDir + "/settings.yml"
	// SettingsDeclarationAltPath is the alternative spelling for declared repository settings.
	SettingsDeclarationAltPath = DeclarationDir + "/settings.yaml"
	// ConfigDeclarationPath is the primary path for declared repository config.
	ConfigDeclarationPath = DeclarationDir + "/config.yml"
	// ConfigDeclarationAltPath is the alternative spelling for declared repository config.
	ConfigDeclarationAltPath = DeclarationDir + "/config.yaml"
	// TemplateDeclarationDir holds one file per template.
	TemplateDeclarationDir = DeclarationDir + "/templates"
	// SuiteDeclarationDir holds one file per suite.
	SuiteDeclarationDir = DeclarationDir + "/suites"
	// ScheduleDeclarationDir holds one file per schedule.
	ScheduleDeclarationDir = DeclarationDir + "/schedules"
	// DefaultReviewStem is the stem of a repository's default review template.
	DefaultReviewStem = "review"
	// DefaultRepairStem is the stem of a repository's default repair template.
	DefaultRepairStem = "repair"
)

// DeclarationKind is which of the things a declaration file
// declares -- the directory or file it was found in, in the form the id and the
// `repo_declaration` row carry it.
type DeclarationKind string

const (
	DeclaredTemplate DeclarationKind = "template"
	DeclaredSuite    DeclarationKind = "suite"
	DeclaredSchedule DeclarationKind = "schedule"
	DeclaredSettings DeclarationKind = "settings"
)

// DeclarationKinds is the three of them, in the order a sync has to
// write them: a suite names templates and a schedule names either, so
// reading them in this order is also resolving them in it.
func DeclarationKinds() []DeclarationKind {
	return []DeclarationKind{DeclaredTemplate, DeclaredSuite, DeclaredSchedule}
}

// RepoDeclaredPrefix marks an id that came out of a repository's own
// `.grain` directory rather than out of grain's settings.
//
// It is SystemTemplatePrefix's neighbour and works the same way: the
// sequences hand out "template-N", "suite-N" and "sched-N", so neither
// namespace can collide with those or with each other, and a caller with
// an id in hand can tell what it is holding without a lookup. Unlike a
// system template, a declared row really is a row -- a schedule has to
// carry its own next/last firing times somewhere, and giving the three
// kinds three different answers to "is this in the table?" would be a
// worse trade than one prefix.
const RepoDeclaredPrefix = "repo:"

// IsRepoDeclaredID reports whether id names something a repository
// declared -- what pkg/ui checks before letting somebody edit or delete
// a template, suite or schedule, the same guard IsSystemTemplateID is
// for the schedule that fires a system template.
func IsRepoDeclaredID(id string) bool { return strings.HasPrefix(id, RepoDeclaredPrefix) }

// RepoDeclaredID is the id of the kind declared by repo in
// <kind dir>/<stem>.<ext>.
//
// Derived rather than allocated, for the reason this file's own doc
// comment gives, and derived from the *file* rather than from the `name:`
// inside it: a name is prose somebody will reword, and rewording it must
// not orphan the row -- nor silently relocate the suite item and the
// schedule pointing at it, which name the template by file stem for
// exactly this reason.
//
// Colons separate the parts because a GitHub owner or repository name
// cannot contain one, and neither can a stem (ValidateDeclarationStem),
// so the encoding is unambiguous. No slashes: an id is a path segment in
// grain's own API (`PATCH /api/templates/{id}`), and a slash there would
// be a route that no longer matches.
func RepoDeclaredID(kind DeclarationKind, repo RepoRef, stem string) string {
	return RepoDeclaredPrefix + string(kind) + ":" +
		strings.ToLower(repo.Owner) + ":" + strings.ToLower(repo.Name) + ":" + stem
}

// DefaultReviewTemplateID returns the ID of a repository's declared default review template.
func DefaultReviewTemplateID(repo RepoRef) string {
	return RepoDeclaredID(DeclaredTemplate, repo, DefaultReviewStem)
}

// DefaultRepairTemplateID returns the ID of a repository's declared default repair template.
func DefaultRepairTemplateID(repo RepoRef) string {
	return RepoDeclaredID(DeclaredTemplate, repo, DefaultRepairStem)
}

// ValidateDeclarationStem reports whether stem is a usable file name for
// a declaration, and says why when it is not.
//
// The rule is deliberately narrow -- letters, digits, dash, underscore,
// dot, not starting with a dot -- because the stem is half of an
// identity that is written into an id, quoted in error messages, typed
// into a suite's `templates:` list and stored as a primary key. Anything
// looser would have to be escaped somewhere, and an id nobody can type
// is one nobody can point a suite at.
func ValidateDeclarationStem(stem string) error {
	if stem == "" {
		return fmt.Errorf("a declaration file needs a name")
	}
	if strings.HasPrefix(stem, ".") {
		return fmt.Errorf("%q: a declaration file name does not start with a dot", stem)
	}
	for _, r := range stem {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return fmt.Errorf("%q: a declaration file name holds letters, digits, '-', '_' and '.' only", stem)
		}
	}
	return nil
}

// DeclarationStem is the identity half of a declaration's path: the file
// name with its extension taken off. `.grain/templates/nightly.yml` is
// the template "nightly", which is what a suite's `templates:` list and
// a schedule's `template:` name it by.
func DeclarationStem(p string) string {
	base := path.Base(p)
	return strings.TrimSuffix(base, path.Ext(base))
}

// RepoDeclaration is one row of the `repo_declaration` table: where a
// declared template, suite or schedule came from.
//
// It is grain's own record rather than settings -- every field of it is
// re-derived from the repository on the next sync -- and it exists
// because the id alone cannot be trusted to carry the path. A file
// renamed from `nightly.yml` to `nightly.yaml` is the same declaration
// with the same id, and "which file do I edit" is the whole of what an
// operator refused an edit in the UI needs to be told.
type RepoDeclaration struct {
	Kind DeclarationKind
	ID   string
	Repo RepoRef
	// Path is the file in Repo this was read from, repo-relative and
	// slash-separated -- ".grain/templates/nightly.yml".
	Path string
	// Commit is the commit of Repo the file was last read at.
	Commit string
	// SyncedAt is when grain last read it.
	SyncedAt time.Time
	// Push is a declared template's `push:` list (RefPush), as of Commit:
	// the refs beyond their own branch the runs of tasks filed from it may
	// update in Repo. Empty for every other kind, and for a template that
	// declares none.
	Push RefPushSet
}

// DeclaredTemplateFile, DeclaredSuiteFile, DeclaredScheduleFile and DeclaredSettingsFile pair
// what one file declares with the file it was declared in. The path
// travels alongside the value rather than on it, because the value is an
// ordinary Template/Suite/Schedule/RepoSettings from that moment on: nothing that
// fires one, composes one or renders one has any use for the path, and
// the one thing that does -- the `repo_declaration` row -- is written
// once, by the sync.
type DeclaredTemplateFile struct {
	Path     string
	Template Template
	// Push is the file's `push:` list. It travels beside the template
	// rather than on it because it is the declaration's and never a
	// setting: the sync writes it next to the provenance row
	// (`repo_declaration_push`), re-read from the default branch on every
	// sync, and nothing a settings export or a UI edit writes can reach it.
	Push RefPushSet
}

type DeclaredSuiteFile struct {
	Path  string
	Suite Suite
}

type DeclaredScheduleFile struct {
	Path     string
	Schedule Schedule
}

// RepoSettings is what a repository says about itself in
// `.grain/settings.yml` -- and it is two fields, which is the whole
// point of it (grain/task-340).
//
// A repository's settings row (RepoConfig) has fourteen fields and a
// repository may write two of them. pkg/declschema's RepoSettingsSchema
// is the classification, field by field with the reason each refusal is
// refused, and this type is only the shape that survives it: the
// repository's own standing instructions and the setup its checkout
// needs, both of them instructions to an agent standing in a tree it can
// already rewrite.
//
// A field added here without being added there is a field pkg/repodecl
// has no key for, and a key added there without a field here does not
// compile -- which is the arrangement that keeps "may a repository say
// this?" from being answered by whoever is editing the parser that day.
type RepoSettings struct {
	PromptExtension string
	SetupCommand    string
	// PortBranches is `port-branches`: the branches this repository
	// keeps in step by porting each merge on one of them to the others
	// (RepoConfig.DeclaredPortBranches).
	PortBranches []string
	// PortTypes is `port-types`: per "<from>-><to>" direction, the
	// conventional-commit types a merge has to carry to be ported that
	// way (RepoConfig.DeclaredPortTypes).
	PortTypes map[string][]string
}

// ApplyTo records what a repository declared about itself on its config
// row, in the two columns the declaration sync owns.
//
// It writes nothing an operator wrote. Not the capabilities, not the
// secrets, not the switch that made grain read this repository in the
// first place -- and not the operator's own prompt extension or setup
// command either, which sit beside these two and are composed with them
// when they are read (RepoConfig.RepoPromptExtension, SetupScript). A
// declaration is a layer, added to what grain already holds; there is no
// field of RepoConfig it can reduce and none it can replace.
//
// Which is also why this is an assignment rather than an append: the
// declared layer lives in its own column, so re-reading an unchanged
// file writes the same two values it wrote last time instead of
// appending the repository's text to itself once per poll.
func (s RepoSettings) ApplyTo(c *RepoConfig) {
	c.DeclaredPromptExtension = s.PromptExtension
	c.DeclaredSetupCommand = s.SetupCommand
	c.DeclaredPortBranches = slices.Clone(s.PortBranches)
	c.DeclaredPortTypes = maps.Clone(s.PortTypes)
}

// Changes is every column applying s to c would actually alter, in
// ApplyTo's own order and named the way the file names them -- what a
// sync writes into the daemon log before it writes the row, so that
// "which merge changed this?" has an answer after the fact
// (grain/task-476).
//
// That question used to be much larger than it is now. A settings
// declaration was a whole-row overwrite, so the first sync after an
// operator switched RepoConfig.Declarations on could blank
// `base-branch-ci-jobs`, `post-merge-dispatch-jobs`, `unfixable-checks`
// or `clone-url` without saying so, and each of those fails somewhere
// else entirely and weeks later, with nothing connecting it to the merge
// that did it -- which is exactly what happened to this deployment at
// 18:52 UTC on 20 Sep 2026. grain/task-340 took the reach away instead
// of guarding it: a declaration now writes two columns of its own and
// there is no field of an operator's that it can reach, so the loss this
// report was written for can no longer be expressed.
//
// It is still worth the line. A repository's standing prompt and its
// setup command are what every agent dispatched there is handed and what
// every checkout runs, they change by merging a file, and a change to
// either one shows up as an agent behaving differently rather than as
// anything that points at the commit. One line per sync that changes
// something is what makes that traceable; a sync that changes nothing
// logs nothing, which is every sync after the file's own last edit.
//
// It is a report, not a guard: nothing here refuses anything, and a
// caller that ignores the result gets exactly the behaviour it had
// before.
func (s RepoSettings) Changes(c RepoConfig) []SettingsChange {
	want := c
	s.ApplyTo(&want)
	var out []SettingsChange
	add := func(field, from, to string) {
		if from != to {
			out = append(out, SettingsChange{Field: field, From: from, To: to})
		}
	}
	// In ApplyTo's order, so the two can be read side by side.
	add("prompt-extension", renderText(c.DeclaredPromptExtension), renderText(want.DeclaredPromptExtension))
	add("setup-command", renderText(c.DeclaredSetupCommand), renderText(want.DeclaredSetupCommand))
	add("port-branches", renderText(strings.Join(c.DeclaredPortBranches, ", ")),
		renderText(strings.Join(want.DeclaredPortBranches, ", ")))
	add("port-types", renderText(EncodePortTypes(c.DeclaredPortTypes)), renderText(EncodePortTypes(want.DeclaredPortTypes)))
	return out
}

// SettingsChange is one column a settings declaration writes, with both
// values already rendered: what this exists for is a log line and an
// operator reading one, not a caller that wants to branch on the value.
type SettingsChange struct {
	// Field is the key in `.grain/settings.yml`, so that somebody who
	// reads the line knows what to type to put the value back.
	Field string
	// From is the row's value before the sync, To after it. A blanked
	// field reads "(blank)", which is the case worth seeing -- it is what
	// a withdrawn or newly silent settings file looks like.
	From string
	To   string
}

func (c SettingsChange) String() string { return c.Field + ": " + c.From + " -> " + c.To }

// DescribeSettingsChanges is a list of them on one line, because one
// line per sync is what greps usefully and what a reader scanning the
// daemon log for a repository actually wants.
func DescribeSettingsChanges(changes []SettingsChange) string {
	parts := make([]string, len(changes))
	for i, c := range changes {
		parts[i] = c.String()
	}
	return strings.Join(parts, "; ")
}

// renderText renders a free-text field. A prompt extension is prose and
// can run to pages, so a long one is reduced to its first line and a
// length: the change worth seeing in a log line is that it was replaced
// or blanked, and the text itself is on the repository page.
func renderText(s string) string {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return "(blank)"
	}
	first, _, multiline := strings.Cut(trimmed, "\n")
	first = strings.TrimSpace(first)
	runes := []rune(first)
	if !multiline && len(runes) <= 60 {
		return fmt.Sprintf("%q", first)
	}
	if len(runes) > 60 {
		first = string(runes[:60])
	}
	return fmt.Sprintf("%q... (%d characters)", first, len([]rune(trimmed)))
}

// DeclaredSettingsFile pairs what a repo settings file declares with the file it was declared in.
type DeclaredSettingsFile struct {
	Path     string
	Settings RepoSettings
}

// RepoDeclarations is everything one repository's `.grain` directory
// declares, as read from one commit of it -- what pkg/repodecl produces
// and what Store.SyncRepoDeclarations writes.
type RepoDeclarations struct {
	Settings  *DeclaredSettingsFile
	Templates []DeclaredTemplateFile
	Suites    []DeclaredSuiteFile
	Schedules []DeclaredScheduleFile
	// Triggers are its `.grain/triggers` (PathTrigger), kept by a sync
	// in a table of their own rather than as repo_declaration rows.
	Triggers []PathTrigger
	// MergeRules is its `.grain/merge-rules.yml` (grain/task-1659), nil
	// where it has none. Kept in a table of its own, like Triggers.
	MergeRules *MergeRules
}

// Empty reports whether a repository declared nothing at all, which is
// the state very nearly every repository is in.
func (d RepoDeclarations) Empty() bool {
	return d.Settings == nil && len(d.Templates) == 0 && len(d.Suites) == 0 && len(d.Schedules) == 0 &&
		len(d.Triggers) == 0 && d.MergeRules == nil
}

// RepoDeclarationSync is grain's record of one repository's declaration
// polling: the commit whose `.grain` directory produced the rows that
// are live now, when the repository was last looked at, and what went
// wrong if something did.
//
// The commit is what makes polling cheap. Reading a repository's
// declarations means cloning it, and a repository that has not moved
// since the last read has nothing to say; one `git ls-remote` per poll
// answers that without a clone (orchestrator.reconcileDeclarations).
//
// LastError is kept because the alternative is a repository whose
// declarations silently stopped updating. A file that does not parse is
// not a reason to tear down the rows that were working -- the previous
// commit's declarations stay live -- so without this row there would be
// nothing at all to show for a branch that broke the format.
type RepoDeclarationSync struct {
	Repo      RepoRef
	Commit    string
	CheckedAt time.Time
	SyncedAt  time.Time
	LastError string
}
