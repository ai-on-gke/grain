package cli

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
)

// taskfilters.go is the comparisons `grain list` can make and the orders
// it can print in -- the Go half of ui/src/taskFilters.js, over the
// attributes taskattributes.go declares.
//
// # The vocabulary on a command line
//
// A condition is written the way the UI writes one into a URL, because
// that is the one spelling both surfaces already have to agree on:
//
//	grain list -filter repo:any:acme/widgets|acme/gadgets
//	grain list -filter created:within:7 -filter title:has:flake
//
// attribute:comparison:value, one -filter per condition, several values
// joined by "|" -- so an "f" clause out of a link
// (?f=repo:any:acme/widgets|acme/gadgets, paths.js) is pasteable after
// -filter and asks the same question. A comma is accepted for "|" as
// well, since a shell makes "|" the character that needs quoting and a
// list of values is the thing a comma already means everywhere else in
// this CLI (-capabilities, -reads).
//
// The escaping is the URL's too: a value that would be read as a
// separator is written %XX (%7C for "|", %2C for a comma, %3A for a
// colon, %25 for a percent sign). It only ever comes up for a value
// nothing in this deployment has -- no repo, capability id or author
// contains one -- and it is what keeps one spelling working in both
// places rather than two that agree until they don't.
//
// -sort is the same idea from the other side: attribute:direction keys,
// comma-separated, the first deciding and the rest breaking its ties.
//
//	grain list -sort state:asc,created:desc
//
// The fields a deployment declares are attributes like any other, under
// custom.<name> (taskattributes.go's attributesFor) -- which is why
// everything below is parsed against an attributeSet rather than against
// one fixed table:
//
//	grain list -filter custom.priority:any:high|critical -sort custom.due:asc
//
// # What the CLI does differently, and why
//
// The frontend is forgiving about a clause it cannot answer: an
// attribute or a comparison it has never heard of is dropped, so a
// stale link opens the list it names rather than an error. Here the same
// clause is an error naming the words that work. A link is followed; a
// flag is typed, and a listing narrowed by a filter that was quietly
// ignored reads exactly like a deployment with no such tasks. That is
// the rule -state and -origin have always had, applied to the whole
// vocabulary.

// operator is one comparison a condition can make.
//
// `input` is what the operator takes on its right, and is the whole of
// what decides how the value part is read: "choices" is a list, "text",
// "number", "date" and "days" are a single value taken whole (so a title
// may contain a comma without being split into two), and "none" takes no
// value at all.
type operator struct {
	id    string
	input string
	// label is how the frontend's editor reads the comparison out loud
	// ("is any of"). Unused by anything here -- nothing draws a menu --
	// and carried so `grain list -h` can say what a comparison means and
	// so the sync test has something to compare.
	label string
}

// operators is every comparison, in taskFilters.js's own OPERATORS
// order.
var operators = []operator{
	{id: "any", input: "choices", label: "is any of"},
	{id: "none", input: "choices", label: "is none of"},
	{id: "all", input: "choices", label: "has all of"},
	{id: "has", input: "text", label: "contains"},
	{id: "hasnot", input: "text", label: "does not contain"},
	{id: "eq", input: "number", label: "is"},
	{id: "ne", input: "number", label: "is not"},
	{id: "gt", input: "number", label: "is more than"},
	{id: "lt", input: "number", label: "is less than"},
	{id: "after", input: "date", label: "is after"},
	{id: "before", input: "date", label: "is before"},
	{id: "within", input: "days", label: "is within the last"},
	// Offered by no task attribute -- only a schedule's next run looks
	// forward (ui/src/processFilters.js, pkg/schedulefilter) -- but in
	// the table so it reads the same as the frontend's.
	{id: "coming", input: "hours", label: "is within the next"},
	{id: "empty", input: "none", label: "is empty"},
	{id: "filled", input: "none", label: "is set"},
	{id: "legacy", input: "none", label: "is a legacy value"},
}

