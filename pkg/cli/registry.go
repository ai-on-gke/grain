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
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/registry/api"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
)

const registryUsage = `usage: grain registry <status|register|unregister> [args]

  status
  register [-registry-url <url>] [-entry <name>] [-registry-token <tok>]
           [-oidc] [-oidc-audience <aud>] [-rotate-token]
           [-display-name <name>] [-purpose <text>] [-host-kind <kind>]
           [-listed] [-external-url <url>] [-owner-group <group>]
           [-standalone] [-token-file <path>] [<entry>]
  unregister [-registry-url <url>] [-entry <name>] [-registry-token <tok>]
             [-oidc] [-oidc-audience <aud>] [-keep-remote] [-standalone] [<entry>]

Self-registration and background heartbeats to a grain registry
(Settings -> Registry; design/design/registry.design.md).

register creates or updates a connected entry in the registry, stores any
minted grc_... registration credential in the daemon's secret store under
registry-registration-token, saves the registry configuration in Settings,
and sends an immediate heartbeat.`

// registryHTTPClient and registryOIDCToken are seams for unit tests so tests
// never touch the network or host gcloud/ADC credentials.
var (
	registryHTTPClient *http.Client
	registryOIDCToken  func(ctx context.Context, registryURL, audience string) (string, error)
)

type registryDaemonFlags struct {
	url          *string
	entry        *string
	authMode     *string
	oidcAudience *string
	tokenFile    *string
	displayName  *string
	purpose      *string
	hostKind     *string
	listed       *bool
	externalURL  *string
}

func registerRegistryFlags(fs *flag.FlagSet) registryDaemonFlags {
	const seedNote = " (seeds stored configuration on first use; edit in Settings or with 'grain registry' after that)"
	return registryDaemonFlags{
		url: fs.String("registry-url", "",
			"base URL of the grain registry this daemon self-registers with ($"+daemon.RegistryURLEnv+"; empty disables heartbeats)"+seedNote),
		entry: fs.String("registry-entry", "",
			"entry name this daemon registers and heartbeats as in the grain registry ($"+daemon.RegistryEntryEnv+")"+seedNote),
		authMode: fs.String("registry-auth-mode", "",
			"credential mode for registry heartbeats: \"token\" (secret "+secrets.RegistryRegistrationTokenSecret+", -registry-token-file, or $"+daemon.RegistryTokenEnv+") or \"oidc\" (ID token for -registry-oidc-audience)"+seedNote),
		oidcAudience: fs.String("registry-oidc-audience", "",
			"OIDC ID token audience when -registry-auth-mode=oidc (defaults to -registry-url)"+seedNote),
		tokenFile: fs.String("registry-token-file", "",
			"fallback file holding a grc_... registration credential when secret "+secrets.RegistryRegistrationTokenSecret+" is unset ($"+daemon.RegistryTokenEnv+" is also consulted)"),
		displayName: fs.String("registry-display-name", "",
			"human-readable display name advertised in the registry directory"+seedNote),
		purpose: fs.String("registry-purpose", "",
			"short description of this grain deployment's purpose in the registry directory"+seedNote),
		hostKind: fs.String("registry-host-kind", "",
			"connected entry host kind advertised to the registry: \"workstation\", \"kubernetes\", or \"other\" (defaults to the active sandbox backend)"+seedNote),
		listed: fs.Bool("registry-listed", true,
			"include this connected entry in default registry directory listings"+seedNote),
		externalURL: fs.String("registry-external-url", "",
			"optional https:// URL where operators reach this daemon directly"+seedNote),
	}
}

