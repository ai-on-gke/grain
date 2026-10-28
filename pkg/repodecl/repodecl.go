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

// Package repodecl reads the templates, suites and schedules a
// repository declares in its own `.grain` directory (grain/task-60).
//
// It is pkg/ci's LoadDir one layer up, and deliberately its neighbour in
// shape: one directory per kind, one file per thing, YAML with unknown
// keys refused so a misspelt field is an error naming the key rather
// than a setting that silently did nothing. What it produces is ordinary
// model.Template, model.Suite and model.Schedule values -- everything
// that fires, composes or renders one goes on working with no idea a
// repository wrote it down.
//
// What it will not produce is the point. model's repo_declaration.go has
// the whole argument; the short of it is that these files are written in
// a repository every agent grain dispatches there can rewrite, so a
// declaration may say what work to do in *this* repository and when, and
// may not say what that work is allowed to touch. Every field that would
// cross that line -- a target repo, reads, writes, capabilities -- is
// refused here by not existing, and the refusal is a parse error naming
// the key rather than a quiet drop.
package repodecl

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
)

// Load reads everything repo's tree declares.
//
// tree is the repository's root, as an fs.FS so that the same function
// serves a real checkout (os.DirFS over a clone), a test's fstest.MapFS
// and anything else with a file tree -- ci.LoadDir's own reasoning, and
// the reason `grain declarations check` can read a working tree before
// anything is pushed.
//
// repo is what the declarations belong to, and is not read from any
// file: it decides the ids (model.RepoDeclaredID) and it is the target
// of every task they file, which is exactly why no file gets a say in
// it.
//
// A repository with no `.grain` directory, or with none of the three
// sub-directories, declares nothing and is not an error -- which is the
// state very nearly every repository is in.
//
// Errors are joined rather than returned one at a time: somebody who has
// just written three declaration files wants all three verdicts, not the
// first one and then a rebuild. A tree with any error at all produces no
// declarations, because a suite naming a template that failed to parse
// is not a suite anybody meant to run.
func Load(tree fs.FS, repo model.RepoRef) (model.RepoDeclarations, error) {
	templateFiles, err := readDir(tree, model.TemplateDeclarationDir)
	if err != nil {
		return model.RepoDeclarations{}, err
	}
	suiteFiles, err := readDir(tree, model.SuiteDeclarationDir)
	if err != nil {
		return model.RepoDeclarations{}, err
	}
	scheduleFiles, err := readDir(tree, model.ScheduleDeclarationDir)
	if err != nil {
		return model.RepoDeclarations{}, err
	}

	var reviewFiles []file
	for _, ext := range declarationExts {
		p := model.DeclarationDir + "/review" + ext
		if text, err := fs.ReadFile(tree, p); err == nil {
			reviewFiles = append(reviewFiles, file{path: p, stem: model.DefaultReviewStem, text: text})
		}
	}

	var declared model.RepoDeclarations
	var errs []error

	if len(reviewFiles) > 1 {
		errs = append(errs, fmt.Errorf("%s: %s already declares %q -- one file per declaration",
			reviewFiles[1].path, reviewFiles[0].path, model.DefaultReviewStem))
	}

	settings, err := LoadSettings(tree, repo)
	if err != nil {
		errs = append(errs, err)
	} else {
		declared.Settings = settings
	}

	// Templates first, then suites, then schedules: each layer names
	// things in the ones before it by file stem, so this order is also
	// the order in which the names can be resolved. known carries what
	// has been declared so far, which is what makes "no such template"
	// an error at the file that says it rather than a dangling id
	// discovered by a firing weeks later.
	templates := map[string]bool{}
	for _, file := range templateFiles {
		tmpl, push, err := parseTemplate(file, repo)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		templates[file.stem] = true
		declared.Templates = append(declared.Templates, model.DeclaredTemplateFile{Path: file.path, Template: tmpl, Push: push})
	}

	if len(reviewFiles) == 1 {
		file := reviewFiles[0]
		if templates[model.DefaultReviewStem] {
			var existingPath string
			for _, tf := range templateFiles {
				if tf.stem == model.DefaultReviewStem {
					existingPath = tf.path
					break
				}
			}
			errs = append(errs, fmt.Errorf("%s: %s already declares %q -- one file per declaration",
				file.path, existingPath, model.DefaultReviewStem))
		} else {
			tmpl, push, err := parseTemplate(file, repo)
			if err != nil {
				errs = append(errs, err)
			} else {
				templates[model.DefaultReviewStem] = true
				declared.Templates = append(declared.Templates, model.DeclaredTemplateFile{Path: file.path, Template: tmpl, Push: push})
			}
		}
	}
	sort.Slice(declared.Templates, func(i, j int) bool { return declared.Templates[i].Path < declared.Templates[j].Path })

	suites := map[string]bool{}
	for _, file := range suiteFiles {
		suite, err := parseSuite(file, repo, templates)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		suites[file.stem] = true
		declared.Suites = append(declared.Suites, model.DeclaredSuiteFile{Path: file.path, Suite: suite})
	}

	// The repository's own workflows, read once: a schedule file may
	// name one to run (`workflow:`), and every cron in them is a schedule
	// of its own (workflows.go). A tree whose workflows do not parse is
	// an error for the whole repository either way, so a schedule naming
	// one is not also told it does not exist.
	workflows, workflowErr := ci.LoadNativeDir(tree)

	for _, file := range scheduleFiles {
		sched, err := parseSchedule(file, repo, templates, suites, workflows, workflowErr == nil)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		declared.Schedules = append(declared.Schedules, model.DeclaredScheduleFile{Path: file.path, Schedule: sched})
	}

	triggers, err := loadTriggers(tree, repo, templates)
	if err != nil {
		errs = append(errs, err)
	}
	declared.Triggers = triggers

	mergeRules, err := LoadMergeRules(tree)
	if err != nil {
		errs = append(errs, err)
	}
	declared.MergeRules = mergeRules

	// And the schedules a repository declares without meaning to name one:
	// a workflow's own `on: schedule` cron (workflows.go).
	fromWorkflows, err := scheduledWorkflows(workflows, repo)
	if workflowErr != nil {
		errs = append(errs, workflowErr)
	} else if err != nil {
		errs = append(errs, err)
	}
	for _, sched := range fromWorkflows {
		if first, ok := scheduledIn(declared.Schedules, sched.Schedule.ID); ok {
			errs = append(errs, fmt.Errorf("%s: %s already declares the schedule %q -- rename one of them",
				sched.Path, first, sched.Schedule.ID))
			continue
		}
		declared.Schedules = append(declared.Schedules, sched)
	}

	if len(errs) > 0 {
		return model.RepoDeclarations{}, errors.Join(errs...)
	}
	return declared, nil
}

