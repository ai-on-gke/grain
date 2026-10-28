package workflows

import (
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// This file is the YAML surface of the format and nothing else: the
// structs the decoder fills, and the conversion from each of them into
// the type the rest of the package works with. Keeping the two apart is
// what lets Workflow/Job/Step be shaped for their readers -- Jobs a
// sorted slice, Triggers a resolved struct, timeouts a duration -- while
// the file stays shaped like GitHub's, hyphens and all.
//
// The decoder runs with KnownFields set (Parse), so a key that is not a
// field here is an error naming the key. Every field therefore carries
// its own decision about the format: adding a `uses:` to rawStep would
// be the whole of "grain workflows now accept actions", and there is
// nowhere else it could be accepted from.

type rawWorkflow struct {
	Name string            `yaml:"name"`
	On   rawOn             `yaml:"on"`
	Env  map[string]string `yaml:"env"`
	Jobs map[string]rawJob `yaml:"jobs"`
}

type rawJob struct {
	Name           string            `yaml:"name"`
	Needs          rawStrings        `yaml:"needs"`
	RunsOn         string            `yaml:"runs-on"`
	Sandbox        *rawSandbox       `yaml:"sandbox"`
	Env            map[string]string `yaml:"env"`
	TimeoutMinutes int               `yaml:"timeout-minutes"`
	Cache          *bool             `yaml:"cache"`
	Artifacts      []rawArtifact     `yaml:"artifacts"`
	// GitHub's own two keys, said of a job rather than of a trigger --
	// see Job.Paths, and paths.go for what declaring them promises.
	// rawStrings so `paths: terraform/**` and a list both work, the same
	// as `needs`.
	Paths       rawStrings `yaml:"paths"`
	PathsIgnore rawStrings `yaml:"paths-ignore"`
	// The files that decide which sandbox guest this job resolves -- see
	// Job.GuestCache and guestcache.go. rawStrings for the same reason
	// `paths` is one: a single pattern and a list both work.
	GuestCache rawStrings `yaml:"guest-cache"`
	// What this job cannot do its work without -- see Job.Capabilities
	// and requirements.go. rawStrings for the reason `needs` is one: a
	// single name and a list both work.
	// A pointer so that `capabilities: []` -- present and empty, which
	// means "none" -- is told apart from the key being absent, which
	// means "whatever the deployment provides" (Job.NoCapabilities).
	Capabilities *rawCapabilities `yaml:"capabilities"`
	Secrets      rawStrings       `yaml:"secrets"`
	// What this job uses when it is there and runs without when it is
	// not -- see Job.OptionalCapabilities.
	OptionalCapabilities rawStrings `yaml:"optional-capabilities"`
	Retry                *bool      `yaml:"retry"`
	Retries              *int       `yaml:"retries"`
	RetryPolicy          string     `yaml:"retry-policy"`
	Steps                []rawStep  `yaml:"steps"`
}

// rawArtifact is one entry of a job's `artifacts:`, in
// actions/upload-artifact's own two keys: `name` and `path`, the latter
// one path or a multi-line list of them, exactly as that action takes
// it. What GitHub puts on the step (`if: failure()`, `retention-days`)
// has no key here -- see ArtifactSpec for why.
type rawArtifact struct {
	Name string     `yaml:"name"`
	Path rawStrings `yaml:"path"`
}

type rawSandbox struct {
	CPUs     int `yaml:"cpus"`
	MemoryMB int `yaml:"memory-mb"`
	DiskGB   int `yaml:"disk-gb"`
}

type rawStep struct {
	Name             string            `yaml:"name"`
	Run              string            `yaml:"run"`
	Env              map[string]string `yaml:"env"`
	WorkingDirectory string            `yaml:"working-directory"`
	ContinueOnError  bool              `yaml:"continue-on-error"`
	TimeoutMinutes   int               `yaml:"timeout-minutes"`
	// grain's own key, which GitHub has no word for: a step that fetches
	// the environment the job's other steps need rather than testing the
	// tree, so that a cache it misses costs time and not a verdict. See
	// Step.Infrastructure, which is the whole of what declaring it does.
	Infrastructure bool `yaml:"infrastructure"`
}

// GitHub's complete workflow schema belongs to act. Grain reads the fields
// required to schedule jobs independently and leaves their steps in the
// source file for act to interpret inside the sandbox.
type rawGitHubWorkflow struct {
	Name string                  `yaml:"name"`
	On   rawOn                   `yaml:"on"`
	Jobs map[string]rawGitHubJob `yaml:"jobs"`
}

type rawGitHubJob struct {
	Name           string     `yaml:"name"`
	Needs          rawStrings `yaml:"needs"`
	RunsOn         yaml.Node  `yaml:"runs-on"`
	TimeoutMinutes int        `yaml:"timeout-minutes"`
	// Cache is rawJob's own key, read here so that the two paths agree:
	// a job read from .github/workflows is cached like any other, and a
	// job whose purpose is an external side effect says `cache: false`
	// and is not -- the same sentence Job.Cache's doc comment writes.
	//
	// It is grain's key rather than GitHub's, and this decoder (unlike
	// parseGrain's) does not set KnownFields, so a file that never says
	// it parses exactly as it did. Note that a `.grain/workflows` file
	// reaches this parser too, whenever it says `runs-on:` (Parse), and
	// before this field existed its `cache:` was silently dropped.
	Cache       *bool       `yaml:"cache"`
	Retry       *bool       `yaml:"retry"`
	Retries     *int        `yaml:"retries"`
	RetryPolicy string      `yaml:"retry-policy"`
	Steps       []yaml.Node `yaml:"steps"`
}

func parseGitHub(filePath string, data []byte) (Workflow, error) {
	var raw rawGitHubWorkflow
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	if err := dec.Decode(&raw); err != nil {
		return Workflow{}, fmt.Errorf("ci: %s: %w", filePath, unwrapYAML(err))
	}
	var extra rawGitHubWorkflow
	if err := dec.Decode(&extra); err == nil {
		return Workflow{}, fmt.Errorf("ci: %s: a workflow file holds one workflow, and this one has a second YAML document in it", filePath)
	}
	triggers, err := raw.On.triggers(nativeWorkflowPath(filePath))
	if err != nil {
		return Workflow{}, fmt.Errorf("ci: %s: on: %w", filePath, err)
	}
	if len(raw.Jobs) == 0 {
		return Workflow{}, fmt.Errorf("ci: %s: a workflow declares at least one job", filePath)
	}
	// The document a second time, untyped: `needs` is read against the
	// job's whole node rather than the few keys rawGitHubJob decodes,
	// because what reads a needed job's outputs may be any key of it and
	// grain models almost none of them (actions_needs.go).
	doc, err := documentNode(data)
	if err != nil {
		return Workflow{}, fmt.Errorf("ci: %s: %w", filePath, err)
	}
	jobNodes := mappingValue(doc, "jobs")
	w := Workflow{Path: filePath, Name: strings.TrimSpace(raw.Name), On: triggers, actions: true}
	if w.Name == "" {
		w.Name = filePath
	}
	ids := make([]string, 0, len(raw.Jobs))
	for id := range raw.Jobs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		r := raw.Jobs[id]
		if !JobIDPattern.MatchString(id) {
			return Workflow{}, fmt.Errorf("ci: %s: jobs.%s: a job id starts with a letter or underscore and holds only letters, digits, underscores and hyphens", filePath, id)
		}
		if r.TimeoutMinutes < 0 {
			return Workflow{}, fmt.Errorf("ci: %s: jobs.%s: timeout-minutes: %d, which is not a length of time", filePath, id, r.TimeoutMinutes)
		}
		if err := checkActionsNeedsIsOrderingOnly(filePath, id, r.Needs, mappingValue(jobNodes, id)); err != nil {
			return w, err
		}
		if len(r.Steps) == 0 {
			return w, fmt.Errorf("ci: %s: jobs.%s: reusable-workflow jobs are not supported yet; a job must declare steps", filePath, id)
		}
		if r.RunsOn.Kind != yaml.ScalarNode || strings.TrimSpace(r.RunsOn.Value) != GitHubRunnerLabel {
			return w, fmt.Errorf("ci: %s: jobs.%s: runs-on must be %q in the first GitHub Actions compatibility release", filePath, id, GitHubRunnerLabel)
		}
		// A job that needs another is given its workflow with its own
		// `needs:` commented out, because act asked for one job runs that
		// job's dependencies too and grain has already run them
		// (actions_needs.go). A job that needs nothing is given the file
		// in the checkout, exactly as it was before `needs` was read at
		// all -- which is also why its fingerprint does not move.
		var workflowText string
		if len(r.Needs) > 0 {
			var err error
			if workflowText, err = pruneActionsNeeds(filePath, id, data); err != nil {
				return w, err
			}
		}
		job := Job{
			ID: id, Name: strings.TrimSpace(r.Name), Needs: r.Needs,
			TimeoutMinutes: r.TimeoutMinutes,
			// Cached by default, exactly as a native job is. What an
			// Actions job is made of is all in the key: the commit, the
			// environment grain hands the sandbox, and this Job itself --
			// whose one step is the act invocation, and whose Actions
			// field carries the workflow path and the event that
			// invocation is given (PlanRunIn fills the event in, and
			// json.Marshal writes both into the fingerprint). Editing the
			// workflow file changes the commit, and the commit is in the
			// key, so a changed ci.yml cannot be served a verdict from
			// before the edit.
			Cache:   true,
			Sandbox: SandboxSpec{CPUs: 4, MemoryMB: 16384, DiskGB: 100},
			Steps:   []Step{{Name: "GitHub Actions", Run: actionsJobScript(filePath, id, "", workflowText)}},
			Actions: &ActionsJob{WorkflowPath: filePath, WorkflowText: workflowText},
		}
		if r.Cache != nil {
			job.Cache = *r.Cache
		}
		if r.Retry != nil {
			job.Retry = *r.Retry
		}
		if r.Retries != nil {
			job.Retries = *r.Retries
			if *r.Retries > 0 {
				job.Retry = true
			}
		}
		if r.RetryPolicy != "" {
			job.RetryPolicy = r.RetryPolicy
			job.Retry = true
		}
		w.Jobs = append(w.Jobs, job)
	}
	// The same check a native workflow gets, and for the same reason: a
	// job needing one that is not there, or a cycle, is a file nobody can
	// run, and the person who can fix it is reading this error.
	if err := validateNeeds(w); err != nil {
		return w, fmt.Errorf("ci: %s: %w", filePath, err)
	}
	return w, nil
}

