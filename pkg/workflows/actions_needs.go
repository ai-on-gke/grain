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

package workflows

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// This file is what `needs:` means for a workflow read out of
// `.github/workflows`, and it exists because the first answer -- refuse
// the whole file -- was the wrong one.
//
// A grain job is one sandbox running one `act --job <id>` invocation, so
// two jobs of an Actions workflow are two machines: what `needs` can mean
// here is the ordering and the failure propagation the engine already
// implements for native jobs (engine.go's `ready`), and what it cannot
// mean is a value or a file travelling from one job to the next. Reading
// `needs` as ordering is worth having on its own -- it is what almost
// every `needs:` in the wild is for, and a repository that writes one
// should not have to choose between GitHub and grain.
//
// Two things stand in the way, and both are handled here rather than left
// to fail somewhere a reader cannot see:
//
//   - act resolves `needs` itself. `act --job image` on a workflow where
//     `image` needs `test` runs *test and then image* in that one
//     invocation, so grain ordering the jobs and act expanding them would
//     run every needed job twice -- once as its own grain job and again
//     inside each dependant's. So each job is handed a copy of its
//     workflow with its own `needs:` commented out (pruneActionsNeeds),
//     and act is left with exactly the one job grain asked for.
//
//   - a job that reads `${{ needs.<job>.outputs.… }}`, or downloads an
//     artifact another job uploaded, is asking for the half that separate
//     sandboxes cannot give. Those are refused by name at parse
//     (checkActionsNeedsIsOrderingOnly), which makes a run of such a file
//     a workflow definition error naming the file, the job and the key --
//     not a green with the value silently empty.

// actionsNeedsContext matches a `${{ … needs … }}` expression: the
// `needs` context, which is a needed job's `outputs` and `result`, and is
// the half of GitHub's `needs` that two sandboxes cannot carry between
// them.
//
// Deliberately loose about what follows the word -- `needs.build.outputs.x`
// and `needs['build']` are both it -- because the cost of matching one
// expression too many is an error message telling somebody to fold two
// jobs together, and the cost of matching one too few is a job reading an
// empty string and passing.
var actionsNeedsContext = regexp.MustCompile(`\$\{\{[^}]*\bneeds\b`)

// downloadArtifactAction is the step that reaches for a file another job
// produced. `actions/download-artifact` by any version, and GitHub's
// `@v4` suffix is not matched on so a new major is covered the day it
// ships rather than the day somebody remembers this line.
const downloadArtifactAction = "actions/download-artifact"

// checkActionsNeedsIsOrderingOnly reports the parts of GitHub's `needs`
// that grain cannot honour, for a job that declares one. Ordering and
// "do not run me if it failed" are honoured; a value or a file crossing
// between two jobs is not, and is an error here rather than an empty
// string in the job.
//
// job is the job's own node, whole, rather than the handful of keys
// rawGitHubJob decodes: what reads `needs.…` may be a step's `run`, a
// step's `with`, the job's `if`, its `env`, or an expression in a
// `strategy` -- all of which are act's business and none of which grain
// models.
func checkActionsNeedsIsOrderingOnly(filePath, id string, needs []string, job *yaml.Node) error {
	if len(needs) == 0 || job == nil {
		return nil
	}
	var expression, download string
	walkYAML(job, func(n *yaml.Node) {
		if n.Kind != yaml.ScalarNode {
			return
		}
		if expression == "" && actionsNeedsContext.MatchString(n.Value) {
			expression = strings.TrimSpace(n.Value)
		}
	})
	walkYAMLMappings(job, func(key, value *yaml.Node) {
		if download == "" && key.Value == "uses" && value.Kind == yaml.ScalarNode &&
			strings.HasPrefix(strings.TrimSpace(value.Value), downloadArtifactAction) {
			download = strings.TrimSpace(value.Value)
		}
	})
	switch {
	case expression != "":
		return fmt.Errorf("ci: %s: jobs.%s: needs %s, and reads the `needs` context (%s): "+
			"grain runs each job of an Actions workflow in a sandbox of its own, so `needs` orders the jobs "+
			"and a needed job's outputs and result do not travel to this one -- "+
			"fold the two jobs into one, or have this job work the value out for itself",
			filePath, id, quotedList(needs), firstLine(expression))
	case download != "":
		return fmt.Errorf("ci: %s: jobs.%s: needs %s, and downloads an artifact (`uses: %s`): "+
			"grain runs each job of an Actions workflow in a sandbox of its own and destroys it when the job ends, "+
			"so a file one job uploaded is not there for the next one to download -- "+
			"fold the two jobs into one, or have this job rebuild what it needs",
			filePath, id, quotedList(needs), download)
	}
	return nil
}

