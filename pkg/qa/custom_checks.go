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

package qa

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

type CustomCheckDecl struct {
	ID              string                 `json:"id" yaml:"id"`
	Label           string                 `json:"label" yaml:"label"`
	Kind            string                 `json:"kind" yaml:"kind"`
	Engine          string                 `json:"engine" yaml:"engine"` // alias for kind
	Severity        string                 `json:"severity" yaml:"severity"`
	Message         string                 `json:"message" yaml:"message"`
	Paths           []string               `json:"paths,omitempty" yaml:"paths,omitempty"`
	Exclude         []string               `json:"exclude,omitempty" yaml:"exclude,omitempty"`
	ForbidAdded     string                 `json:"forbid-added,omitempty" yaml:"forbid-added,omitempty"`
	RequireAdded    string                 `json:"require-added,omitempty" yaml:"require-added,omitempty"`
	MaxChangedLines int                    `json:"max-changed-lines,omitempty" yaml:"max-changed-lines,omitempty"`
	MaxFiles        int                    `json:"max-files,omitempty" yaml:"max-files,omitempty"`
	WhenChanged     []string               `json:"when-changed,omitempty" yaml:"when-changed,omitempty"`
	RequireChanged  []string               `json:"require-changed,omitempty" yaml:"require-changed,omitempty"`
	AllowWaiver     bool                   `json:"allow-waiver,omitempty" yaml:"allow-waiver,omitempty"`
	SchemaJSON      map[string]interface{} `json:"schema-json,omitempty" yaml:"schema-json,omitempty"`
	Fixtures        *CustomCheckFixtures   `json:"fixtures" yaml:"fixtures"`
}

type CustomCheckFixtures struct {
	Fail []CustomCheckFixture `json:"fail" yaml:"fail"`
	Pass []CustomCheckFixture `json:"pass" yaml:"pass"`
}

type CustomCheckFixture struct {
	Name         string   `json:"name" yaml:"name"`
	File         string   `json:"file,omitempty" yaml:"file,omitempty"`
	AddedLines   []string `json:"added-lines,omitempty" yaml:"added-lines,omitempty"`
	ChangedFiles []string `json:"changed-files,omitempty" yaml:"changed-files,omitempty"`
	Content      string   `json:"content,omitempty" yaml:"content,omitempty"`
	Diff         string   `json:"diff,omitempty" yaml:"diff,omitempty"`
}

type CustomCheckFinding struct {
	RuleID   string
	Severity string
	FilePath string
	Line     int
	Summary  string
	Evidence string
}

type CustomCheckContext struct {
	ChangedFiles    []string
	AddedLines      map[string][]string // filepath -> lines
	TotalLinesAdded int
	CallerNotes     string
	ReadFile        func(path string) ([]byte, error)
}

func matchPath(path string, patterns, excludes []string) bool {
	matched := false
	if len(patterns) == 0 {
		matched = true
	}
	for _, p := range patterns {
		if matchedGlob(p, path) {
			matched = true
			break
		}
	}
	if !matched {
		return false
	}
	for _, p := range excludes {
		if matchedGlob(p, path) {
			return false
		}
	}
	return true
}

