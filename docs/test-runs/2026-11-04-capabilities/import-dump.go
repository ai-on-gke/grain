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

//go:build ignore

// import-dump.go <dump-dir> <from-schema> <new-data-dir>: the "state-repo
// export imports into the current schema" half of §8 item 1, without the
// in-place migration a daemon would prefer. It builds a fresh store at this
// build's SchemaVersion and fills it the way a schema-upgrade cutover does
// (staterepo.MigrateDump, then staterepo.ImportMigrated), so the grants in
// it are what the dump route makes of a schema-93 export.
//
//	go run docs/test-runs/2026-11-04-capabilities/import-dump.go DIR 93 OUT
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: import-dump.go DUMP-DIR FROM-SCHEMA NEW-DATA-DIR")
		os.Exit(2)
	}
	dir, out := os.Args[1], os.Args[3]
	from, err := strconv.Atoi(os.Args[2])
	if err != nil {
		panic(err)
	}
	if err := os.MkdirAll(filepath.Join(out, "store"), 0o755); err != nil {
		panic(err)
	}
	_, db, err := daemon.OpenStore("sqlite", "", out)
	if err != nil {
		panic(err)
	}
	defer db.Close()
	migrated, err := staterepo.MigrateDump(dir, out, from, model.SchemaVersion)
	if err != nil {
		fmt.Fprintln(os.Stderr, "MigrateDump:", err)
		os.Exit(1)
	}
	if err := staterepo.ImportMigrated(context.Background(), db, migrated); err != nil {
		fmt.Fprintln(os.Stderr, "ImportMigrated:", err)
		os.Exit(1)
	}
	fmt.Printf("imported %s (schema %d) into %s at schema %d\n", dir, from, out, model.SchemaVersion)
}