func (r rawJob) job(id string) (Job, error) {
	if !JobIDPattern.MatchString(id) {
		return Job{}, fmt.Errorf("a job id starts with a letter or underscore and holds only letters, digits, underscores and hyphens")
	}
	if r.RunsOn != "" && r.RunsOn != RunnerLabel {
		return Job{}, fmt.Errorf("runs-on: %q, but grain has one runner to offer and it is called %q "+
			"(a workflow copied from .github/workflows fails here rather than running its steps somewhere they were not written for)",
			r.RunsOn, RunnerLabel)
	}
	if r.TimeoutMinutes < 0 {
		return Job{}, fmt.Errorf("timeout-minutes: %d, which is not a length of time", r.TimeoutMinutes)
	}
	if len(r.Steps) == 0 {
		return Job{}, fmt.Errorf("a job declares at least one step")
	}
	job := Job{
		ID:             id,
		Name:           strings.TrimSpace(r.Name),
		Needs:          r.Needs,
		Env:            r.Env,
		TimeoutMinutes: r.TimeoutMinutes,
		Cache:          true,
	}
	if r.Cache != nil {
		job.Cache = *r.Cache
	}
	if r.Retry != nil {
		job.Retry = *r.Retry
	}
	if r.Retries != nil {
		job.Retries = *r.Retries
		if *r.Retries > 0 {
			job.Retry = true
		}
	}
	if r.RetryPolicy != "" {
		job.RetryPolicy = r.RetryPolicy
		job.Retry = true
	}
	if r.Sandbox != nil {
		if r.Sandbox.CPUs < 0 || r.Sandbox.MemoryMB < 0 || r.Sandbox.DiskGB < 0 {
			return Job{}, fmt.Errorf("sandbox: a negative size is not a size")
		}
		job.Sandbox = SandboxSpec{CPUs: r.Sandbox.CPUs, MemoryMB: r.Sandbox.MemoryMB, DiskGB: r.Sandbox.DiskGB}
	}
	// GitHub's own rule, and for GitHub's own reason: the two keys are
	// two ways of saying the same thing, and a job that says both has
	// said something whose meaning depends on which one the reader
	// believes. Either takes `!` (see paths.go), which is how a job says
	// "these, except those" without needing both.
	if len(r.Paths) > 0 && len(r.PathsIgnore) > 0 {
		return Job{}, fmt.Errorf("paths and paths-ignore: a job declares one or the other, not both -- `paths-ignore:` is `paths: [\"**\", \"!...\"]` said the short way, and a `!` pattern in either is how one list says both halves")
	}
	for i, pattern := range r.Paths {
		if err := validPathFilter(pattern); err != nil {
			return Job{}, fmt.Errorf("paths[%d]: %w", i, err)
		}
		job.Paths = append(job.Paths, strings.TrimSpace(pattern))
	}
	for i, pattern := range r.PathsIgnore {
		if err := validPathFilter(pattern); err != nil {
			return Job{}, fmt.Errorf("paths-ignore[%d]: %w", i, err)
		}
		job.PathsIgnore = append(job.PathsIgnore, strings.TrimSpace(pattern))
	}
	// The same filters `paths:` takes, checked the same way: a pattern
	// that could match nothing is refused here rather than quietly
	// becoming a job grain warms no cache for (guestcache.go's "What
	// happens when the declaration is wrong").
	for i, pattern := range r.GuestCache {
		if err := validPathFilter(pattern); err != nil {
			return Job{}, fmt.Errorf("guest-cache[%d]: %w", i, err)
		}
		job.GuestCache = append(job.GuestCache, strings.TrimSpace(pattern))
	}
	// The two requirement keys. Only the shape of a name is checked
	// here: whether this deployment provides it is not a property of the
	// file and is answered where the job would start, by a job that does
	// not run and says which name it wanted (requirements.go).
	var capabilities rawCapabilities
	if r.Capabilities != nil {
		capabilities = *r.Capabilities
		// Present and empty: this job has said it needs nothing the
		// deployment provides, which is not the same as saying nothing.
		job.NoCapabilities = len(capabilities) == 0
	}
	for i, id := range capabilities {
		if err := validateRequirementName("capabilities", id); err != nil {
			return Job{}, fmt.Errorf("capabilities[%d]: %w", i, err)
		}
		if IsCapabilityRef(id) {
			if err := Capabilities.CheckGrant(id); err != nil {
				return Job{}, fmt.Errorf("capabilities[%d]: %w", i, err)
			}
		}
		job.Capabilities = append(job.Capabilities, id)
	}
	for i, name := range r.Secrets {
		if err := validateRequirementName("secrets", name); err != nil {
			return Job{}, fmt.Errorf("secrets[%d]: %w", i, err)
		}
		if err := ValidateReleaseSecretName(name); err != nil {
			return Job{}, fmt.Errorf("secrets[%d]: %w", i, err)
		}
		job.Secrets = append(job.Secrets, name)
	}
	// Optional capabilities get the same shape check and two more. A
	// name cannot be both needed and not needed, so one in both lists is
	// a mistake worth saying rather than resolving either way. And
	// repository access is met by declaring it (requirements.go), so an
	// optional `repo-read:`/`repo-write:` would be a requirement wearing
	// the wrong key.
	for i, id := range r.OptionalCapabilities {
		if err := validateRequirementName("optional-capabilities", id); err != nil {
			return Job{}, fmt.Errorf("optional-capabilities[%d]: %w", i, err)
		}
		if slices.Contains(job.Capabilities, id) {
			return Job{}, fmt.Errorf("optional-capabilities[%d]: %q is also under capabilities -- a job either requires a capability or runs without it, so name it in one list", i, id)
		}
		if Capabilities.IsRepoAccess(id) {
			return Job{}, fmt.Errorf("optional-capabilities[%d]: %q is repository access, which declaring it is what grants -- name it under capabilities", i, id)
		}
		job.OptionalCapabilities = append(job.OptionalCapabilities, id)
	}
	seen := make(map[string]bool, len(r.Artifacts))
	for i, ra := range r.Artifacts {
		spec, err := ra.spec()
		if err != nil {
			return Job{}, fmt.Errorf("artifacts[%d]: %w", i, err)
		}
		if seen[spec.Name] {
			return Job{}, fmt.Errorf("artifacts[%d]: two artifacts called %q -- a name is what one is downloaded by, so they have to differ", i, spec.Name)
		}
		seen[spec.Name] = true
		job.Artifacts = append(job.Artifacts, spec)
	}
	for i, rs := range r.Steps {
		if strings.TrimSpace(rs.Run) == "" {
			return Job{}, fmt.Errorf("steps[%d]: a step is a `run:` script (grain workflows have no `uses:` -- see the package documentation for what a job's sandbox arrives holding)", i)
		}
		if rs.TimeoutMinutes < 0 {
			return Job{}, fmt.Errorf("steps[%d]: timeout-minutes: %d, which is not a length of time", i, rs.TimeoutMinutes)
		}
		job.Steps = append(job.Steps, Step{
			Name:             strings.TrimSpace(rs.Name),
			Run:              rs.Run,
			Env:              rs.Env,
			WorkingDirectory: strings.TrimSpace(rs.WorkingDirectory),
			ContinueOnError:  rs.ContinueOnError,
			TimeoutMinutes:   rs.TimeoutMinutes,
			Infrastructure:   rs.Infrastructure,
		})
	}
	return job, nil
}

