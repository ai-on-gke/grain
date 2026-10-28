// Command grain is core grain: everything is in pkg/cli, which this only
// runs. google/cmd/grain is the same command with the Google-internal
// registrations linked in (google/README.md).
package main

import "github.com/ai-on-gke/grain/pkg/cli"

func main() { cli.Main() }