// operatorsByType is which of them each kind of attribute offers, in the
// order the editor's menu offers them -- taskFilters.js's own
// OPERATORS_BY_TYPE. Text takes no "is any of": its values are titles
// and ids, which nobody picks off a list of every one in the
// deployment.
var operatorsByType = map[attrType][]string{
	typeEnum:   {"any", "none"},
	typeTags:   {"any", "none", "all"},
	typeText:   {"has", "hasnot"},
	typeDate:   {"within", "after", "before"},
	typeNumber: {"eq", "ne", "gt", "lt"},
}

func operatorByID(id string) *operator {
	for i := range operators {
		if operators[i].id == id {
			return &operators[i]
		}
	}
	return nil
}

// operatorsFor is the comparisons one attribute offers: its type's, the
// two about having no value at all for the attributes where that is a
// real answer, and -- for a field this deployment declares -- the one
// about carrying a value the declaration has moved on from.
func operatorsFor(a *attribute) []string {
	ops := operatorsByType[a.typ]
	out := make([]string, 0, len(ops)+3)
	out = append(out, ops...)
	if a.people {
		out = append(out, "has", "hasnot")
	}
	if a.canBeEmpty() {
		out = append(out, "empty", "filled")
	}
	if a.legacy != nil {
		out = append(out, "legacy")
	}
	return out
}

// offers is whether any attribute in this vocabulary takes the named
// comparison -- what `grain list -h` prints its list of comparisons out
// of, so it names the ones somebody could actually type. A deployment
// that declares a number field is the case where the four that compare
// numbers become worth printing.
func (s attributeSet) offers(op string) bool {
	for i := range s {
		if contains(operatorsFor(&s[i]), op) {
			return true
		}
	}
	return false
}

// noneValue is what a condition takes to mean "the tasks that have none
// at all" -- the UI's own "No repo"/"Repo default"/"No capabilities"
// menu entries. A repo is owner/name and a capability id is a bare word,
// so neither can collide with it; a branch literally named "none" is the
// one thing this spelling cannot ask about, which is the bargain -repo
// none has always made.
//
// noneURLValue is what the frontend writes for the same thing (NONE,
// taskAttributes.js). Both are accepted, so a clause copied out of a
// link works unaltered and a clause typed by hand does not have to be
// spelt with underscores.
const (
	noneValue    = "none"
	noneURLValue = "__none__"
)

// condition is one narrowing: an attribute, a comparison, and the values
// to compare against. Every condition has to hold for a task to be
// listed.
type condition struct {
	attr   *attribute
	op     *operator
	values []string
}

// parseCondition reads one -filter's worth of "attribute:comparison:
// value[|value]" into a condition, or says why it is not one.
func (s attributeSet) parseCondition(raw string) (condition, error) {
	parts := strings.SplitN(raw, ":", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return condition{}, fmt.Errorf("malformed -filter %q: a condition is written attribute:comparison:value, as in repo:any:acme/widgets", raw)
	}
	rest := ""
	if len(parts) == 3 {
		rest = parts[2]
	}
	cond, err := s.newCondition(parts[0], parts[1], writtenValues(operatorByID(parts[1]), rest)...)
	if err != nil {
		return condition{}, fmt.Errorf("-filter %q: %w", raw, err)
	}
	return cond, nil
}

// writtenValues cuts the value part of a written condition into the
// values it holds, undoing paths.js's escaping on each.
//
// Only a comparison over a list of choices is cut at all, and it is cut
// on both separators: "|" because that is what a link carries, and ","
// because that is what a list of values means everywhere else in this
// CLI (-capabilities, -reads) and because a shell makes "|" the one that
// needs quoting. Everything else -- a title to look for, a date, a
// number of days -- is one value taken whole, so a title with a comma in
// it is not silently two questions. A choice that has to carry a
// separator is escaped %7C or %2C, exactly as in a link.
func writtenValues(op *operator, rest string) []string {
	if rest == "" {
		return nil
	}
	if op == nil || op.input != "choices" {
		return []string{unescapeValue(rest)}
	}
	parts := strings.FieldsFunc(rest, func(r rune) bool { return r == '|' || r == ',' })
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		values = append(values, unescapeValue(part))
	}
	return values
}