// spec turns one `artifacts:` entry into the declaration the rest of the
// package works with, refusing everything this can see from the file
// alone.
//
// The paths are checked hard, because they are the one part of a
// workflow file that becomes a path on the machine the collector runs
// in: absolute paths and `..` are refused outright so that "collect
// these files" can never mean "collect /etc", and a quote is refused
// because the collector puts these into a shell script.
func (r rawArtifact) spec() (ArtifactSpec, error) {
	name := strings.TrimSpace(r.Name)
	if name == "" {
		return ArtifactSpec{}, fmt.Errorf("an artifact needs a `name:` -- it is what the thing is downloaded by")
	}
	if !ArtifactNamePattern.MatchString(name) {
		return ArtifactSpec{}, fmt.Errorf("name: %q -- an artifact name starts with a letter, a digit or an underscore and holds only those and hyphens (it becomes a filename and a URL)", name)
	}
	spec := ArtifactSpec{Name: name}
	// `path: |` with several lines under it is one scalar holding
	// newlines, which is how actions/upload-artifact's own multi-path
	// form is written; `path: [a, b]` is a list. Both end up here as the
	// same slice of paths.
	for _, entry := range r.Path {
		for _, line := range strings.Split(entry, "\n") {
			path := strings.TrimSpace(line)
			if path == "" {
				continue
			}
			if err := validArtifactPath(path); err != nil {
				return ArtifactSpec{}, fmt.Errorf("path: %w", err)
			}
			spec.Paths = append(spec.Paths, path)
		}
	}
	if len(spec.Paths) == 0 {
		return ArtifactSpec{}, fmt.Errorf("artifact %q needs a `path:` -- one path, or several on their own lines", name)
	}
	return spec, nil
}

