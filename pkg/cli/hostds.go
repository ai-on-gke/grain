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
	"os"
	"sort"
	"strings"

	"github.com/ai-on-gke/grain/pkg/ui"
)

const hostdsUsage = `usage: grain hostds <list|register|install|set-owner|delete|delete-revoked> [args]

  list
  register [-owner <email>] [-label k=v]... [-token-file <path>] <name>
  install <name>
  set-owner <name> <email|"">
  delete <name>
  delete-revoked

The hostd registry (Settings -> Hostds): the grain hostds this daemon
takes host work through. register prints the new hostd's token once, or
writes it to -token-file (mode 0600) for grain hostd -server -token-file,
and then how to install it on its machine. install prints those
instructions again for a hostd already registered; they never carry its
token, which the installer asks for or reads from $GRAIN_HOSTD_TOKEN.

A hostd's owner is the person or service account answerable for it. A
hostd that dials in through IAP or another identity verifier's proxy
must be verified as its owner, so give a remote hostd the identity it
connects as: its service account (Workload Identity or a key), or the
login of the person it runs as. A hostd over loopback is named by its
token alone and needs no owner.

delete removes a revoked hostd from the registry, with its tokens, so its
name can be registered again. A live hostd is refused -- revoke it first
(Settings -> Hostds) -- and so is one a model connection's hostd or
hostdFallback, or a capability server's hostd, still names. Usage-limit
pauses keyed to it are cleared and listed. Past runs keep its name.
delete-revoked deletes every revoked hostd nothing still names, and lists
the ones it skipped.`

// cmdHostds is `grain hostds`: the hostd registry, through /api/hostds.
func cmdHostds(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	sub, subArgs := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		fmt.Println(hostdsUsage)
		return nil
	case "list", "ls":
		list, err := c.ListHostds(ctx)
		if err != nil {
			return err
		}
		printHostds(out, list)
		return nil
	case "register", "create":
		return cmdHostdsRegister(ctx, c, out, subArgs)
	case "install":
		fs := flag.NewFlagSet("grain hostds install", flag.ContinueOnError)
		rest, err := parseArgs(fs, subArgs)
		if err != nil {
			return err
		}
		if len(rest) != 1 {
			return errors.New("usage: grain hostds install <name>")
		}
		inst, err := c.HostdInstall(ctx, rest[0])
		if err != nil {
			return err
		}
		if out.json {
			out.encode(inst)
			return nil
		}
		if len(inst.Lines) == 0 {
			return fmt.Errorf("the daemon sent no install instructions for hostd %s", rest[0])
		}
		printHostdInstall(&inst)
		return nil
	case "set-owner":
		fs := flag.NewFlagSet("grain hostds set-owner", flag.ContinueOnError)
		rest, err := parseArgs(fs, subArgs)
		if err != nil {
			return err
		}
		if len(rest) != 2 {
			return errors.New(`usage: grain hostds set-owner <name> <email|"">`)
		}
		owner := rest[1]
		h, err := c.UpdateHostd(ctx, rest[0], ui.UpdateHostdRequest{Owner: &owner})
		if err != nil {
			return err
		}
		if out.json {
			out.encode(h)
			return nil
		}
		fmt.Printf("hostd %s: owner %s\n", h.Name, ownerOrNone(h.Owner))
		return nil
	case "delete", "rm":
		fs := flag.NewFlagSet("grain hostds delete", flag.ContinueOnError)
		rest, err := parseArgs(fs, subArgs)
		if err != nil {
			return err
		}
		if len(rest) != 1 {
			return errors.New("usage: grain hostds delete <name>")
		}
		d, err := c.DeleteHostd(ctx, rest[0])
		if err != nil {
			return err
		}
		if out.json {
			out.encode(d)
			return nil
		}
		printDeletedHostd(d)
		return nil
	case "delete-revoked":
		fs := flag.NewFlagSet("grain hostds delete-revoked", flag.ContinueOnError)
		rest, err := parseArgs(fs, subArgs)
		if err != nil {
			return err
		}
		if len(rest) != 0 {
			return errors.New("usage: grain hostds delete-revoked")
		}
		res, err := c.DeleteRevokedHostds(ctx)
		if err != nil {
			return err
		}
		if out.json {
			out.encode(res)
			return nil
		}
		for _, d := range res.Deleted {
			printDeletedHostd(d)
		}
		for _, sk := range res.Skipped {
			fmt.Printf("skipped hostd %s: %s\n", sk.Name, sk.Reason)
		}
		if len(res.Deleted) == 0 && len(res.Skipped) == 0 {
			fmt.Println("No revoked hostds to delete.")
		}
		return nil
	default:
		return fmt.Errorf("unknown hostds subcommand %q\n%s", sub, hostdsUsage)
	}
}

