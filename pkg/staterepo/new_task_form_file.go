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

package staterepo

// settings/new-task-form.yml: the New Task form's configuration
// (design/design/config-repo-settings.design.md §6, model.NewTaskForm). It is
// grain_config.new_task_form written as YAML, and read the way
// settings/capabilities.yml is: dumpRowsOf puts what the file says over
// the column tables/ holds, so the file wins wherever it is present. An
// export writes the file exactly when the column is set and removes a
// stale one otherwise.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ai-on-gke/grain/pkg/model"
)

// NewTaskFormFile is the form configuration's name inside SettingsDir.
const NewTaskFormFile = "new-task-form.yml"

// NewTaskFormPath is its path in the repository.
const NewTaskFormPath = SettingsDir + "/" + NewTaskFormFile

// newTaskFormColumn is the grain_config column the file projects onto.
const newTaskFormColumn = "new_task_form"

// readNewTaskFormDoc parses the file, or reports that there is none.
func readNewTaskFormDoc(dir string) (*model.NewTaskForm, error) {
	data, err := os.ReadFile(filepath.Join(dir, SettingsDir, NewTaskFormFile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("staterepo: reading %s: %w", NewTaskFormPath, err)
	}
	return ParseNewTaskFormFile(data)
}

// ParseNewTaskFormFile parses settings/new-task-form.yml, refusing
// unknown keys, and checks its shape (model.NewTaskForm.Validate). An
// empty file is an empty configuration, which draws the form as built.
func ParseNewTaskFormFile(data []byte) (*model.NewTaskForm, error) {
	form := &model.NewTaskForm{}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(form); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("staterepo: parsing %s: %w", NewTaskFormPath, err)
	}
	if err := form.Validate(); err != nil {
		return nil, fmt.Errorf("staterepo: %s: %w", NewTaskFormPath, err)
	}
	return form, nil
}

// overlayNewTaskForm is dumpRowsOf's step for grain_config: the column
// as the file says, when there is a file.
func overlayNewTaskForm(dir, table string, rows []map[string]any, present bool) ([]map[string]any, bool, error) {
	if table != grainConfigTable || !present || len(rows) == 0 {
		return rows, present, nil
	}
	form, err := readNewTaskFormDoc(dir)
	if err != nil || form == nil {
		return rows, present, err
	}
	encoded, err := model.EncodeNewTaskForm(form)
	if err != nil {
		return nil, false, err
	}
	out := make([]map[string]any, len(rows))
	for i, row := range rows {
		cp := copyRow(row)
		cp[newTaskFormColumn] = encoded
		out[i] = cp
	}
	return out, true, nil
}

// exportNewTaskForm writes settings/new-task-form.yml from the database
// when its configuration carries one, and removes the file when it does
// not.
func exportNewTaskForm(ctx context.Context, db querier, dir string) error {
	path := filepath.Join(dir, SettingsDir, NewTaskFormFile)
	data, err := newTaskFormDocumentFor(ctx, db)
	if err != nil {
		return err
	}
	if data == nil {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("staterepo: removing %s: %w", NewTaskFormPath, err)
		}
		return nil
	}
	return writeFileIfChanged(path, data)
}

func newTaskFormDocumentFor(ctx context.Context, db querier) ([]byte, error) {
	rows, err := declTableRows(ctx, db, grainConfigTable)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	text, _ := textOf(rows[0][newTaskFormColumn])
	form, err := model.DecodeNewTaskForm(text)
	if err != nil || form == nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.WriteString(newTaskFormHeader)
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(form); err != nil {
		return nil, fmt.Errorf("staterepo: writing %s: %w", NewTaskFormPath, err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("staterepo: writing %s: %w", NewTaskFormPath, err)
	}
	return buf.Bytes(), nil
}

const newTaskFormHeader = `# The New Task form (design/design/config-repo-settings.design.md §6): which
# fields it shows (shown), tucks under "Advanced options" (more) or
# leaves out and files with their default (hidden), in what order and
# under what labels, and what the capability picker offers and pre-adds.
# Layers override the deployment's own (default) per board, then per
# repository, then per template. Edit it in a pull request; a person
# approves every change to it.
`

// checkNewTaskForm is Check's half of the form: the file parses, names
// only fields the form has, hides nothing the form cannot do without,
// and pre-adds or offers only capabilities the catalogue defines and
// admits to a task of each layer's repository and template.
func checkNewTaskForm(dir string) error {
	form, err := readNewTaskFormDoc(dir)
	if err != nil || form == nil {
		return err
	}
	rows, present, err := dumpRowsOf(dir, grainConfigTable)
	if err != nil {
		return err
	}
	var cat *model.CapabilityCatalogue
	if doc, err := readCapabilitiesDoc(dir); err != nil {
		return err
	} else if doc != nil {
		if cat, err = doc.validate(); err != nil {
			return err
		}
	} else if present && len(rows) > 0 {
		policyText, _ := textOf(rows[0]["capability_policy"])
		if policy, err := model.DecodeCapabilityPolicy(policyText); err == nil {
			cat = policy.Catalogue
		}
	}
	var targets []string
	if present && len(rows) > 0 {
		t, _ := textOf(rows[0]["target_repos"])
		for _, r := range strings.Split(t, ",") {
			if r = strings.TrimSpace(r); r != "" {
				targets = append(targets, strings.ToLower(r))
			}
		}
	}

	var errs []string
	judge := func(where string, l model.NewTaskFormLayer, repo *model.RepoRef, templateID string) {
		if def, ok := l.Fields["repo"].Default.(string); ok && len(targets) > 0 && l.Fields["repo"].Visibility == model.FormFieldHidden {
			if d := strings.ToLower(strings.TrimSpace(def)); d != "none" && !containsFold(targets, d) {
				errs = append(errs, fmt.Sprintf("%s.fields.repo: default %s is not one of the deployment's target repos", where, def))
			}
		}
		if cat == nil || l.Capabilities == nil {
			return
		}
		for _, list := range []struct {
			name   string
			grants *[]string
		}{{"defaults", l.Capabilities.Defaults}, {"offered", l.Capabilities.Offered}} {
			if list.grants == nil {
				continue
			}
			for _, g := range *list.grants {
				if strings.TrimSpace(g) == "*" {
					continue
				}
				if err := cat.FormGrantRefusal(g, repo, templateID); err != nil {
					errs = append(errs, fmt.Sprintf("%s.capabilities.%s: %v", where, list.name, err))
				}
			}
		}
	}
	if form.Default != nil {
		judge("default", *form.Default, nil, "")
	}
	for _, k := range sortedLayerNames(form.Boards) {
		judge("boards."+k, form.Boards[k], nil, "")
	}
	for _, k := range sortedLayerNames(form.Repos) {
		ref, err := model.ParseRepo(k)
		if err != nil {
			continue // Validate has said so
		}
		judge("repos."+k, form.Repos[k], &ref, "")
	}
	for _, k := range sortedLayerNames(form.Templates) {
		judge("templates."+k, form.Templates[k], nil, k)
	}
	if len(errs) > 0 {
		return fmt.Errorf("staterepo: %s: %s", NewTaskFormPath, strings.Join(errs, "; "))
	}
	return nil
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

func sortedLayerNames(m map[string]model.NewTaskFormLayer) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
