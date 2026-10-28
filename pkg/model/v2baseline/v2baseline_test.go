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

package v2baseline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// -freeze-v2-baseline wrote frozen/, once, from the build that shipped
// 2.0.0. It refuses to run at any other schema: regenerating the fixture
// from a later build would throw away the very store this package exists
// to prove later builds can open.
var freeze = flag.Bool("freeze-v2-baseline", false,
	"write pkg/model/v2baseline/frozen from this build (only at model.V2BaselineSchemaVersion)")

// sumsDigest pins frozen/SHA256SUMS, which pins every other frozen file
// by its path under frozen/. A change here is a change to what grain
// promised 2.0 deployments, and is never the fix for a failing test.
const sumsDigest = "4e68875dd32543752a53923e25beaaff3931a45c72db87dbf8900e20c339f3ec"

// sumsFile is the manifest TestFreeze writes beside the frozen files.
const sumsFile = "SHA256SUMS"

func TestFreeze(t *testing.T) {
	if !*freeze {
		t.Skip("run with -freeze-v2-baseline to write frozen/ (only ever at the 2.0 baseline)")
	}
	if model.SchemaVersion != model.V2BaselineSchemaVersion {
		t.Fatalf("this build is at schema %d and the 2.0 baseline is %d: frozen/ is written once, at the "+
			"baseline, and never again", model.SchemaVersion, model.V2BaselineSchemaVersion)
	}
	ctx := context.Background()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := Seed(ctx, store); err != nil {
		t.Fatal(err)
	}
	rows, err := DumpRows(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, text string) {
		path := filepath.Join(FrozenDir, name)
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("schema.sqlite.sql", Join(model.Statements()))
	write("schema.postgres.sql", Join(postgres.Statements()))
	write("rows.sql", rows)
	repo := filepath.Join(FrozenDir, "state-repo")
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := staterepo.Export(ctx, db, repo); err != nil {
		t.Fatal(err)
	}
	if err := staterepo.WriteSchemaVersion(repo, model.SchemaVersion); err != nil {
		t.Fatal(err)
	}
	digests, err := digestFrozen()
	if err != nil {
		t.Fatal(err)
	}
	sums := renderSums(digests)
	write(sumsFile, sums)
	sum := sha256.Sum256([]byte(sums))
	t.Logf("wrote %s; pin sumsDigest = %q", FrozenDir, hex.EncodeToString(sum[:]))
}

// TestTheFrozenBaselineIsUnchanged fails when anything under frozen/
// differs from what 2.0.0 wrote -- a file edited, added or removed.
func TestTheFrozenBaselineIsUnchanged(t *testing.T) {
	if *freeze {
		t.Skip("freezing")
	}
	sums, err := os.ReadFile(filepath.Join(FrozenDir, sumsFile))
	if err != nil {
		t.Fatalf("reading frozen/%s: %v", sumsFile, err)
	}
	if sum := sha256.Sum256(sums); hex.EncodeToString(sum[:]) != sumsDigest {
		t.Errorf("frozen/%s has changed. It pins what grain 2.0.0 wrote: put it back", sumsFile)
	}
	want := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(sums)), "\n") {
		digest, name, ok := strings.Cut(line, "  ")
		if !ok {
			t.Fatalf("frozen/%s holds a line that is not \"<sha256>  <path>\": %q", sumsFile, line)
		}
		want[name] = digest
	}
	got, err := digestFrozen()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range sortedKeys(want) {
		if got[name] == "" {
			t.Errorf("frozen/%s is gone. It is part of what grain 2.0.0 wrote, and stays", name)
		} else if got[name] != want[name] {
			t.Errorf("frozen/%s has changed. It is what grain 2.0.0 wrote: put it back, and carry the "+
				"schema forward with a migration (pkg/model/migrations.go) instead", name)
		}
	}
	for _, name := range sortedKeys(got) {
		if _, ok := want[name]; !ok {
			t.Errorf("frozen/%s was added after 2.0.0. Nothing joins the frozen baseline", name)
		}
	}
}

