package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"github.com/bwsalmon/grain/pkg/ui"
)

// cmdGCPKey is `grain gcp-key`: the deployment's named GCP keys, each
// adding capability gcp-key:<name> (docs/team-gcp-keys.md), through
// /api/gcp-keys (single-tenant reset stage 4e).
func cmdGCPKey(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	const usage = "usage: grain gcp-key <list|get|set|delete> [args]"
	if len(args) == 0 {
		return errors.New(usage)
	}
	sub, subArgs := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		fmt.Println(usage)
		return nil
	case "list", "ls":
		return cmdGCPKeyList(ctx, c, out, subArgs)
	case "get":
		return cmdGCPKeyGet(ctx, c, out, subArgs)
	case "set":
		return cmdGCPKeySet(ctx, c, out, subArgs)
	case "delete", "rm":
		return cmdGCPKeyDelete(ctx, c, out, subArgs)
	default:
		return fmt.Errorf("unknown gcp-key subcommand %q", sub)
	}
}

func cmdGCPKeyList(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain gcp-key list", flag.ContinueOnError)
	if _, err := parseArgs(fs, args); err != nil {
		return err
	}
	keys, err := c.ListGCPKeys(ctx)
	if err != nil {
		return err
	}
	if out.json {
		out.encode(keys)
		return nil
	}
	if len(keys) == 0 {
		fmt.Println("No GCP keys configured.")
		return nil
	}
	for _, k := range keys {
		fmt.Printf("- %s:\n    capability: %s\n    project: %s\n    service_account: %s\n    count: %d\n    max_age: %s\n    minter_credential: %s\n",
			k.Name, k.CapabilityName(), k.ProjectID, k.ServiceAccountEmail, k.ServiceAccountCount, k.MaxKeyAge, k.MinterCredential)
	}
	return nil
}

func cmdGCPKeyGet(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain gcp-key get", flag.ContinueOnError)
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return errors.New("usage: grain gcp-key get <name>")
	}
	key, err := c.GetGCPKey(ctx, rest[0])
	if err != nil {
		return err
	}
	if out.json {
		out.encode(key)
		return nil
	}
	fmt.Printf("Name:              %s\n", key.Name)
	fmt.Printf("Capability:        %s\n", key.CapabilityName())
	fmt.Printf("Project:           %s\n", key.ProjectID)
	fmt.Printf("Service Account:   %s\n", key.ServiceAccountEmail)
	fmt.Printf("Account Count:     %d\n", key.ServiceAccountCount)
	fmt.Printf("Max Key Age:       %s\n", key.MaxKeyAge)
	fmt.Printf("Minter Credential: %s\n", key.MinterCredential)
	return nil
}

func cmdGCPKeySet(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain gcp-key set", flag.ContinueOnError)
	project := fs.String("project", "", "GCP project ID")
	account := fs.String("account", "", "GCP agent service account email")
	count := fs.Int("count", 1, "number of agent service accounts")
	maxAge := fs.String("max-age", "", "maximum key age duration (e.g. 1h, 30m)")
	minter := fs.String("minter", "", "minter secret name (defaults to gcp-key-minter-<name>)")
	description := fs.String("description", "", "human-readable description of the key (kept as it is when omitted; pass --description= to clear it)")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return errors.New("usage: grain gcp-key set <name> --project <project-id> --account <sa-email> [--count <n>] [--max-age <duration>] [--minter <minter-cred>] [--description <text>]")
	}
	name := rest[0]
	if *project == "" {
		return errors.New("--project is required")
	}
	if *account == "" {
		return errors.New("--account is required")
	}

	req := ui.SetGCPKeyRequest{
		Name:                name,
		ProjectID:           *project,
		ServiceAccountEmail: *account,
		ServiceAccountCount: *count,
		MaxKeyAge:           *maxAge,
		MinterCredential:    *minter,
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "description" {
			req.Description = description
		}
	})
	key, err := c.SetGCPKey(ctx, req)
	if err != nil {
		return err
	}
	if out.json {
		out.encode(key)
		return nil
	}
	fmt.Printf("Configured GCP key %q (%s).\n", key.Name, key.CapabilityName())
	return nil
}

func cmdGCPKeyDelete(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain gcp-key delete", flag.ContinueOnError)
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return errors.New("usage: grain gcp-key delete <name>")
	}
	name := rest[0]
	if err := c.DeleteGCPKey(ctx, name); err != nil {
		return err
	}
	if out.json {
		out.encode(map[string]string{"status": "deleted", "name": name})
		return nil
	}
	fmt.Printf("Deleted GCP key %q.\n", name)
	return nil
}
