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

	"github.com/ai-on-gke/grain/pkg/daemon"
)

const dbDowngradeUsage = `usage: grain db downgrade -to N [-data-dir DIR]

Prepare an explicit operator-authored disaster-recovery schema downgrade from
the store in DIR to lower schema version N.

Schema downgrades are never allowed through UI/API upgrades, automatic
upgrades, rollback, or setup.sh. This offline host command exports the
stopped deployment's rows into DIR/schema-upgrade with an operator-authored
downgrade note so the first start of a build at schema N moves the newer
store aside and rebuilds at N, leaving anything only the newer schema holds
in the archived store.

Refuses while a daemon holds DIR or its PostgreSQL store.

Flags:
  -to N           target schema version (< current store schema, required)
  -data-dir DIR   root directory of the grain installation (defaults to $GRAIN_DATA_DIR)
`

func dbDowngradeCmd(args []string) {
	err := runDBDowngrade(context.Background(), args, os.Stdout)
	if err == nil || errors.Is(err, flag.ErrHelp) {
		return
	}
	fmt.Fprintln(os.Stderr, "grain db downgrade: "+err.Error())
	os.Exit(1)
}

func runDBDowngrade(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("grain db downgrade", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stderr, dbDowngradeUsage) }
	to := fs.Int("to", 0, "target schema version")
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
	if *to <= 0 {
		return errors.New("-to is required and must be a positive schema version")
	}
	if err := daemon.RefuseWhileDaemonAttached(ctx, "grain db downgrade", *dataDir, nil, false); err != nil {
		return err
	}
	from, dir, err := daemon.PrepareStoreSchemaDowngrade(ctx, *dataDir, *to)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "prepared operator downgrade %d -> %d in %s\n", from, *to, dir)
	fmt.Fprintf(out, "Next: install and start the grain build whose schema-version is %d (do not start the newer build again first).\n", *to)
	return nil
}
