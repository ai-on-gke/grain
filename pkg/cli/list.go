package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
)

// `grain list` asks the same question the UI's own task views do
// (NarrowingBar.jsx): which of the deployment's tasks, in what order.
// The vocabulary is deliberately the same on both sides -- the same
// attributes under the same ids, the same comparisons, the same words
// for the ways a task can have been filed -- so a narrowing means one
// thing whether it is built out of menus or typed after -filter, and a
// condition out of a link
// (?f=repo:any:acme/widgets|acme/gadgets) is pasteable here unaltered.
//
// The attributes are taskattributes.go and the comparisons over them
// taskfilters.go, both of which say what they mirror and why the
// mirroring is a test rather than an endpoint. What is left here is one
// invocation: the flags, what they narrow to, and printing what
// survives.
//
// It narrows in the client rather than on the server because GET
// /api/tasks answers with the whole task list either way, and the UI
// narrows that same answer in the browser. A query parameter would move
// this loop across the wire without removing it from the frontend, and
// would leave two places to look for what a word like "origin" covers.

// taskQuery is one invocation's worth of narrowing and ordering.
//
// `now` and `zone` are the two things the date comparisons need and
// cannot be told by a task: the moment "within the last 7 days" counts
// back from, and the wall clock a bare day is read in. cmdList fills the
// zone in from the deployment's own setting, so `created:after:
// 2026-09-01` covers the same tasks here as it does in the browser,
// where every timestamp on screen is already printed in that zone.
type taskQuery struct {
	search  string
	filters []condition
	sort    []sortSpec

	// hidden is the states this listing leaves out before any condition
	// is consulted -- closed and deferred, unless something asked for
	// them (hiddenStates). It is a set of model.State values as strings,
	// nil when nothing is being hidden.
	hidden map[string]bool

	now  time.Time
	zone *time.Location
}

func (q taskQuery) match(t ui.Task) bool {
	if q.hidden[string(t.State)] {
		return false
	}
	now, zone := q.now, q.zone
	if now.IsZero() {
		now = time.Now()
	}
	if zone == nil {
		zone = time.UTC
	}
	for _, c := range q.filters {
		if !c.match(t, now, zone) {
			return false
		}
	}
	if q.search == "" {
		return true
	}
	// Title or id, the two things the UI's own search box looks at, so
	// the same words find the same tasks in both places.
	needle := strings.ToLower(q.search)
	return strings.Contains(strings.ToLower(t.Title), needle) || strings.Contains(strings.ToLower(t.ID), needle)
}

// apply is the whole of what a narrowing does to a listing: keep the
// tasks every condition holds for, then order them. It returns a new
// slice rather than sorting the caller's.
func (q taskQuery) apply(tasks []ui.Task) []ui.Task {
	kept := make([]ui.Task, 0, len(tasks))
	for _, t := range tasks {
		if q.match(t) {
			kept = append(kept, t)
		}
	}
	// The same zone the conditions are answered in, since an order over
	// a date attribute reads a value exactly as a filter over it does.
	zone := q.zone
	if zone == nil {
		zone = time.UTC
	}
	sortTasks(kept, q.sort, zone)
	return kept
}

// hideable is the states a listing leaves out unless it is asked for
// them, mirroring ui/src/state.js's SET_ASIDE_STATES -- a list is about
// what is in play, and neither a task somebody closed nor one they
// deliberately put aside is. On a deployment with a year of history
// behind it that is most of the rows, which is exactly what the UI has
// never shown and what this command printed anyway.
//
// flag names the switch that turns each back on, and setting is the
// deployment-wide default behind it (ui.Settings' own field, the same
// one the frontend's own default condition reads) -- nil for deferred,
// which has no such setting there either.
//
// The frontend says the same thing as a filter condition now rather than
// as a rule of its own (ui/src/taskFilters.js's defaultFilters, which a
// reader edits like any other condition); here it is still flags and
// this list, since a listing has no standing toolbar to carry a default
// condition in and `-state closed` is already the way to ask.
var hideable = []struct {
	state   string
	flag    string
	setting func(ui.Settings) bool
}{
	{state: string(model.StateDeferred), flag: "deferred"},
	{state: string(model.StateClosed), flag: "closed",
		setting: func(s ui.Settings) bool { return s.ShowClosedByDefault }},
}

