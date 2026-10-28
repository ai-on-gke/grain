package postgres

import (
	"go/ast"
	"go/constant"
	"go/types"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

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
	if testing.Short() {
		t.Skip("type-checks pkg/model")
	}
	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
		packages.NeedTypes | packages.NeedTypesInfo, Dir: "."}
	pkgs, err := packages.Load(cfg, "github.com/bwsalmon/grain/pkg/model")
	if err != nil {
		t.Fatalf("loading pkg/model: %v", err)
	}
	if packages.PrintErrors(pkgs) > 0 || len(pkgs) != 1 {
		t.Fatalf("loading pkg/model failed")
	}
	pkg := pkgs[0]

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
