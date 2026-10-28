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

// Package schedulefilter is the one vocabulary for asking a narrower
// question of a list of schedules: where each is declared, what it
// fires, which repository and branch it targets, whether it is enabled,
// its cadence, and when it last fired and next fires (grain/task-429).
//
// It is pkg/workflows/filter.go's idea applied to schedules. There are two
// readers asking the same question about the same rows -- the Processes
// page (ui/src/processFilters.js) and anybody calling GET
// /api/schedules, which is what an agent-facing list tool would call
// too -- and "disabled schedules declared in bwsalmon/grain" has to be
// one set with one name on both sides, or a report from either cannot
// be checked against the other.
//
// # Why the grammar is the address bar's
//
// The browser already writes a narrowed list down as a query string
// (ui/src/paths.js): `q` for the search box, `sort` as comma-separated
// "attribute:direction" keys, and one `f` per condition, spelled
// "attribute:comparison:value|value". This package reads exactly that,
// so the query a narrowed Processes page shows in its address bar can be
// pasted onto /api/schedules and asks the daemon the same question:
//
//	/processes?f=source:any:bwsalmon/grain&f=enabled:any:no
//	/api/schedules?f=source:any:bwsalmon/grain&f=enabled:any:no
//
// The attributes and their values are processFilters.js's, word for
// word: a schedule's source is the repository that declares it, or
// "system" for one of grain's own, or "deployment" for one somebody
// created here (in the UI, over the API, or in the state repository's
// tables, which are the same rows); its type is "template", "task",
// "suite", "workflow" or "system".
//
// Where the two differ is what they do with a condition they cannot
// read. The page drops it and says so, because a stale link should still
// open the list it names; this answers 400, because a caller asking the
// API a question it does not understand should be told rather than
// handed every schedule as though it had matched.
package schedulefilter

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Facts is everything a condition can ask of one schedule, already
// reduced to the words the vocabulary uses. pkg/ui builds one from each
// ui.Schedule it is about to answer with (ui.scheduleFacts), which keeps
// this package free of the API's own types.
type Facts struct {
	ID   string
	Name string
	// Kind is the row kind the Processes page files the schedule under:
	// KindWorkflow for one that runs a workflow, KindSchedule otherwise.
	Kind string
	// Source is a repository ("bwsalmon/grain") for a schedule a
	// repository declares, SourceSystem or SourceDeployment otherwise.
	Source string
	// DeclaredPath is the declaring file, for the search box.
	DeclaredPath string
	Type         string
	Repo         string
	Base         string
	// Template and Suite are the names (or ids, where no name is known)
	// of what the schedule fires, empty where it fires neither.
	Template    string
	Suite       string
	Enabled     bool
	Cadence     string
	TimeZone    string
	AutoApprove bool
	AutoMerge   bool
	Overlap     string
	// LastOutcome is how the last firing went: a CI conclusion for a
	// workflow schedule, OutcomeNotFiring for one held by its previous
	// firing, OutcomeFired for any other that has fired, and empty for
	// one that never has.
	LastOutcome string
	Next        time.Time
	Last        *time.Time
	Created     time.Time
}

// The words for a source and a type that are not a repository.
const (
	SourceSystem     = "system"
	SourceDeployment = "deployment"

	TypeTemplate = "template"
	TypeTask     = "task"
	TypeSuite    = "suite"
	TypeWorkflow = "workflow"
	TypeSystem   = "system"

	OutcomeFired     = "fired"
	OutcomeNotFiring = "not-firing"
)

// The page's row kinds (ui/src/processes.js's PROCESS_KINDS). Only the
// first two are ever a schedule's; the others name the page's templates,
// suites and triggers, which this list never holds, so asking for one of
// them alone is a question with no schedules in its answer.
const (
	KindSchedule = "schedule"
	KindWorkflow = "workflow"
)

// Kinds is every kind, in the order the page's chips show them and a
// sort by kind puts them in.
var Kinds = []string{KindSchedule, KindWorkflow, "template", "suite", "trigger"}

// Types is every type, in the order a menu offers them and a sort by
// type puts them in.
var Types = []string{TypeTemplate, TypeTask, TypeSuite, TypeWorkflow, TypeSystem}

// Cadences is every cadence, in the shortest-to-longest order a sort by
// cadence puts them in (processFilters.js's CADENCE_ORDER).
var Cadences = []string{"everyNMinutes", "everyNHours", "daily", "weekly", "monthly"}

// Overlaps is every overlap policy, in the order a sort by overlap puts
// them in (processFilters.js).
var Overlaps = []string{"skip", "allow"}

var yesNoOrder = []string{"yes", "no"}

