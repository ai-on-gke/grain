package cli

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// grain/task-127: the second half of the agreement taskattributes_test.
// go opened.
//
// That file compares the two tables' *shape* -- the same attributes, of
// the same types, taking the same comparisons. This one compares what
// can actually be written with them: every condition and every sort key
// the UI's editor can build (NarrowingBar.jsx, over taskAttributes.js
// and taskFilters.js), spelled the way a link spells it (paths.js), fed
// to the parser `grain list -filter` and `-sort` use. A vocabulary can
// agree on paper and disagree in practice -- an enum whose values are a
// fixed set on one side and a different fixed set on the other, a
// legacy sort word one side answers to, an escape the other does not
// undo -- and each of those is a clause somebody pastes out of the
// address bar and gets an error for.
//
// The table is the frontend's, read off disk: every case below is
// generated from what the editor offers rather than written out here, so
// an attribute or a comparison added there and not here fails without
// anybody remembering to add a row. The reading is by regex for the
// reason taskattributes_test.go gives -- making `go test ./...` depend
// on node would cost far more than this check is worth -- and it fails
// loudly ("this test's patterns need updating") rather than quietly
// reading nothing.
//
// The vocabulary the cases are generated from is one deployment's
// (declaredVocabulary): the built-ins *and* the attributes a declared
// field turns into on either side, since custom.<name> is exactly where
// two surfaces built from one declaration can drift apart, and a clause
// naming one is as pasteable out of a link as any other
// (grain/task-168).

// --- reading the frontend's tables -----------------------------------

// frontendOrder is the fixed vocabulary one descriptor's values come out
// of: a literal list, or STATE_ORDER, which lives with the states
// themselves (state.js) because the sidebar orders by it too.
func frontendOrder(t *testing.T, chunk string) []string {
	t.Helper()
	m := jsAttrOrder.FindStringSubmatch(chunk)
	if m == nil {
		return nil
	}
	raw := strings.TrimSpace(m[1])
	if raw == "STATE_ORDER" {
		return frontendStrings(t, "state.js", "export const STATE_ORDER = [", "];")
	}
	if !strings.HasPrefix(raw, "[") {
		t.Fatalf("an attribute's order is %q, which this test cannot read: its patterns need updating", raw)
	}
	return jsStrings(raw)
}

// jsStrings is every double-quoted string in a fragment of JavaScript,
// in the order they are written -- which is how every list this test
// reads is written.
func jsStrings(fragment string) []string {
	var out []string
	for _, m := range jsString.FindAllStringSubmatch(fragment, -1) {
		out = append(out, m[1])
	}
	return out
}

// frontendStrings is a declared list of strings, by the line that opens
// it and the one that closes it.
func frontendStrings(t *testing.T, file, opening, closing string) []string {
	t.Helper()
	values := jsStrings(jsBlock(t, frontendSource(t, file), opening, closing))
	if len(values) == 0 {
		t.Fatalf("read nothing out of %s's %s: this test's patterns need updating", file, opening)
	}
	return values
}

// jsOperatorInputs is each comparison's id and what it takes on its
// right -- the same OPERATORS table taskattributes_test.go compares
// against this build's, read here as a map because what a case needs is
// "what does `within` take".
func jsOperatorInputs(t *testing.T) map[string]string {
	t.Helper()
	block := jsBlock(t, frontendSource(t, "taskFilters.js"), "export const OPERATORS = {", "};")
	inputs := map[string]string{}
	for _, m := range jsOperator.FindAllStringSubmatch(block, -1) {
		inputs[m[1]] = m[3]
	}
	if len(inputs) == 0 {
		t.Fatal("read no operators at all out of taskFilters.js: this test's patterns need updating")
	}
	return inputs
}

// jsOperatorsByType is which comparisons each type offers, in menu
// order.
func jsOperatorsByType(t *testing.T) map[string][]string {
	t.Helper()
	block := jsBlock(t, frontendSource(t, "taskFilters.js"), "const OPERATORS_BY_TYPE = {", "};")
	byType := map[string][]string{}
	for _, row := range jsByType.FindAllStringSubmatch(block, -1) {
		byType[strings.ToLower(row[1])] = jsStrings(row[2])
	}
	if len(byType) == 0 {
		t.Fatal("read no OPERATORS_BY_TYPE rows at all out of taskFilters.js: this test's patterns need updating")
	}
	return byType
}

