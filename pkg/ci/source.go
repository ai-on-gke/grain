package ci

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Source is where a repository's workflows come from: given a repo and
// a branch, the parsed contents of its explicit `.grain/workflows`, or
// its `.github/workflows` when that directory declares no workflow of
// grain's own, and the commit they were read at.
//
// An interface because a workflow does not have to come out of the
// repository it runs against. FSSource reads a checkout that already
// exists, which is what a test needs and what a deployment running CI
// against its own source tree can use. DeclaredSource reads workflows
// grain holds on its own side, which is the only place they can live
// for the repositories grain does *not* control: nobody is going to
// accept a pull request adding a grain-specific CI directory to their
// repository. Layered composes the two, so a repository can be
// described by whichever of them has something to say about it -- and
// nothing in the engine, the plan or the format knows which one a
// workflow arrived from.
type Source interface {
	// Workflows returns every workflow declared for repo at ref, and the
	// commit ref resolved to (empty where the source has no notion of
	// one). A repository that declares none is not an error: no
	// workflows, no error, and PlanRun makes that an empty plan.
	Workflows(ctx context.Context, repo, ref string) (workflows []Workflow, commit string, err error)
}

// TreeSource is a Source that can also say what the commit it read those
// workflows at *contains* -- which is what keying a job on the files it
// declares needs (paths.go).
//
// One method returning both rather than a second call, because the source
// that can answer this is the one that clones the branch onto the
// controller (cmd/grain's ciWorkflowSource) and a second call would be a
// second clone of a whole branch on the request that starts a run.
//
// Optional, and a Source that does not implement it is not diminished: a
// nil tree means every job keys on the commit, which is what they all did
// before `paths:` existed. DeclaredSource is the honest example -- it
// reads rows out of grain's own store and has no checkout to look at.
type TreeSource interface {
	Source
	// WorkflowsAndTree is Workflows, plus the content of the tree those
	// workflows were read out of. A nil tree is "this source cannot say",
	// and is not an error.
	WorkflowsAndTree(ctx context.Context, repo, ref string) (workflows []Workflow, commit string, tree *Tree, err error)
}

// workflowsAndTree asks source for a tree where it has one to give, and
// falls back to the plain Source contract where it has not. It is the one
// place the optional interface is probed, so nothing else has to know the
// distinction exists.
func workflowsAndTree(ctx context.Context, source Source, repo, ref string) ([]Workflow, string, *Tree, error) {
	if ts, ok := source.(TreeSource); ok {
		return ts.WorkflowsAndTree(ctx, repo, ref)
	}
	workflows, commit, err := source.Workflows(ctx, repo, ref)
	return workflows, commit, nil, err
}

// BranchSource is a Source that can also say whether a commit is part of
// a branch's history -- which is what Service.Start asks when a caller
// pins a run to a commit that is not the tip the source resolved.
//
// The question matters because the job's sandbox checks out the commit
// it is given, fetching it by name when no branch brought it down, and
// GitHub serves any object it holds for the repository by name: a
// pull request's head from a fork (refs/pull/N/head) included. A commit
// that is on the branch is a tree somebody with write access put there;
// one that is not could be anybody's, and is handed none of the
// repository's capabilities (Request.untrustedCommit).
//
// Optional. A source that cannot answer leaves every off-tip commit
// untrusted, which costs a pinned run its capabilities and nothing
// else.
type BranchSource interface {
	Source
	// OnBranch reports whether commit is reachable from ref. A commit
	// the source does not hold at all is false, not an error.
	OnBranch(ctx context.Context, repo, ref, commit string) (bool, error)
}

// commitOnBranch asks source whether commit is on ref, false where it
// cannot say.
func commitOnBranch(ctx context.Context, source Source, repo, ref, commit string) (bool, error) {
	if bs, ok := source.(BranchSource); ok {
		return bs.OnBranch(ctx, repo, ref, commit)
	}
	return false, nil
}

// sameCommit is whether a caller's commit names the resolved one: the
// full SHA, or an abbreviation of it long enough for git to accept.
func sameCommit(asked, resolved string) bool {
	asked, resolved = strings.ToLower(asked), strings.ToLower(resolved)
	return asked == resolved || (len(asked) >= 7 && strings.HasPrefix(resolved, asked))
}

// SourceFunc adapts a function to Source, for the wiring that has one
// closure's worth of work to do.
type SourceFunc func(ctx context.Context, repo, ref string) ([]Workflow, string, error)