// asksFor is whether this narrowing names a state outright -- `-state
// closed`, or the condition it stands for. Asking for a state is asking
// to see it, which is the frontend's own rule (App.jsx's
// selectStateFilter: picking "Closed" in the rail drops the condition
// that was leaving closed tasks out, rather than showing an empty list
// where the two disagree). Only "any" counts: state:none:closed is a
// listing with
// closed tasks taken out of it, not one about them.
func (q taskQuery) asksFor(state string) bool {
	for _, c := range q.filters {
		if c.attr.id == "state" && c.op.id == "any" && contains(c.values, state) {
			return true
		}
	}
	return false
}

// needsSettings is whether deciding what to hide needs the deployment's
// own answer -- so a listing that has already been told, by a flag or by
// the states it asks about, costs no extra round trip.
func (q taskQuery) needsSettings(show map[string]*bool) bool {
	for _, h := range hideable {
		if h.setting == nil || q.asksFor(h.state) || show[h.flag] != nil {
			continue
		}
		return true
	}
	return false
}

// hiddenStates is what q.hidden ends up holding: every hideable state
// that nothing has asked for. A -closed/-deferred flag wins over the
// deployment's default, in either direction, the way editing the
// frontend's own default condition wins over ui.Settings once somebody
// touches it.
func hiddenStates(q taskQuery, show map[string]*bool, settings ui.Settings) map[string]bool {
	hidden := map[string]bool{}
	for _, h := range hideable {
		switch {
		case q.asksFor(h.state):
		case show[h.flag] != nil:
			if !*show[h.flag] {
				hidden[h.state] = true
			}
		case h.setting != nil && h.setting(settings):
		default:
			hidden[h.state] = true
		}
	}
	if len(hidden) == 0 {
		return nil
	}
	return hidden
}

// hiddenNote is the line a listing owes its reader when it left rows out:
// "there is nothing here" and "there is nothing here that you asked to
// see" are different answers, and only the second one has something to
// do about it. Empty when nothing was hidden, or when nothing hidden
// existed -- a note about no tasks helps nobody.
func hiddenNote(hidden map[string]bool, tasks []ui.Task) string {
	counts := map[string]int{}
	for _, t := range tasks {
		if hidden[string(t.State)] {
			counts[string(t.State)]++
		}
	}
	var parts, flags []string
	for _, h := range hideable {
		if counts[h.state] == 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%d %s", counts[h.state], h.state))
		flags = append(flags, "-"+h.flag)
	}
	if len(parts) == 0 {
		return ""
	}
	return fmt.Sprintf("%s not shown -- pass %s to include them",
		strings.Join(parts, " and "), strings.Join(flags, " and "))
}

// needsZone is whether anything in this narrowing is answered against a
// wall clock -- which is what decides whether cmdList asks the
// deployment which one it keeps.
//
// Two things put it there. A bare day written in the condition, which
// begins when the deployment's clock says it does (an RFC3339 value
// names an instant on its own, and "within the last 7 days" counts back
// from a moment). And any use at all of a custom date attribute, filter
// or sort: what such a field holds is a calendar day
// (model.CustomFieldDateLayout), so reading a *task's* value needs the
// zone even when the condition names an instant.
func (q taskQuery) needsZone() bool {
	for _, c := range q.filters {
		if c.attr.typ != typeDate {
			continue
		}
		if c.attr.custom {
			return true
		}
		if c.op.id != "after" && c.op.id != "before" {
			continue
		}
		if _, err := time.Parse("2006-01-02", c.values[0]); err == nil {
			return true
		}
	}
	for _, s := range q.sort {
		if s.attr.typ == typeDate && s.attr.custom {
			return true
		}
	}
	return false
}

func contains(haystack []string, needle string) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}

// conditionList is the repeatable -filter flag: the text of one
// condition per occurrence, kept as it was typed and parsed later.
//
// Later, because what an attribute name means depends on the
// deployment: custom.priority is a real attribute where the field is
// declared and a typo where it is not (taskattributes.go), and nothing
// can say which without asking. So the flag collects, listFlags.query
// parses against the vocabulary cmdList has resolved by then, and an
// error still quotes the text that was actually typed.
type conditionList []string

func (l *conditionList) String() string {
	if l == nil {
		return ""
	}
	return strings.Join(*l, " ")
}

func (l *conditionList) Set(raw string) error {
	*l = append(*l, raw)
	return nil
}