// frontendOperatorsFor is taskFilters.js's own operatorsFor over a
// descriptor this test has read: its type's comparisons, plus the two
// about having no value at all where that is a real answer (its
// canBeEmpty, to the letter -- the same rule attribute.canBeEmpty keeps
// on this side).
func frontendOperatorsFor(attr jsAttribute, byType map[string][]string) []string {
	ops := byType[attr.typ]
	if attr.haveNone || attr.typ == string(typeDate) || attr.typ == string(typeNumber) {
		return append(append([]string{}, ops...), "empty", "filled")
	}
	return ops
}

// --- the vocabulary of one deployment --------------------------------

// declaredVocabulary is what the editor offers and what `grain list`
// answers to, for a deployment declaring one field of every shape there
// is (list_custom_test.go's declarations): the frontend's descriptors,
// built-in and custom, and the attributeSet this CLI builds from the
// same declarations.
//
// Both halves come from that one list of declarations, which is what
// makes a custom case worth generating at all: the frontend's half is
// what attributesFor(config) would put in the editor's menus for a
// deployment that declares them, and this CLI's is what `-filter` is
// parsed against once a command line has named something the built-ins
// do not have (cmdList fetches the same declarations from GET
// /api/config). So `custom.priority:any:low` is checked here exactly the
// way `state:any:queued` is -- generated from the editor's side, spelt
// as a link spells it, and read by the parser behind the flag.
func declaredVocabulary(t *testing.T) ([]jsAttribute, attributeSet) {
	t.Helper()
	fields := declarations()
	want := append(frontendAttributes(t), frontendCustomAttributes(t, fields)...)
	return want, attributesFor(fields)
}

// --- what the editor can write ---------------------------------------

// sampleValues is the value part of a condition the editor could
// produce, for one attribute and one comparison: enough to exercise the
// checking `grain list` does before it asks the daemon anything, and
// nothing that depends on a particular deployment's tasks.
//
// A comparison over choices gets one of the attribute's own values --
// the first of a fixed vocabulary, an ordinary-looking one otherwise --
// and, where having none is an answer, the frontend's spelling of it.
// The rest are one value of the shape the editor's control produces: a
// day from its date picker, a count from its number box, some text from
// its text box.
func sampleValues(attr jsAttribute, input string) []string {
	switch input {
	case "choices":
		value := "acme/widgets"
		if len(attr.order) > 0 {
			value = attr.order[0]
		}
		values := []string{value}
		if attr.haveNone {
			values = append(values, noneURLValue)
		}
		return values
	case "text":
		return []string{"flake"}
	case "number":
		return []string{"3"}
	case "date":
		return []string{"2026-09-01"}
	case "days":
		return []string{"7"}
	case "none":
		// paths.js writes a condition with no values as a clause ending
		// in its separator ("created:empty:"); a person types it without
		// one. Both are the same question.
		return []string{"", ""}
	}
	return nil
}

// TestEveryConditionTheEditorCanWriteParses walks the whole cross
// product of the frontend's attributes and the comparisons it offers
// each of them, writes each condition the way paths.js writes one into a
// link, and asks `grain list`'s own parser to read it. A clause the
// editor can build and this CLI answers with "unknown attribute" or
// "takes no comparison" is the drift this whole pair of tables is
// arranged to avoid.
func TestEveryConditionTheEditorCanWriteParses(t *testing.T) {
	t.Parallel()
	inputs := jsOperatorInputs(t)
	byType := jsOperatorsByType(t)
	want, attrs := declaredVocabulary(t)
	for _, attr := range want {
		ops := frontendOperatorsFor(attr, byType)
		if len(ops) == 0 {
			t.Errorf("the editor offers no comparison at all for %s (a %s): this test's patterns need updating", attr.id, attr.typ)
			continue
		}
		for _, op := range ops {
			input, ok := inputs[op]
			if !ok {
				t.Errorf("%s offers the comparison %q, which taskFilters.js's OPERATORS does not declare", attr.id, op)
				continue
			}
			for i, value := range sampleValues(attr, input) {
				raw := fmt.Sprintf("%s:%s:%s", attr.id, op, value)
				if input == "none" && i == 1 {
					raw = fmt.Sprintf("%s:%s", attr.id, op)
				}
				t.Run(raw, func(t *testing.T) {
					cond, err := attrs.parseCondition(raw)
					if err != nil {
						t.Fatalf("the editor can ask this and `grain list -filter %s` cannot: %v", raw, err)
					}
					if cond.attr.id != attr.id || cond.op.id != op {
						t.Fatalf("-filter %s read as %s:%s", raw, cond.attr.id, cond.op.id)
					}
				})
			}
		}
	}
}

