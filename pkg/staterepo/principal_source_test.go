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

package staterepo_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// TestADumpFromBeforePrincipalSourcesImportsWithEmptySources is
// operator-attribution spec §7.1's state-repository half: this build
// exports every principal's source, and a dump written by schema 84,
// which has no *_source columns at all, imports with every source empty
// rather than failing or guessing one.
func TestADumpFromBeforePrincipalSourcesImportsWithEmptySources(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openRealDB(t)
	operator := model.Principal{Kind: model.PrincipalHuman, ID: "operator", Source: model.SourceAccessCode}
	tk := task("sourced-1")
	tk.Origin.Attribution = model.Attribution{Actor: operator}
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddComment(ctx, model.Comment{TaskID: "sourced-1",
		Author: model.Attribution{Actor: operator}, Body: "hello", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(t.TempDir(), "dump")
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	// This build writes the source, and a round trip keeps it.
	same, sameDB := openRealDB(t)
	if err := staterepo.Import(ctx, sameDB, dir); err != nil {
		t.Fatalf("importing: %v", err)
	}
	if got, err := same.GetTask(ctx, "sourced-1"); err != nil || got == nil || got.Origin.Attribution.Actor != operator {
		t.Fatalf("round trip: task %+v, %v; want its origin actor %+v", got, err, operator)
	}

	// Age the dump to schema 84: no *_source column in any table file.
	stripped := 0
	for _, table := range []string{"task", "task_comment"} {
		path := filepath.Join(dir, staterepo.TablesDir, table+".json")
		var rows []map[string]any
		if err := json.Unmarshal([]byte(readGolden(t, path)), &rows); err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		for _, row := range rows {
			for col := range row {
				if strings.HasSuffix(col, "_source") {
					delete(row, col)
					stripped++
				}
			}
		}
		data, err := json.MarshalIndent(rows, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if stripped == 0 {
		t.Fatal("the dump this build wrote carries no *_source column, so there is no pre-upgrade shape to make here")
	}
	if err := staterepo.WriteSchemaVersion(dir, 84); err != nil {
		t.Fatal(err)
	}

	fresh, freshDB := openRealDB(t)
	if err := staterepo.Import(ctx, freshDB, dir); err != nil {
		t.Fatalf("importing a dump written before the source columns existed: %v", err)
	}
	legacy := model.Principal{Kind: model.PrincipalHuman, ID: "operator"}
	got, err := fresh.GetTask(ctx, "sourced-1")
	if err != nil || got == nil {
		t.Fatalf("the task in the aged dump did not arrive: (%+v, %v)", got, err)
	}
	if got.Origin.Attribution.Actor != legacy {
		t.Errorf("origin actor = %+v, want %+v with no source", got.Origin.Attribution.Actor, legacy)
	}
	comments, err := fresh.Comments(ctx, "sourced-1")
	if err != nil || len(comments) != 1 {
		t.Fatalf("comments: %v, %v", comments, err)
	}
	if comments[0].Author.Actor != legacy {
		t.Errorf("comment author = %+v, want %+v with no source", comments[0].Author.Actor, legacy)
	}
}