// listFlags is what the command line said, before any of it has been
// turned into conditions. The shorthands are the flags this command had
// when the UI's toolbar was one menu per attribute, kept because they
// are shorter than the conditions they stand for and because scripts
// use them: -repo acme/widgets is repo:any:acme/widgets, and nothing
// about it has changed.
//
// The three tri-state fields are pointers because "unset" and "false"
// are different questions: -auto-merge asks for the tasks that merge
// themselves, -auto-merge=false for the ones that need a human, and
// leaving it off asks nothing at all. cmdList fills them in from
// FlagSet.Visit, which is the only thing that can tell a flag left at
// its default from one explicitly set to it.
type listFlags struct {
	state      string
	repo       string
	base       string
	capability string
	author     string
	origin     string
	search     string

	blocked     *bool
	autoMerge   *bool
	interactive *bool

	filters conditionList
	sortBy  string
}

// query turns one command line into the narrowing it describes, or into
// the error that says which word nobody can act on. Everything is
// checked here, before the request: a misspelt -state is answered
// straight away rather than after a round trip that was never going to
// be printed.
//
// The shorthands come first and the -filter conditions after, in the
// order they were written. Order is not something a reader can see --
// every condition has to hold either way -- but it is what the errors
// and `-json` echo back, and putting the flags somebody typed in the
// order they typed them is the least surprising of the choices.
func (f listFlags) query(attrs attributeSet) (taskQuery, error) {
	q := taskQuery{search: f.search, now: time.Now(), zone: time.UTC}
	shorthands := []struct {
		flag  string
		attr  string
		value string
	}{
		{flag: "state", attr: "state", value: f.state},
		{flag: "repo", attr: "repo", value: f.repo},
		{flag: "base", attr: "base", value: f.base},
		{flag: "capability", attr: "capability", value: f.capability},
		{flag: "author", attr: "author", value: f.author},
		{flag: "origin", attr: "origin", value: f.origin},
		{flag: "blocked", attr: "blocked", value: pickValue(f.blocked, "yes", "no")},
		{flag: "auto-merge", attr: "autoMerge", value: pickValue(f.autoMerge, "on", "off")},
		{flag: "interactive", attr: "kind", value: pickValue(f.interactive, "interactive", "background")},
	}
	for _, s := range shorthands {
		if s.value == "" {
			continue
		}
		cond, err := attrs.newCondition(s.attr, "any", s.value)
		if err != nil {
			return taskQuery{}, fmt.Errorf("-%s: %w", s.flag, err)
		}
		q.filters = append(q.filters, cond)
	}
	for _, raw := range f.filters {
		cond, err := attrs.parseCondition(raw)
		if err != nil {
			return taskQuery{}, err
		}
		q.filters = append(q.filters, cond)
	}
	specs, err := attrs.parseSort(f.sortBy)
	if err != nil {
		return taskQuery{}, err
	}
	q.sort = specs
	return q, nil
}

// namesAnUndeclaredAttribute is whether this command line asks about
// something the built-in vocabulary does not have -- which is exactly
// when the deployment's own declarations have to be fetched before
// anything can be parsed, and only then: the ordinary listing costs no
// extra round trip.
//
// A name nothing declares either is answered the same way once they
// arrive: an error naming the attributes that do exist, this
// deployment's own among them, which is a better answer than the
// built-in list alone could give.
func (f listFlags) namesAnUndeclaredAttribute() bool {
	for _, raw := range f.filters {
		name, _, _ := strings.Cut(raw, ":")
		if name != "" && builtinAttributes.byID(name) == nil {
			return true
		}
	}
	if _, named := legacySorts[f.sortBy]; named || f.sortBy == "" {
		return false
	}
	for _, key := range strings.Split(f.sortBy, ",") {
		name, _, _ := strings.Cut(strings.TrimSpace(key), ":")
		if name != "" && builtinAttributes.byID(name) == nil {
			return true
		}
	}
	return false
}

// pickValue is a tri-state flag as the value of the attribute it asks
// about: nothing at all when it was never set.
func pickValue(set *bool, yes, no string) string {
	switch {
	case set == nil:
		return ""
	case *set:
		return yes
	default:
		return no
	}
}

