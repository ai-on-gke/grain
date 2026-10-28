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

	"github.com/bwsalmon/grain/pkg/model"
)

func schemaVersionCmd(args []string) {
	fs := flag.NewFlagSet("grain schema-version", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return
	}
	fmt.Println(model.SchemaVersion)
}