// newCondition is one condition from its parts, checking everything that
// can be checked without seeing a single task: that the attribute and
// the comparison exist, that the comparison is one this attribute
// offers, that it has been given the number of values it takes, and that
// those values are ones the attribute can hold.
//
// It is what -filter and every shorthand flag both go through, so
// `-state in_progress` and `-filter state:any:in_progress` are refused
// by the same code with the same words.
func (s attributeSet) newCondition(attrID, opID string, values ...string) (condition, error) {
	attr := s.byID(attrID)
	if attr == nil {
		return condition{}, fmt.Errorf("unknown attribute %q: one of %s", attrID, strings.Join(s.ids(), ", "))
	}
	op := operatorByID(opID)
	if op == nil || !contains(operatorsFor(attr), op.id) {
		return condition{}, fmt.Errorf("%s takes no comparison %q: one of %s", attr.id, opID, strings.Join(operatorsFor(attr), ", "))
	}
	switch {
	case op.input == "none":
		if len(values) > 0 {
			return condition{}, fmt.Errorf("%q takes no value", op.id)
		}
	case len(values) == 0:
		// The editor leaves a half-built condition matching everything,
		// since it adds the row before there is anything to compare
		// against. A command line has no half-built state: a condition
		// with no value is a question nobody finished asking.
		return condition{}, fmt.Errorf("%q needs a value", op.id)
	case op.input != "choices" && len(values) > 1:
		return condition{}, fmt.Errorf("%q takes one value, not %d", op.id, len(values))
	}
	for i, v := range values {
		switch op.input {
		case "choices":
			// The frontend's own spelling of "has none at all" is
			// accepted alongside this command's, so a clause pasted out
			// of a link works unaltered.
			if v == noneURLValue {
				v = noneValue
				values[i] = v
			}
			if err := checkValue(attr, v); err != nil {
				return condition{}, err
			}
		case "number":
			if _, err := strconv.ParseFloat(v, 64); err != nil {
				return condition{}, fmt.Errorf("%q takes a number, not %q", op.id, v)
			}
		case "days":
			if n, err := strconv.ParseFloat(v, 64); err != nil || n < 0 {
				return condition{}, fmt.Errorf("%q takes a number of days, not %q", op.id, v)
			}
		case "date":
			if _, ok := parseFilterDay(v, time.UTC); !ok {
				return condition{}, fmt.Errorf("%q takes a date written yyyy-mm-dd, or a full RFC3339 timestamp, not %q", op.id, v)
			}
		}
	}
	return condition{attr: attr, op: op, values: values}, nil
}

// checkValue refuses a value the attribute could never hold. That is
// only decidable for the attributes with a fixed vocabulary -- a state,
// an origin, on or off -- plus "none", which is only an answer for the
// attributes where having no value is one. A repo or an author is
// checked against nothing: the CLI would have to ask the deployment what
// repos it has, and a filter naming one nobody has filed a task against
// yet is a question with a real answer rather than a typo.
func checkValue(attr *attribute, value string) error {
	if value == noneValue {
		if !attr.canBeEmpty() {
			return fmt.Errorf("every task has a %s, so %q is not one of its values", attr.id, noneValue)
		}
		return nil
	}
	if _, ok := attr.aliases[value]; ok {
		return nil
	}
	if len(attr.order) > 0 && !contains(attr.order, value) {
		return fmt.Errorf("unknown %s %q: one of %s", attr.id, value, strings.Join(attr.order, ", "))
	}
	return nil
}

// unescapeValue reads back the %XX escaping paths.js writes for a value
// that would otherwise be mistaken for a separator.
func unescapeValue(v string) string {
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] == '%' && i+2 < len(v) {
			if n, err := strconv.ParseUint(v[i+1:i+3], 16, 8); err == nil {
				b.WriteByte(byte(n))
				i += 2
				continue
			}
		}
		b.WriteByte(v[i])
	}
	return b.String()
}