// TestTheBaselineInCodeIsTheFrozenOne: at the 2.0 baseline, the schema in
// code is the frozen one, on both engines. Past it, the schema in code is
// the frozen one plus Migrations, which Run checks against a real store
// on each engine (a migrated 2.0.0 store has the schema Init creates
// fresh).
func TestTheBaselineInCodeIsTheFrozenOne(t *testing.T) {
	if *freeze {
		t.Skip("freezing")
	}
	if model.SchemaVersion != model.V2BaselineSchemaVersion {
		t.Skipf("schema %d is past the 2.0 baseline (%d); the migrated-store schema check in Run covers it",
			model.SchemaVersion, model.V2BaselineSchemaVersion)
	}
	for _, c := range []struct {
		file string
		code []string
	}{
		{"schema.sqlite.sql", model.Statements()},
		{"schema.postgres.sql", postgres.Statements()},
	} {
		frozenStmts := Split(File(t, c.file))
		if len(frozenStmts) != len(c.code) {
			t.Errorf("%s holds %d statements and this build's schema %d: Tables or Views changed without a "+
				"SchemaVersion bump and its migration", c.file, len(frozenStmts), len(c.code))
			continue
		}
		for i := range c.code {
			if frozenStmts[i] != c.code[i] {
				t.Errorf("statement %d of this build's schema differs from %s's: Tables or Views changed "+
					"without a SchemaVersion bump and its migration (pkg/model/migrations.go)\nfrozen:\n%s\ncode:\n%s",
					i, c.file, frozenStmts[i], c.code[i])
				break
			}
		}
	}
}

// TestEveryVersionPastTheV2BaselineHasAMigration is the policy's own
// test: a SchemaVersion bump past 2.0.0 with no in-place migration.
func TestEveryVersionPastTheV2BaselineHasAMigration(t *testing.T) {
	if missing := model.MissingMigrations(); len(missing) > 0 {
		t.Errorf("model.SchemaVersion is %d and no in-place migration produces %v. From 2.0.0 on every bump "+
			"ships one (pkg/model/migrations.go), on SQLite and on Postgres; refusing the old store and "+
			"re-importing it through the state repository is not an upgrade path any more",
			model.SchemaVersion, missing)
	}
	last := model.V2BaselineSchemaVersion
	for _, m := range model.Migrations {
		if m.Version != last+1 {
			t.Errorf("migration to %d follows the one to %d: Migrations go one version at a time, in order", m.Version, last)
		}
		if m.Version > model.SchemaVersion {
			t.Errorf("migration to %d is past this build's schema %d", m.Version, model.SchemaVersion)
		}
		if len(m.Statements) == 0 && m.Up == nil {
			t.Errorf("migration to %d does nothing", m.Version)
		}
		last = m.Version
	}
}

func TestTheFrozenStateRepositoryIsStampedTheBaseline(t *testing.T) {
	if *freeze {
		t.Skip("freezing")
	}
	got, err := staterepo.ReadSchemaVersion(filepath.Join(FrozenDir, "state-repo"))
	if err != nil {
		t.Fatal(err)
	}
	if got != model.V2BaselineSchemaVersion {
		t.Errorf("frozen/state-repo is stamped %d, want %d", got, model.V2BaselineSchemaVersion)
	}
}

func digestFrozen() (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(FrozenDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(filepath.ToSlash(path), FrozenDir+"/")
		if name == sumsFile {
			return nil
		}
		sum := sha256.Sum256(b)
		out[name] = hex.EncodeToString(sum[:])
		return nil
	})
	return out, err
}

// renderSums is SHA256SUMS's text: one "<sha256>  <path>" line a file,
// sorted, as sha256sum writes it.
func renderSums(digests map[string]string) string {
	var b strings.Builder
	for _, name := range sortedKeys(digests) {
		b.WriteString(digests[name] + "  " + name + "\n")
	}
	return b.String()
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
