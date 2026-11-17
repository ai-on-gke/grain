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

package checks

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/ai-on-gke/grain/pkg/qa"
)

const defaultComponentStyleWarnBytes = 8 * 1024

var appComponentFeatureImportRe = regexp.MustCompile(`from\s+["']\.\/([A-Za-z0-9_-]+)\/([^"']+)["']`)

var allowedAppComponentImports = map[string]bool{
	"shared":   true,
	"services": true,
	"api":      true,
}

// CheckUIBudgetAndSpecFloor enforces golden:ui-budget-and-spec-floor across ui-ng/ changes:
//  1. Dynamically reads anyComponentStyle warning budget from ui-ng/angular.json (default 8 KiB)
//     and blocks modified/new component stylesheets that exceed the budget AND grow over base (ratchet).
//  2. Blocks newly added eager feature route imports in ui-ng/src/app/app.component.ts so the
//     450 kB initial bundle ceiling stays protected via lazy loadComponent routes.
//  3. Blocks newly added *.component.ts files that lack a corresponding *.spec.ts in the directory.
func CheckUIBudgetAndSpecFloor(ctx context.Context, ws WorkspaceReader, pd ParsedDiff) ([]Finding, error) {
	touchesUI := false
	for _, df := range pd.Files {
		p := df.EffectivePath()
		if strings.HasPrefix(p, "ui-ng/") || strings.HasPrefix(p, "ui/") {
			touchesUI = true
			break
		}
	}
	if !touchesUI {
		return nil, nil
	}

	styleWarnBytes := readAngularStyleBudgetBytes(ctx, ws)
	addedSpecDirs := map[string]bool{}
	for _, df := range pd.Files {
		if !df.IsDeleted && strings.HasSuffix(df.EffectivePath(), ".spec.ts") {
			addedSpecDirs[path.Dir(df.EffectivePath())] = true
		}
	}

	var findings []Finding
	for _, df := range pd.Files {
		if df.IsDeleted {
			continue
		}
		p := df.EffectivePath()

		// 1. Component stylesheet non-regressing budget ratchet (excludes global src/styles.css).
		if strings.HasPrefix(p, "ui-ng/src/app/") && (strings.HasSuffix(p, ".css") || strings.HasSuffix(p, ".scss")) {
			headBytes, err := ws.ReadFileAtHead(ctx, p)
			if err == nil && len(headBytes) > styleWarnBytes {
				baseLen := 0
				if !df.IsNew {
					if b, bErr := ws.ReadFileAtBase(ctx, nonEmptyPath(df.OldPath, p)); bErr == nil {
						baseLen = len(b)
					}
				}
				if df.IsNew || len(headBytes) > baseLen {
					findings = append(findings, Finding{
						RuleID:   qa.RuleUIBudgetAndSpecFloor,
						Stage:    "S3",
						Severity: "blocking",
						FilePath: p,
						Line:     1,
						Summary: fmt.Sprintf("Component stylesheet %s is %d bytes (exceeds %d-byte budget and grew from %d bytes).",
							p, len(headBytes), styleWarnBytes, baseLen),
						Evidence: "Trim component CSS under the angular.json anyComponentStyle warning budget or reuse shared tokens/utilities without duplicating shared component styleUrls.",
					})
				}
			}
		}

		// 2. AppComponent eager feature import guard.
		if p == "ui-ng/src/app/app.component.ts" {
			for _, al := range df.AddedLines {
				m := appComponentFeatureImportRe.FindStringSubmatch(al.Text)
				if m == nil {
					continue
				}
				dir, target := m[1], m[2]
				if allowedAppComponentImports[dir] {
					continue
				}
				if dir == "sessions" && strings.HasPrefix(target, "session-helpers") {
					continue
				}
				findings = append(findings, Finding{
					RuleID:   qa.RuleUIBudgetAndSpecFloor,
					Stage:    "S3",
					Severity: "blocking",
					FilePath: p,
					Line:     al.Line,
					Summary:  fmt.Sprintf("Eager feature import `./%s/%s` added to app.component.ts threatens the initial bundle budget.", dir, target),
					Evidence: "Load feature pages via lazy `loadComponent` in app.routes.ts or dynamic `import(...)` / `@defer`.",
				})
			}
		}

		// 3. New Angular component unit-spec floor.
		if df.IsNew && strings.HasPrefix(p, "ui-ng/src/app/") && strings.HasSuffix(p, ".component.ts") {
			specPath := strings.TrimSuffix(p, ".component.ts") + ".component.spec.ts"
			altSpecPath := strings.TrimSuffix(p, ".component.ts") + ".spec.ts"
			if _, err1 := ws.ReadFileAtHead(ctx, specPath); err1 != nil {
				if _, err2 := ws.ReadFileAtHead(ctx, altSpecPath); err2 != nil && !addedSpecDirs[path.Dir(p)] {
					findings = append(findings, Finding{
						RuleID:   qa.RuleUIBudgetAndSpecFloor,
						Stage:    "S3",
						Severity: "blocking",
						FilePath: p,
						Line:     1,
						Summary:  fmt.Sprintf("Newly added Angular component %s has no accompanying %s.", p, path.Base(specPath)),
						Evidence: "Add a unit spec file covering the new component's rendering, states, and interactions.",
					})
				}
			}
		}
	}

	return findings, nil
}

func readAngularStyleBudgetBytes(ctx context.Context, ws WorkspaceReader) int {
	raw, err := ws.ReadFileAtHead(ctx, "ui-ng/angular.json")
	if err != nil || len(raw) == 0 {
		return defaultComponentStyleWarnBytes
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return defaultComponentStyleWarnBytes
	}
	var found int
	var walk func(v any)
	walk = func(v any) {
		if found > 0 {
			return
		}
		switch x := v.(type) {
		case map[string]any:
			if x["type"] == "anyComponentStyle" {
				if mw, ok := x["maximumWarning"].(string); ok {
					if n := parseByteBudgetString(mw); n > 0 {
						found = n
						return
					}
				}
			}
			for _, child := range x {
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	walk(doc)
	if found > 0 {
		return found
	}
	return defaultComponentStyleWarnBytes
}

func parseByteBudgetString(raw string) int {
	s := strings.ToLower(strings.TrimSpace(raw))
	mult := 1.0
	switch {
	case strings.HasSuffix(s, "mib"):
		mult = 1024 * 1024
		s = strings.TrimSuffix(s, "mib")
	case strings.HasSuffix(s, "mb"):
		mult = 1000 * 1000
		s = strings.TrimSuffix(s, "mb")
	case strings.HasSuffix(s, "kib"):
		mult = 1024
		s = strings.TrimSuffix(s, "kib")
	case strings.HasSuffix(s, "kb"):
		// Angular CLI interprets "8kB" as 8000 bytes (SI decimal kilobytes).
		mult = 1000
		s = strings.TrimSuffix(s, "kb")
	case strings.HasSuffix(s, "b"):
		s = strings.TrimSuffix(s, "b")
	}
	val, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || val <= 0 {
		return 0
	}
	return int(val * mult)
}