// Workflows implements Source.
func (f SourceFunc) Workflows(ctx context.Context, repo, ref string) ([]Workflow, string, error) {
	return f(ctx, repo, ref)
}

// workflowDefinitionError marks a deterministic error in workflow text. A
// Service records one as a failed run tied to the requested commit; errors
// without this marker are source-access failures and remain retryable.
type workflowDefinitionError struct{ err error }

func (e *workflowDefinitionError) Error() string { return e.err.Error() }
func (e *workflowDefinitionError) Unwrap() error { return e.err }

func markWorkflowDefinitionError(err error) error {
	if err == nil {
		return nil
	}
	var marked *workflowDefinitionError
	if errors.As(err, &marked) {
		return err
	}
	return &workflowDefinitionError{err: err}
}

func isWorkflowDefinitionError(err error) bool {
	var marked *workflowDefinitionError
	return errors.As(err, &marked)
}

// FSSource reads workflows out of a checkout that is already on this
// machine: Root/<the repository's own name>, or Root itself for a
// single-repository deployment (see Dir).
//
// It resolves no commit and does no fetching -- whatever is in the
// directory is what runs. That makes it exactly right for two callers
// and wrong for everything else: a test, and an operator pointing grain
// CI at grain's own source checkout to watch it work before there is
// anything cloning branches on demand.
type FSSource struct {
	// Root is the directory holding the checkout.
	Root string
	// PerRepo, when set, reads Root/<owner>/<name> rather than Root
	// itself, so one root can hold several repositories' checkouts.
	PerRepo bool
}

// Dir is the directory this source reads repo's workflows from.
func (s FSSource) Dir(repo string) string {
	if !s.PerRepo {
		return s.Root
	}
	return filepath.Join(s.Root, filepath.FromSlash(repo))
}

// Workflows implements Source.
func (s FSSource) Workflows(_ context.Context, repo, _ string) ([]Workflow, string, error) {
	dir := s.Dir(repo)
	if dir == "" {
		return nil, "", fmt.Errorf("ci: this deployment's workflow source has no directory configured")
	}
	return LoadDir(os.DirFS(dir))
}

// LoadDir reads and parses every workflow of one repository tree: the
// files in `.grain/workflows` where that directory declares at least one
// workflow, and `.github/workflows` otherwise. Files are ordered by path.
//
// The rule is "a directory that says something is authoritative", not "a
// directory that exists". Repositories that already describe their checks
// to GitHub need no duplicate Grain-only file, and a `.grain/workflows`
// holding one is an explicit override of them -- but a `.grain/workflows`
// holding no workflow file at all (a README, a `.disabled`, a `.yml`
// someone renamed, a subdirectory: loadWorkflowEntries skips all four)
// overrides nothing, because it names nothing to run instead. Keying on
// the directory's existence made such a tree report "this repository
// declares no workflows", which is the one empty plan PlanRun keeps as a
// quiet success -- so a repository with a full `.github/workflows` suite
// could be shadowed into a vacuous green by a directory that said
// nothing, which is exactly the untested-tree-merges shape grain/task-254
// took the rest of out. Falling back instead restores the invariant that
// silence means the *tree* holds no workflow file anywhere, rather than
// that grain looked in the wrong one of two directories.
//
// The cost, stated because somebody may be relying on it: emptying
// `.grain/workflows` no longer turns grain CI off for a repository whose
// `.github/workflows` has files in it. That was never a declared way to
// say "run nothing" -- and it is not one anywhere else either, since a
// workflow that matches no event is an errored run rather than a quiet
// one -- so an opt-out that means it belongs on the repo's own config
// (model.RepoConfig), not in the shape of a directory.
//
// An fs.FS rather than a directory path so the same function serves a
// real checkout (os.DirFS), an embedded tree, and a test's fstest.MapFS
// with no temp directory at all. It is exported because it is the whole
// of "what does this repository declare", which is worth being able to
// ask of a tree from anywhere -- a CLI checking a file before it is
// pushed, most obviously.
func LoadDir(tree fs.FS) ([]Workflow, string, error) {
	workflows, commit, err := loadWorkflowDir(tree, WorkflowDir)
	if err != nil || len(workflows) > 0 {
		// A file under WorkflowDir that does not parse is still this
		// repository's answer and still an error: falling back to
		// GitHub's files because grain's own are broken would run a
		// different suite than the one somebody wrote, and call it a
		// verdict on the commit.
		return workflows, commit, err
	}
	return loadWorkflowDir(tree, GitHubWorkflowDir)
}

