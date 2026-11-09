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
	"time"

	"github.com/ai-on-gke/grain/pkg/ui"
)

const accessUsage = `usage: grain access <list|allow|role|deny> [args]

  list
  allow [--role <set>] [--reason <why>] [--label <name>] <user:<email>|group:<email>|serviceAccount:<email>|delegate:<email>>
  role  [--reason <why>] <entry> <set>
  deny  <user:<email>|group:<email>|serviceAccount:<email>|delegate:<email>>

Manage the allowed users, groups and delegating apps list (Settings -> Access):
who may sign in when authenticated by Cloud IAP or another registered
IdentityVerifier, and with what role. A role is an API scope set
(docs/agent-api.md, "Scope sets"): grain/operator (everything, the
default for user:/group:/serviceAccount:), grain/delegated (the default for
delegate:), grain/contributor, grain/viewer, or one the deployment declares.
Adding an existing entry is a no-op; 'role' changes an entry's role.

A role holding a dangerous scope -- grain/operator among them -- is
assigned only with the access code, and needs --reason. A serviceAccount:
or delegate: entry always needs --reason; for delegate:, --label sets an
optional short identifier matching [a-z][a-z0-9-]{0,31}. Accounts whose keys
grain hands to tasks (the gcp-key pools) are always refused, whatever their
role.`

// cmdAccess is `grain access`: manage the verified-identity allowlist
// through /api/access/principals.
func cmdAccess(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	sub, subArgs := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		fmt.Println(accessUsage)
		return nil
	case "list", "ls":
		fs := flag.NewFlagSet("grain access list", flag.ContinueOnError)
		rest, err := parseArgs(fs, subArgs)
		if err != nil {
			return err
		}
		if len(rest) != 0 {
			return errors.New("usage: grain access list")
		}
		resp, err := c.ListAllowedPrincipals(ctx)
		if err != nil {
			return err
		}
		if out.json {
			out.encode(resp)
			return nil
		}
		if len(resp.Entries) == 0 {
			fmt.Println("No allowed users or groups.")
		} else {
			for _, p := range resp.Entries {
				by := p.AddedBy
				if by == "" {
					by = "-"
				}
				line := fmt.Sprintf("%s\t%s\tadded by %s\t%s", p.Entry, p.Role, by, p.AddedAt.UTC().Format(time.RFC3339))
				if p.Reason != "" {
					line += "\t" + p.Reason
				}
				if p.Label != "" {
					line += "\tlabel=" + p.Label
				}
				fmt.Println(line)
			}
		}
		for _, dropped := range resp.DroppedFlagEntries {
			fmt.Printf("warning: dropped flag entry %s (only user: and group: are imported; add a service account with 'grain access allow serviceAccount:<email> --reason <why>')\n", dropped)
		}
		return nil
	case "allow", "add":
		fs := flag.NewFlagSet("grain access allow", flag.ContinueOnError)
		reason := fs.String("reason", "", "why the entry is added (required for serviceAccount:, delegate:, and for a role with a dangerous scope)")
		role := fs.String("role", "", "the scope set the entry's sessions are held to (default grain/operator, or grain/delegated for delegate:)")
		label := fs.String("label", "", "optional short label ([a-z][a-z0-9-]{0,31}) for a delegate: entry (such as planning-app)")
		rest, err := parseArgs(fs, subArgs)
		if err != nil {
			return err
		}
		if len(rest) != 1 {
			return errors.New("usage: grain access allow [--role <set>] [--reason <why>] [--label <name>] <user:<email>|group:<email>|serviceAccount:<email>|delegate:<email>>")
		}
		resp, err := c.AllowPrincipal(ctx, ui.AllowPrincipalRequest{Entry: rest[0], Reason: *reason, Role: *role, Label: *label})
		if err != nil {
			return err
		}
		if out.json {
			out.encode(resp)
			return nil
		}
		if resp.Created {
			fmt.Printf("allowed %s as %s\n", resp.Principal.Entry, resp.Principal.Role)
		} else {
			fmt.Printf("already allowed: %s (as %s; change it with 'grain access role')\n", resp.Principal.Entry, resp.Principal.Role)
		}
		return nil
	case "role":
		fs := flag.NewFlagSet("grain access role", flag.ContinueOnError)
		reason := fs.String("reason", "", "why the role changes (required for a role with a dangerous scope)")
		rest, err := parseArgs(fs, subArgs)
		if err != nil {
			return err
		}
		if len(rest) != 2 {
			return errors.New("usage: grain access role [--reason <why>] <user:<email>|group:<email>|serviceAccount:<email>> <set>")
		}
		resp, err := c.SetPrincipalRole(ctx, rest[0], ui.SetPrincipalRoleRequest{Role: rest[1], Reason: *reason})
		if err != nil {
			return err
		}
		if out.json {
			out.encode(resp)
			return nil
		}
		if resp.Previous == resp.Role {
			fmt.Printf("%s already has role %s\n", resp.Entry, resp.Role)
		} else {
			fmt.Printf("%s: role %s -> %s\n", resp.Entry, resp.Previous, resp.Role)
		}
		return nil
	case "deny", "remove", "rm":
		fs := flag.NewFlagSet("grain access deny", flag.ContinueOnError)
		rest, err := parseArgs(fs, subArgs)
		if err != nil {
			return err
		}
		if len(rest) != 1 {
			return errors.New("usage: grain access deny <user:<email>|group:<email>|serviceAccount:<email>>")
		}
		resp, err := c.DenyPrincipal(ctx, rest[0])
		if err != nil {
			return err
		}
		if out.json {
			out.encode(resp)
			return nil
		}
		fmt.Printf("removed %s\n", resp.Entry)
		return nil
	default:
		return fmt.Errorf("unknown access subcommand %q\n%s", sub, accessUsage)
	}
}
