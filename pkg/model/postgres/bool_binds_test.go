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

package postgres

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/types"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"golang.org/x/tools/go/packages"
)

var loadModelPkg = sync.OnceValues(func() (*packages.Package, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo,
		Dir: ".",
	}
	pkgs, err := packages.Load(cfg, "github.com/ai-on-gke/grain/pkg/model")
	if err != nil {
		return nil, fmt.Errorf("loading pkg/model: %w", err)
	}
	if packages.PrintErrors(pkgs) > 0 || len(pkgs) != 1 {
		return nil, fmt.Errorf("loading pkg/model failed")
	}
	return pkgs[0], nil
})

func modelPackage(t *testing.T) *packages.Package {
	t.Helper()
	if testing.Short() {
		t.Skip("type-checks pkg/model")
	}
	pkg, err := loadModelPkg()
	if err != nil {
		t.Fatal(err)
	}
	return pkg
}

// TestEveryBoolBindNamesABooleanColumn reads every statement pkg/model
// binds a Go bool into, and fails for a column adaptDDL does not make
// BOOLEAN. The schema spells a flag INTEGER 0/1 on both backends, and only
// the names in boolColumns become BOOLEAN on PostgreSQL; any other is
// BIGINT, which SQLite fills from a bool without complaint and pgx refuses
// ("unable to encode false into binary format for int8"). That is how
// scheduled_workflow_run.review_not_needed stopped every workflow schedule
// firing on a Postgres deployment for two days (grain/task-4537), and
// ci_run.recheck and ci_job.flake before it: each one a column added
// without its boolColumns entry, caught only once a test happened to write
// it on Postgres.
//
// This reads the source instead of waiting for a test to reach the write:
// every ExecContext/QueryContext/QueryRowContext call in pkg/model whose
// query is a constant, each `?` matched to the column it fills (by
// position in an INSERT/REPLACE column list, or the `col` = ? in front
// of it anywhere else), and the argument in that position checked for a
// bool type.
func TestEveryBoolBindNamesABooleanColumn(t *testing.T) {
	pkg := modelPackage(t)

	checked := 0
	for _, file := range pkg.Syntax {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !boundQueryMethods[sel.Sel.Name] || len(call.Args) < 2 || call.Ellipsis.IsValid() {
				return true
			}
			tv, ok := pkg.TypesInfo.Types[call.Args[1]]
			if !ok || tv.Value == nil || tv.Value.Kind() != constant.String {
				return true
			}
			query := constant.StringVal(tv.Value)
			args := call.Args[2:]
			for i, col := range placeholderColumns(query) {
				if col == "" || i >= len(args) {
					continue
				}
				at, ok := pkg.TypesInfo.Types[args[i]]
				if !ok {
					continue
				}
				basic, ok := at.Type.Underlying().(*types.Basic)
				if !ok || basic.Kind() != types.Bool {
					continue
				}
				checked++
				if !boolColumnsSet[col] {
					t.Errorf("%s: binds a Go bool into %q, which PostgreSQL holds as BIGINT: "+
						"add %q to boolColumns in adapter.go",
						pkg.Fset.Position(args[i].Pos()), col, col)
				}
			}
			return true
		})
	}
	// A guard on the matching itself: the store binds dozens of flags, and
	// a parse that silently found none would pass whatever the schema said.
	if checked < 20 {
		t.Fatalf("matched only %d bool binds to columns; the statement parsing has stopped working", checked)
	}
}

// TestEveryBoolExpressionTranslatesToBoolean inspects every constant SQL
// query passed to ExecContext/QueryContext/QueryRowContext (and each/DDL
// statements) in pkg/model, translates it with translateQuery, and fails if
// any boolColumns column still appears in an unadapted integer COALESCE or
// integer (=, !=, <>) comparison. On SQLite boolean columns are INTEGER 0/1,
// so COALESCE(`t`.`interactive`, 0) = 0 passes SQLite tests yet fails every
// reconcile tick on PostgreSQL with SQLSTATE 42804 ("COALESCE types boolean
// and integer cannot be matched").
func TestEveryBoolExpressionTranslatesToBoolean(t *testing.T) {
	// Guard the detector itself against going blind on the unadapted shapes
	// PostgreSQL rejects.
	for _, bad := range []string{
		`SELECT COUNT(*) FROM "task_run" AS "r" LEFT JOIN "task" AS "t" ON "t"."id" = "r"."task_id" WHERE COALESCE("t"."interactive", 0) = 0`,
		`SELECT COALESCE("interactive", 1) FROM "task"`,
		`SELECT * FROM "task" AS "t" WHERE COALESCE("t"."interactive", FALSE) = 0`,
		`SELECT * FROM "task" AS "t" WHERE "t"."interactive" = 0`,
		`SELECT * FROM "task" WHERE "interactive" != 1`,
		`SELECT * FROM "task" AS "t" WHERE "t"."interactive" <> 0`,
	} {
		if match := findUnadaptedBoolExpression(bad); match == "" {
			t.Fatalf("detector missed unadapted boolean expression in %q", bad)
		}
		if adapted := translateQuery(bad); findUnadaptedBoolExpression(adapted) != "" {
			t.Fatalf("translateQuery(%q) = %q still matched unadapted boolean expression", bad, adapted)
		}
	}

	pkg := modelPackage(t)

	checked := 0
	for _, file := range pkg.Syntax {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			queryArg, ok := sqlQueryArg(call)
			if !ok {
				return true
			}
			tv, ok := pkg.TypesInfo.Types[queryArg]
			if !ok || tv.Value == nil || tv.Value.Kind() != constant.String {
				return true
			}
			query := constant.StringVal(tv.Value)
			checked++
			translated := translateQuery(query)
			if match := findUnadaptedBoolExpression(translated); match != "" {
				t.Errorf("%s: translated query still contains unadapted integer boolean expression %q:\n  translated: %s",
					pkg.Fset.Position(queryArg.Pos()), match, translated)
			}
			return true
		})
	}
	for i, stmt := range model.Statements() {
		translated := translateQuery(stmt)
		if match := findUnadaptedBoolExpression(translated); match != "" {
			t.Errorf("model.Statements()[%d]: translated DDL/view still contains unadapted integer boolean expression %q:\n  translated: %s",
				i, match, translated)
		}
	}
	if checked < 100 {
		t.Fatalf("inspected only %d constant SQL queries in pkg/model; query call detection has stopped working", checked)
	}
}