// match is whether one task satisfies one condition -- matchesCondition
// (taskFilters.js) case for case.
//
// `now` and `loc` are what the two date comparisons are answered
// against: the moment "within the last 7 days" is counted back from, and
// the wall clock a bare day is read in. Both are the caller's, so a
// listing is not at the mercy of the machine it is printed on.
func (c condition) match(t ui.Task, now time.Time, loc *time.Location) bool {
	values := c.attr.values(t)
	wanted := c.values
	if len(c.attr.aliases) > 0 {
		wanted = nil
		for _, w := range c.values {
			if expanded, ok := c.attr.aliases[w]; ok {
				wanted = append(wanted, expanded...)
			} else {
				wanted = append(wanted, w)
			}
		}
	}
	switch c.op.id {
	case "any":
		return matchesAny(values, wanted)
	case "none":
		return !matchesAny(values, wanted)
	case "all":
		for _, w := range wanted {
			if w == noneValue {
				if len(values) != 0 {
					return false
				}
				continue
			}
			if !contains(values, w) {
				return false
			}
		}
		return true
	case "has":
		return containsText(values, wanted[0])
	case "hasnot":
		return !containsText(values, wanted[0])
	case "empty":
		return len(values) == 0
	case "filled":
		return len(values) > 0
	case "legacy":
		// Only a declared field has one (customAttribute), and only
		// that attribute offers the comparison, so nothing built in can
		// reach this. A condition out of a link that names one anyway
		// narrows nothing rather than emptying the listing, which is
		// what every other unanswerable clause does here.
		if c.attr.legacy == nil {
			return true
		}
		return c.attr.legacy(t)
	case "eq", "ne", "gt", "lt":
		return matchesNumber(c.op.id, values, wanted[0])
	case "after", "before":
		day, ok := parseFilterDay(wanted[0], loc)
		if !ok {
			return true
		}
		return matchesTime(c.op.id, values, day, loc)
	case "within":
		days, err := strconv.ParseFloat(wanted[0], 64)
		if err != nil {
			return true
		}
		cutoff := now.Add(-time.Duration(days * float64(24*time.Hour)))
		return matchesTime("after", values, cutoff, loc)
	case "coming":
		hours, err := strconv.ParseFloat(wanted[0], 64)
		if err != nil {
			return true
		}
		cutoff := now.Add(time.Duration(hours * float64(time.Hour)))
		return matchesTime("before", values, cutoff.Add(time.Nanosecond), loc)
	}
	return true
}

func matchesAny(values, wanted []string) bool {
	if len(values) == 0 {
		return contains(wanted, noneValue)
	}
	for _, v := range values {
		if contains(wanted, v) {
			return true
		}
	}
	return false
}

func containsText(values []string, needle string) bool {
	needle = strings.ToLower(needle)
	for _, v := range values {
		if strings.Contains(strings.ToLower(v), needle) {
			return true
		}
	}
	return false
}

func matchesNumber(op string, values []string, want string) bool {
	n, err := strconv.ParseFloat(want, 64)
	if err != nil {
		return true
	}
	nums := make([]float64, 0, len(values))
	for _, v := range values {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			nums = append(nums, f)
		}
	}
	if len(nums) == 0 {
		return false
	}
	if op == "ne" {
		for _, f := range nums {
			if f == n {
				return false
			}
		}
		return true
	}
	for _, f := range nums {
		switch op {
		case "eq":
			if f == n {
				return true
			}
		case "gt":
			if f > n {
				return true
			}
		case "lt":
			if f < n {
				return true
			}
		}
	}
	return false
}

// matchesTime is "after" and "before" over a date attribute's values.
// "after" is inclusive of the moment named and "before" exclusive of it,
// so a day named in both reads as a range with no gap and no overlap --
// the frontend's own reading. A task whose value is not a moment at all,
// or has none, matches neither.
func matchesTime(op string, values []string, at time.Time, loc *time.Location) bool {
	for _, v := range values {
		ts, ok := momentOf(v, loc)
		if !ok {
			continue
		}
		if op == "after" && !ts.Before(at) {
			return true
		}
		if op == "before" && ts.Before(at) {
			return true
		}
	}
	return false
}