func (f registryDaemonFlags) apply(cfg *daemon.Config, passed map[string]bool) {
	urlVal := strings.TrimSpace(*f.url)
	if !passed["registry-url"] && urlVal == "" {
		urlVal = strings.TrimSpace(os.Getenv(daemon.RegistryURLEnv))
	}
	entryVal := strings.TrimSpace(*f.entry)
	if !passed["registry-entry"] && entryVal == "" {
		entryVal = strings.TrimSpace(os.Getenv(daemon.RegistryEntryEnv))
	}
	cfg.RegistryTokenFile = strings.TrimSpace(*f.tokenFile)
	cfg.Registry = model.RegistryConfig{
		URL:          urlVal,
		Entry:        entryVal,
		AuthMode:     model.RegistryAuthMode(strings.TrimSpace(*f.authMode)),
		OIDCAudience: strings.TrimSpace(*f.oidcAudience),
		DisplayName:  strings.TrimSpace(*f.displayName),
		Purpose:      strings.TrimSpace(*f.purpose),
		HostKind:     api.HostKind(strings.TrimSpace(*f.hostKind)),
		Listed:       *f.listed,
		ExternalURL:  strings.TrimSpace(*f.externalURL),
	}
}

func checkDaemonTail(cfg daemon.Config) error {
	if err := daemon.CheckGritURL(cfg); err != nil {
		return err
	}
	return checkRegistryConfig(cfg)
}

func checkRegistryConfig(cfg daemon.Config) error {
	r := cfg.Registry
	anySet := r.URL != "" || r.Entry != "" || r.AuthMode != "" || r.OIDCAudience != "" ||
		r.DisplayName != "" || r.Purpose != "" || r.HostKind != "" || r.ExternalURL != ""
	if !anySet {
		return nil
	}
	if (r.URL == "") != (r.Entry == "") {
		return errors.New("-registry-url and -registry-entry must be set together")
	}
	if _, err := model.NormalizeRegistryConfig(r, false); err != nil {
		return fmt.Errorf("registry configuration: %w", err)
	}
	return nil
}

// cmdRegistry implements `grain registry <status|register|unregister>`.
func cmdRegistry(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	if len(args) == 0 {
		args = []string{"status"}
	}
	sub, subArgs := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		fmt.Println(registryUsage)
		return nil
	case "status":
		return cmdRegistryStatus(ctx, c, out, subArgs)
	case "register":
		return cmdRegistryRegister(ctx, c, out, subArgs)
	case "unregister":
		return cmdRegistryUnregister(ctx, c, out, subArgs)
	default:
		return fmt.Errorf("unknown registry subcommand %q\n%s", sub, registryUsage)
	}
}

func cmdRegistryStatus(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain registry status", flag.ContinueOnError)
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return errors.New("usage: grain registry status")
	}
	snap, err := c.GetRegistry(ctx)
	if err != nil {
		return err
	}
	printRegistrySnapshot(out, snap)
	return nil
}

