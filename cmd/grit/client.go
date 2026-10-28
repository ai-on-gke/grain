package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/bwsalmon/grain/pkg/grit"
)

type scopeSlice []grit.ClientScope

func (s *scopeSlice) String() string { return "scope:repo" }
func (s *scopeSlice) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		sc, err := grit.ParseScopeSpec(part)
		if err != nil {
			return err
		}
		*s = append(*s, sc)
	}
	return nil
}

func clientCmd(_ context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "grit client: usage: grit client <add|scope|revoke|list> [flags]")
		return 2
	}
	sub := args[0]
	rest := args[1:]

	switch sub {
	case "add":
		fs := flag.NewFlagSet("grit client add", flag.ContinueOnError)
		fs.SetOutput(stderr)
		dataDir := fs.String("data-dir", "./grit-data", "state directory")
		id := fs.String("id", "", "client identifier")
		maxJobs := fs.Int("max-jobs", 0, "maximum concurrent jobs for this client")
		runsPerMin := fs.Int("runs-per-min", 0, "maximum run submissions per minute")
		uploadPerMin := fs.Int64("upload-bytes-per-min", 0, "maximum tree upload bytes per minute")
		var scopes scopeSlice
		fs.Var(&scopes, "scope", "client scope grant (repeatable scope:repo, e.g. submit:owner/*,read:*)")
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		clientID := strings.TrimSpace(*id)
		if clientID == "" && fs.NArg() > 0 {
			clientID = strings.TrimSpace(fs.Arg(0))
		}
		if clientID == "" {
			fmt.Fprintln(stderr, "grit client add: client id is required (-id or positional argument)")
			return 2
		}
		tok, secretHash, err := grit.GenerateClientToken(clientID)
		if err != nil {
			fmt.Fprintf(stderr, "grit client add: %v\n", err)
			return 1
		}
		store, err := grit.OpenStore(*dataDir)
		if err != nil {
			fmt.Fprintf(stderr, "grit client add: %v\n", err)
			return 1
		}
		defer func() { _ = store.Close() }()
		err = store.CreateOrUpdateClient(grit.ClientRecord{
			ID:                       clientID,
			SecretSHA256:             secretHash,
			Scopes:                   scopes,
			MaxConcurrentJobs:        *maxJobs,
			RunsPerMinute:            *runsPerMin,
			TreeUploadBytesPerMinute: *uploadPerMin,
		})
		if err != nil {
			fmt.Fprintf(stderr, "grit client add: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, tok)
		return 0

	case "scope":
		fs := flag.NewFlagSet("grit client scope", flag.ContinueOnError)
		fs.SetOutput(stderr)
		dataDir := fs.String("data-dir", "./grit-data", "state directory")
		id := fs.String("id", "", "client identifier")
		var scopes scopeSlice
		fs.Var(&scopes, "scope", "replacement scope grant (repeatable scope:repo)")
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		clientID := strings.TrimSpace(*id)
		if clientID == "" && fs.NArg() > 0 {
			clientID = strings.TrimSpace(fs.Arg(0))
		}
		if clientID == "" {
			fmt.Fprintln(stderr, "grit client scope: client id is required")
			return 2
		}
		store, err := grit.OpenStore(*dataDir)
		if err != nil {
			fmt.Fprintf(stderr, "grit client scope: %v\n", err)
			return 1
		}
		defer func() { _ = store.Close() }()
		if err := store.SetClientScopes(clientID, scopes); err != nil {
			fmt.Fprintf(stderr, "grit client scope: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "updated scopes for %s\n", clientID)
		return 0

	case "revoke":
		fs := flag.NewFlagSet("grit client revoke", flag.ContinueOnError)
		fs.SetOutput(stderr)
		dataDir := fs.String("data-dir", "./grit-data", "state directory")
		id := fs.String("id", "", "client identifier")
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		clientID := strings.TrimSpace(*id)
		if clientID == "" && fs.NArg() > 0 {
			clientID = strings.TrimSpace(fs.Arg(0))
		}
		if clientID == "" {
			fmt.Fprintln(stderr, "grit client revoke: client id is required")
			return 2
		}
		store, err := grit.OpenStore(*dataDir)
		if err != nil {
			fmt.Fprintf(stderr, "grit client revoke: %v\n", err)
			return 1
		}
		defer func() { _ = store.Close() }()
		if err := store.RevokeClient(clientID); err != nil {
			fmt.Fprintf(stderr, "grit client revoke: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "revoked %s\n", clientID)
		return 0

	case "list":
		fs := flag.NewFlagSet("grit client list", flag.ContinueOnError)
		fs.SetOutput(stderr)
		dataDir := fs.String("data-dir", "./grit-data", "state directory")
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		store, err := grit.OpenStore(*dataDir)
		if err != nil {
			fmt.Fprintf(stderr, "grit client list: %v\n", err)
			return 1
		}
		defer func() { _ = store.Close() }()
		clients, err := store.ListClients()
		if err != nil {
			fmt.Fprintf(stderr, "grit client list: %v\n", err)
			return 1
		}
		for _, c := range clients {
			status := "active"
			if c.Revoked {
				status = "revoked"
			}
			var scStrs []string
			for _, sc := range c.Scopes {
				scStrs = append(scStrs, string(sc.Scope)+":"+sc.Repo)
			}
			fmt.Fprintf(stdout, "%s\t%s\t%s\n", c.ID, status, strings.Join(scStrs, ","))
		}
		return 0

	default:
		fmt.Fprintf(stderr, "grit client: unknown subcommand %q (want add, scope, revoke, or list)\n", sub)
		return 2
	}
}