// scheduledIn is the file an id has already been declared by, for the one
// collision two sources of schedules can have: `.grain/schedules/x.yml` and
// a workflow whose derived stem is also x (workflowSchedulePrefix keeps that
// to a file somebody has gone out of their way to name). One id written
// twice is one row, with whichever was read last winning, so it is an error
// naming both files instead.
func scheduledIn(schedules []model.DeclaredScheduleFile, id string) (string, bool) {
	for _, declared := range schedules {
		if declared.Schedule.ID == id {
			return declared.Path, true
		}
	}
	return "", false
}

// file is one declaration file: where it was, what it is called, and
// what is in it.
type file struct {
	path string
	stem string
	text []byte
}

// declarationExts is what a declaration file may be called. `.yml` and
// `.yaml` both, because both are what people type, and nothing else:
// a directory of one file per declaration has no room for a README or an
// editor's backup to be mistaken for one, and skipping files by
// extension the way ci.LoadDir does would silently ignore a
// `nightly.yml.txt` somebody meant to be live.
var declarationExts = []string{".yml", ".yaml"}

// readDir reads one kind's directory, ordered by path so that a tree
// produces the same declarations in the same order every time.
func readDir(tree fs.FS, dir string) ([]file, error) {
	entries, err := fs.ReadDir(tree, dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}
	var out []file
	var errs []error
	stems := map[string]string{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			errs = append(errs, fmt.Errorf("%s/%s: %s holds one file per declaration, not a tree",
				dir, name, dir))
			continue
		}
		ext := strings.ToLower(path.Ext(name))
		if !slices.Contains(declarationExts, ext) {
			errs = append(errs, fmt.Errorf("%s/%s: a declaration file is .yml or .yaml", dir, name))
			continue
		}
		stem := strings.TrimSuffix(name, ext)
		if err := model.ValidateDeclarationStem(stem); err != nil {
			errs = append(errs, fmt.Errorf("%s/%s: %w", dir, name, err))
			continue
		}
		if first, ok := stems[stem]; ok {
			// Two files, one identity: the stem is what a suite names a
			// template by and what the row's id is derived from, so
			// nightly.yml and nightly.yaml would be one declaration
			// written twice, with whichever was read last winning.
			errs = append(errs, fmt.Errorf("%s/%s: %s already declares %q -- one file per declaration",
				dir, name, first, stem))
			continue
		}
		stems[stem] = dir + "/" + name
		text, err := fs.ReadFile(tree, dir+"/"+name)
		if err != nil {
			return nil, fmt.Errorf("reading %s/%s: %w", dir, name, err)
		}
		out = append(out, file{path: dir + "/" + name, stem: stem, text: text})
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out, nil
}