// None is the value that stands for "has no value at all" in "is any
// of" and "is none of" -- taskAttributes.js's NONE, spelled the same so
// a link carrying it means the same here.
const None = "__none__"

// Condition is one `f` clause: an attribute, a comparison, and what it
// compares against.
type Condition struct {
	Attr   string
	Op     string
	Values []string
}

// SortKey is one key of a sort; the first decides and each later one
// breaks the ties of those before it.
type SortKey struct {
	Attr string
	Desc bool
}

// Filter is a whole question: the search box, the conditions (all of
// which must hold), and the order.
type Filter struct {
	Search     string
	Conditions []Condition
	Sort       []SortKey
}

// Empty is whether the filter asks nothing and orders nothing.
func (f Filter) Empty() bool {
	return strings.TrimSpace(f.Search) == "" && len(f.Conditions) == 0 && len(f.Sort) == 0
}

type attrType int

const (
	typeEnum attrType = iota
	typeText
	typeDate
)

type attribute struct {
	typ    attrType
	values func(Facts) []string
	moment func(Facts) *time.Time
	// order is a fixed vocabulary's own order, which is what it sorts
	// by rather than alphabetically -- yes before no, as on the page.
	order []string
}

func one(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

func yesNo(b bool) []string {
	if b {
		return []string{"yes"}
	}
	return []string{"no"}
}

func at(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// attributes is the vocabulary: processFilters.js's schedule attributes,
// by the same ids.
var attributes = map[string]attribute{
	"kind":            {typ: typeEnum, values: func(f Facts) []string { return one(f.Kind) }, order: Kinds},
	"name":            {typ: typeText, values: func(f Facts) []string { return one(f.Name) }},
	"id":              {typ: typeText, values: func(f Facts) []string { return one(f.ID) }},
	"source":          {typ: typeEnum, values: func(f Facts) []string { return one(f.Source) }},
	"type":            {typ: typeEnum, values: func(f Facts) []string { return one(f.Type) }, order: Types},
	"repo":            {typ: typeEnum, values: func(f Facts) []string { return one(f.Repo) }},
	"base":            {typ: typeEnum, values: func(f Facts) []string { return one(f.Base) }},
	"template":        {typ: typeEnum, values: func(f Facts) []string { return one(f.Template) }},
	"suite":           {typ: typeEnum, values: func(f Facts) []string { return one(f.Suite) }},
	"enabled":         {typ: typeEnum, values: func(f Facts) []string { return yesNo(f.Enabled) }, order: yesNoOrder},
	"cadence":         {typ: typeEnum, values: func(f Facts) []string { return one(f.Cadence) }, order: Cadences},
	"timeZone":        {typ: typeEnum, values: func(f Facts) []string { return one(f.TimeZone) }},
	"autoApprove":     {typ: typeEnum, values: func(f Facts) []string { return yesNo(f.AutoApprove) }, order: yesNoOrder},
	"autoMerge":       {typ: typeEnum, values: func(f Facts) []string { return yesNo(f.AutoMerge) }, order: yesNoOrder},
	"requireApproval": {typ: typeEnum, values: func(f Facts) []string { return nil }, order: yesNoOrder},
	"overlap":         {typ: typeEnum, values: func(f Facts) []string { return one(f.Overlap) }, order: Overlaps},
	"lastOutcome":     {typ: typeEnum, values: func(f Facts) []string { return one(f.LastOutcome) }},
	"next":            {typ: typeDate, moment: func(f Facts) *time.Time { return at(f.Next) }},
	"last":            {typ: typeDate, moment: func(f Facts) *time.Time { return f.Last }},
	"created":         {typ: typeDate, moment: func(f Facts) *time.Time { return at(f.Created) }},
}

// ops is which comparisons each type of attribute answers --
// taskFilters.js's OPERATORS_BY_TYPE for these three types, with "is
// within the next N hours" for dates, and the two about having no value
// at all on every attribute.
var ops = map[attrType][]string{
	typeEnum: {"any", "none", "empty", "filled"},
	typeText: {"has", "hasnot", "empty", "filled"},
	typeDate: {"within", "coming", "after", "before", "empty", "filled"},
}

// Attributes is every attribute id this vocabulary has, sorted -- what
// an error names when a caller asks for one it does not.
func Attributes() []string {
	out := make([]string, 0, len(attributes))
	for id := range attributes {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// legacySorts are the orders the Processes page's one-Select toolbar
// wrote before it had sort keys: a link somebody kept still sorts the
// way it said.
var legacySorts = map[string][]SortKey{
	"next":   {{Attr: "next"}},
	"last":   {{Attr: "last", Desc: true}},
	"name":   {{Attr: "name"}},
	"custom": {},
}

// ParseQuery reads a filter out of a query string in the address bar's
// encoding (the package comment). A query with none of q, sort, kind or
// f is the zero Filter, which matches every schedule and leaves the
// order alone.
//
// `kind` is how the page writes its kind chips -- `?kind=schedule,suite`
// rather than an `f` (processFilters.js's processNarrowingQuery) -- so it
// is read as the "kind is any of" condition it stands for, first, as the
// page puts it. Ignoring it would answer `?kind=workflow` with every
// schedule, where the page shows only the workflow ones.
func ParseQuery(query url.Values) (Filter, error) {
	f := Filter{Search: strings.TrimSpace(query.Get("q"))}
	if raw := strings.TrimSpace(query.Get("kind")); raw != "" {
		cond, err := parseKinds(raw)
		if err != nil {
			return Filter{}, err
		}
		f.Conditions = append(f.Conditions, cond)
	}
	if raw := strings.TrimSpace(query.Get("sort")); raw != "" {
		keys, err := parseSort(raw)
		if err != nil {
			return Filter{}, err
		}
		f.Sort = keys
	}
	for _, raw := range query["f"] {
		cond, err := ParseCondition(raw)
		if err != nil {
			return Filter{}, err
		}
		f.Conditions = append(f.Conditions, cond)
	}
	return f, nil
}

// parseKinds reads `?kind=` as a kind condition. The page drops a kind it
// has no chip for; this refuses one, for the reason the package comment
// gives.
func parseKinds(raw string) (Condition, error) {
	cond := Condition{Attr: "kind", Op: "any"}
	for _, k := range strings.Split(raw, ",") {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if rank(Kinds, k) == len(Kinds) {
			return Condition{}, fmt.Errorf("%q is not a kind: the kinds are %s", k, strings.Join(Kinds, ", "))
		}
		cond.Values = append(cond.Values, k)
	}
	return cond, nil
}

func parseSort(raw string) ([]SortKey, error) {
	if keys, ok := legacySorts[raw]; ok {
		return keys, nil
	}
	var keys []SortKey
	for _, part := range strings.Split(raw, ",") {
		attr, dir, _ := strings.Cut(strings.TrimSpace(part), ":")
		if attr == "" {
			continue
		}
		if _, ok := attributes[attr]; !ok {
			return nil, fmt.Errorf("cannot sort schedules by %q: the attributes are %s", attr, strings.Join(Attributes(), ", "))
		}
		switch dir {
		case "", "asc":
			keys = append(keys, SortKey{Attr: attr})
		case "desc":
			keys = append(keys, SortKey{Attr: attr, Desc: true})
		default:
			return nil, fmt.Errorf("%q is not a sort direction: use asc or desc", dir)
		}
	}
	return keys, nil
}

// ParseCondition reads one `f` clause -- taskFilters.js's
// conditionFromQuery, with the attribute and comparison checked against
// this vocabulary.
func ParseCondition(raw string) (Condition, error) {
	attr, rest, ok := strings.Cut(raw, ":")
	op, values, ok2 := strings.Cut(rest, ":")
	if !ok || !ok2 || attr == "" || op == "" {
		return Condition{}, fmt.Errorf("%q is not a condition: write attribute:comparison:value, e.g. enabled:any:no", raw)
	}
	a, known := attributes[attr]
	if !known {
		return Condition{}, fmt.Errorf("schedules have no attribute %q: the attributes are %s", attr, strings.Join(Attributes(), ", "))
	}
	allowed := false
	for _, o := range ops[a.typ] {
		allowed = allowed || o == op
	}
	if !allowed {
		return Condition{}, fmt.Errorf("%q cannot be compared with %q: use one of %s", attr, op, strings.Join(ops[a.typ], ", "))
	}
	cond := Condition{Attr: attr, Op: op}
	if values != "" {
		for _, v := range strings.Split(values, "|") {
			if u, err := url.PathUnescape(v); err == nil {
				v = u
			}
			cond.Values = append(cond.Values, v)
		}
	}
	return cond, nil
}

// Matches is whether one schedule survives every condition and the
// search. now and loc are the clock the date comparisons read: a day
// named in "is after 2026-10-24" starts at midnight in loc, the zone the
// deployment prints its times in, as taskFilters.js's dayStart reads it.
func (f Filter) Matches(facts Facts, now time.Time, loc *time.Location) bool {
	for _, c := range f.Conditions {
		if !c.matches(facts, now, loc) {
			return false
		}
	}
	q := strings.ToLower(strings.TrimSpace(f.Search))
	if q == "" {
		return true
	}
	return strings.Contains(strings.ToLower(SearchText(facts)), q)
}

// SearchText is what the search box looks through: the name and the id,
// and the other names a row shows -- its repository, what it fires, and
// the file that declares it. processFilters.js's searchText is the same
// list.
func SearchText(f Facts) string {
	return strings.Join([]string{f.Name, f.ID, f.Repo, f.Template, f.Suite, f.Source, f.DeclaredPath}, " ")
}

func (c Condition) matches(facts Facts, now time.Time, loc *time.Location) bool {
	a := attributes[c.Attr]
	if a.typ == typeDate {
		return c.matchesMoment(a.moment(facts), now, loc)
	}
	values := a.values(facts)
	switch c.Op {
	case "empty":
		return len(values) == 0
	case "filled":
		return len(values) > 0
	}
	// A condition still waiting for its value narrows nothing, as an
	// editor's half-written row does on the page.
	if len(c.Values) == 0 {
		return true
	}
	switch c.Op {
	case "any":
		return anyOf(values, c.Values)
	case "none":
		return !anyOf(values, c.Values)
	case "has", "hasnot":
		want := strings.ToLower(c.Values[0])
		found := false
		for _, v := range values {
			found = found || strings.Contains(strings.ToLower(v), want)
		}
		return found == (c.Op == "has")
	}
	return true
}

func anyOf(values, wanted []string) bool {
	if len(values) == 0 {
		for _, w := range wanted {
			if w == None {
				return true
			}
		}
		return false
	}
	for _, v := range values {
		for _, w := range wanted {
			if v == w {
				return true
			}
		}
	}
	return false
}

func (c Condition) matchesMoment(t *time.Time, now time.Time, loc *time.Location) bool {
	switch c.Op {
	case "empty":
		return t == nil
	case "filled":
		return t != nil
	}
	if len(c.Values) == 0 {
		return true
	}
	switch c.Op {
	case "within", "coming":
		n, err := strconv.ParseFloat(c.Values[0], 64)
		if err != nil {
			return true
		}
		if t == nil {
			return false
		}
		if c.Op == "within" {
			return !t.Before(now.Add(-time.Duration(n * float64(24*time.Hour))))
		}
		// "Is within the next N hours" includes a time already gone by:
		// a schedule whose next run is overdue fires as soon as it can,
		// which is sooner than anything inside the window.
		return !t.After(now.Add(time.Duration(n * float64(time.Hour))))
	case "after", "before":
		if loc == nil {
			loc = time.UTC
		}
		day, err := time.ParseInLocation("2006-01-02", c.Values[0], loc)
		if err != nil {
			return true
		}
		if t == nil {
			return false
		}
		if c.Op == "after" {
			return !t.Before(day)
		}
		return t.Before(day)
	}
	return true
}

// Apply is the filter over a whole list: the items it keeps, in the
// order it asks for. facts reads one item; items whose order the sort
// does not decide keep the order they arrived in.
func Apply[T any](f Filter, items []T, facts func(T) Facts, now time.Time, loc *time.Location) []T {
	type row struct {
		item  T
		facts Facts
	}
	rows := make([]row, 0, len(items))
	for _, it := range items {
		fa := facts(it)
		if f.Matches(fa, now, loc) {
			rows = append(rows, row{it, fa})
		}
	}
	if len(f.Sort) > 0 {
		sort.SliceStable(rows, func(i, j int) bool {
			for _, k := range f.Sort {
				if n := compare(attributes[k.Attr], rows[i].facts, rows[j].facts, k.Desc); n != 0 {
					return n < 0
				}
			}
			return false
		})
	}
	out := make([]T, len(rows))
	for i, r := range rows {
		out[i] = r.item
	}
	return out
}

// compare orders two schedules by one attribute, a missing value last
// whichever way the key points -- taskFilters.js's comparatorFor.
func compare(a attribute, x, y Facts, desc bool) int {
	sign := 1
	if desc {
		sign = -1
	}
	if a.typ == typeDate {
		tx, ty := a.moment(x), a.moment(y)
		switch {
		case tx == nil && ty == nil:
			return 0
		case tx == nil:
			return 1
		case ty == nil:
			return -1
		}
		return sign * tx.Compare(*ty)
	}
	vx, vy := a.values(x), a.values(y)
	switch {
	case len(vx) == 0 && len(vy) == 0:
		return 0
	case len(vx) == 0:
		return 1
	case len(vy) == 0:
		return -1
	}
	if len(a.order) > 0 {
		return sign * (rank(a.order, vx[0]) - rank(a.order, vy[0]))
	}
	return sign * strings.Compare(strings.ToLower(vx[0]), strings.ToLower(vy[0]))
}

func rank(order []string, v string) int {
	for i, o := range order {
		if o == v {
			return i
		}
	}
	return len(order)
}