func cmdRegistryRegister(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain registry register", flag.ContinueOnError)
	regURLFlag := fs.String("registry-url", "", "base URL of the grain registry ($"+daemon.RegistryURLEnv+")")
	entryFlag := fs.String("entry", "", "entry name in the grain registry ($"+daemon.RegistryEntryEnv+"; or positional <entry>)")
	regTokenFlag := fs.String("registry-token", "", "bearer token for authenticating to the registry ($"+daemon.RegistryTokenEnv+")")
	oidcFlag := fs.Bool("oidc", false, "use OIDC ID tokens for registration and daemon heartbeats instead of minting a grc_... token")
	oidcAudFlag := fs.String("oidc-audience", "", "OIDC ID token audience for the registry (defaults to -registry-url)")
	rotateToken := fs.Bool("rotate-token", false, "rotate an existing connected entry's grc_... registration credential")
	displayName := fs.String("display-name", "", "human-readable display name in the registry directory")
	purpose := fs.String("purpose", "", "short description of this grain's purpose")
	hostKind := fs.String("host-kind", "", "connected entry host kind: \"workstation\", \"kubernetes\", or \"other\"")
	listed := fs.Bool("listed", true, "include this entry in default registry directory listings")
	externalURL := fs.String("external-url", "", "optional https:// URL where operators reach this daemon directly")
	ownerGroup := fs.String("owner-group", "", "optional group principal that co-owns this entry")
	standalone := fs.Bool("standalone", false, "register on the registry without writing settings or secrets to a local grain daemon")
	tokenFile := fs.String("token-file", "", "with -standalone, write a minted grc_... token to this file (mode 0600) instead of printing it")

	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		return errors.New(registryUsage)
	}
	passed := visitedFlags(fs)

	var existingCfg model.RegistryConfig
	var existingSnap model.RegistrySnapshot
	var daemonHasSecret bool
	if !*standalone {
		settings, sErr := c.GetSettings(ctx)
		if sErr != nil {
			return sErr
		}
		existingCfg = settings.Registry
		daemonHasSecret = settings.RegistryTokenSet
		if snap, snapErr := c.GetRegistry(ctx); snapErr == nil {
			existingSnap = snap
		}
	}

	regURL := firstNonEmptyTrimmed(*regURLFlag, os.Getenv(daemon.RegistryURLEnv), existingCfg.URL)
	if regURL == "" {
		return errors.New("-registry-url is required (or set $" + daemon.RegistryURLEnv + ")")
	}
	posEntry := ""
	if len(rest) == 1 {
		posEntry = rest[0]
	}
	if posEntry != "" && *entryFlag != "" && posEntry != *entryFlag {
		return fmt.Errorf("conflicting entry names %q and -entry=%q", posEntry, *entryFlag)
	}
	entryName := firstNonEmptyTrimmed(posEntry, *entryFlag, os.Getenv(daemon.RegistryEntryEnv), existingCfg.Entry)
	if entryName == "" {
		return errors.New("an entry name is required (pass <entry>, -entry, or $" + daemon.RegistryEntryEnv + ")")
	}

	newCfg := existingCfg
	newCfg.URL = regURL
	newCfg.Entry = entryName
	if *oidcFlag {
		newCfg.AuthMode = model.RegistryAuthOIDC
	} else if passed["oidc"] || newCfg.AuthMode == "" {
		newCfg.AuthMode = model.RegistryAuthToken
	}
	if passed["oidc-audience"] {
		newCfg.OIDCAudience = strings.TrimSpace(*oidcAudFlag)
	}
	if passed["display-name"] {
		newCfg.DisplayName = strings.TrimSpace(*displayName)
	}
	if passed["purpose"] {
		newCfg.Purpose = strings.TrimSpace(*purpose)
	}
	if passed["host-kind"] {
		newCfg.HostKind = api.HostKind(strings.TrimSpace(*hostKind))
	}
	if passed["listed"] || existingCfg.Entry == "" {
		newCfg.Listed = *listed
	}
	if passed["external-url"] {
		newCfg.ExternalURL = strings.TrimSpace(*externalURL)
	}
	if passed["owner-group"] {
		newCfg.OwnerGroup = strings.TrimSpace(*ownerGroup)
	}
	normCfg, err := model.NormalizeRegistryConfig(newCfg, false)
	if err != nil {
		return err
	}

	effectiveHostKind := normCfg.HostKind
	if effectiveHostKind == "" {
		if existingSnap.EffectiveHostKind != "" {
			effectiveHostKind = existingSnap.EffectiveHostKind
		} else {
			effectiveHostKind = api.HostKindWorkstation
		}
	}

	bearer, err := resolveRegistryCLIBearer(ctx, normCfg.URL, normCfg.EffectiveAudience(), *regTokenFlag)
	if err != nil {
		return err
	}

	var mintedToken string
	var remoteEntry *api.Entry
	// If the caller supplied an already-minted grc_... registration credential,
	// store it directly on the daemon and let the immediate heartbeat validate it
	// (grc_... tokens authorize heartbeats, not POST /api/entries).
	if strings.HasPrefix(bearer, api.RegistrationTokenPrefix) {
		if *standalone {
			return errors.New("-standalone cannot create an entry with a grc_... heartbeat token; authenticate with OIDC or an admin/creator token")
		}
		mintedToken = bearer
	} else {
		regClient, clientErr := newRegistryAPIClient(normCfg.URL, bearer)
		if clientErr != nil {
			return clientErr
		}
		scopes := model.ToAPIScopes(existingSnap.AdvertisedScopes)
		wantMint := normCfg.EffectiveAuthMode() == model.RegistryAuthToken
		remoteEntry, mintedToken, err = upsertConnectedEntry(ctx, regClient, normCfg, effectiveHostKind, scopes, wantMint, *rotateToken || *standalone || !daemonHasSecret)
		if err != nil {
			return err
		}
	}

	if *standalone {
		if *tokenFile != "" && mintedToken != "" {
			if err := writeTokenFile(*tokenFile, mintedToken); err != nil {
				return fmt.Errorf("writing -token-file: %w", err)
			}
		}
		if out.json {
			res := map[string]any{"entry": remoteEntry, "registryUrl": normCfg.URL}
			if mintedToken != "" && *tokenFile == "" {
				res["token"] = mintedToken
			}
			out.encode(res)
			return nil
		}
		fmt.Printf("registered connected entry %s on %s\n", normCfg.Entry, normCfg.URL)
		if mintedToken != "" {
			if *tokenFile != "" {
				fmt.Printf("registration token written to %s\n", *tokenFile)
			} else {
				fmt.Printf("registration token (shown once): %s\n", mintedToken)
			}
		}
		return nil
	}

	if mintedToken != "" {
		if err := c.SetSecret(ctx, secrets.RegistryRegistrationTokenSecret, secrets.AgentCredentialKey, mintedToken); err != nil {
			return fmt.Errorf("storing %s in daemon secrets: %w", secrets.RegistryRegistrationTokenSecret, err)
		}
	}
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{Registry: &normCfg}); err != nil {
		return fmt.Errorf("updating daemon registry settings: %w", err)
	}
	snap, hbErr := c.TriggerRegistryHeartbeat(ctx)
	if hbErr != nil {
		return fmt.Errorf("initial registry heartbeat failed: %w", hbErr)
	}
	printRegistrySnapshot(out, snap)
	return nil
}