// TestAttributeValueVocabulariesMatchTheFrontend is the values
// themselves, for the attributes whose values are a fixed set: the same
// words in the same order.
//
// The words matter because this CLI refuses one it does not know
// (checkValue), so an origin the editor offers and this table has not
// heard of is a menu entry whose -filter equivalent is an error. The
// order matters because it is what `-sort state:asc` orders by -- a
// state's rank is its position in this list on both sides, so two lists
// holding the same words in different orders would print two different
// listings for one sort key.
func TestAttributeValueVocabulariesMatchTheFrontend(t *testing.T) {
	t.Parallel()
	vocabulary, attrs := declaredVocabulary(t)
	for _, want := range vocabulary {
		attr := attrs.byID(want.id)
		if attr == nil {
			if want.custom {
				// Both sides were handed the same declarations, so a
				// declared field with no attribute here is this CLI
				// dropping one rather than a difference of tables.
				t.Errorf("the deployment declares %s and this CLI builds no attribute for it", want.id)
			}
			// Which built-in attributes exist at all is TestTaskAttributes
			// MatchTheFrontend's report to make; nothing to add here.
			continue
		}
		if strings.Join(attr.order, ",") != strings.Join(want.order, ",") {
			t.Errorf("%s takes the values [%s] here and [%s] in the frontend",
				want.id, strings.Join(attr.order, ", "), strings.Join(want.order, ", "))
			continue
		}
		for _, value := range want.order {
			if _, err := attrs.newCondition(want.id, "any", value); err != nil {
				t.Errorf("the editor offers %s %q and this CLI refuses it: %v", want.id, value, err)
			}
		}
	}
}

// --- orders ----------------------------------------------------------

var (
	jsSortDirection = regexp.MustCompile(`(?m)^\s*<MenuItem value="([^"]+)">\{labels\.`)
	jsLegacySort    = regexp.MustCompile(`(?m)^\s*(\w+): \[(.*)\],$`)
	jsSortKey       = regexp.MustCompile(`\{ attr: "([^"]+)", dir: "([^"]+)" \}`)
	jsSeparators    = regexp.MustCompile(`const SEPARATORS = /\[([^\]]*)\]/g;`)
)

// frontendSortDirections is the two ways the editor's own direction menu
// can point (NarrowingBar.jsx's SortRow).
func frontendSortDirections(t *testing.T) []string {
	t.Helper()
	block := jsBlock(t, frontendSource(t, "components/NarrowingBar.jsx"), "function SortRow(", "}")
	var dirs []string
	for _, m := range jsSortDirection.FindAllStringSubmatch(block, -1) {
		dirs = append(dirs, m[1])
	}
	if len(dirs) == 0 {
		t.Fatal("read no sort directions at all out of NarrowingBar.jsx: this test's patterns need updating")
	}
	return dirs
}

// TestEverySortKeyTheEditorCanWriteParses is the sort's half of the
// cross product: the editor offers every attribute as a sort key, in
// either direction (SortRow draws one menu of all of them and one of the
// two directions), and every one of those keys is a `-sort` this command
// takes.
func TestEverySortKeyTheEditorCanWriteParses(t *testing.T) {
	t.Parallel()
	dirs := frontendSortDirections(t)
	vocabulary, attrs := declaredVocabulary(t)
	for _, attr := range vocabulary {
		for _, dir := range dirs {
			key := attr.id + ":" + dir
			t.Run(key, func(t *testing.T) {
				specs, err := attrs.parseSort(key)
				if err != nil {
					t.Fatalf("the editor can order by this and `grain list -sort %s` cannot: %v", key, err)
				}
				if len(specs) != 1 || specs[0].attr.id != attr.id || specs[0].desc != (dir == "desc") {
					t.Fatalf("-sort %s read as %v", key, specs)
				}
			})
		}
	}
	// And several of them at once, in the comma-separated spelling both
	// sides write: the whole vocabulary as one order, which is what a
	// link carrying every tie-breaker somebody set would say.
	var keys []string
	for _, attr := range vocabulary {
		keys = append(keys, attr.id+":asc")
	}
	joined := strings.Join(keys, ",")
	specs, err := attrs.parseSort(joined)
	if err != nil {
		t.Fatalf("-sort %s: %v", joined, err)
	}
	if len(specs) != len(keys) {
		t.Fatalf("-sort over %d keys read as %d", len(keys), len(specs))
	}
}