func cmdList(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain list", flag.ContinueOnError)
	var f listFlags
	fs.Var(&f.filters, "filter", "only tasks matching this `condition`, written attribute:comparison:value -- repeatable, and every one of them has to hold")
	fs.StringVar(&f.sortBy, "sort", "", "print in this `order`: attribute:asc or attribute:desc keys, comma-separated, the first deciding and the rest breaking its ties")
	fs.StringVar(&f.search, "search", "", "only tasks whose title or id contains this text, case-insensitively")
	fs.StringVar(&f.state, "state", "", "shorthand for -filter state:any:VALUE ("+strings.Join(stateNames(), ", ")+")")
	fs.StringVar(&f.repo, "repo", "", `shorthand for -filter repo:any:VALUE (owner/name, or "none" for the tasks with no target repo)`)
	fs.StringVar(&f.base, "base", "", `shorthand for -filter base:any:VALUE (or "none" for the tasks taking the repo's default)`)
	fs.StringVar(&f.capability, "capability", "", `shorthand for -filter capability:any:VALUE (or "none" for the tasks carrying none)`)
	fs.StringVar(&f.author, "author", "", "shorthand for -filter author:any:VALUE")
	fs.StringVar(&f.origin, "origin", "", "shorthand for -filter origin:any:VALUE ("+strings.Join(taskOrigins, ", ")+")")
	// The three tri-state flags. Each asks for the tasks that have the
	// attribute; "=false" asks for the ones that don't; leaving it off
	// asks nothing -- which is why Visit, rather than the values
	// themselves, is what decides whether the question was asked.
	blocked := fs.Bool("blocked", false, "only tasks waiting on another task (-blocked=false for only the unblocked ones)")
	autoMerge := fs.Bool("auto-merge", false, "only tasks that merge themselves (-auto-merge=false for only those needing a human)")
	interactive := fs.Bool("interactive", false, "only live chats (-interactive=false for only background tasks)")
	// The two switches behind the states a listing leaves out (hideable
	// above), tri-state for the reason the three above are: -closed asks
	// for the closed tasks as well, -closed=false leaves them out of a
	// deployment that shows them by default, and leaving it off takes
	// that deployment's own answer.
	closed := fs.Bool("closed", false, "list closed tasks too (-closed=false to leave them out where the deployment shows them by default)")
	deferred := fs.Bool("deferred", false, "list deferred tasks too")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "usage: grain list [flags]\n\nFlags:\n")
		fs.PrintDefaults()
		fmt.Fprint(os.Stderr, filterHelp(builtinAttributes))
	}
	if err := fs.Parse(args); err != nil {
		// The vocabulary above is the built-in half, which is all a
		// usage message can be written from offline. `grain list -h`
		// asks the deployment for the other half and prints it under a
		// heading of its own -- a deployment that cannot be reached
		// simply says nothing, since help that fails is worse than help
		// that is short.
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(os.Stderr, declaredHelp(ctx, c))
		}
		return err
	}
	// Nothing a listing narrows by is positional -- a search term is
	// -search and a state is -state -- so a bare word is a filter that
	// would silently list everything instead.
	if err := noArgs(fs, fs.Args()); err != nil {
		return err
	}
	show := map[string]*bool{}
	fs.Visit(func(fl *flag.Flag) {
		switch fl.Name {
		case "blocked":
			f.blocked = blocked
		case "auto-merge":
			f.autoMerge = autoMerge
		case "interactive":
			f.interactive = interactive
		case "closed":
			show["closed"] = closed
		case "deferred":
			show["deferred"] = deferred
		}
	})
	// Read the same config response the browser uses. Besides carrying the
	// deployment query, it carries the custom-field vocabulary that query may
	// name, so a default over custom.priority does not require a second request
	// or parse differently here.
	listConfig, configErr := c.TaskListConfig(ctx)
	// A daemon that did not answer this, after the client's whole retry
	// budget, will not answer the settings and the listing that follow
	// either: each would spend a budget of its own, three in all.
	var down *ui.DaemonUnavailableError
	if errors.As(configErr, &down) {
		return configErr
	}
	attrs := builtinAttributes
	if configErr == nil {
		attrs = attributesFor(listConfig.CustomFields)
	} else if f.namesAnUndeclaredAttribute() {
		return fmt.Errorf("asking which fields this deployment declares: %w", configErr)
	}
	q, err := f.query(attrs)
	if err != nil {
		return err
	}

	// A command-line condition is the whole explicit filter query, just as a
	// URL carrying f= is the browser's whole query. Deployment policy only
	// seeds an untouched listing; it is not a floor underneath an explicit
	// request. The legacy visibility switches keep their established additive
	// behavior, and are also explicit enough not to inherit arbitrary policy.
	explicitFilters := len(q.filters) > 0
	explicitVisibility := len(show) > 0
	usingDeploymentDefault := !explicitFilters && !explicitVisibility &&
		configErr == nil && listConfig.DefaultFilters != nil
	if usingDeploymentDefault {
		q.filters = append(q.filters, deploymentDefaultFilters(listConfig, attrs)...)
		q.hidden = hiddenFromDefaultFilters(q.filters)
	} else if !explicitFilters {
		// Compatibility with an older daemon (no defaultFilters field), and
		// with -closed/-deferred. This is the historical CLI policy expressed
		// before deployment defaults existed.
		q, err = withDefaultTaskKind(q, attrs)
		if err != nil {
			return err
		}
	}

	// One settings request at most. Configured defaults own state visibility;
	// only the compatibility/visibility path still needs the old hidden-state
	// settings. Date conditions use UTC.
	var settings ui.Settings
	needsHidden := !usingDeploymentDefault && !explicitFilters
	if needsHidden && q.needsSettings(show) {
		settings = deploymentSettings(ctx, c)
	}
	if q.needsZone() {
		q.zone = time.UTC
	}
	if needsHidden {
		q.hidden = hiddenStates(q, show, settings)
	}
	tasks, err := c.ListTasks(ctx)
	if err != nil {
		return err
	}
	out.tasks(q.apply(tasks))
	out.note(hiddenNote(q.hidden, tasks))
	return nil
}

