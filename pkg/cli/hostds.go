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
	"time"

	"github.com/ai-on-gke/grain/pkg/ui"
)

const hostRegistryUsage = `usage: grain host <subcommand> [args]

  list
  pending
  accept [-name <name>] [-labels k=v,...] [-capacity N] <id>
  reject <id>
  codes
  invite [-name <code-name>] [-expires <dur>] [-max-uses N] [-labels k=v,...] [-capacity N] [-allow <principals>]
  rotate-code <code-name>
  revoke-code <code-name>
  rename <old-name> <new-name>
  update [-owner <email>] [-labels k=v,...] [-capacity N] <name>
  revoke <name>
  delete <name>
  rekey [-token-file <path>] [-key-file <path>] <name>
  register [-owner <email>] [-label k=v]... [-token-file <path>] <name>
  install <name>
  set-owner <name> <email|"">
  delete-revoked

Hosts (Settings -> Hosts): invite machines with one shared enrollment snippet,
accept or reject the hosts that join, and manage accepted hosts. invite
creates an enrollment code; a host joins with grain host -server -join
<code> as a verified person and waits for the deployment owner to accept
it (pending, accept, reject).

register is the legacy per-host token registry: it prints the new host's
token once, or writes it to -token-file (mode 0600) for grain host -server
-token-file, and then how to install it on its machine. install prints
those instructions again for a host already registered; they never carry
its token. rekey moves a registered host to a key-bound credential.

A host's owner is the person or service account answerable for it. A
host that dials in through IAP or another identity verifier's proxy
must be verified as its owner, so give a remote host the identity it
connects as: its service account (Workload Identity or a key), or the
login of the person it runs as. A host over loopback is named by its
token alone and needs no owner.

delete removes a revoked host from the registry, with its tokens, so its
name can be registered again. A live host is refused -- revoke it first
(Settings -> Hosts) -- and so is one a model connection's host or host
fallback, or a capability server's host, still names. Usage-limit pauses
keyed to it are cleared and listed. Past runs keep its name.
delete-revoked deletes every revoked host nothing still names, and lists
the ones it skipped.`

// hostRegistryVerb reports whether word, after "grain host", names one of
// the host registry's subcommands (cmdHostds) rather than starting the
// host agent, whose arguments are all flags. It must name every
// subcommand cmdHostds takes (TestEveryHostSubcommandReachesTheRegistry):
// one missing here starts the host agent instead, which a machine with no
// /etc/grain/hostd.yaml answers with "lstat /etc/grain/hostd.yaml" -- what
// `grain host delete-revoked` said until it was added.
func hostRegistryVerb(word string) bool {
	switch word {
	case "list", "ls", "register", "create", "install", "set-owner", "help",
		"pending", "accept", "reject", "rekey", "codes", "invite", "rotate-code", "revoke-code",
		"rename", "update", "revoke", "delete", "rm", "delete-revoked":
		return true
	}
	return false
}

