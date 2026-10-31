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

	"github.com/ai-on-gke/grain/pkg/ui"
)

// cmdSandbox is `grain sandbox <subcommand>`. probe is its only one.
func cmdSandbox(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	if len(args) == 0 {
		return errors.New("a subcommand is required: grain sandbox probe -id <id> [-access dialout|kubectl] -- <command> [args...]")
	}
	switch args[0] {
	case "probe":
		return cmdSandboxProbe(ctx, c, out, os.Stdout, os.Stderr, args[1:])
	default:
		return fmt.Errorf("unknown \"grain sandbox\" subcommand %q: want probe", args[0])
	}
}

// cmdSandboxProbe is `grain sandbox probe`: have the daemon build one
// throwaway sandbox (POST /api/sandbox-probe), run the command after
// "--" in it over the deployment's guest-access channel, and delete it.
// The command's stdout and stderr are copied to this process's, and a
// non-zero exit is reported as an error, so a script can use it as a
// check -- scripts/deploy-gke.sh --check does.
//
// The call waits for the sandbox to boot, which on a cold node is
// minutes: pass the global -timeout (`grain -timeout=15m sandbox probe
// ...`) when the default is not enough.
func cmdSandboxProbe(ctx context.Context, c *ui.HTTPClient, out *printer, stdout, stderr io.Writer, args []string) error {
	fs := flag.NewFlagSet("grain sandbox probe", flag.ContinueOnError)
	id := fs.String("id", "", "names the probe; the sandbox is probe-<id> and its pod's name carries it (required)")
	access := fs.String("access", "", "the guest-access channel the deployment must be using: dialout or kubectl (default: whichever it uses)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*id) == "" {
		return errors.New("-id is required")
	}
	argv := fs.Args()
	if len(argv) == 0 {
		return errors.New("a command to run is required: grain sandbox probe -id <id> -- <command> [args...]")
	}
	res, err := c.ProbeSandbox(ctx, ui.SandboxProbeRequest{ID: *id, Access: *access, Argv: argv})
	if err != nil {
		return err
	}
	if out.json {
		out.encode(res)
	} else {
		io.WriteString(stdout, res.Stdout)
		io.WriteString(stderr, res.Stderr)
		fmt.Fprintf(stderr, "grain: probe sandbox %s (pod %s) ran over %s and was deleted\n", res.Sandbox, res.Pod, res.Access)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("probe command exited with status %d", res.ExitCode)
	}
	return nil
}