func cmdHostdsRegister(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain hostds register", flag.ContinueOnError)
	owner := fs.String("owner", "", "the hostd's owner: the email or service account it connects as through IAP")
	tokenFile := fs.String("token-file", "", "write the hostd's token to this file (mode 0600) rather than print it")
	var labels stringList
	fs.Var(&labels, "label", "k=v the hostd is selected by (repeatable)")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New(hostdsUsage)
	}
	req := ui.CreateHostdRequest{Name: rest[0], Owner: *owner}
	if req.Labels, err = pairs("-label", labels); err != nil {
		return err
	}
	issued, err := c.CreateHostd(ctx, req)
	if err != nil {
		return err
	}
	if *tokenFile != "" {
		if err := writeTokenFile(*tokenFile, issued.Token); err != nil {
			return fmt.Errorf("writing -token-file: %w", err)
		}
	}
	if out.json {
		if *tokenFile != "" {
			issued.Token = ""
		}
		out.encode(issued)
		return nil
	}
	fmt.Printf("registered hostd %s, owner %s\n", issued.Hostd.Name, ownerOrNone(issued.Hostd.Owner))
	if *tokenFile != "" {
		fmt.Printf("its token is in %s: grain hostd -server <this daemon's URL> -token-file %s\n", *tokenFile, *tokenFile)
	} else {
		fmt.Printf("its token, shown this once: %s\n", issued.Token)
	}
	if issued.Hostd.Owner == "" {
		fmt.Println("it has no owner: it can dial in over loopback, but not through IAP until `grain hostds set-owner` gives it one")
	}
	printHostdInstall(issued.Install)
	return nil
}

// printHostdInstall prints how to install a hostd on its machine, as the
// daemon rendered it (ui.RegisterHostdInstall): its note, then its lines
// indented as one block. Nothing when the daemon sent none.
func printHostdInstall(inst *ui.HostdInstall) {
	if inst == nil || len(inst.Lines) == 0 {
		return
	}
	fmt.Println("to install it on its machine, run:")
	for _, l := range inst.Lines {
		fmt.Println("    " + l)
	}
	if inst.Note != "" {
		fmt.Println(inst.Note)
	}
}

// printDeletedHostd says a hostd was deleted, and which usage-limit
// pauses keyed to it went with it.
func printDeletedHostd(d ui.DeletedHostd) {
	fmt.Printf("deleted hostd %s from the registry\n", d.Name)
	for _, p := range d.ClearedPauses {
		fmt.Printf("  cleared its usage-limit pause %s\n", p)
	}
}

func ownerOrNone(owner string) string {
	if owner == "" {
		return "(none)"
	}
	return owner
}

func printHostds(out *printer, list []ui.HostdView) {
	if out.json {
		out.encode(list)
		return
	}
	if len(list) == 0 {
		fmt.Println("No hostds registered.")
		return
	}
	for _, h := range list {
		state := "disconnected"
		switch {
		case h.Revoked():
			state = "revoked"
		case h.Connection != nil && h.Connection.Connected:
			state = "connected"
			if h.Connection.Principal != "" {
				state += " as " + h.Connection.Principal
			}
		case h.Connection == nil:
			state = "-"
		}
		if h.Shared {
			state += " (deprecated shared -hostd-token-file)"
		}
		var labels []string
		for k, v := range h.Labels {
			labels = append(labels, k+"="+v)
		}
		sort.Strings(labels)
		fmt.Printf("%s\towner %s\t%s\t%s\n", h.Name, ownerOrNone(h.Owner), state, strings.Join(labels, ","))
	}
}

// writeTokenFile writes token to path readable by its owner alone, even
// when path already exists with a wider mode (which os.WriteFile keeps).
func writeTokenFile(path, token string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.WriteString(token + "\n"); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