// pruneActionsNeeds returns data with `jobs.<id>.needs` commented out,
// which is the file act is given for this job: grain has already decided
// the order, and act asked for one job must not go and run that job's
// dependencies over again.
//
// The lines are commented rather than deleted, and nothing else in the
// file is touched: every other line keeps its number, so a line act
// reports an error on is the line the author is looking at in their own
// checkout.
//
// It verifies its own work before returning it -- the pruned text is
// re-parsed and compared, node for node, against the original document
// with that one key removed (yamlEqual). A `needs:` written somewhere
// this cannot safely cut, a flow mapping holding the whole job on one
// line, is then a named error at parse rather than a mangled workflow
// handed to act.
func pruneActionsNeeds(filePath, id string, data []byte) (string, error) {
	doc, err := documentNode(data)
	if err != nil {
		return "", fmt.Errorf("ci: %s: jobs.%s: %w", filePath, id, err)
	}
	job := mappingValue(mappingValue(doc, "jobs"), id)
	key, value := mappingEntry(job, "needs")
	if key == nil {
		return string(data), nil
	}
	first, last := key.Line, lastLine(value)
	lines := strings.Split(string(data), "\n")
	if first < 1 || last < first || last > len(lines) {
		return "", fmt.Errorf("ci: %s: jobs.%s: grain cannot tell which lines `needs:` occupies, so it cannot run this job on its own", filePath, id)
	}
	for i := first - 1; i < last; i++ {
		lines[i] = "#" + lines[i]
	}
	pruned := strings.Join(lines, "\n")

	want, err := documentNode(data)
	if err != nil {
		return "", fmt.Errorf("ci: %s: jobs.%s: %w", filePath, id, err)
	}
	removeMappingEntry(mappingValue(mappingValue(want, "jobs"), id), "needs")
	got, err := documentNode([]byte(pruned))
	if err != nil || !yamlEqual(want, got) {
		return "", fmt.Errorf("ci: %s: jobs.%s: grain cannot take `needs:` out of this job without changing the rest of it, "+
			"so it cannot run the job on its own -- write `needs:` on a line of its own", filePath, id)
	}
	return pruned, nil
}

// documentNode decodes one YAML document and returns its root value
// node, skipping the document wrapper so callers work in the mapping the
// file actually is. An empty document gives a nil node, which every
// helper here treats as "no such key".
func documentNode(data []byte) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, unwrapYAML(err)
	}
	if doc.Kind == yaml.DocumentNode {
		if len(doc.Content) == 0 {
			return nil, nil
		}
		return doc.Content[0], nil
	}
	return &doc, nil
}

// mappingEntry returns the key and value nodes of one entry of a
// mapping, or two nils where the mapping or the key is absent.
func mappingEntry(node *yaml.Node, key string) (*yaml.Node, *yaml.Node) {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i], node.Content[i+1]
		}
	}
	return nil, nil
}

// mappingValue is mappingEntry's value half, for the lookups that only
// want to descend.
func mappingValue(node *yaml.Node, key string) *yaml.Node {
	_, value := mappingEntry(node, key)
	return value
}

// removeMappingEntry drops one key and its value from a mapping, in
// place. It is what pruneActionsNeeds compares its own textual edit
// against.
func removeMappingEntry(node *yaml.Node, key string) {
	if node == nil || node.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content = append(node.Content[:i:i], node.Content[i+2:]...)
			return
		}
	}
}

// lastLine is the highest line number anything under node sits on: where
// a key's value ends, which for `needs` is either the key's own line or
// the last item of the list under it.
func lastLine(node *yaml.Node) int {
	if node == nil {
		return 0
	}
	last := node.Line
	for _, child := range node.Content {
		if l := lastLine(child); l > last {
			last = l
		}
	}
	return last
}

// walkYAML calls fn for node and everything under it.
func walkYAML(node *yaml.Node, fn func(*yaml.Node)) {
	if node == nil {
		return
	}
	fn(node)
	for _, child := range node.Content {
		walkYAML(child, fn)
	}
}

// walkYAMLMappings calls fn for every key/value pair of every mapping
// under node, which is how a step is recognised by the key it was
// written under rather than by its position.
func walkYAMLMappings(node *yaml.Node, fn func(key, value *yaml.Node)) {
	walkYAML(node, func(n *yaml.Node) {
		if n.Kind != yaml.MappingNode {
			return
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			fn(n.Content[i], n.Content[i+1])
		}
	})
}

// yamlEqual compares two documents by what they mean rather than by how
// they were written: kind, tag, scalar value and children, and nothing
// about lines, quoting, indentation or comments. It is the whole of
// pruneActionsNeeds' safety check, so it compares the tag as well as the
// value -- `needs: [1]` and `needs: ["1"]` are different documents and a
// prune that turned one into the other would be a prune that changed the
// file.
func yamlEqual(a, b *yaml.Node) bool {
	switch {
	case a == nil || b == nil:
		return a == b
	case a.Kind != b.Kind, a.Tag != b.Tag, a.Value != b.Value, len(a.Content) != len(b.Content):
		return false
	}
	for i := range a.Content {
		if !yamlEqual(a.Content[i], b.Content[i]) {
			return false
		}
	}
	return true
}

// quotedList writes a `needs:` back the way an error message wants to
// read it: `"build"`, or `"build" and "lint"`.
func quotedList(names []string) string {
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		quoted = append(quoted, fmt.Sprintf("%q", name))
	}
	switch len(quoted) {
	case 0:
		return "nothing"
	case 1:
		return quoted[0]
	}
	return strings.Join(quoted[:len(quoted)-1], ", ") + " and " + quoted[len(quoted)-1]
}

// firstLine keeps an error message on one line, which is where grain
// errors are read (a CLI row, a JSON field, a task comment). A `run:`
// script that mentions the needs context can be a page long, and the
// first line of it is enough to find.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i]) + " …"
	}
	return s
}
