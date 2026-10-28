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

// schemaversion.go implements `grain schema-version`: prints
// pkg/model.SchemaVersion, the store schema version this build was
// compiled against, and exits.
//
// bwsalmon/agents#394's staging deploy script (../scripts/setup.sh) is
// the caller: pkg/model.SchemaVersion's own doc comment says it is
// bumped exactly when Tables or Views change in a way Store.Init's own
// `CREATE TABLE IF NOT EXISTS` cannot safely reconcile an existing
// database into -- Init adds a table that is missing outright, never a
// column on one that already exists, so a build newer than the database
// it finds would otherwise start silently against stale columns instead
// of refusing or fixing anything. Comparing this number against a marker
// recorded on the data disk at the previous deploy is what lets
// setup.sh decide whether to move the store aside before starting
// grain-daemon.service, without duplicating the constant itself (or its
// definition of "breaking") anywhere outside pkg/model.
package cli

import (
	"flag"
	"fmt"

	"github.com/ai-on-gke/grain/pkg/model"
)

func schemaVersionCmd(args []string) {
	fs := flag.NewFlagSet("grain schema-version", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return
	}
	fmt.Println(model.SchemaVersion)
}
