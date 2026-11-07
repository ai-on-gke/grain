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

package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"
)

const dbCheckUsage = `usage: grain db check [-data-dir DIR]

Say which store a daemon started on DIR would open, and whether it is
behind the writes a previous daemon on DIR made (its high-water mark,
DIR/store-highwater.json). Exits 1 when it is behind, naming the other
stores present and how to recover. It only reads, and is safe beside a
running daemon. setup.sh runs it before it restarts the daemon, and it is
the "verify" step of an offline store migration (docs/database.md).

Flags:
  -data-dir DIR   root directory of the grain installation (defaults to $GRAIN_DATA_DIR)
`

func dbCheckCmd(args []string) {
	err := runDBCheck(context.Background(), args, os.Stdout)
	if err == nil || errors.Is(err, flag.ErrHelp) {
		return
	}
	fmt.Fprintln(os.Stderr, "grain db check: "+err.Error())
	os.Exit(1)
}

func runDBCheck(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("grain db check", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stderr, dbCheckUsage) }
	dataDir := fs.String("data-dir", dataDirDefault(), "root data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if *dataDir == "" {
		return errors.New("-data-dir is required")
	}
	driver, dsn, err := daemon.ResolveDatabase(*dataDir)
	if err != nil {
		return err
	}
	if driver != "postgres" && dsn == "" && !daemon.HasSQLiteStore(*dataDir) {
		fmt.Fprintf(out, "store: none yet (%s); a daemon started here creates it\n", daemon.StoreDescription(driver, dsn, *dataDir))
		return nil
	}
	db, err := daemon.OpenRawDB(*dataDir)
	if err != nil {
		return fmt.Errorf("opening %s: %w", daemon.StoreDescription(driver, dsn, *dataDir), err)
	}
	defer db.Close()
	check, err := daemon.CheckStore(ctx, db, driver, dsn, *dataDir)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "store: %s\n", check.Store)
	fmt.Fprintf(out, "  holds: %s\n", describeMarks(check.Marks))
	if check.Mark == nil {
		fmt.Fprintf(out, "high-water mark: none yet (%s is written by the first daemon to run here)\n", daemon.StoreHighWaterFile)
	} else {
		fmt.Fprintf(out, "high-water mark (%s): %s; last moved %s by %s\n", daemon.StoreHighWaterFile,
			describeMarks(*check.Mark), formatMarkTime(check.Mark.LastWriteAt), check.Mark.Store)
	}
	if len(check.Others) > 0 {
		fmt.Fprintf(out, "other stores: %s\n", strings.Join(check.Others, "; "))
	}
	if check.Message != "" {
		return errors.New(check.Message)
	}
	fmt.Fprintln(out, "ok: the store is at or past the high-water mark")
	return nil
}

func describeMarks(m daemon.StoreHighWater) string {
	s := fmt.Sprintf("last task #%d", m.LastTaskID)
	if m.LastRunID != "" {
		s += fmt.Sprintf(", last run %s started %s", m.LastRunID, formatMarkTime(m.LastRunAt))
	} else {
		s += ", no runs"
	}
	return s
}

func formatMarkTime(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.UTC().Format(time.RFC3339)
}
