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
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/hub/api"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// `grain hub delegating-apps ...` (registry admins) and
// `grain hub grants ...` (any signed-in person) manage what
// `delegation_policy: consent` reads: the service accounts registered to act
// on people's behalf, and each person's consent to one of them.

const hubDelegatingAppsUsage = `usage: grain hub delegating-apps <subcommand> [args]   (hub admins only)

  list     [-hub-url <url>]
  create   [-hub-url <url>] -principal <service-account-email> [-display-name <text>]
           [-description <text>] [-homepage <url>] [-owner <email>] [-owner-group <group>]
           [-max-access <read|write>] [-max-grant-ttl <duration>]
           [-require-subject-token] [-redirect-uri <url>]... <name>
  disable  [-hub-url <url>] <name>
  enable   [-hub-url <url>] <name>

A delegating app is a service account that may call grains on behalf of the
people who grant it consent (auth.delegation_policy: consent). -principal takes
the bare service-account email or serviceAccount:<email>; the hub stores
the serviceAccount: form either way.`

const hubGrantsUsage = `usage: grain hub grants <subcommand> [args]

  list     [-hub-url <url>] [-include-revoked]
  create   [-hub-url <url>] -entry <name|*>... [-access <read|write>] [-ttl <duration>] <app>
  revoke   [-hub-url <url>] <grant-id|app>

Grants are always your own: create consents, as the signed-in person, to <app>
acting for you against the named entries ("*" for every entry you can reach),
and revoke withdraws one of your grants by its dlg_... ID or by app name.`

func isHelpWord(s string) bool {
	switch s {
	case "-h", "-help", "--help", "help":
		return true
	}
	return false
}

func cmdHubDelegatingApps(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	if len(args) == 0 || isHelpWord(args[0]) {
		fmt.Println(hubDelegatingAppsUsage)
		return nil
	}
	sub, subArgs := args[0], args[1:]
	switch sub {
	case "list", "ls":
		return cmdHubDelegatingAppsList(ctx, c, out, subArgs)
	case "create":
		return cmdHubDelegatingAppsCreate(ctx, c, out, subArgs)
	case "disable":
		return cmdHubDelegatingAppsSetDisabled(ctx, c, out, subArgs, true)
	case "enable":
		return cmdHubDelegatingAppsSetDisabled(ctx, c, out, subArgs, false)
	default:
		return fmt.Errorf("unknown hub delegating-apps subcommand %q\n%s", sub, hubDelegatingAppsUsage)
	}
}

func cmdHubGrants(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	if len(args) == 0 || isHelpWord(args[0]) {
		fmt.Println(hubGrantsUsage)
		return nil
	}
	sub, subArgs := args[0], args[1:]
	switch sub {
	case "list", "ls":
		return cmdHubGrantsList(ctx, c, out, subArgs)
	case "create":
		return cmdHubGrantsCreate(ctx, c, out, subArgs)
	case "revoke":
		return cmdHubGrantsRevoke(ctx, c, out, subArgs)
	default:
		return fmt.Errorf("unknown hub grants subcommand %q\n%s", sub, hubGrantsUsage)
	}
}

func printDelegatingApp(a api.DelegatingApp) {
	state := "enabled"
	if a.DisabledAt != nil {
		state = "disabled " + a.DisabledAt.UTC().Format(time.RFC3339)
		if a.DisabledBy != "" {
			state += " by " + a.DisabledBy
		}
	}
	fmt.Printf("%-24s %-56s max_access=%-5s max_ttl=%-8s owner=%s %s\n",
		a.Name, a.Principal, a.MaxAccess, formatTTLSeconds(a.MaxGrantTTLSeconds), a.Owner, state)
}

func formatTTLSeconds(sec int64) string {
	d := time.Duration(sec) * time.Second
	if d > 0 && d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", int64(d/(24*time.Hour)))
	}
	return d.String()
}

func cmdHubDelegatingAppsList(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain hub delegating-apps list", flag.ContinueOnError)
	tf := bindHubTargetFlags(fs)
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return errors.New("usage: grain hub delegating-apps list [-hub-url <url>]")
	}
	regClient, _, err := tf.client(ctx, c)
	if err != nil {
		return err
	}
	apps, err := regClient.ListDelegatingApps(ctx)
	if err != nil {
		return err
	}
	if out.json {
		if apps == nil {
			apps = []api.DelegatingApp{}
		}
		out.encode(apps)
		return nil
	}
	if len(apps) == 0 {
		fmt.Println("no delegating apps registered")
		return nil
	}
	for _, a := range apps {
		printDelegatingApp(a)
	}
	return nil
}