// validArtifactPath is what a collected path may be: somewhere inside
// the workspace, named literally.
//
// No globbing, unlike actions/upload-artifact. A glob would have to be
// expanded by the sandbox's own shell, which is the one place this
// package must not let a workflow file improvise, and every path grain's
// own workflows have ever wanted to keep is a directory with a name.
func validArtifactPath(path string) error {
	switch {
	case strings.HasPrefix(path, "/"), strings.HasPrefix(path, "~"):
		return fmt.Errorf("%q is outside the workspace -- an artifact path is relative to the checkout", path)
	case path == "..", strings.HasPrefix(path, "../"), strings.Contains(path, "/../"), strings.HasSuffix(path, "/.."):
		return fmt.Errorf("%q climbs out of the workspace", path)
	case strings.ContainsAny(path, "'\"`$*?[]\\\n"):
		return fmt.Errorf("%q holds a character an artifact path may not (a path is taken literally: there is no globbing and no shell expansion here)", path)
	}
	return nil
}

// rawStrings is a YAML field that may be written as one scalar or as a
// list of them -- `needs: build` and `needs: [build, lint]` are both
// legal on GitHub, and both are written in the wild.
type rawStrings []string

func (s *rawStrings) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		var one string
		if err := node.Decode(&one); err != nil {
			return err
		}
		*s = []string{one}
		return nil
	case yaml.SequenceNode:
		var many []string
		if err := node.Decode(&many); err != nil {
			return err
		}
		*s = many
		return nil
	default:
		return fmt.Errorf("expected a name or a list of names")
	}
}