func upsertConnectedEntry(
	ctx context.Context,
	client *api.Client,
	cfg model.RegistryConfig,
	hostKind api.HostKind,
	scopes []api.Scope,
	wantToken bool,
	needFreshToken bool,
) (*api.Entry, string, error) {
	existing, err := client.GetEntry(ctx, cfg.Entry)
	if err != nil && !api.IsNotFound(err) {
		return nil, "", err
	}
	listed := cfg.Listed
	if api.IsNotFound(err) {
		created, createErr := client.CreateEntry(ctx, api.CreateEntryRequest{
			Name:        cfg.Entry,
			Kind:        api.KindConnected,
			HostKind:    hostKind,
			ExternalURL: cfg.ExternalURL,
			DisplayName: cfg.DisplayName,
			Purpose:     cfg.Purpose,
			OwnerGroup:  cfg.OwnerGroup,
			Listed:      &listed,
			Tags:        slices.Clone(cfg.Tags),
			Scopes:      scopes,
			MintToken:   wantToken,
		})
		if createErr != nil {
			return nil, "", createErr
		}
		return &created.Entry, created.Token, nil
	}

	var minted string
	if existing.Kind == api.KindRedirect {
		promoted, promErr := client.PromoteConnected(ctx, cfg.Entry, api.PromoteConnectedRequest{
			HostKind:  hostKind,
			MintToken: wantToken,
		})
		if promErr != nil {
			return nil, "", promErr
		}
		existing = promoted.Entry
		minted = promoted.Token
	}

	extURL := cfg.ExternalURL
	disp := cfg.DisplayName
	purp := cfg.Purpose
	group := cfg.OwnerGroup
	hk := hostKind
	updated, updErr := client.UpdateEntry(ctx, cfg.Entry, api.UpdateEntryRequest{
		DisplayName: &disp,
		Purpose:     &purp,
		HostKind:    &hk,
		ExternalURL: &extURL,
		OwnerGroup:  &group,
		Listed:      &listed,
		Tags:        &cfg.Tags,
	})
	if updErr != nil {
		return nil, "", updErr
	}
	if wantToken && minted == "" && needFreshToken {
		rot, rotErr := client.RotateCredential(ctx, cfg.Entry, api.RotateCredentialRequest{})
		if rotErr != nil {
			return nil, "", rotErr
		}
		minted = rot.Token
	}
	return &updated, minted, nil
}