// TestLegacySortsMatchTheFrontend is the named orders. Both sides keep a
// table of the words the old one-Select toolbar wrote, so a link
// somebody kept still opens the order it names (paths.js's LEGACY_SORTS)
// and a script that says `-sort newest` goes on printing what it printed
// (taskfilters.go's legacySorts). They are two tables of the same words,
// and a word meaning one order in a link and another on a command line
// would be worse than either side not having it.
func TestLegacySortsMatchTheFrontend(t *testing.T) {
	t.Parallel()
	block := jsBlock(t, frontendSource(t, "paths.js"), "const LEGACY_SORTS = {", "};")
	rows := jsLegacySort.FindAllStringSubmatch(block, -1)
	if len(rows) == 0 {
		t.Fatal("read no legacy sorts at all out of paths.js: this test's patterns need updating")
	}
	for _, row := range rows {
		name := row[1]
		var want []string
		for _, key := range jsSortKey.FindAllStringSubmatch(row[2], -1) {
			want = append(want, key[1]+":"+key[2])
		}
		t.Run(name, func(t *testing.T) {
			specs, err := builtinAttributes.parseSort(name)
			if err != nil {
				t.Fatalf("a link can say sort=%s and `grain list -sort %s` cannot: %v", name, name, err)
			}
			var got []string
			for _, s := range specs {
				dir := "asc"
				if s.desc {
					dir = "desc"
				}
				got = append(got, s.attr.id+":"+dir)
			}
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("%q is [%s] here and [%s] in the frontend", name, strings.Join(got, ", "), strings.Join(want, ", "))
			}
		})
	}
}

// TestLegacyFilterParametersNameAttributesThisCLIHas is the other half
// of what an old link carries: the parameters the one-menu-per-attribute
// toolbar wrote ("/?repo=acme/widgets"), each of which the frontend
// still reads as a condition on the attribute of that name. Every one of
// them has to be an attribute here too, or a link that old is pasteable
// into a browser and not into this command.
func TestLegacyFilterParametersNameAttributesThisCLIHas(t *testing.T) {
	t.Parallel()
	for _, id := range frontendStrings(t, "paths.js", "const LEGACY_FILTERS = [", "];") {
		if builtinAttributes.byID(id) == nil {
			t.Errorf("an old link can narrow by %q and this CLI has no such attribute: one of %s", id, strings.Join(builtinAttributes.ids(), ", "))
		}
	}
}

// --- escaping --------------------------------------------------------

// TestTheFrontendsEscapingIsUnderstood pins the one thing a value has to
// survive to cross between the two surfaces: a character that would
// otherwise be read as a separator, written %XX by paths.js's
// escapeValue and read back by unescapeValue here.
//
// The characters are read off paths.js's own SEPARATORS rather than
// listed, so widening that set on the frontend -- a separator this side
// then would not undo -- fails here.
func TestTheFrontendsEscapingIsUnderstood(t *testing.T) {
	t.Parallel()
	m := jsSeparators.FindStringSubmatch(frontendSource(t, "paths.js"))
	if m == nil {
		t.Fatal("cannot find SEPARATORS in paths.js: this test's patterns need updating")
	}
	separators := m[1]
	if !strings.Contains(separators, "%") {
		t.Errorf("paths.js escapes [%s], which does not include the escape character itself", separators)
	}
	for _, c := range separators {
		// One value carrying the separator, escaped exactly as
		// escapeValue writes it, in the condition a link would hold.
		value := fmt.Sprintf("acme%%%02X widgets", c)
		raw := "repo:any:" + value
		cond, err := builtinAttributes.parseCondition(raw)
		if err != nil {
			t.Errorf("-filter %s: %v", raw, err)
			continue
		}
		want := fmt.Sprintf("acme%c widgets", c)
		if len(cond.values) != 1 || cond.values[0] != want {
			t.Errorf("-filter %s read as %q, want the one value %q", raw, cond.values, want)
		}
	}
}