// rawCapabilities is a job's capabilities: a name, or a list of names,
// capability references and {capability, with} grant objects
// (docs/capabilities-spec.md §2.2.1), each read as its grant string.
type rawCapabilities []string

func (s *rawCapabilities) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		var one string
		if err := node.Decode(&one); err != nil {
			return err
		}
		*s = []string{one}
		return nil
	}
	if node.Kind != yaml.SequenceNode {
		return fmt.Errorf("expected a name or a list of capabilities")
	}
	list, err := Capabilities.DecodeList(node)
	if err != nil {
		return err
	}
	*s = rawCapabilities(list)
	return nil
}

type rawScheduleEntry struct {
	Cron     string `yaml:"cron"`
	TimeZone string `yaml:"timezone"`
	Review   string `yaml:"review"`
}

// rawOn is the `on:` block in all three shapes GitHub accepts it in: a
// scalar (`on: push`), a list (`on: [push, workflow_dispatch]`) and a
// mapping (`on: {push: {branches: [main]}}`). The first two carry no
// branch filter, which means every branch.
type rawOn struct {
	events map[Event]*BranchTrigger
	// schedules are the `on: schedule` entries as written, kept rather than
	// dropped (grain/task-344). They are translated into cadences by
	// triggers(), not here: whether a cron this deployment cannot express
	// is an error depends on which directory the file came from, and an
	// UnmarshalYAML does not know.
	schedules []rawScheduleEntry
	// present distinguishes "this file has no `on:` at all" from "this
	// file has one that matches nothing", so triggers() can say which.
	present bool
}