func cmdRegistryUnregister(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain registry unregister", flag.ContinueOnError)
	regURLFlag := fs.String("registry-url", "", "base URL of the grain registry ($"+daemon.RegistryURLEnv+")")
	entryFlag := fs.String("entry", "", "entry name in the grain registry ($"+daemon.RegistryEntryEnv+"; or positional <entry>)")
	regTokenFlag := fs.String("registry-token", "", "bearer token for deleting the entry on the registry ($"+daemon.RegistryTokenEnv+")")
	oidcFlag := fs.Bool("oidc", false, "authenticate remote entry deletion with an OIDC ID token")
	oidcAudFlag := fs.String("oidc-audience", "", "OIDC ID token audience for the registry (defaults to -registry-url)")
	keepRemote := fs.Bool("keep-remote", false, "clear the daemon's local registry settings and credential without deleting the entry on the registry")
	standalone := fs.Bool("standalone", false, "delete the entry on the registry without contacting a local grain daemon")

	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		return errors.New(registryUsage)
	}

	var existingCfg model.RegistryConfig
	if !*standalone {
		settings, sErr := c.GetSettings(ctx)
		if sErr != nil {
			return sErr
		}
		existingCfg = settings.Registry
	}

	posEntry := ""
	if len(rest) == 1 {
		posEntry = rest[0]
	}
	regURL := firstNonEmptyTrimmed(*regURLFlag, os.Getenv(daemon.RegistryURLEnv), existingCfg.URL)
	entryName := firstNonEmptyTrimmed(posEntry, *entryFlag, os.Getenv(daemon.RegistryEntryEnv), existingCfg.Entry)
	aud := firstNonEmptyTrimmed(*oidcAudFlag, existingCfg.OIDCAudience, regURL)

	remoteDeleted := false
	if !*keepRemote {
		if *standalone && (regURL == "" || entryName == "") {
			return errors.New("-standalone unregister requires -registry-url and <entry>")
		}
		if regURL != "" && entryName != "" {
			bearer, bErr := resolveRegistryCLIBearer(ctx, regURL, aud, *regTokenFlag)
			if bErr != nil && (*standalone || *oidcFlag || *regTokenFlag != "") {
				return bErr
			}
			regClient, cErr := newRegistryAPIClient(regURL, bearer)
			if cErr != nil {
				return cErr
			}
			if delErr := regClient.DeleteEntry(ctx, entryName); delErr != nil && !api.IsNotFound(delErr) {
				return fmt.Errorf("deleting %s on registry %s (pass -keep-remote to clear local daemon settings only): %w", entryName, regURL, delErr)
			}
			remoteDeleted = true
		}
	}

	if !*standalone {
		empty := model.RegistryConfig{}
		if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{Registry: &empty}); err != nil {
			return fmt.Errorf("clearing daemon registry settings: %w", err)
		}
		if delErr := c.DeleteSecret(ctx, secrets.RegistryRegistrationTokenSecret); delErr != nil {
			var notFound *ui.NotFoundError
			if !errors.As(delErr, &notFound) {
				return fmt.Errorf("deleting %s secret: %w", secrets.RegistryRegistrationTokenSecret, delErr)
			}
		}
	}

	if out.json {
		out.encode(map[string]any{
			"unregistered":  true,
			"entry":         entryName,
			"registryUrl":   regURL,
			"remoteDeleted": remoteDeleted,
		})
		return nil
	}
	switch {
	case *standalone:
		fmt.Printf("deleted registry entry %s from %s\n", entryName, regURL)
	case remoteDeleted:
		fmt.Printf("unregistered %s from %s and cleared local registry settings\n", entryName, regURL)
	case entryName != "":
		fmt.Printf("disconnected %s locally (remote entry kept on %s)\n", entryName, regURL)
	default:
		fmt.Println("registry configuration cleared")
	}
	return nil
}