// deploymentDefaultFilters decodes the query stored in the deployment config
// with the same repeated-f= shape the browser reads. A malformed or stale
// clause is ignored here just as conditionFromQuery/resolveConditions ignore it
// in the web UI; a bad deployment default must not turn `grain list` into a
// command that cannot list anything. Explicit -filter values remain strict.
func deploymentDefaultFilters(config ui.TaskListConfig, attrs attributeSet) []condition {
	raw := ""
	if config.DefaultFilters != nil {
		raw = *config.DefaultFilters
	}
	// Keep the historical checkbox effective for deployments whose config row
	// still carries the built-in query, matching taskFilters.js exactly.
	if config.ShowClosedByDefault && raw == model.DefaultTaskFilters {
		raw = "f=state:none:deferred&f=kind:none:interactive"
	}
	params, _ := url.ParseQuery(raw)
	filters := make([]condition, 0, len(params["f"]))
	for _, written := range params["f"] {
		condition, err := attrs.parseCondition(written)
		if err == nil {
			filters = append(filters, condition)
		}
	}
	return filters
}

// hiddenFromDefaultFilters preserves the CLI's useful note for the ordinary
// state-exclusion default. The conditions themselves do the filtering; this
// set only lets hiddenNote say what was omitted and which visibility flag can
// reveal it.
func hiddenFromDefaultFilters(filters []condition) map[string]bool {
	hidden := map[string]bool{}
	for _, condition := range filters {
		if condition.attr.id != "state" || condition.op.id != "none" {
			continue
		}
		for _, value := range condition.values {
			for _, h := range hideable {
				if value == h.state {
					hidden[value] = true
				}
			}
		}
	}
	if len(hidden) == 0 {
		return nil
	}
	return hidden
}

// withDefaultTaskKind leaves live chats out of an ordinary listing on an old
// daemon that cannot report deployment defaults. New daemons carry this policy
// in DefaultFilters, including an explicitly empty or interactive-only policy.
func withDefaultTaskKind(q taskQuery, attrs attributeSet) (taskQuery, error) {
	for _, condition := range q.filters {
		if condition.attr.id == "kind" {
			return q, nil
		}
	}
	condition, err := attrs.newCondition("kind", "none", "interactive")
	if err != nil {
		return taskQuery{}, err
	}
	q.filters = append(q.filters, condition)
	return q, nil
}

