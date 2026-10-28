// setup.go is `grain setup`, bwsalmon/agents#358: a mode alongside
// daemon/ui/mcpserver (main.go's own switch), not one of runCLI's
// task-store commands, because it needs no store -- it exists to
// bootstrap the external infrastructure a store, or anything else here,
// would run against on a genuinely new installation.
//
// `grain setup gcp` wraps pkg/gcpsetup.EnsureInfrastructure: it builds
// the two GCP service accounts the gcp-key/gemini-key capabilities need
// and grants the IAM roles that let the minter account administer the
// agent account's keys (and, with -enable-gemini-key, mint API keys
// project-wide). scripts/setup.sh already does the *host* half of a new
// install (build, install, systemd, laying out the SQLite store's
// directory) but only ever seeds
// an already-minted GRAIN_GCP_SERVICE_ACCOUNT_KEY_FILE -- README.md's
// own "Accepted limits" list says plainly that "GCP token minting has
// never run against a real project," and this is what an operator runs
// first to have one to seed setup.sh with. -mint-key plus -key-out
// produces exactly that file.
//
// Not everything here can always be automated: whatever GCP identity
// this runs as (Application Default Credentials by default -- the same
// `export GOOGLE_APPLICATION_CREDENTIALS=...` pattern a gcp-key
// capability's own sandbox placement uses) may lack permission for one
// step (most commonly, creating IAM bindings needs an Owner/IAM-Admin
// role a project Editor does not have). Rather than aborting,
// EnsureInfrastructure records that step as manual with the exact gcloud
// command to run, and cmdSetupGCP prints every manual step at the end --
// safe, and expected, to run this command again afterward: every step is
// get-or-create, so it picks up wherever the manual steps left off.
package cli

import (
	"context"
	"flag"
	"fmt"
	"os"

	"google.golang.org/api/option"

	"github.com/ai-on-gke/grain/pkg/gcpsetup"
)

func setupCmd(args []string) {
	if len(args) == 0 || args[0] != "gcp" {
		fmt.Fprintln(os.Stderr, `usage: grain setup gcp [flags]

Bootstraps the external GCP infrastructure the gcp-key and gemini-key
capabilities need: the agent and minter service accounts, the IAM roles
between them, and (with -enable-gemini-key) the project-level API-key
admin role and the APIs both capabilities call. Safe to run more than
once -- every step is get-or-create, so a re-run after fixing a manual
step (see its own output) picks up right there.`)
		os.Exit(2)
	}
	if err := cmdSetupGCP(context.Background(), args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "grain setup gcp: "+err.Error())
		os.Exit(1)
	}
}

func cmdSetupGCP(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("grain setup gcp", flag.ExitOnError)
	project := fs.String("project", "", "GCP project to bootstrap (required)")
	credentialsFile := fs.String("credentials-file", "",
		"a GCP service-account key file to authenticate with; empty uses Application Default Credentials "+
			"(gcloud auth application-default login, or GOOGLE_APPLICATION_CREDENTIALS)")
	agentAccountID := fs.String("agent-account-id", gcpsetup.DefaultAgentAccountID,
		"account_id of the service account gcp-key mints per-task keys for")
	agentAccountCount := fs.Int("agent-account-count", 1,
		"number of agent service accounts to create for gcp-key (default 1)")
	minterAccountID := fs.String("minter-account-id", gcpsetup.DefaultMinterAccountID,
		"account_id of the service account that mints/revokes the agent account's keys")
	enableGeminiKey := fs.Bool("enable-gemini-key", false,
		"also grant the minter account roles/serviceusage.apiKeysAdmin and enable the Gemini/API Keys APIs, for the gemini-key capability")
	mintKey := fs.Bool("mint-key", false,
		"mint a key for the minter account and write it to -key-out -- do this once, on a new installation; "+
			"a later run leaves an existing key alone (see this file's own doc comment on why)")
	keyOut := fs.String("key-out", "",
		"path to write the minted minter key to (required with -mint-key); refused if the file already has content, "+
			"the same never-overwrite rule scripts/setup.sh's own seed_secret follows")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *project == "" {
		return fmt.Errorf("-project is required")
	}
	if *mintKey && *keyOut == "" {
		return fmt.Errorf("-key-out is required with -mint-key")
	}
	if *mintKey {
		if info, err := os.Stat(*keyOut); err == nil && info.Size() > 0 {
			return fmt.Errorf("-key-out %s already has content -- refusing to overwrite an existing key; "+
				"remove it first if you really want a new one minted", *keyOut)
		}
	}

	opts := []option.ClientOption{}
	if *credentialsFile != "" {
		opts = append(opts, option.WithCredentialsFile(*credentialsFile))
	}
	admin, err := gcpsetup.NewRealAdmin(ctx, *project, opts...)
	if err != nil {
		return err
	}

	result, err := gcpsetup.EnsureInfrastructure(ctx, admin, gcpsetup.Options{
		ProjectID:         *project,
		AgentAccountID:    *agentAccountID,
		AgentAccountCount: *agentAccountCount,
		MinterAccountID:   *minterAccountID,
		EnableGeminiKey:   *enableGeminiKey,
		MintMinterKey:     *mintKey,
	})
	if err != nil {
		return err
	}

	printSetupResult(result)

	if *mintKey && result.MinterKeyJSON != "" {
		if err := os.WriteFile(*keyOut, []byte(result.MinterKeyJSON), 0o600); err != nil {
			return fmt.Errorf("writing -key-out: %w", err)
		}
		fmt.Printf("\nWrote the minter's key to %s\n", *keyOut)
	}

	if !result.AllManual() {
		fmt.Printf("\nNext: feed these into scripts/setup.sh or `grain settings`:\n")
		fmt.Printf("  GRAIN_GCP_PROJECT=%s\n", *project)
		fmt.Printf("  GRAIN_GCP_SERVICE_ACCOUNT_EMAIL=%s\n", result.AgentEmail)
		if len(result.AgentEmails) > 1 {
			fmt.Printf("  GRAIN_GCP_SERVICE_ACCOUNT_COUNT=%d\n", len(result.AgentEmails))
		}
		if *mintKey && result.MinterKeyJSON != "" {
			fmt.Printf("  GRAIN_GCP_SERVICE_ACCOUNT_KEY_FILE=%s\n", *keyOut)
		}
	}
	return nil
}

func printSetupResult(result gcpsetup.Result) {
	fmt.Println("grain setup gcp:")
	var manual []gcpsetup.Step
	for _, s := range result.Steps {
		switch s.Status {
		case gcpsetup.StepDone:
			fmt.Printf("  [done]   %-70s %s\n", s.Name, s.Detail)
		case gcpsetup.StepManual:
			fmt.Printf("  [manual] %-70s %s\n", s.Name, s.Detail)
			manual = append(manual, s)
		}
	}
	if len(manual) == 0 {
		return
	}
	fmt.Println("\nSome steps need a human with more access than this ran with. Run these, then re-run this command:")
	for _, s := range manual {
		fmt.Printf("  %s\n", s.Command)
	}
}