// momentOf is when one value of a date attribute happened, or false for
// a value that is not a moment at all -- taskFilters.js's own momentOf,
// and the same two shapes parseFilterDay reads a written date in, for
// the reason the two have to agree.
//
// A timestamp (a task's createdAt) is read as itself. A bare calendar
// day -- what a custom date field holds
// (model.CustomFieldDateLayout) -- is read as that day *in the
// deployment's own zone*, the same reading the day named in the
// condition gets: read as UTC instead, a field whose value is the 4th
// would answer no to "is after the 4th" on every deployment west of
// UTC, since UTC midnight falls on the 3rd there.
func momentOf(value string, loc *time.Location) (time.Time, bool) {
	return parseFilterDay(value, loc)
}

// parseFilterDay is the moment a date value begins: a bare yyyy-mm-dd
// read as that day in `loc` -- the deployment's own wall clock, which is
// the zone every timestamp grain prints is written in -- or a full
// RFC3339 timestamp taken as the instant it names.
func parseFilterDay(value string, loc *time.Location) (time.Time, bool) {
	if day, err := time.ParseInLocation("2006-01-02", value, loc); err == nil {
		return day, true
	}
	if at, err := time.Parse(time.RFC3339, value); err == nil {
		return at, true
	}
	return time.Time{}, false
}

// --- sorting ---------------------------------------------------------

// sortKey is one task's value for one sort key: a number, some text, or
// nothing at all. Nothing sorts last in *both* directions -- a run of
// tasks with no target repo at the top of "repo:asc" would bury what the
// order was picked to find, and so would reversing it.
type sortKey struct {
	empty bool
	isNum bool
	num   float64
	text  string
}

var missingKey = sortKey{empty: true}

func numberKey(n float64) sortKey {
	return sortKey{isNum: true, num: n, text: strconv.FormatFloat(n, 'f', -1, 64)}
}

func textKey(s string) sortKey {
	if s == "" {
		return missingKey
	}
	return sortKey{text: s}
}

// compareKeys orders two keys: numerically when both are numbers, and
// otherwise by their text, case-insensitively -- so "Widget" and
// "widget" land together rather than in two alphabets.
func compareKeys(a, b sortKey) int {
	if a.isNum && b.isNum {
		switch {
		case a.num < b.num:
			return -1
		case a.num > b.num:
			return 1
		default:
			return 0
		}
	}
	if n := strings.Compare(strings.ToLower(a.text), strings.ToLower(b.text)); n != 0 {
		return n
	}
	return strings.Compare(a.text, b.text)
}

// sortSpec is one key of an order: an attribute and a direction.
type sortSpec struct {
	attr *attribute
	desc bool
}

// legacySorts are the orders -sort took when it was one word naming one
// of seven fixed orders, each as the keys it always meant. A script that
// says `-sort newest` goes on printing what it printed -- the same
// bargain paths.js keeps for the links the old toolbar wrote.
//
// Which is why "manual" is here beside "backlog": paths.js's own
// LEGACY_SORTS calls the backlog's own order that, so `?sort=manual` is
// what a kept link says, and one word meaning an order in a link and
// nothing at all on a command line is exactly the divergence -filter is
// spelled the way it is to avoid. Both words, one order (grain/task-127,
// list_vocabulary_test.go).
var legacySorts = map[string][]string{
	"backlog": nil,
	"manual":  nil,
	"newest":  {"created:desc"},
	"oldest":  {"created:asc"},
	"title":   {"title:asc"},
	"state":   {"state:asc"},
	"repo":    {"repo:asc"},
	"author":  {"author:asc"},
}