// deploymentSettings is what a listing needs to know about the
// deployment rather than about its tasks: the wall clock it keeps
// (Settings.TimeZone), which is the zone a bare day in a date condition
// is read in -- the same reading the frontend gives one, where the
// timestamps being filtered are printed in that zone too -- and whether
// it shows closed tasks by default (Settings.ShowClosedByDefault, the
// setting behind the frontend's own default condition).
//
// Asked for only when the listing actually turns on one of those, and
// answered with the zero value rather than an error when the settings
// cannot be read: an older daemon, or one that has never been
// configured, is no reason not to print the listing. model.LoadLocation
// resolves an empty zone against the database the binary carries, and a
// deployment that could not be asked hides closed tasks, which is what
// every deployment does until somebody says otherwise.
func deploymentSettings(ctx context.Context, c *ui.HTTPClient) ui.Settings {
	settings, err := c.GetSettings(ctx)
	if err != nil {
		return ui.Settings{}
	}
	return settings
}

// filterHelp is the vocabulary itself, printed under the flags: which
// attributes there are, and which comparisons each of them takes. It is
// generated from the same table the filters are answered out of, so a
// new attribute is documented by existing.
func filterHelp(attrs attributeSet) string {
	var b strings.Builder
	b.WriteString("\nConditions (-filter, repeatable) are written attribute:comparison:value, the\n")
	b.WriteString("spelling the UI writes into a link, so a condition can be pasted between them:\n\n")
	b.WriteString("  grain list -filter repo:any:acme/widgets,acme/gadgets -filter created:within:7\n")
	b.WriteString("  grain list -filter capability:all:gh,gcp -sort state:asc,created:desc\n\n")
	b.WriteString("Several values in one condition are separated by \",\" or \"|\"; a value that has\n")
	b.WriteString("to carry one is written %2C or %7C. \"none\" is \"has no value for this at all\".\n\n")
	b.WriteString("The fields this deployment declares are attributes too, under custom.<name>,\n")
	b.WriteString("with the comparisons their declared type takes -- they are listed below when\n")
	b.WriteString("the deployment can be asked:\n\n")
	b.WriteString("  grain list -filter custom.priority:any:high|critical -sort custom.due:asc\n\n")
	b.WriteString("Attributes, and the comparisons each takes:\n")
	b.WriteString(attributeLines(attrs))
	b.WriteString("\nComparisons:\n")
	for _, op := range operators {
		// Only the ones some attribute above actually offers: the table
		// carries a comparison for every type the frontend's editor
		// knows, and a deployment whose tasks have no numeric attribute
		// has no use for the four that compare numbers.
		if !attrs.offers(op.id) {
			continue
		}
		fmt.Fprintf(&b, "  %-8s %s\n", op.id, op.label)
	}
	b.WriteString("\n-sort takes attribute:asc or attribute:desc keys over the same attributes, and\n")
	b.WriteString("still answers to the named orders " + strings.Join(legacySortNames(), ", ") + ".\n")
	return b.String()
}

// attributeLines is one line per attribute: its id and the comparisons
// it takes, aligned, and for a declared field what the deployment calls
// it -- "custom.due" is not much use to somebody looking for the one
// named "Due date".
func attributeLines(attrs attributeSet) string {
	var b strings.Builder
	width := 0
	for _, a := range attrs {
		if len(a.id) > width {
			width = len(a.id)
		}
	}
	for i := range attrs {
		a := &attrs[i]
		fmt.Fprintf(&b, "  %-*s  %s", width, a.id, strings.Join(operatorsFor(a), ", "))
		if a.custom && a.label != "" {
			fmt.Fprintf(&b, "  (%s)", a.label)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// declaredHelp is the half of the vocabulary only the deployment knows:
// the fields it declares, as the attributes they are. Printed after the
// built-in usage by `grain list -h`, which is where somebody finds out
// that this deployment has a custom.priority at all -- `grain create -h`
// and `grain update -h` print the same declarations as the values their
// -field can set (customfields.go's declaredFieldHelp), and nothing else
// here lists them.
//
// Empty when the deployment declares none, and empty when it cannot be
// asked: help is not worth failing over, and every flag it describes
// goes on working.
func declaredHelp(ctx context.Context, c *ui.HTTPClient) string {
	fields, err := c.CustomFields(ctx)
	if err != nil || len(fields) == 0 {
		return ""
	}
	custom := attributesFor(fields).custom()
	if len(custom) == 0 {
		return ""
	}
	return "\nFields this deployment declares, which are attributes like any other:\n" +
		attributeLines(custom)
}
