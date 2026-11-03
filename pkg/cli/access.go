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

const accessUsage = `usage: grain access <list|allow|deny> [args]

  list
  allow <user:<email>|group:<email>>
  deny  <user:<email>|group:<email>>

Manage the allowed users and groups list (Settings -> Access): who may
sign in when authenticated by Cloud IAP or another registered
IdentityVerifier. Adding an existing entry is a no-op.`

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
				fmt.Printf("%s\tadded by %s\t%s\n", p.Entry, by, p.AddedAt.UTC().Format(time.RFC3339))
			}
		}
		for _, dropped := range resp.DroppedFlagEntries {
			fmt.Printf("warning: dropped unsupported flag entry %s (only user: and group: are supported)\n", dropped)
		}
		return nil
	case "allow", "add":
		fs := flag.NewFlagSet("grain access allow", flag.ContinueOnError)
		rest, err := parseArgs(fs, subArgs)
		if err != nil {
			return err
		}
		if len(rest) != 1 {
			return errors.New("usage: grain access allow <user:<email>|group:<email>>")
		}
		resp, err := c.AllowPrincipal(ctx, ui.AllowPrincipalRequest{Entry: rest[0]})
		if err != nil {
			return err
		}
		if out.json {
			out.encode(resp)
			return nil
		}
		if resp.Created {
			fmt.Printf("allowed %s\n", resp.Principal.Entry)
		} else {
			fmt.Printf("already allowed: %s\n", resp.Principal.Entry)
		}
		return nil
	case "deny", "remove", "rm":
		fs := flag.NewFlagSet("grain access deny", flag.ContinueOnError)
		rest, err := parseArgs(fs, subArgs)
		if err != nil {
			return err
		}
		if len(rest) != 1 {
			return errors.New("usage: grain access deny <user:<email>|group:<email>>")
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