func EvaluateCustomCheck(check CustomCheckDecl, ctx CustomCheckContext) ([]CustomCheckFinding, error) {
	kind := check.Kind
	if kind == "" {
		kind = check.Engine
	}
	severity := check.Severity
	if severity == "" {
		severity = "warn"
	}

	var findings []CustomCheckFinding

	switch kind {
	case "pattern":
		var forbid, require *regexp.Regexp
		if check.ForbidAdded != "" {
			if len(check.ForbidAdded) > 512 {
				return nil, fmt.Errorf("forbid-added regex exceeds 512 bytes")
			}
			re, err := regexp.Compile(check.ForbidAdded)
			if err != nil {
				return nil, fmt.Errorf("invalid forbid-added regex: %w", err)
			}
			forbid = re
		}
		if check.RequireAdded != "" {
			if len(check.RequireAdded) > 512 {
				return nil, fmt.Errorf("require-added regex exceeds 512 bytes")
			}
			re, err := regexp.Compile(check.RequireAdded)
			if err != nil {
				return nil, fmt.Errorf("invalid require-added regex: %w", err)
			}
			require = re
		}

		for file, lines := range ctx.AddedLines {
			if file != "" && !matchPath(file, check.Paths, check.Exclude) {
				continue
			}
			foundRequire := false
			for i, line := range lines {
				if forbid != nil && forbid.MatchString(line) {
					findings = append(findings, CustomCheckFinding{
						RuleID:   check.ID,
						Severity: severity,
						FilePath: file,
						Line:     i + 1,
						Summary:  check.Message,
						Evidence: line,
					})
				}
				if require != nil && require.MatchString(line) {
					foundRequire = true
				}
			}
			if require != nil && !foundRequire && len(lines) > 0 {
				findings = append(findings, CustomCheckFinding{
					RuleID:   check.ID,
					Severity: severity,
					FilePath: file,
					Line:     1,
					Summary:  check.Message,
				})
			}
		}

	case "diff-budget":
		matchedFiles := 0
		linesAdded := 0
		for _, file := range ctx.ChangedFiles {
			if matchPath(file, check.Paths, check.Exclude) {
				matchedFiles++
				linesAdded += len(ctx.AddedLines[file])
			}
		}
		if len(ctx.ChangedFiles) == 0 && len(ctx.AddedLines[""]) > 0 {
			linesAdded += len(ctx.AddedLines[""])
		}
		if check.MaxFiles > 0 && matchedFiles > check.MaxFiles {
			findings = append(findings, CustomCheckFinding{
				RuleID:   check.ID,
				Severity: severity,
				Summary:  fmt.Sprintf("%s (budget: %d files, changed: %d files)", check.Message, check.MaxFiles, matchedFiles),
			})
		}
		if check.MaxChangedLines > 0 && linesAdded > check.MaxChangedLines {
			findings = append(findings, CustomCheckFinding{
				RuleID:   check.ID,
				Severity: severity,
				Summary:  fmt.Sprintf("%s (budget: %d lines, changed: %d lines)", check.Message, check.MaxChangedLines, linesAdded),
			})
		}

	case "companion":
		if severity == "warn" && strings.TrimSpace(ctx.CallerNotes) != "" {
			break
		}
		if check.AllowWaiver && strings.TrimSpace(ctx.CallerNotes) != "" {
			break
		}

		hasWhen := false
		for _, f := range ctx.ChangedFiles {
			for _, w := range check.WhenChanged {
				if matchedGlob(w, f) {
					hasWhen = true
					break
				}
			}
			if hasWhen {
				break
			}
		}

		if hasWhen {
			hasRequire := false
			for _, f := range ctx.ChangedFiles {
				for _, r := range check.RequireChanged {
					if matchedGlob(r, f) {
						hasRequire = true
						break
					}
				}
				if hasRequire {
					break
				}
			}
			if !hasRequire {
				findings = append(findings, CustomCheckFinding{
					RuleID:   check.ID,
					Severity: severity,
					Summary:  check.Message,
				})
			}
		}

	case "jsonschema":
		if check.SchemaJSON == nil {
			return nil, fmt.Errorf("jsonschema engine requires schema-json")
		}
		for _, file := range ctx.ChangedFiles {
			if !matchPath(file, check.Paths, check.Exclude) {
				continue
			}
			if ctx.ReadFile == nil {
				continue
			}
			content, err := ctx.ReadFile(file)
			if err != nil {
				findings = append(findings, CustomCheckFinding{
					RuleID:   check.ID,
					Severity: severity,
					FilePath: file,
					Summary:  fmt.Sprintf("failed to read file: %v", err),
				})
				continue
			}
			if len(content) == 0 {
				continue
			}
			var data interface{}
			if err := json.Unmarshal(content, &data); err != nil {
				var yamlData interface{}
				if yErr := yaml.Unmarshal(content, &yamlData); yErr == nil {
					if rawJ, mErr := json.Marshal(yamlData); mErr == nil && json.Unmarshal(rawJ, &data) == nil {
						err = nil
					}
				}
				if err != nil {
					findings = append(findings, CustomCheckFinding{
						RuleID:   check.ID,
						Severity: severity,
						FilePath: file,
						Summary:  fmt.Sprintf("failed to parse JSON/YAML: %v", err),
					})
					continue
				}
			}
			if err := validateJSONSchema(data, check.SchemaJSON, 0); err != nil {
				findings = append(findings, CustomCheckFinding{
					RuleID:   check.ID,
					Severity: severity,
					FilePath: file,
					Summary:  fmt.Sprintf("%s: %v", check.Message, err),
				})
			}
		}

	default:
		return nil, fmt.Errorf("unknown custom check engine %q", kind)
	}

	return findings, nil
}