// cmdHostds is `grain host <subcommand>` (alias `grain hosts`, hidden aliases
// `grain hostds` and `grain hostd <verb>`): the host registry and enrollment
// handshake over /api/hostds.
func cmdHostds(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	sub, subArgs := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		fmt.Println(hostRegistryUsage)
		return nil
	case "list", "ls":
		fs := flag.NewFlagSet("grain host list", flag.ContinueOnError)
		rest, err := parseArgs(fs, subArgs)
		if err != nil {
			return err
		}
		if len(rest) != 0 {
			return errors.New("usage: grain host list")
		}
		list, err := c.ListHostds(ctx)
		if err != nil {
			return err
		}
		printHostds(out, list)
		return nil
	case "pending":
		return cmdHostPending(ctx, c, out, subArgs)
	case "accept":
		return cmdHostAccept(ctx, c, out, subArgs)
	case "reject":
		return cmdHostReject(ctx, c, out, subArgs)
	case "codes":
		return cmdHostCodes(ctx, c, out, subArgs)
	case "invite":
		return cmdHostInvite(ctx, c, out, subArgs)
	case "rotate-code":
		return cmdHostRotateCode(ctx, c, out, subArgs)
	case "revoke-code":
		return cmdHostRevokeCode(ctx, c, out, subArgs)
	case "rekey":
		return cmdHostRekey(ctx, c, out, subArgs)
	case "rename":
		fs := flag.NewFlagSet("grain host rename", flag.ContinueOnError)
		rest, err := parseArgs(fs, subArgs)
		if err != nil {
			return err
		}
		if len(rest) != 2 {
			return errors.New("usage: grain host rename <old-name> <new-name>")
		}
		h, err := c.RenameHostd(ctx, rest[0], rest[1])
		if err != nil {
			return err
		}
		if out.json {
			out.encode(h)
			return nil
		}
		fmt.Printf("renamed host %s -> %s\n", rest[0], h.Name)
		return nil
	case "update":
		return cmdHostUpdate(ctx, c, out, subArgs)
	case "revoke":
		fs := flag.NewFlagSet("grain host revoke", flag.ContinueOnError)
		rest, err := parseArgs(fs, subArgs)
		if err != nil {
			return err
		}
		if len(rest) != 1 {
			return errors.New("usage: grain host revoke <name>")
		}
		h, err := c.RevokeHostd(ctx, rest[0])
		if err != nil {
			return err
		}
		if out.json {
			out.encode(h)
			return nil
		}
		fmt.Printf("revoked host %s\n", h.Name)
		return nil
	case "register", "create":
		return cmdHostdsRegister(ctx, c, out, subArgs)
	case "install":
		fs := flag.NewFlagSet("grain host install", flag.ContinueOnError)
		rest, err := parseArgs(fs, subArgs)
		if err != nil {
			return err
		}
		if len(rest) != 1 {
			return errors.New("usage: grain host install <name>")
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
			return fmt.Errorf("the daemon sent no install instructions for host %s", rest[0])
		}
		printHostdInstall(&inst)
		return nil
	case "set-owner":
		fs := flag.NewFlagSet("grain host set-owner", flag.ContinueOnError)
		rest, err := parseArgs(fs, subArgs)
		if err != nil {
			return err
		}
		if len(rest) != 2 {
			return errors.New(`usage: grain host set-owner <name> <email|"">`)
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
		fmt.Printf("host %s: owner %s\n", h.Name, ownerOrNone(h.Owner))
		return nil
	case "delete", "rm":
		fs := flag.NewFlagSet("grain host delete", flag.ContinueOnError)
		rest, err := parseArgs(fs, subArgs)
		if err != nil {
			return err
		}
		if len(rest) != 1 {
			return errors.New("usage: grain host delete <name>")
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
		fs := flag.NewFlagSet("grain host delete-revoked", flag.ContinueOnError)
		rest, err := parseArgs(fs, subArgs)
		if err != nil {
			return err
		}
		if len(rest) != 0 {
			return errors.New("usage: grain host delete-revoked")
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
			fmt.Printf("skipped host %s: %s\n", sk.Name, sk.Reason)
		}
		if len(res.Deleted) == 0 && len(res.Skipped) == 0 {
			fmt.Println("No revoked hosts to delete.")
		}
		return nil
	default:
		return fmt.Errorf("unknown host subcommand %q\n%s", sub, hostRegistryUsage)
	}
}

func cmdHostUpdate(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain host update", flag.ContinueOnError)
	owner := fs.String("owner", "", "update the host's owner email or service account")
	labelsCSV := fs.String("labels", "", "replace labels with comma-separated k=v pairs")
	var labelList stringList
	fs.Var(&labelList, "label", "k=v label for the host (repeatable)")
	capacity := fs.Int("capacity", -1, "set maximum concurrent agent CLI runs (0 for unlimited)")
	var ownerSet, labelsSet bool
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "owner":
			ownerSet = true
		case "labels", "label":
			labelsSet = true
		}
	})
	if len(rest) != 1 {
		return errors.New("usage: grain host update [-owner <email>] [-labels k=v,...] [-capacity N] <name>")
	}
	var req ui.UpdateHostdRequest
	if ownerSet {
		req.Owner = owner
	}
	if labelsSet {
		labels, _, err := parseHostLabelFlags(*labelsCSV, labelList)
		if err != nil {
			return err
		}
		if labels == nil {
			labels = map[string]string{}
		}
		req.Labels = &labels
	}
	if *capacity >= 0 {
		capVal := *capacity
		req.Capacity = &capVal
	}
	h, err := c.UpdateHostd(ctx, rest[0], req)
	if err != nil {
		return err
	}
	if out.json {
		out.encode(h)
		return nil
	}
	fmt.Printf("updated host %s (owner %s, capacity %d, labels %s)\n", h.Name, ownerOrNone(h.Owner), h.Capacity, formatHostLabels(h.Labels))
	return nil
}

func cmdHostdsRegister(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain host register", flag.ContinueOnError)
	owner := fs.String("owner", "", "the host's owner: the email or service account it connects as through IAP")
	tokenFile := fs.String("token-file", "", "write the host's token to this file (mode 0600) rather than print it")
	var labels stringList
	fs.Var(&labels, "label", "k=v the host is selected by (repeatable)")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New(hostRegistryUsage)
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
	fmt.Printf("registered host %s, owner %s\n", issued.Hostd.Name, ownerOrNone(issued.Hostd.Owner))
	if *tokenFile != "" {
		fmt.Printf("its token is in %s: grain host -server <this daemon's URL> -token-file %s\n", *tokenFile, *tokenFile)
	} else {
		fmt.Printf("its token, shown this once: %s\n", issued.Token)
	}
	if issued.Hostd.Owner == "" {
		fmt.Println("it has no owner: it can dial in over loopback, but not through IAP until `grain host set-owner` gives it one")
	}
	printHostdInstall(issued.Install)
	return nil
}

// printHostdInstall prints how to install a host on its machine, as the
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

// printDeletedHostd says a host was deleted, and which usage-limit
// pauses keyed to it went with it.
func printDeletedHostd(d ui.DeletedHostd) {
	fmt.Printf("deleted host %s from the registry\n", d.Name)
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
		fmt.Println("No hosts registered.")
		return
	}
	for _, h := range list {
		state := h.State
		if state == "" {
			state = "offline"
			switch {
			case h.Revoked():
				state = "revoked"
			case h.Connection != nil && h.Connection.Connected:
				state = "connected"
			}
		}
		if state == "connected" && h.Connection != nil && h.Connection.Principal != "" {
			state += " as " + h.Connection.Principal
		}
		if h.Shared {
			state += " (deprecated shared -hostd-token-file)"
		}
		checkin := "-"
		if h.LastSeenAt != nil {
			checkin = h.LastSeenAt.UTC().Format(time.RFC3339)
		}
		authInfo := "legacy-token"
		if h.KeyFingerprint != "" {
			authInfo = h.KeyFingerprint
		}
		fmt.Printf("%s\t%s\towner %s\tcheckin %s\t%s\tcap %d\t%s\n",
			h.Name, state, ownerOrNone(h.Owner), checkin, authInfo, h.Capacity, formatHostLabels(h.Labels))
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
