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
	"log"
	"os"
	"sort"
	"strings"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

const hostdsUsage = `usage: grain hostds <list|register|set-owner> [args]

  list
  register [-owner <email>] [-label k=v]... [-token-file <path>] <name>
  set-owner <name> <email|"">

The hostd registry (Settings -> Hostds): the grain hostds this daemon
takes host work through. register prints the new hostd's token once, or
writes it to -token-file (mode 0600) for grain hostd -server -token-file.

A hostd's owner is the person or service account answerable for it. A
hostd that dials in through IAP or another identity verifier's proxy
must be verified as its owner, so give a remote hostd the identity it
connects as: its service account (Workload Identity or a key), or the
login of the person it runs as. A hostd over loopback is named by its
token alone and needs no owner.`

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
	return nil
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

// hostdRemote is the acceptor for a grain hostd dialing in with the token
// in tokenFile, or $GRAIN_HOSTD_TOKEN, as hostd name owned by owner; nil
// when neither names a token.
func hostdRemote(tokenFile, name, owner string) (*hostd.Remote, error) {
	token := strings.TrimSpace(os.Getenv("GRAIN_HOSTD_TOKEN"))
	if tokenFile != "" {
		t, err := daemon.ReadTokenFile(tokenFile)
		if err != nil {
			return nil, fmt.Errorf("reading -hostd-token-file: %w", err)
		}
		token = t
	}
	if token == "" {
		return nil, nil
	}
	if name == "" {
		name = hostd.DefaultSharedName
	}
	if err := model.ValidateHostdName(name); err != nil {
		return nil, fmt.Errorf("-hostd-token-name: %w", err)
	}
	if name == model.LocalHostd {
		return nil, fmt.Errorf("-hostd-token-name: %q is the co-located hostd's name", name)
	}
	owner, err := model.NormalizeHostdOwner(owner)
	if err != nil {
		return nil, fmt.Errorf("-hostd-token-owner: %w", err)
	}
	log.Printf("grain daemon: -hostd-token-file is deprecated and goes in the next release: register hostd %s in the hostd registry (POST /api/hostds) and give it the token it answers with", name)
	return &hostd.Remote{Token: token, SharedName: name, SharedOwner: owner, Logf: log.Printf}, nil
}