const maxJSONSchemaDepth = 32

func validateJSONSchemaStructure(schema map[string]interface{}, depth int) error {
	if depth > maxJSONSchemaDepth {
		return fmt.Errorf("schema nesting exceeds maximum depth %d", maxJSONSchemaDepth)
	}
	if props, ok := schema["properties"].(map[string]interface{}); ok {
		for k, v := range props {
			if sub, okS := v.(map[string]interface{}); okS {
				if err := validateJSONSchemaStructure(sub, depth+1); err != nil {
					return fmt.Errorf("property %q: %w", k, err)
				}
			}
		}
	}
	if items, ok := schema["items"].(map[string]interface{}); ok {
		if err := validateJSONSchemaStructure(items, depth+1); err != nil {
			return fmt.Errorf("items: %w", err)
		}
	}
	return nil
}

func validateJSONSchema(data interface{}, schema map[string]interface{}, depth int) error {
	if depth > maxJSONSchemaDepth {
		return fmt.Errorf("maximum schema depth %d exceeded", maxJSONSchemaDepth)
	}
	t, _ := schema["type"].(string)
	switch t {
	case "object":
		obj, ok := data.(map[string]interface{})
		if !ok {
			return fmt.Errorf("expected object")
		}
		if req, ok := schema["required"].([]interface{}); ok {
			for _, r := range req {
				if rk, okS := r.(string); okS {
					if _, exists := obj[rk]; !exists {
						return fmt.Errorf("missing required property %q", rk)
					}
				}
			}
		}
		props, _ := schema["properties"].(map[string]interface{})
		if addl, hasAddl := schema["additionalProperties"].(bool); hasAddl && !addl && props != nil {
			for k := range obj {
				if _, allowed := props[k]; !allowed {
					return fmt.Errorf("disallowed additional property %q", k)
				}
			}
		}
		for k, v := range props {
			if val, exists := obj[k]; exists {
				if sub, okS := v.(map[string]interface{}); okS {
					if err := validateJSONSchema(val, sub, depth+1); err != nil {
						return fmt.Errorf("property %q invalid: %v", k, err)
					}
				}
			}
		}
	case "array":
		arr, ok := data.([]interface{})
		if !ok {
			return fmt.Errorf("expected array")
		}
		if items, okI := schema["items"].(map[string]interface{}); okI {
			for idx, elem := range arr {
				if err := validateJSONSchema(elem, items, depth+1); err != nil {
					return fmt.Errorf("item[%d] invalid: %v", idx, err)
				}
			}
		}
	case "string":
		s, ok := data.(string)
		if !ok {
			return fmt.Errorf("expected string")
		}
		if pat, okP := schema["pattern"].(string); okP && pat != "" {
			re, err := regexp.Compile(pat)
			if err != nil || !re.MatchString(s) {
				return fmt.Errorf("string %q does not match pattern %q", s, pat)
			}
		}
		if enums, okE := schema["enum"].([]interface{}); okE && len(enums) > 0 {
			matched := false
			for _, ev := range enums {
				if fmt.Sprint(ev) == s {
					matched = true
					break
				}
			}
			if !matched {
				return fmt.Errorf("value %q not in enum", s)
			}
		}
	case "number", "integer":
		num, ok := data.(float64)
		if !ok {
			return fmt.Errorf("expected %s", t)
		}
		if t == "integer" && num != float64(int64(num)) {
			return fmt.Errorf("expected integer, got %v", num)
		}
	case "boolean":
		if _, ok := data.(bool); !ok {
			return fmt.Errorf("expected boolean")
		}
	}
	return nil
}