// LoadNativeDir reads the workflows a repository declares in grain's own
// WorkflowDir, and only those: unlike LoadDir it never falls back to
// `.github/workflows`, and a tree with no `.grain/workflows` directory
// declares nothing rather than borrowing GitHub's files.
//
// It exists for the one caller that has to know which directory an answer
// came from: pkg/repodecl, which turns a native workflow's `on: schedule`
// cron into one of this deployment's task schedules and must not do that to
// a file GitHub's own scheduler already runs (rawOn.triggers has the whole
// of that argument).
func LoadNativeDir(tree fs.FS) ([]Workflow, error) {
	workflows, _, err := loadWorkflowDir(tree, WorkflowDir)
	return workflows, err
}

func loadWorkflowDir(tree fs.FS, dir string) ([]Workflow, string, error) {
	entries, err := fs.ReadDir(tree, dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", nil
		}
		return nil, "", fmt.Errorf("ci: reading %s: %w", dir, err)
	}
	return loadWorkflowEntries(tree, dir, entries)
}

func loadWorkflowEntries(tree fs.FS, dir string, entries []fs.DirEntry) ([]Workflow, string, error) {

	var names []string
	for _, entry := range entries {
		if entry.IsDir() || !isWorkflowFile(entry.Name()) {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)

	var (
		workflows []Workflow
		firstErr  error
	)
	for _, name := range names {
		filePath := path.Join(dir, name)
		data, err := fs.ReadFile(tree, filePath)
		if err != nil {
			return nil, "", fmt.Errorf("ci: reading %s: %w", filePath, err)
		}
		w, err := parseForSource(filePath, data)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		workflows = append(workflows, w)
	}
	return workflows, "", firstErr
}

// DeclaredWorkflow is one workflow declared on grain's own side: the
// path it would have had in the repository, and the file itself.
//
// The pair model.RepoWorkflow carries, restated here so this package
// keeps its own vocabulary -- pkg/ci is above pkg/model in the import
// graph and reading a repo's rows is the caller's job, not this
// package's.
type DeclaredWorkflow struct {
	// Path is the workflow's path as it would appear in the repository,
	// ".grain/workflows/tests.yml". It is what Workflow.Path becomes,
	// which is what every error and every result row names the workflow
	// by, and what Layered composes the two sides by.
	Path string
	// Text is the workflow file, YAML.
	Text string
}

// DeclaredSource is Source over workflows grain declares itself, for
// the repositories it does not control (grain/task-245).
//
// The whole of the implementation is one lookup and the same parse an
// in-repo file gets: nothing about the format, the errors or the plan
// differs by which side a workflow came from, which is the property
// that makes running CI on somebody else's repository the same exercise
// as running it on grain's own.
//
// It resolves no commit -- what the deployment declares is what runs,
// whatever the branch is at. A Layered source pairs it with one that
// does resolve a commit, which is what a run needs to mean anything
// afterwards.
type DeclaredSource struct {
	// Declared returns the workflows this deployment declares for repo,
	// in any order. Required; a nil one declares nothing, which is
	// exactly what a deployment with no store behind it has to say.
	Declared func(ctx context.Context, repo string) ([]DeclaredWorkflow, error)
}

// Workflows implements Source.
func (s DeclaredSource) Workflows(ctx context.Context, repo, _ string) ([]Workflow, string, error) {
	if s.Declared == nil {
		return nil, "", nil
	}
	declared, err := s.Declared(ctx, repo)
	if err != nil {
		return nil, "", err
	}
	workflows, err := ParseDeclared(declared)
	return workflows, "", err
}

// ParseDeclared parses every workflow of one repository declared on
// grain's side, in path order -- LoadDir's counterpart for text that
// never was a file, and the same parse, so the same file is the same
// workflow either way.
//
// Exported for the reason LoadDir is: it is the whole of "what does
// this deployment declare for this repository", and the surface that
// edits those rows has to be able to ask it before it saves one. A
// workflow refused here is refused at the form, which is where somebody
// is still looking at what they typed, rather than at the run.
func ParseDeclared(declared []DeclaredWorkflow) ([]Workflow, error) {
	sorted := make([]DeclaredWorkflow, len(declared))
	copy(sorted, declared)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })

	var (
		workflows []Workflow
		firstErr  error
	)
	for _, d := range sorted {
		w, err := parseForSource(d.Path, []byte(d.Text))
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		workflows = append(workflows, w)
	}
	return workflows, firstErr
}