func (o *rawOn) UnmarshalYAML(node *yaml.Node) error {
	o.present = true
	o.events = map[Event]*BranchTrigger{}
	switch node.Kind {
	case yaml.ScalarNode:
		var name string
		if err := node.Decode(&name); err != nil {
			return err
		}
		return o.add(name, nil)
	case yaml.SequenceNode:
		var names []string
		if err := node.Decode(&names); err != nil {
			return err
		}
		for _, name := range names {
			if err := o.add(name, nil); err != nil {
				return err
			}
		}
		return nil
	case yaml.MappingNode:
		if len(node.Content)%2 != 0 {
			return fmt.Errorf("expected a mapping of event names")
		}
		for i := 0; i < len(node.Content); i += 2 {
			var name string
			if err := node.Content[i].Decode(&name); err != nil {
				return err
			}
			value := node.Content[i+1]
			// Cron entries and dispatch inputs are not branch filters.
			switch name {
			case "workflow_run":
				// workflow_run remains GitHub's responsibility.
				o.events[Event("workflow_run")] = nil
				continue
			case "schedule":
				var entries []rawScheduleEntry
				if value.Kind != yaml.SequenceNode {
					return fmt.Errorf("schedule: expected a list of cron entries")
				}
				if err := value.Decode(&entries); err != nil {
					return fmt.Errorf("schedule: %w", err)
				}
				if len(entries) == 0 {
					return fmt.Errorf("schedule: expected at least one cron entry")
				}
				for _, entry := range entries {
					cron := strings.TrimSpace(entry.Cron)
					if cron == "" {
						return fmt.Errorf("schedule: an entry must contain cron")
					}
					tz := strings.TrimSpace(entry.TimeZone)
					if tz != "" && !ValidTimeZone(tz) {
						return fmt.Errorf("schedule: invalid timezone %q", tz)
					}
					review := ReviewPolicy(strings.TrimSpace(entry.Review))
					if err := review.Validate(); err != nil {
						return fmt.Errorf("schedule: %w", err)
					}
					o.schedules = append(o.schedules, rawScheduleEntry{Cron: cron, TimeZone: tz,
						Review: string(review)})
				}
				continue
			case string(EventManual):
				if value.Kind == yaml.MappingNode {
					for k := 0; k < len(value.Content); k += 2 {
						if value.Content[k].Value != "inputs" {
							return fmt.Errorf("workflow_dispatch: unexpected field %q", value.Content[k].Value)
						}
						if value.Content[k+1].Kind != yaml.MappingNode {
							return fmt.Errorf("workflow_dispatch.inputs: expected a mapping")
						}
					}
					if err := o.add(name, nil); err != nil {
						return err
					}
					continue
				}
			}
			// `on: {push:}` -- an event named with nothing under it --
			// is every branch, exactly as on GitHub.
			if value.Kind == yaml.ScalarNode && value.Tag == "!!null" {
				if err := o.add(name, nil); err != nil {
					return err
				}
				continue
			}
			var filter rawBranchTrigger
			if err := value.Decode(&filter); err != nil {
				return fmt.Errorf("%s: %w", name, unwrapYAML(err))
			}
			if err := o.add(name, &BranchTrigger{Branches: filter.Branches, BranchesIgnore: filter.BranchesIgnore}); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("expected an event name, a list of them, or a mapping")
	}
}

// rawBranchTrigger is what may sit under `push:` and `pull_request:`.
// GitHub has `paths`, `paths-ignore`, `tags`, `types` and more here;
// none of them are implemented, so all of them are refused by name
// rather than accepted and ignored.
//
// It walks the mapping itself rather than being decoded field by field,
// because KnownFields is a property of a *decoder* and this node is
// reached through a node.Decode, which has none: a struct with yaml
// tags would silently swallow `paths:` here however strictly the
// document as a whole was decoded.
type rawBranchTrigger struct {
	Branches       rawStrings
	BranchesIgnore rawStrings
}

func (r *rawBranchTrigger) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode || len(node.Content)%2 != 0 {
		return fmt.Errorf("expected `branches` and/or `branches-ignore`")
	}
	for i := 0; i < len(node.Content); i += 2 {
		var key string
		if err := node.Content[i].Decode(&key); err != nil {
			return err
		}
		switch key {
		case "branches":
			if err := node.Content[i+1].Decode(&r.Branches); err != nil {
				return err
			}
		case "branches-ignore":
			if err := node.Content[i+1].Decode(&r.BranchesIgnore); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%q is not a filter grain applies -- an event takes `branches` and `branches-ignore`", key)
		}
	}
	return nil
}