func parseDiffFixture(diff string) (file string, added []string) {
	rawLines := strings.Split(diff, "\n")
	hasPlusPrefix := false
	for _, l := range rawLines {
		if strings.HasPrefix(l, "+++ b/") {
			file = strings.TrimSpace(strings.TrimPrefix(l, "+++ b/"))
			continue
		}
		if strings.HasPrefix(l, "+++ ") || strings.HasPrefix(l, "--- ") || strings.HasPrefix(l, "@@") || strings.HasPrefix(l, "diff --git ") {
			continue
		}
		if strings.HasPrefix(l, "+") {
			hasPlusPrefix = true
			added = append(added, l[1:])
		}
	}
	if !hasPlusPrefix {
		for _, l := range rawLines {
			if strings.TrimSpace(l) != "" {
				added = append(added, l)
			}
		}
	}
	return file, added
}

func fixtureToContext(f CustomCheckFixture) CustomCheckContext {
	ctx := CustomCheckContext{
		ChangedFiles: f.ChangedFiles,
		AddedLines:   map[string][]string{},
	}
	file := f.File
	addedLines := f.AddedLines
	if f.Diff != "" {
		diffFile, diffAdded := parseDiffFixture(f.Diff)
		if file == "" && diffFile != "" {
			file = diffFile
		}
		if len(addedLines) == 0 {
			addedLines = diffAdded
		}
	}
	if file == "" && len(f.ChangedFiles) == 1 {
		file = f.ChangedFiles[0]
	}
	if file != "" || len(addedLines) > 0 || f.Content != "" {
		if len(ctx.ChangedFiles) == 0 && file != "" {
			ctx.ChangedFiles = []string{file}
		}
		if len(addedLines) > 0 {
			ctx.AddedLines[file] = addedLines
		} else if f.Content != "" {
			ctx.AddedLines[file] = strings.Split(f.Content, "\n")
		}
		if f.Content != "" {
			contentBytes := []byte(f.Content)
			ctx.ReadFile = func(path string) ([]byte, error) {
				if path == file {
					return contentBytes, nil
				}
				return nil, fmt.Errorf("unexpected file %s", path)
			}
		}
	}
	return ctx
}

func ValidateCustomCheck(c CustomCheckDecl) error {
	if c.Fixtures == nil || len(c.Fixtures.Fail) == 0 || len(c.Fixtures.Pass) == 0 {
		return fmt.Errorf("custom check %q must provide at least one fail and one pass fixture", c.ID)
	}

	// Compile regex to ensure they are valid
	if c.ForbidAdded != "" {
		if _, err := regexp.Compile(c.ForbidAdded); err != nil {
			return err
		}
	}
	if c.RequireAdded != "" {
		if _, err := regexp.Compile(c.RequireAdded); err != nil {
			return err
		}
	}
	if len(c.SchemaJSON) > 0 {
		if err := validateJSONSchemaStructure(c.SchemaJSON, 0); err != nil {
			return fmt.Errorf("custom check %q invalid schema-json: %w", c.ID, err)
		}
	}

	for i, f := range c.Fixtures.Fail {
		ctx := fixtureToContext(f)
		findings, err := EvaluateCustomCheck(c, ctx)
		if err != nil {
			return fmt.Errorf("fixture fail[%d] %q failed evaluation: %v", i, f.Name, err)
		}
		if len(findings) == 0 {
			return fmt.Errorf("fixture fail[%d] %q produced 0 findings", i, f.Name)
		}
	}

	for i, f := range c.Fixtures.Pass {
		ctx := fixtureToContext(f)
		findings, err := EvaluateCustomCheck(c, ctx)
		if err != nil {
			return fmt.Errorf("fixture pass[%d] %q failed evaluation: %v", i, f.Name, err)
		}
		if len(findings) > 0 {
			return fmt.Errorf("fixture pass[%d] %q produced %d findings", i, f.Name, len(findings))
		}
	}

	return nil
}