func legacySortNames() []string {
	names := make([]string, 0, len(legacySorts))
	for name := range legacySorts {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// parseSort reads -sort: either one of the legacy words, or a
// comma-separated list of attribute:direction keys.
func (s attributeSet) parseSort(raw string) ([]sortSpec, error) {
	if raw == "" {
		return nil, nil
	}
	if keys, ok := legacySorts[raw]; ok {
		return s.parseSortKeys(keys, raw)
	}
	return s.parseSortKeys(strings.Split(raw, ","), raw)
}

func (s attributeSet) parseSortKeys(keys []string, raw string) ([]sortSpec, error) {
	var specs []sortSpec
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		name, dir, hasDir := strings.Cut(key, ":")
		attr := s.byID(name)
		if attr == nil {
			return nil, fmt.Errorf("unknown -sort %q: a key is attribute:asc or attribute:desc, over one of %s -- or one of the named orders %s",
				raw, strings.Join(s.ids(), ", "), strings.Join(legacySortNames(), ", "))
		}
		if !hasDir {
			dir = "asc"
		}
		if dir != "asc" && dir != "desc" {
			return nil, fmt.Errorf("unknown direction %q in -sort %q: asc or desc", dir, raw)
		}
		specs = append(specs, sortSpec{attr: attr, desc: dir == "desc"})
	}
	return specs, nil
}

// keyOf is the one value a task is ordered by for one attribute:
// whatever the attribute nominates (a state's rank, an id as a number),
// and otherwise its first value read as its type.
//
// `loc` is there for the date attributes alone, and for the same reason
// the comparisons take one: a custom date field holds a calendar day,
// and the day it names begins when the deployment's own clock says it
// does. An order and a filter that read a day differently would sort a
// list into an order its own filter disputes.
func (s sortSpec) keyOf(t ui.Task, loc *time.Location) sortKey {
	if s.attr.key != nil {
		return s.attr.key(t)
	}
	values := s.attr.values(t)
	if len(values) == 0 {
		return missingKey
	}
	v := values[0]
	switch s.attr.typ {
	case typeDate:
		at, ok := momentOf(v, loc)
		if !ok {
			return missingKey
		}
		return numberKey(float64(at.UnixNano()))
	case typeNumber:
		n, err := strconv.ParseFloat(v, 64)
		if err != nil || math.IsNaN(n) {
			return missingKey
		}
		return numberKey(n)
	}
	if len(s.attr.order) > 0 {
		for i, known := range s.attr.order {
			if known == v {
				return numberKey(float64(i))
			}
		}
		// A value no fixed vocabulary knows about -- a state from a
		// newer daemon -- sorts after the ones it does rather than
		// before.
		return numberKey(float64(len(s.attr.order)))
	}
	return textKey(v)
}

// compare orders two tasks on one key, missing values last whichever way
// it points.
func (s sortSpec) compare(a, b ui.Task, loc *time.Location) int {
	x, y := s.keyOf(a, loc), s.keyOf(b, loc)
	if x.empty || y.empty {
		switch {
		case x.empty && y.empty:
			return 0
		case x.empty:
			return 1
		default:
			return -1
		}
	}
	if s.desc {
		return -compareKeys(x, y)
	}
	return compareKeys(x, y)
}

func taskClosedTime(t ui.Task) time.Time {
	if t.ClosedAt != nil {
		return *t.ClosedAt
	}
	if t.EndedAt != nil {
		return *t.EndedAt
	}
	if t.CreatedAt != nil {
		return *t.CreatedAt
	}
	return time.Time{}
}

// sortTasks puts tasks in the order the keys ask for: the first decides,
// the next breaks its ties, and so on. No keys is the backlog's own
// order -- what the daemon served, which is the one order this command
// cannot compute for itself and the one a task's position in the queue
// is read off (open tasks in backlog order, closed tasks newest first).
//
// The sort is stable, so tasks that compare equal on every key keep the
// order they arrived in: an order is "the backlog, grouped by
// X" rather than a reshuffle inside each group.
func sortTasks(tasks []ui.Task, specs []sortSpec, loc *time.Location) {
	if len(specs) == 0 {
		var open, closed []ui.Task
		for _, t := range tasks {
			if t.State == model.StateClosed {
				closed = append(closed, t)
			} else {
				open = append(open, t)
			}
		}
		sort.SliceStable(closed, func(i, j int) bool {
			timeI := taskClosedTime(closed[i])
			timeJ := taskClosedTime(closed[j])
			if !timeI.Equal(timeJ) {
				return timeI.After(timeJ)
			}
			return closed[i].ID < closed[j].ID
		})
		copy(tasks, append(open, closed...))
		return
	}
	if loc == nil {
		loc = time.UTC
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		for _, s := range specs {
			if n := s.compare(tasks[i], tasks[j], loc); n != 0 {
				return n < 0
			}
		}
		return false
	})
}