func sqlQueryArg(call *ast.CallExpr) (ast.Expr, bool) {
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		switch fun.Sel.Name {
		case "ExecContext", "QueryContext", "QueryRowContext":
			if len(call.Args) >= 2 {
				return call.Args[1], true
			}
		case "Exec", "Query", "QueryRow":
			if len(call.Args) >= 1 {
				return call.Args[0], true
			}
		}
	case *ast.Ident:
		// pkg/model's internal each(ctx, q, query, args, scan) helper wraps QueryContext.
		if fun.Name == "each" && len(call.Args) >= 3 {
			return call.Args[2], true
		}
	}
	return nil, false
}

var unadaptedBoolExprDetectors = sync.OnceValue(func() []*regexp.Regexp {
	escaped := make([]string, 0, len(boolColumns))
	for _, col := range boolColumns {
		escaped = append(escaped, regexp.QuoteMeta(col))
	}
	boolColAlt := "(?:" + strings.Join(escaped, "|") + ")"
	boolColIdent := `(?:"` + boolColAlt + `"|` + "`" + boolColAlt + "`" + `|\b` + boolColAlt + `\b)`
	sqlQual := `(?:(?:"[^"]+"|` + "`[^`]+`" + `|\b\w+\b)\s*\.\s*)*`
	boolCoalesceArgs := `(?:` + sqlQual + boolColIdent + `\s*,\s*)+`

	return []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bCOALESCE\s*\(\s*` + boolCoalesceArgs + `(?:0|1)\b[^)]*\)`),
		regexp.MustCompile(`(?i)\bCOALESCE\s*\(\s*` + boolCoalesceArgs + `[^)]*\)\s*(?:=|!=|<>)\s*(?:0|1)\b`),
		regexp.MustCompile(`(?i)` + sqlQual + boolColIdent + `\s*(?:=|!=|<>)\s*(?:0|1)\b`),
		regexp.MustCompile(`(?i)(?:^|[^$\w])(?:0|1)\s*(?:=|!=|<>)\s*` + sqlQual + boolColIdent),
	}
})

func findUnadaptedBoolExpression(translated string) string {
	for _, re := range unadaptedBoolExprDetectors() {
		if m := re.FindString(translated); m != "" {
			return strings.TrimSpace(m)
		}
	}
	return ""
}

var boundQueryMethods = map[string]bool{
	"ExecContext": true, "QueryContext": true, "QueryRowContext": true,
	"Exec": true, "Query": true, "QueryRow": true,
}

var (
	reInsertColumns = regexp.MustCompile("(?is)^\\s*(?:INSERT|REPLACE)(?:\\s+OR\\s+\\w+)?\\s+INTO\\s+\\S+\\s*\\(([^)]*)\\)\\s*VALUES\\s*\\((.*?)\\)")
	reColumnBefore  = regexp.MustCompile("`?(\\w+)`?\\s*(?:=|<>|!=)\\s*$")
)

// placeholderColumns is the column each `?` in query fills, in order, or
// "" for one it cannot tell.
func placeholderColumns(query string) []string {
	var out []string
	rest := query
	if m := reInsertColumns.FindStringSubmatchIndex(query); m != nil {
		cols := splitIdentifiers(query[m[2]:m[3]])
		vals := splitSqlValues(query[m[4]:m[5]])
		for i, v := range vals {
			n := strings.Count(v, "?")
			if n == 0 {
				continue
			}
			col := ""
			if n == 1 && strings.TrimSpace(v) == "?" && len(cols) == len(vals) {
				col = cleanIdentifier(cols[i])
			}
			for range n {
				out = append(out, col)
				col = ""
			}
		}
		rest = query[m[1]:]
	}
	for {
		i := strings.IndexByte(rest, '?')
		if i < 0 {
			return out
		}
		col := ""
		if m := reColumnBefore.FindStringSubmatch(rest[:i]); m != nil {
			col = m[1]
		}
		out = append(out, col)
		rest = rest[i+1:]
	}
}