func cmdHubDelegatingAppsCreate(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain hub delegating-apps create", flag.ContinueOnError)
	tf := bindHubTargetFlags(fs)
	principal := fs.String("principal", "", "service account the app calls as: an email, or serviceAccount:<email>")
	displayName := fs.String("display-name", "", "name people see when granting consent (defaults to the app name)")
	description := fs.String("description", "", "what the app does with the access it is granted")
	homepage := fs.String("homepage", "", "URL describing the app")
	owner := fs.String("owner", "", "person accountable for the app (defaults to you)")
	ownerGroup := fs.String("owner-group", "", "optional group co-owning the app")
	maxAccess := fs.String("max-access", "write", "most access a grant to this app may carry: read or write")
	maxTTL := fs.Duration("max-grant-ttl", 0, "longest a grant to this app may last before it has to be renewed; 0 means 90 days")
	requireSubject := fs.Bool("require-subject-token", false, "require a subject token alongside on-behalf-of requests")
	var redirectURIs multiStringFlag
	fs.Var(&redirectURIs, "redirect-uri", "consent redirect URI the app may use (repeatable)")

	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 || strings.TrimSpace(*principal) == "" {
		return errors.New("usage: grain hub delegating-apps create -principal <service-account-email> [flags] <name>")
	}
	if *maxTTL < 0 {
		return errors.New("-max-grant-ttl must not be negative")
	}
	regClient, _, err := tf.client(ctx, c)
	if err != nil {
		return err
	}
	app, err := regClient.CreateDelegatingApp(ctx, api.CreateDelegatingAppRequest{
		Name:                strings.TrimSpace(rest[0]),
		Principal:           strings.TrimSpace(*principal),
		DisplayName:         *displayName,
		Description:         *description,
		Homepage:            *homepage,
		Owner:               *owner,
		OwnerGroup:          *ownerGroup,
		MaxAccess:           *maxAccess,
		MaxGrantTTLSeconds:  int64(*maxTTL / time.Second),
		RequireSubjectToken: *requireSubject,
		RedirectURIs:        []string(redirectURIs),
	})
	if err != nil {
		return err
	}
	if out.json {
		out.encode(app)
		return nil
	}
	fmt.Printf("registered delegating app %s as %s\n", app.Name, app.Principal)
	printDelegatingApp(*app)
	return nil
}

func cmdHubDelegatingAppsSetDisabled(ctx context.Context, c *ui.HTTPClient, out *printer, args []string, disabled bool) error {
	verb := "enable"
	if disabled {
		verb = "disable"
	}
	fs := flag.NewFlagSet("grain hub delegating-apps "+verb, flag.ContinueOnError)
	tf := bindHubTargetFlags(fs)
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return fmt.Errorf("usage: grain hub delegating-apps %s [-hub-url <url>] <name>", verb)
	}
	regClient, _, err := tf.client(ctx, c)
	if err != nil {
		return err
	}
	app, err := regClient.SetDelegatingAppDisabled(ctx, strings.TrimSpace(rest[0]), disabled)
	if err != nil {
		return err
	}
	if out.json {
		out.encode(app)
		return nil
	}
	fmt.Printf("delegating app %s %sd\n", app.Name, verb)
	return nil
}

func printDelegationGrant(g api.DelegationGrant) {
	state := "expires " + g.ExpiresAt.UTC().Format(time.RFC3339)
	if g.RevokedAt != nil {
		state = "revoked " + g.RevokedAt.UTC().Format(time.RFC3339)
	}
	fmt.Printf("%-21s app=%-24s access=%-5s entries=%-24s %s\n",
		g.ID, g.App, g.Access, strings.Join(g.Entries, ","), state)
}

func cmdHubGrantsList(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain hub grants list", flag.ContinueOnError)
	tf := bindHubTargetFlags(fs)
	includeRevoked := fs.Bool("include-revoked", false, "also list grants you have revoked")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return errors.New("usage: grain hub grants list [-hub-url <url>] [-include-revoked]")
	}
	regClient, _, err := tf.client(ctx, c)
	if err != nil {
		return err
	}
	grants, err := regClient.ListDelegationGrants(ctx, *includeRevoked)
	if err != nil {
		return err
	}
	if out.json {
		if grants == nil {
			grants = []api.DelegationGrant{}
		}
		out.encode(grants)
		return nil
	}
	if len(grants) == 0 {
		fmt.Println("no delegation grants")
		return nil
	}
	for _, g := range grants {
		printDelegationGrant(g)
	}
	return nil
}

func cmdHubGrantsCreate(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain hub grants create", flag.ContinueOnError)
	tf := bindHubTargetFlags(fs)
	var entries multiStringFlag
	fs.Var(&entries, "entry", `entry the app may reach on your behalf, or "*" for every entry you can reach (repeatable)`)
	access := fs.String("access", "", "read or write (defaults to the app's max access)")
	ttl := fs.Duration("ttl", 0, "how long the grant lasts, capped at the app's max grant TTL; 0 means 30 days")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 || len(entries) == 0 {
		return errors.New("usage: grain hub grants create -entry <name|*>... [-access <read|write>] [-ttl <duration>] <app>")
	}
	if *ttl < 0 {
		return errors.New("-ttl must not be negative")
	}
	var list []string
	for _, e := range entries {
		for _, p := range strings.Split(e, ",") {
			if p = strings.TrimSpace(p); p != "" {
				list = append(list, p)
			}
		}
	}
	regClient, _, err := tf.client(ctx, c)
	if err != nil {
		return err
	}
	grant, err := regClient.CreateDelegationGrant(ctx, api.CreateDelegationGrantRequest{
		App:        strings.TrimSpace(rest[0]),
		Entries:    list,
		Access:     strings.TrimSpace(*access),
		TTLSeconds: int64(*ttl / time.Second),
	})
	if err != nil {
		return err
	}
	if out.json {
		out.encode(grant)
		return nil
	}
	fmt.Printf("granted %s %s access to %s until %s (%s)\n",
		grant.App, grant.Access, strings.Join(grant.Entries, ","), grant.ExpiresAt.UTC().Format(time.RFC3339), grant.ID)
	return nil
}

func cmdHubGrantsRevoke(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain hub grants revoke", flag.ContinueOnError)
	tf := bindHubTargetFlags(fs)
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("usage: grain hub grants revoke [-hub-url <url>] <grant-id|app>")
	}
	regClient, _, err := tf.client(ctx, c)
	if err != nil {
		return err
	}
	grant, err := regClient.RevokeDelegationGrant(ctx, strings.TrimSpace(rest[0]))
	if err != nil {
		return err
	}
	if out.json {
		out.encode(grant)
		return nil
	}
	fmt.Printf("revoked grant %s to %s\n", grant.ID, grant.App)
	return nil
}
