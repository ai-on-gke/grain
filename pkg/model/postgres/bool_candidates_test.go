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
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// everyBoolExprReplacer is adaptBoolExpressions before it narrowed the
// columns it tries: every column's regexps, every time.
func everyBoolExprReplacer(query string) string {
	for _, r := range boolExprReplacers {
		query = r.re1.ReplaceAllString(query, "${1}TRUE")
		query = r.re0.ReplaceAllString(query, "${1}FALSE")
		query = r.reNe1.ReplaceAllString(query, "${1}TRUE")
		query = r.reNe0.ReplaceAllString(query, "${1}FALSE")
	}
	return query
}

// everyBoolDDLReplacer is the boolean step of adaptDDL, unnarrowed.
func everyBoolDDLReplacer(query string) string {
	for _, r := range boolDDLReplacers {
		query = r.reDef1.ReplaceAllString(query, "${1}BOOLEAN${2}TRUE")
		query = r.reDef0.ReplaceAllString(query, "${1}BOOLEAN${2}FALSE")
		query = r.reDefNN.ReplaceAllString(query, "${1}BOOLEAN${2}")
		query = r.reDef.ReplaceAllString(query, "${1}BOOLEAN")
	}
	return query
}

// TestNarrowedBoolReplacersAnswerAsEveryColumnDoes holds the candidate
// prefilters to the rewrite they skip work for: on the whole schema,
// each statement of it, and comparisons written every way the regexps
// allow, the narrowed and the exhaustive rewrites agree byte for byte.
func TestNarrowedBoolReplacersAnswerAsEveryColumnDoes(t *testing.T) {
	queries := append([]string{}, model.Statements()...)
	queries = append(queries, strings.Join(model.Statements(), ";\n"))
	for _, col := range boolColumns {
		upper := strings.ToUpper(col)
		queries = append(queries,
			`SELECT 1 FROM t WHERE "`+col+`" = 1 AND `+col+`=0 AND `+"`"+col+"`"+` != 1 AND "`+col+"\"\t!=\n0",
			`SELECT 1 FROM t WHERE x.`+upper+` = 1 OR not_`+col+` = 0 OR `+col+` = 10 OR `+col+` == 1 OR `+col+` >= 1 OR `+col+` !=1`,
			`SELECT 1 FROM t WHERE `+col+` = 1x OR `+col+` = 1`,
			`SELECT 1 FROM t WHERE `+col+`_at = 1 OR "`+col+`" = 1;`,
			`CREATE TABLE t ("`+col+`" INTEGER NOT NULL DEFAULT 1, "`+col+"`\n\tinteger not null default 0, `"+col+"` INTEGER NOT NULL, \""+upper+"\" INTEGER, "+col+` INTEGER, "x`+col+`"INTEGER)`,
			`SELECT 'ünïcode' FROM t WHERE `+col+` = 1`,
		)
	}
	for _, q := range queries {
		if got, want := adaptBoolExpressions(q), everyBoolExprReplacer(q); got != want {
			t.Errorf("adaptBoolExpressions(%q)\n= %q\nwant %q", q, got, want)
		}
		if got, want := adaptBoolDDL(q), everyBoolDDLReplacer(q); got != want {
			t.Errorf("boolean DDL rewrite of %q\n= %q\nwant %q", q, got, want)
		}
	}
}