func (o *rawOn) add(name string, filter *BranchTrigger) error {
	event := Event(strings.TrimSpace(name))
	switch event {
	case EventPush, EventPullRequest, EventRelease:
		if filter == nil {
			filter = &BranchTrigger{}
		}
		o.events[event] = filter
	case EventManual:
		if filter != nil && (len(filter.Branches) > 0 || len(filter.BranchesIgnore) > 0) {
			return fmt.Errorf("%s: takes no branch filter -- a dispatch names its own branch", event)
		}
		o.events[event] = &BranchTrigger{}
	default:
		return fmt.Errorf("%q is not an event grain runs workflows for (%s, %s, %s, %s, %s)",
			name, EventPush, EventPullRequest, EventRelease, EventManual, EventSchedule)
	}
	return nil
}

// triggers resolves the `on:` block. native says whether this file is one
// of grain's own (WorkflowDir) rather than a GitHub Actions file, which is
// the whole of what decides what happens to a cron:
//
//   - a native workflow's cron becomes a task schedule of this
//     deployment's (cron.go), so one that cannot be expressed as a cadence
//     is a parse error here rather than a job that silently never runs;
//   - a `.github/workflows` cron is GitHub's own scheduler's business and
//     is read and dropped, exactly as it was before any of this existed.
//     Translating it as well would run every nightly twice on a repository
//     whose Actions still work, and grain does not own the cadence of a
//     file it merely borrows.
func (o rawOn) triggers(native bool) (Triggers, error) {
	var t Triggers
	if !o.present {
		return t, fmt.Errorf("a workflow says what runs it -- there is no `on:` here, so nothing would ever run this file")
	}
	t.Push = o.events[EventPush]
	t.PullRequest = o.events[EventPullRequest]
	t.Release = o.events[EventRelease]
	_, t.Manual = o.events[EventManual]
	if native {
		for _, entry := range o.schedules {
			recurrence, err := ParseCron(entry.Cron)
			if err != nil {
				return t, err
			}
			recurrence.TimeZone = entry.TimeZone
			t.Schedule = append(t.Schedule, ScheduleTrigger{
				Cron:       entry.Cron,
				TimeZone:   entry.TimeZone,
				Recurrence: recurrence,
				Review:     ReviewPolicy(entry.Review),
			})
		}
	}
	if !t.Any() && len(o.events) == 0 && len(o.schedules) == 0 {
		return t, fmt.Errorf("names no event, so nothing would ever run this file")
	}
	return t, nil
}