// Layered composes sources into one, in order, and is how a repository
// that is described from both sides gets one answer.
//
// The composition rule, in full: the union of what every source
// declares, keyed by Workflow.Path, and for a path more than one of
// them declares, **the last source wins**. Union, because a repository
// with an in-repo workflow and a configured one has two workflows and
// dropping either would be grain deciding which of them somebody meant.
// Last-wins, because the wiring puts DeclaredSource last (cmd/grain's
// buildCIService), and that is the ordering an operator needs: a
// workflow they cannot edit -- it is in a repository that is not theirs
// -- is one they can nonetheless replace, by declaring their own at the
// same path. The reverse ordering would leave them able to add
// workflows to that repository and never to correct one.
//
// What this composes is *what runs*, and nothing else. A workflow that
// arrives here from the repo object is operator config and one from the
// repository is not (docs/data-model.md, "The tree is operator config")
// -- but the package doc's "What a workflow file is trusted with"
// answer is "nothing" for both, so winning a path buys a workflow no
// reach it would not otherwise have had. Overriding is a way to change
// which commands run in a sandbox, never what that sandbox may touch.
//
// The commit is the first non-empty one any source resolves, in order,
// so pairing a source that reads a real branch with one that has no
// notion of a commit reports the branch's.
//
// A source that fails fails the whole composition rather than being
// skipped. Half a repository's workflows is a green run that tested
// less than somebody thinks it did, and a missing directory is already
// "no workflows, no error" (LoadDir) -- so an error here is a real one:
// a repository that could not be read, or a file that does not parse.
type Layered []Source

// Workflows implements Source.
func (l Layered) Workflows(ctx context.Context, repo, ref string) ([]Workflow, string, error) {
	workflows, commit, _, err := l.WorkflowsAndTree(ctx, repo, ref)
	return workflows, commit, err
}

// WorkflowsAndTree implements TreeSource.
//
// The tree comes from the same source the commit did -- the first that
// resolves one, in order -- rather than from the first source that happens
// to have a tree at all. A tree is only meaningful as the content *of* a
// commit, and pairing one source's files with another's commit would be a
// key that describes a tree nobody ran.
func (l Layered) WorkflowsAndTree(ctx context.Context, repo, ref string) ([]Workflow, string, *Tree, error) {
	var (
		order    []string
		byPath   = map[string]Workflow{}
		commit   string
		tree     *Tree
		firstErr error
	)
	for _, source := range l {
		if source == nil {
			continue
		}
		workflows, c, t, err := workflowsAndTree(ctx, source, repo, ref)
		if err != nil {
			if !isWorkflowDefinitionError(err) {
				return nil, "", nil, err
			}
			if firstErr == nil {
				firstErr = err
			}
		}
		if commit == "" && c != "" {
			commit, tree = c, t
		}
		for _, w := range workflows {
			if _, seen := byPath[w.Path]; !seen {
				order = append(order, w.Path)
			}
			byPath[w.Path] = w
		}
	}
	sort.Strings(order)

	var out []Workflow
	for _, p := range order {
		out = append(out, byPath[p])
	}
	return out, commit, tree, firstErr
}

// OnBranch implements BranchSource: the answer of the first source that
// can give one, which is the one that clones the branch.
func (l Layered) OnBranch(ctx context.Context, repo, ref, commit string) (bool, error) {
	for _, source := range l {
		if bs, ok := source.(BranchSource); ok && source != nil {
			return bs.OnBranch(ctx, repo, ref, commit)
		}
	}
	return false, nil
}

// Parse still reports unsupported jobs to direct callers. Sources keep the
// successfully parsed trigger metadata so only matching workflows fail a run.
func parseForSource(filePath string, data []byte) (Workflow, error) {
	w, err := Parse(filePath, data)
	if err != nil && w.Path != "" {
		w = w.WithPlanningError(markWorkflowDefinitionError(err))
		return w, nil
	}
	return w, markWorkflowDefinitionError(err)
}

// isWorkflowFile accepts both YAML extensions and ignores other files.
func isWorkflowFile(name string) bool {
	if strings.HasPrefix(name, ".") {
		return false
	}
	ext := strings.ToLower(path.Ext(name))
	return ext == ".yml" || ext == ".yaml"
}