func resolveRegistryCLIBearer(ctx context.Context, registryURL, audience, flagToken string) (string, error) {
	if tok := firstNonEmptyTrimmed(flagToken, os.Getenv(daemon.RegistryTokenEnv)); tok != "" {
		return tok, nil
	}
	if registryOIDCToken != nil {
		return registryOIDCToken(ctx, registryURL, audience)
	}
	rc := ui.NewHTTPClient(registryURL)
	rc.Audience = firstNonEmptyTrimmed(audience, registryURL)
	return rc.GoogleIDToken(ctx)
}

func newRegistryAPIClient(registryURL, bearer string) (*api.Client, error) {
	var opts []api.ClientOption
	if registryHTTPClient != nil {
		opts = append(opts, api.WithHTTPClient(registryHTTPClient))
	}
	if bearer != "" {
		opts = append(opts, api.WithTokenSource(api.StaticTokenSource(bearer)))
	}
	return api.NewClient(registryURL, opts...)
}

func printRegistrySnapshot(out *printer, snap model.RegistrySnapshot) {
	if out.json {
		out.encode(snap)
		return
	}
	if !snap.Enabled {
		fmt.Println("Registry: disabled (run 'grain registry register -registry-url <url> <entry>' or configure in Settings -> Registry)")
		return
	}
	fmt.Printf("State:       %s\n", snap.State)
	fmt.Printf("Registry:    %s\n", snap.Config.URL)
	fmt.Printf("Entry:       %s (host_kind=%s, listed=%t)\n", snap.Config.Entry, snap.EffectiveHostKind, snap.Config.Listed)
	if snap.Config.DisplayName != "" {
		fmt.Printf("Display:     %s\n", snap.Config.DisplayName)
	}
	if snap.Config.Purpose != "" {
		fmt.Printf("Purpose:     %s\n", snap.Config.Purpose)
	}
	if snap.Config.ExternalURL != "" {
		fmt.Printf("External:    %s\n", snap.Config.ExternalURL)
	}
	cred := string(snap.Config.EffectiveAuthMode())
	if snap.CredentialSource != "" && snap.CredentialSource != cred {
		cred += " (" + snap.CredentialSource + ")"
	} else if !snap.TokenConfigured {
		cred += " (missing credential)"
	}
	fmt.Printf("Auth:        %s\n", cred)
	fmt.Printf("Health:      %s (active=%d, awaiting_reply=%d)\n", snap.Health, snap.ActiveTasks, snap.AwaitingReplyTasks)
	if snap.LastHeartbeatAt != nil {
		line := snap.LastHeartbeatAt.UTC().Format(time.RFC3339)
		if snap.HeartbeatIntervalSeconds > 0 {
			line += fmt.Sprintf(" (every %ds)", snap.HeartbeatIntervalSeconds)
		}
		fmt.Printf("Heartbeat:   %s\n", line)
	} else {
		fmt.Println("Heartbeat:   never")
	}
	if snap.LastError != "" {
		fmt.Printf("Last error:  %s\n", snap.LastError)
	}
	for _, c := range snap.ScopeConflicts {
		prefix := c.PathPrefix
		if prefix == "" {
			prefix = "(repo root)"
		}
		fmt.Printf("Conflict:    %s %s conflicts with %s\n", c.Repo, prefix, c.ConflictWith)
	}
	if len(snap.AdvertisedScopes) == 0 {
		fmt.Println("Scopes:      (none)")
		return
	}
	fmt.Println("Scopes:")
	for _, s := range snap.AdvertisedScopes {
		prefix := s.PathPrefix
		if prefix == "" {
			prefix = "/"
		}
		fmt.Printf("  %-28s %-18s %-10s (%s)\n", s.Repo, prefix, s.Role, s.Origin)
	}
}

func visitedFlags(fs *flag.FlagSet) map[string]bool {
	m := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { m[f.Name] = true })
	return m
}

func firstNonEmptyTrimmed(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}