// --- Saying what an unknown key is, rather than naming a Go type ---
//
// KnownFields is what makes a key the format does not define an error
// (Parse), and yaml.v3's own wording for one is "line 203: field
// infrastructure not found in type ci.rawStep". Two things are wrong
// with that sentence in front of the person who has to act on it. It
// names an unexported Go struct, which is not something a workflow
// author has or wants; and it says nothing about what the file *may*
// say instead, so the next move is reading this package rather than
// fixing the file.
//
// It also understates the damage. Every workflow file a repository
// declares is parsed before any job is selected, so one unknown key
// stops every job in the repository -- not the job it is in, and not
// even only the workflow it is in. That happened here on 21 Sep 2026:
// `infrastructure:` (606cdb96c) reached .grain/workflows before the
// daemon reading it had the field, and grain CI errored in zero seconds
// on main and on every branch for three hours, which is the whole of
// this deployment's gate. The key was a valid one written against a
// newer grain than the build parsing it -- the same trap `artifacts:`
// and `paths-ignore:` each fell into -- and nothing in the message said
// so, so three tasks decoded "type ci.rawStep" separately.
//
// So the strictness stays, because it is what catches a typo in a
// workflow, and the message carries what the reader needs: the format's
// own noun for the thing, the keys it does take, and the fact that a
// key this build does not know may simply be one a later grain adds.
// The key list is read off the structs above with reflection rather
// than written out, so a field added there is in the message the same
// day.

// unknownFieldLine matches yaml.v3's own wording for a key that has no
// field on the struct being decoded. Anything else in a TypeError (a
// value of the wrong shape, most often) is left exactly as the library
// wrote it.
var unknownFieldLine = regexp.MustCompile(`^line (\d+): field (\S+) not found in type (\S+)$`)

// rawTypeNouns is what to call each decoded struct in an error, in the
// format's own words rather than Go's. A type missing from here is
// reported in yaml.v3's wording unchanged, which is the right failure:
// a wrong noun would be worse than no noun.
var rawTypeNouns = map[string]struct {
	noun string
	typ  reflect.Type
}{
	"workflows.rawWorkflow": {"a workflow", reflect.TypeOf(rawWorkflow{})},
	"workflows.rawJob":      {"a job", reflect.TypeOf(rawJob{})},
	"workflows.rawStep":     {"a step", reflect.TypeOf(rawStep{})},
	"workflows.rawSandbox":  {"a job's `sandbox:`", reflect.TypeOf(rawSandbox{})},
	"workflows.rawArtifact": {"an `artifacts:` entry", reflect.TypeOf(rawArtifact{})},
}

// unknownFieldNote is said once per error however many unknown keys it
// holds: it is about the format and about the blast radius, neither of
// which is per-key.
const unknownFieldNote = "an unrecognised key is a typo, or a key a newer grain than this build understands; either way no job in this repository runs until it is gone, because every workflow file is parsed before any job is selected"

// explainUnknownFields rewrites the "field X not found in type Y" lines
// of a yaml.v3 TypeError into the format's own vocabulary, and returns
// whether it rewrote any -- which is what decides whether the note
// above is worth appending.
func explainUnknownFields(lines []string) ([]string, bool) {
	out := make([]string, 0, len(lines))
	rewrote := false
	for _, line := range lines {
		m := unknownFieldLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			out = append(out, line)
			continue
		}
		known, ok := rawTypeNouns[m[3]]
		if !ok {
			out = append(out, line)
			continue
		}
		rewrote = true
		out = append(out, fmt.Sprintf("line %s: %s has no %q field -- %s takes: %s",
			m[1], known.noun, m[2], known.noun, strings.Join(yamlFieldNames(known.typ), ", ")))
	}
	return out, rewrote
}

// yamlFieldNames is the keys a struct decodes, in the order the decoder
// would accept them alphabetically, read off the `yaml:` tags so the
// list cannot drift from the struct it describes.
func yamlFieldNames(t reflect.Type) []string {
	var names []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			// yaml.v3's own default for an untagged field.
			name = strings.ToLower(f.Name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
