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
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/registry/api"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
)

const registryUsage = `usage: grain registry <subcommand> [args]

  status
  list              [-registry-url <url>] [-all] [-owner <email>] [-owner-group <group>]
                    [-kind <instance|connected>] [-status <online|degraded|offline>]
                    [-listed] [-conflict] [-include-deleted]
  directory         [-registry-url <url>] [-q <text>] [-repo <owner/name>] [-tag <tag>]
                    [-status <online|degraded|offline>] [-kind <kind>] [-host-kind <kind>]
                    [-owner <email>] [-limit <n>]
  route             [-registry-url <url>] [-repo <owner/name>] [-path <subpath>]
                    [-include-offline] [<repo> [<path>]]
  show              [-registry-url <url>] <name>
  create            [-registry-url <url>] [-name <name>] [-shape <shape>] [-zone <zone>]
                    [-display-name <text>] [-purpose <text>] [-owner-group <group>]
                    [-listed] [-tags <t1,t2>] [-allow-ssh] [-ship-logs]
                    [-scope <repo[:prefix[:role]]>]... [<name>]
  register          [-registry-url <url>] [-entry <name>] [-registry-token <tok>]
                    [-oidc] [-oidc-audience <aud>] [-mint-token] [-rotate-token]
                    [-display-name <name>] [-purpose <text>] [-host-kind <kind>]
                    [-connected-principal <email>] [-listed] [-owner-group <group>]
                    [-tags <t1,t2>] [-scope <repo[:prefix[:role]]>]...
                    [-standalone] [-token-file <path>] [<entry>]
  unregister        [-registry-url <url>] [-entry <name>] [-registry-token <tok>]
                    [-oidc] [-oidc-audience <aud>] [-keep-remote] [-standalone] [<entry>]
  update            [-registry-url <url>] [-display-name <text>] [-purpose <text>]
                    [-host-kind <kind>] [-connected-principal <email>]
                    [-owner-group <group>] [-listed] [-unlisted] [-tags <t1,t2>]
                    [-shape <shape>] [-allow-ssh] [-ship-logs] <name>
  scopes            [-registry-url <url>] [-set <repo[:prefix[:role]]>]... [-clear] <name>
  rotate-credential [-registry-url <url>] [-label <label>] [-cred-id <uuid>]
                    [-revoke-previous] [-overlap <duration>] [-token-file <path>] <name>
  upgrade           [-registry-url <url>] [-version <vX.Y.Z>] <name>
  resize            [-registry-url <url>] -shape <shape> [-version <vX.Y.Z>] <name>
  delete            [-registry-url <url>] [-confirm <name>] [-yes] [-delete-disk] <name>
  restore           [-registry-url <url>] <name>
  transfer          [-registry-url <url>] -to <email> [-owner-group <group>]
                    [-clear-owner-group] <name>
  accept-transfer   [-registry-url <url>] <name>

Self-registration, directory discovery, path routing, and control-plane management
for a grain registry (Settings -> Registry; design/design/registry.design.md).`

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
	connect      *bool
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
		connect: fs.Bool("registry-connect", false,
			"supervise an outbound wss://<registry-host>/api/entries/<name>/connect tunnel and serve accepted mux streams in-process behind the registry assertion verifier gate ($"+daemon.RegistryConnectEnv+"; requires -ui-registry-issuer, -ui-registry-audience, and -ui-registry-keys-url)"),
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
	connectVal := *f.connect
	if !passed["registry-connect"] && !connectVal {
		if env := strings.TrimSpace(os.Getenv(daemon.RegistryConnectEnv)); env != "" {
			connectVal = env == "1" || strings.EqualFold(env, "true") || strings.EqualFold(env, "yes")
		}
	}
	cfg.RegistryTokenFile = strings.TrimSpace(*f.tokenFile)
	cfg.RegistryConnect = connectVal
	cfg.Registry = model.RegistryConfig{
		URL:          urlVal,
		Entry:        entryVal,
		AuthMode:     model.RegistryAuthMode(strings.TrimSpace(*f.authMode)),
		OIDCAudience: strings.TrimSpace(*f.oidcAudience),
		DisplayName:  strings.TrimSpace(*f.displayName),
		Purpose:      strings.TrimSpace(*f.purpose),
		HostKind:     api.HostKind(strings.TrimSpace(*f.hostKind)),
		Listed:       *f.listed,
	}
}

func checkRegistryConfig(cfg daemon.Config) error {
	if err := daemon.CheckRegistryConnect(cfg); err != nil {
		return err
	}
	r := cfg.Registry
	anySet := r.URL != "" || r.Entry != "" || r.AuthMode != "" || r.OIDCAudience != "" ||
		r.DisplayName != "" || r.Purpose != "" || r.HostKind != ""
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

// cmdRegistry implements `grain registry <subcommand>`.
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
	case "list":
		return cmdRegistryList(ctx, c, out, subArgs)
	case "directory":
		return cmdRegistryDirectory(ctx, c, out, subArgs)
	case "route":
		return cmdRegistryRoute(ctx, c, out, subArgs)
	case "show":
		return cmdRegistryShow(ctx, c, out, subArgs)
	case "create":
		return cmdRegistryCreate(ctx, c, out, subArgs)
	case "register":
		return cmdRegistryRegister(ctx, c, out, subArgs)
	case "unregister":
		return cmdRegistryUnregister(ctx, c, out, subArgs)
	case "update":
		return cmdRegistryUpdate(ctx, c, out, subArgs)
	case "scopes":
		return cmdRegistryScopes(ctx, c, out, subArgs)
	case "rotate-credential":
		return cmdRegistryRotateCredential(ctx, c, out, subArgs)
	case "upgrade":
		return cmdRegistryUpgrade(ctx, c, out, subArgs)
	case "resize":
		return cmdRegistryResize(ctx, c, out, subArgs)
	case "delete":
		return cmdRegistryDelete(ctx, c, out, subArgs)
	case "restore":
		return cmdRegistryRestore(ctx, c, out, subArgs)
	case "transfer":
		return cmdRegistryTransfer(ctx, c, out, subArgs)
	case "accept-transfer":
		return cmdRegistryAcceptTransfer(ctx, c, out, subArgs)
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
	mintToken := fs.Bool("mint-token", false, "mint a fresh grc_... registration credential for this connected entry")
	rotateToken := fs.Bool("rotate-token", false, "rotate an existing connected entry's grc_... registration credential")
	displayName := fs.String("display-name", "", "human-readable display name in the registry directory")
	purpose := fs.String("purpose", "", "short description of this grain's purpose")
	hostKind := fs.String("host-kind", "", "connected entry host kind: \"workstation\", \"kubernetes\", or \"other\"")
	connectedPrincipal := fs.String("connected-principal", "", "optional workload service account email authorized to connect on behalf of this entry")
	listed := fs.Bool("listed", true, "include this entry in default registry directory listings")
	ownerGroup := fs.String("owner-group", "", "optional group principal that co-owns this entry")
	tagsFlag := fs.String("tags", "", "comma-separated catalog tags")
	var scopeFlags multiStringFlag
	fs.Var(&scopeFlags, "scope", "declared repository scope <owner/repo>[:<path_prefix>[:<primary|secondary>]] (repeatable)")
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
	var configRepoOnly bool
	if !*standalone {
		settings, sErr := c.GetSettings(ctx)
		if sErr != nil {
			return sErr
		}
		existingCfg = settings.Registry
		daemonHasSecret = settings.RegistryTokenSet
		configRepoOnly = settings.ConfigRepoOnly
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
	if passed["owner-group"] {
		newCfg.OwnerGroup = strings.TrimSpace(*ownerGroup)
	}
	if passed["tags"] {
		newCfg.Tags = parseCommaTags(*tagsFlag)
	}
	var explicitScopes []api.ScopeInput
	if len(scopeFlags) > 0 {
		var scErr error
		explicitScopes, scErr = parseScopeInputs(scopeFlags)
		if scErr != nil {
			return scErr
		}
	}
	normCfg, err := model.NormalizeRegistryConfig(newCfg, false)
	if err != nil {
		return err
	}
	cfgChanged := !reflect.DeepEqual(existingCfg, normCfg)
	if !*standalone && configRepoOnly && cfgChanged {
		if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{Registry: &normCfg}); err != nil {
			return fmt.Errorf("updating daemon registry settings: %w", err)
		}
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
		scopes := model.ToScopeInputs(existingSnap.AdvertisedScopes)
		if len(explicitScopes) > 0 {
			scopes = explicitScopes
		}
		wantMint := normCfg.EffectiveAuthMode() == model.RegistryAuthToken || *mintToken
		retargeting := (existingCfg.URL != "" && existingCfg.URL != normCfg.URL) ||
			(existingCfg.Entry != "" && !strings.EqualFold(existingCfg.Entry, normCfg.Entry))
		remoteEntry, mintedToken, err = upsertConnectedEntry(ctx, regClient, normCfg, effectiveHostKind, strings.TrimSpace(*connectedPrincipal), scopes, wantMint, *rotateToken || *mintToken || *standalone || !daemonHasSecret || retargeting)
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
		printConnectedConfigSnippet(normCfg.URL, normCfg.Entry)
		return nil
	}

	retargetingURL := existingCfg.URL != "" && existingCfg.URL != normCfg.URL
	if retargetingURL {
		// Clear the previous registry URL before swapping the stored grc_...
		// credential so the background manager never sends the old registry's
		// token to the new URL or the new registry's token to the old URL.
		// Running UpdateSettings first also enforces human-principal, RBAC and
		// config-repo lock checks before touching secrets.Store.
		empty := model.RegistryConfig{}
		if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{Registry: &empty}); err != nil {
			return fmt.Errorf("clearing previous daemon registry target before retargeting: %w", err)
		}
		if mintedToken != "" {
			if err := c.SetSecret(ctx, secrets.RegistryRegistrationTokenSecret, secrets.AgentCredentialKey, mintedToken); err != nil {
				return fmt.Errorf("storing %s in daemon secrets: %w", secrets.RegistryRegistrationTokenSecret, err)
			}
		} else if daemonHasSecret {
			if delErr := c.DeleteSecret(ctx, secrets.RegistryRegistrationTokenSecret); delErr != nil {
				var notFound *ui.NotFoundError
				if !errors.As(delErr, &notFound) {
					return fmt.Errorf("deleting previous %s secret: %w", secrets.RegistryRegistrationTokenSecret, delErr)
				}
			}
		}
		if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{Registry: &normCfg}); err != nil {
			return fmt.Errorf("updating daemon registry settings: %w", err)
		}
	} else {
		if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{Registry: &normCfg}); err != nil {
			return fmt.Errorf("updating daemon registry settings: %w", err)
		}
		if mintedToken != "" {
			if err := c.SetSecret(ctx, secrets.RegistryRegistrationTokenSecret, secrets.AgentCredentialKey, mintedToken); err != nil {
				return fmt.Errorf("storing %s in daemon secrets: %w", secrets.RegistryRegistrationTokenSecret, err)
			}
		}
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
	connectedPrincipal string,
	scopes []api.ScopeInput,
	wantToken bool,
	needFreshToken bool,
) (*api.Entry, string, error) {
	_, err := client.GetEntry(ctx, cfg.Entry)
	if err != nil && !api.IsNotFound(err) {
		return nil, "", err
	}
	listed := cfg.Listed
	if api.IsNotFound(err) {
		credLabel := ""
		if wantToken {
			credLabel = "grain-cli"
		}
		created, createErr := client.CreateEntry(ctx, api.CreateEntryRequest{
			Name:               cfg.Entry,
			Kind:               api.KindConnected,
			HostKind:           hostKind,
			ConnectedPrincipal: connectedPrincipal,
			DisplayName:        cfg.DisplayName,
			Purpose:            cfg.Purpose,
			OwnerGroup:         cfg.OwnerGroup,
			Listed:             &listed,
			Tags:               slices.Clone(cfg.Tags),
			Scopes:             scopes,
			CredentialLabel:    credLabel,
		})
		if createErr != nil {
			return nil, "", createErr
		}
		tok := ""
		if wantToken {
			tok = created.RegistrationToken
		}
		return &created.Entry, tok, nil
	}

	disp := cfg.DisplayName
	purp := cfg.Purpose
	group := cfg.OwnerGroup
	hk := hostKind
	updReq := api.UpdateEntryRequest{
		DisplayName: &disp,
		Purpose:     &purp,
		HostKind:    &hk,
		OwnerGroup:  &group,
		Listed:      &listed,
		Tags:        &cfg.Tags,
	}
	if connectedPrincipal != "" {
		cp := connectedPrincipal
		updReq.ConnectedPrincipal = &cp
	}
	updated, updErr := client.UpdateEntry(ctx, cfg.Entry, updReq)
	if updErr != nil {
		return nil, "", updErr
	}
	if len(scopes) > 0 {
		if scResp, scErr := client.SetScopes(ctx, cfg.Entry, scopes); scErr == nil && scResp != nil {
			updated.Scopes = scResp.Scopes
		}
	}
	var minted string
	if wantToken && needFreshToken {
		rot, rotErr := client.RotateCredential(ctx, cfg.Entry, api.RotateCredentialRequest{Label: "grain-cli"})
		if rotErr != nil {
			return nil, "", rotErr
		}
		minted = rot.RegistrationToken
	}
	return updated, minted, nil
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
	var configRepoOnly bool
	if !*standalone {
		settings, sErr := c.GetSettings(ctx)
		if sErr != nil {
			return sErr
		}
		existingCfg = settings.Registry
		configRepoOnly = settings.ConfigRepoOnly
	}
	if !*standalone && configRepoOnly && !reflect.DeepEqual(existingCfg, model.RegistryConfig{}) {
		empty := model.RegistryConfig{}
		if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{Registry: &empty}); err != nil {
			return fmt.Errorf("clearing daemon registry settings: %w", err)
		}
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
			if delErr := regClient.DeleteEntry(ctx, entryName, api.DeleteEntryRequest{ConfirmName: entryName}); delErr != nil && !api.IsNotFound(delErr) {
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
	cfg := api.ClientConfig{
		BaseURL:    registryURL,
		HTTPClient: registryHTTPClient,
	}
	if strings.HasPrefix(bearer, api.RegistrationTokenPrefix) {
		cfg.RegistrationToken = bearer
	} else {
		cfg.BearerToken = bearer
	}
	return api.NewClient(cfg)
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
		fmt.Printf("Conflict:    %s %s conflicts with %s\n", c.Repo, prefix, c.ConflictingEntry)
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

type multiStringFlag []string

func (m *multiStringFlag) String() string {
	return strings.Join(*m, ",")
}

func (m *multiStringFlag) Set(val string) error {
	*m = append(*m, strings.TrimSpace(val))
	return nil
}

type registryTargetFlags struct {
	url          *string
	token        *string
	oidcAudience *string
}

func bindRegistryTargetFlags(fs *flag.FlagSet) registryTargetFlags {
	return registryTargetFlags{
		url:          fs.String("registry-url", "", "base URL of the grain registry ($"+daemon.RegistryURLEnv+")"),
		token:        fs.String("registry-token", "", "bearer token for authenticating to the registry ($"+daemon.RegistryTokenEnv+")"),
		oidcAudience: fs.String("oidc-audience", "", "OIDC ID token audience for the registry (defaults to -registry-url)"),
	}
}

func (tf registryTargetFlags) client(ctx context.Context, c *ui.HTTPClient) (*api.Client, string, error) {
	regURL := firstNonEmptyTrimmed(*tf.url, os.Getenv(daemon.RegistryURLEnv))
	aud := strings.TrimSpace(*tf.oidcAudience)
	if regURL == "" && c != nil {
		if settings, err := c.GetSettings(ctx); err == nil {
			regURL = strings.TrimSpace(settings.Registry.URL)
			if aud == "" {
				aud = strings.TrimSpace(settings.Registry.OIDCAudience)
			}
		}
	}
	if regURL == "" {
		return nil, "", errors.New("-registry-url is required (or set $" + daemon.RegistryURLEnv + ")")
	}
	if aud == "" {
		aud = regURL
	}
	bearer, err := resolveRegistryCLIBearer(ctx, regURL, aud, *tf.token)
	if err != nil {
		return nil, "", err
	}
	cli, err := newRegistryAPIClient(regURL, bearer)
	if err != nil {
		return nil, "", err
	}
	return cli, regURL, nil
}

func parseCommaTags(raw string) []string {
	var out []string
	for _, p := range strings.Split(raw, ",") {
		t := strings.ToLower(strings.TrimSpace(p))
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

func parseScopeInputs(rawSpecs []string) ([]api.ScopeInput, error) {
	var out []api.ScopeInput
	for _, raw := range rawSpecs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		parts := strings.SplitN(raw, ":", 3)
		repo, err := api.NormalizeRepo(parts[0])
		if err != nil {
			return nil, fmt.Errorf("invalid scope %q: %w", raw, err)
		}
		prefix := ""
		role := api.RolePrimary
		switch len(parts) {
		case 2:
			second := strings.TrimSpace(parts[1])
			if second == string(api.RolePrimary) || second == string(api.RoleSecondary) {
				role = api.ScopeRole(second)
			} else {
				prefix = strings.Trim(second, "/")
			}
		case 3:
			prefix = strings.Trim(strings.TrimSpace(parts[1]), "/")
			r := api.ScopeRole(strings.TrimSpace(parts[2]))
			if r != "" {
				if !r.Valid() {
					return nil, fmt.Errorf("invalid scope role %q in %q (want primary or secondary)", r, raw)
				}
				role = r
			}
		}
		out = append(out, api.ScopeInput{
			Repo:       repo,
			PathPrefix: prefix,
			Role:       role,
		})
	}
	return out, nil
}

func printConnectedConfigSnippet(registryURL, entryName string) {
	fmt.Printf("outbound tunnel config snippet:\n")
	fmt.Printf("  grain daemon -registry-url %s -registry-entry %s -registry-connect -ui-require-verifier=registry\n", registryURL, entryName)
}

func cmdRegistryList(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain registry list", flag.ContinueOnError)
	tf := bindRegistryTargetFlags(fs)
	all := fs.Bool("all", false, "list all entries across the registry (admin only)")
	owner := fs.String("owner", "", "filter by owner email")
	ownerGroup := fs.String("owner-group", "", "filter by co-owning group")
	kind := fs.String("kind", "", "filter by entry kind: instance or connected")
	status := fs.String("status", "", "filter by status: online, degraded, or offline")
	listed := fs.Bool("listed", false, "filter by listed state")
	conflict := fs.Bool("conflict", false, "filter entries that have active primary scope conflicts")
	includeDeleted := fs.Bool("include-deleted", false, "include soft-deleted entries within their 30-day reservation window")

	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return errors.New("usage: grain registry list [flags]")
	}
	passed := visitedFlags(fs)

	regClient, _, err := tf.client(ctx, c)
	if err != nil {
		return err
	}
	q := api.ListEntriesQuery{
		Owner:          strings.TrimSpace(*owner),
		OwnerGroup:     strings.TrimSpace(*ownerGroup),
		Kind:           api.EntryKind(strings.TrimSpace(*kind)),
		Status:         api.EntryStatus(strings.TrimSpace(*status)),
		IncludeDeleted: *includeDeleted,
		All:            *all,
		Mine:           !*all && *owner == "" && *ownerGroup == "",
	}
	if passed["listed"] {
		v := *listed
		q.Listed = &v
	}
	if passed["conflict"] {
		v := *conflict
		q.Conflict = &v
	}
	entries, err := regClient.ListEntries(ctx, q)
	if err != nil {
		return err
	}
	if out.json {
		out.encode(entries)
		return nil
	}
	if len(entries) == 0 {
		fmt.Println("no registry entries found")
		return nil
	}
	for _, e := range entries {
		urlStr := e.URL
		if urlStr == "" {
			urlStr = "-"
		}
		fmt.Printf("%-24s %-10s %-10s %-9s listed=%-5t owner=%-24s %s\n",
			e.Name, e.Kind, e.State, e.Status, e.Listed, e.Owner, urlStr)
	}
	return nil
}

func cmdRegistryDirectory(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain registry directory", flag.ContinueOnError)
	tf := bindRegistryTargetFlags(fs)
	query := fs.String("q", "", "free-text search across name, display name, purpose, owner, tags, and scopes")
	repo := fs.String("repo", "", "filter by repository (<owner/name>)")
	tag := fs.String("tag", "", "filter by catalog tag")
	status := fs.String("status", "", "filter by status: online, degraded, or offline")
	kind := fs.String("kind", "", "filter by entry kind: instance or connected")
	hostKind := fs.String("host-kind", "", "filter by host kind: registry-vm, workstation, kubernetes, or other")
	owner := fs.String("owner", "", "filter by owner email")
	limit := fs.Int("limit", 0, "maximum number of entries to return")

	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		return errors.New("usage: grain registry directory [flags] [<query>]")
	}
	qVal := strings.TrimSpace(*query)
	if len(rest) == 1 && qVal == "" {
		qVal = strings.TrimSpace(rest[0])
	}

	regClient, _, err := tf.client(ctx, c)
	if err != nil {
		return err
	}
	resp, err := regClient.Directory(ctx, api.DirectoryQuery{
		Query:    qVal,
		Repo:     strings.TrimSpace(*repo),
		Tag:      strings.TrimSpace(*tag),
		Status:   api.EntryStatus(strings.TrimSpace(*status)),
		Kind:     api.EntryKind(strings.TrimSpace(*kind)),
		HostKind: api.HostKind(strings.TrimSpace(*hostKind)),
		Owner:    strings.TrimSpace(*owner),
		Limit:    *limit,
	})
	if err != nil {
		return err
	}
	if out.json {
		out.encode(resp)
		return nil
	}
	if len(resp.Entries) == 0 {
		fmt.Println("no listed directory entries matched")
		return nil
	}
	for _, e := range resp.Entries {
		title := e.Name
		if e.DisplayName != "" {
			title = fmt.Sprintf("%s (%s)", e.DisplayName, e.Name)
		}
		fmt.Printf("%-34s %-9s %-13s owner=%-22s %s\n", title, e.Status, e.HostKind, e.Owner, e.URL)
	}
	return nil
}

func cmdRegistryRoute(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain registry route", flag.ContinueOnError)
	tf := bindRegistryTargetFlags(fs)
	repoFlag := fs.String("repo", "", "repository (<owner/name>)")
	pathFlag := fs.String("path", "", "path within repository")
	includeOffline := fs.Bool("include-offline", false, "include offline candidates in route resolution")

	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) > 2 {
		return errors.New("usage: grain registry route [-repo <owner/name>] [-path <subpath>] [-include-offline] [<repo> [<path>]]")
	}
	repo := strings.TrimSpace(*repoFlag)
	filePath := strings.TrimSpace(*pathFlag)
	if len(rest) >= 1 && repo == "" {
		repo = strings.TrimSpace(rest[0])
	}
	if len(rest) == 2 && filePath == "" {
		filePath = strings.TrimSpace(rest[1])
	}
	if repo == "" {
		return errors.New("route requires -repo <owner/name> (or positional <repo>)")
	}

	regClient, _, err := tf.client(ctx, c)
	if err != nil {
		return err
	}
	resp, err := regClient.Route(ctx, api.RouteQuery{
		Repo:           repo,
		Path:           filePath,
		IncludeOffline: *includeOffline,
	})
	if err != nil {
		return err
	}
	if out.json {
		out.encode(resp)
		return nil
	}
	if len(resp.Candidates) == 0 {
		fmt.Printf("no matching grain entries for %s %s\n", resp.Repo, resp.Path)
		return nil
	}
	if resp.Primary != nil {
		fmt.Printf("Primary: %s (%s, %s, prefix=%q)\n",
			resp.Primary.Name, resp.Primary.Status, resp.Primary.URL, resp.Primary.MatchedScope.PathPrefix)
	}
	for _, cand := range resp.Candidates {
		prefix := cand.MatchedScope.PathPrefix
		if prefix == "" {
			prefix = "/"
		}
		fmt.Printf("  %-24s role=%-9s status=%-8s prefix=%-18s source=%-8s %s\n",
			cand.Name, cand.MatchedScope.Role, cand.Status, prefix, cand.MatchedScope.Source, cand.URL)
	}
	return nil
}

func cmdRegistryShow(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain registry show", flag.ContinueOnError)
	tf := bindRegistryTargetFlags(fs)

	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("usage: grain registry show [-registry-url <url>] <name>")
	}
	name := strings.TrimSpace(rest[0])

	regClient, _, err := tf.client(ctx, c)
	if err != nil {
		return err
	}
	entry, err := regClient.GetEntry(ctx, name)
	if err != nil {
		return err
	}
	if out.json {
		out.encode(entry)
		return nil
	}
	fmt.Printf("Name:        %s\n", entry.Name)
	if entry.URL != "" {
		fmt.Printf("URL:         %s\n", entry.URL)
	}
	fmt.Printf("Kind:        %s (host_kind=%s)\n", entry.Kind, entry.HostKind)
	fmt.Printf("State:       %s (status=%s, listed=%t)\n", entry.State, entry.Status, entry.Listed)
	if entry.DisplayName != "" {
		fmt.Printf("Display:     %s\n", entry.DisplayName)
	}
	if entry.Purpose != "" {
		fmt.Printf("Purpose:     %s\n", entry.Purpose)
	}
	fmt.Printf("Owner:       %s\n", entry.Owner)
	if entry.OwnerGroup != "" {
		fmt.Printf("OwnerGroup:  %s\n", entry.OwnerGroup)
	}
	if entry.PendingOwner != "" {
		fmt.Printf("PendingTransfer: %s\n", entry.PendingOwner)
	}
	if entry.Shape != "" {
		fmt.Printf("Shape:       %s\n", entry.Shape)
	}
	if entry.GrainVersion != "" {
		fmt.Printf("Version:     %s (%s)\n", entry.GrainVersion, entry.GrainCommit)
	}
	if entry.TunnelConnected != nil {
		fmt.Printf("Tunnel:      connected=%t\n", *entry.TunnelConnected)
	}
	if len(entry.Scopes) > 0 {
		fmt.Println("Scopes:")
		for _, sc := range entry.Scopes {
			prefix := sc.PathPrefix
			if prefix == "" {
				prefix = "/"
			}
			fmt.Printf("  %-26s %-16s role=%-9s source=%-8s active=%t\n",
				sc.Repo, prefix, sc.Role, sc.Source, sc.Active)
		}
	}
	for _, cf := range entry.ScopeConflicts {
		fmt.Printf("Conflict:    %s/%s conflicts with %s\n", cf.Repo, cf.PathPrefix, cf.ConflictingEntry)
	}
	return nil
}

func generateRandomInstanceName() string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return "grain-" + hex.EncodeToString(b[:])
}

func cmdRegistryCreate(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain registry create", flag.ContinueOnError)
	tf := bindRegistryTargetFlags(fs)
	nameFlag := fs.String("name", "", "entry name (defaults to a random generated name when omitted)")
	shape := fs.String("shape", "", "VM shape name (defaults to registry default shape)")
	zone := fs.String("zone", "", "optional GCE zone override")
	displayName := fs.String("display-name", "", "human-readable display name in the registry directory")
	purpose := fs.String("purpose", "", "short description of this hosted grain's purpose")
	ownerGroup := fs.String("owner-group", "", "optional co-owning group principal")
	listed := fs.Bool("listed", false, "include this entry in default registry directory listings")
	tags := fs.String("tags", "", "comma-separated catalog tags")
	allowSSH := fs.Bool("allow-ssh", true, "enable OS Login break-glass SSH access")
	shipLogs := fs.Bool("ship-logs", false, "enable fleet log shipping")
	var scopeFlags multiStringFlag
	fs.Var(&scopeFlags, "scope", "declared repository scope <owner/repo>[:<path_prefix>[:<primary|secondary>]] (repeatable)")

	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		return errors.New("usage: grain registry create [flags] [<name>]")
	}
	passed := visitedFlags(fs)

	entryName := strings.TrimSpace(*nameFlag)
	if len(rest) == 1 {
		if entryName != "" && entryName != strings.TrimSpace(rest[0]) {
			return fmt.Errorf("conflicting entry names %q and -name=%q", rest[0], entryName)
		}
		entryName = strings.TrimSpace(rest[0])
	}
	if entryName == "" {
		entryName = generateRandomInstanceName()
	}

	scopes, err := parseScopeInputs(scopeFlags)
	if err != nil {
		return err
	}

	regClient, _, err := tf.client(ctx, c)
	if err != nil {
		return err
	}
	req := api.CreateEntryRequest{
		Name:        entryName,
		Kind:        api.KindInstance,
		DisplayName: strings.TrimSpace(*displayName),
		Purpose:     strings.TrimSpace(*purpose),
		OwnerGroup:  strings.TrimSpace(*ownerGroup),
		Listed:      listed,
		Tags:        parseCommaTags(*tags),
		Shape:       strings.TrimSpace(*shape),
		Zone:        strings.TrimSpace(*zone),
		Scopes:      scopes,
	}
	if passed["allow-ssh"] {
		v := *allowSSH
		req.AllowSSH = &v
	}
	if passed["ship-logs"] {
		v := *shipLogs
		req.ShipLogs = &v
	}
	resp, err := regClient.CreateEntry(ctx, req)
	if err != nil {
		return err
	}
	if out.json {
		out.encode(resp)
		return nil
	}
	fmt.Printf("created hosted instance entry %s (state=%s, shape=%s, url=%s)\n",
		resp.Entry.Name, resp.Entry.State, resp.Entry.Shape, resp.Entry.URL)
	return nil
}

func cmdRegistryUpdate(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain registry update", flag.ContinueOnError)
	tf := bindRegistryTargetFlags(fs)
	displayName := fs.String("display-name", "", "human-readable display name")
	purpose := fs.String("purpose", "", "short purpose description")
	hostKind := fs.String("host-kind", "", "connected entry host kind: workstation, kubernetes, or other")
	connectedPrincipal := fs.String("connected-principal", "", "connected workload principal")
	ownerGroup := fs.String("owner-group", "", "co-owning group principal")
	listed := fs.Bool("listed", false, "mark entry listed in directory")
	unlisted := fs.Bool("unlisted", false, "mark entry unlisted in directory")
	tags := fs.String("tags", "", "comma-separated catalog tags")
	shape := fs.String("shape", "", "target VM shape")
	allowSSH := fs.Bool("allow-ssh", false, "enable or disable break-glass SSH")
	shipLogs := fs.Bool("ship-logs", false, "enable or disable fleet log shipping")

	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("usage: grain registry update [flags] <name>")
	}
	if *listed && *unlisted {
		return errors.New("-listed and -unlisted cannot be used together")
	}
	passed := visitedFlags(fs)
	name := strings.TrimSpace(rest[0])

	var req api.UpdateEntryRequest
	if passed["display-name"] {
		v := strings.TrimSpace(*displayName)
		req.DisplayName = &v
	}
	if passed["purpose"] {
		v := strings.TrimSpace(*purpose)
		req.Purpose = &v
	}
	if passed["host-kind"] {
		v := api.HostKind(strings.TrimSpace(*hostKind))
		req.HostKind = &v
	}
	if passed["connected-principal"] {
		v := strings.TrimSpace(*connectedPrincipal)
		req.ConnectedPrincipal = &v
	}
	if passed["owner-group"] {
		v := strings.TrimSpace(*ownerGroup)
		req.OwnerGroup = &v
	}
	if passed["listed"] {
		v := *listed
		req.Listed = &v
	} else if passed["unlisted"] && *unlisted {
		v := false
		req.Listed = &v
	}
	if passed["tags"] {
		v := parseCommaTags(*tags)
		req.Tags = &v
	}
	if passed["shape"] {
		v := strings.TrimSpace(*shape)
		req.Shape = &v
	}
	if passed["allow-ssh"] {
		v := *allowSSH
		req.AllowSSH = &v
	}
	if passed["ship-logs"] {
		v := *shipLogs
		req.ShipLogs = &v
	}

	regClient, _, err := tf.client(ctx, c)
	if err != nil {
		return err
	}
	updated, err := regClient.UpdateEntry(ctx, name, req)
	if err != nil {
		return err
	}
	if out.json {
		out.encode(updated)
		return nil
	}
	fmt.Printf("updated entry %s (state=%s, listed=%t)\n", updated.Name, updated.State, updated.Listed)
	return nil
}

func cmdRegistryScopes(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain registry scopes", flag.ContinueOnError)
	tf := bindRegistryTargetFlags(fs)
	var setFlags multiStringFlag
	fs.Var(&setFlags, "set", "declared scope <owner/repo>[:<path_prefix>[:<primary|secondary>]] (repeatable)")
	clear := fs.Bool("clear", false, "clear all declared scope overrides on the entry")

	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("usage: grain registry scopes [-set <repo[:prefix[:role]]>]... [-clear] <name>")
	}
	name := strings.TrimSpace(rest[0])

	regClient, _, err := tf.client(ctx, c)
	if err != nil {
		return err
	}

	if len(setFlags) == 0 && !*clear {
		entry, gErr := regClient.GetEntry(ctx, name)
		if gErr != nil {
			return gErr
		}
		if out.json {
			out.encode(map[string]any{
				"scopes":          entry.Scopes,
				"reported_scopes": entry.ReportedScopes,
				"declared_scopes": entry.DeclaredScopes,
				"conflicts":       entry.ScopeConflicts,
			})
			return nil
		}
		if len(entry.Scopes) == 0 {
			fmt.Printf("entry %s has no scopes\n", entry.Name)
			return nil
		}
		for _, sc := range entry.Scopes {
			prefix := sc.PathPrefix
			if prefix == "" {
				prefix = "/"
			}
			fmt.Printf("%-26s %-16s role=%-9s source=%-8s active=%t\n",
				sc.Repo, prefix, sc.Role, sc.Source, sc.Active)
		}
		return nil
	}

	var inputs []api.ScopeInput
	if !*clear {
		inputs, err = parseScopeInputs(setFlags)
		if err != nil {
			return err
		}
	}
	resp, err := regClient.SetScopes(ctx, name, inputs)
	if err != nil {
		return err
	}
	if out.json {
		out.encode(resp)
		return nil
	}
	fmt.Printf("updated declared scopes on %s (%d effective scopes)\n", name, len(resp.Scopes))
	return nil
}

func cmdRegistryRotateCredential(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain registry rotate-credential", flag.ContinueOnError)
	tf := bindRegistryTargetFlags(fs)
	label := fs.String("label", "", "optional label for the new grc_... credential")
	credID := fs.String("cred-id", "", "specific credential UUID to replace or expire")
	revokePrev := fs.Bool("revoke-previous", false, "immediately revoke previous active credentials")
	overlap := fs.Duration("overlap", 0, "grace period before previous credentials expire (for example 1h)")
	tokenFile := fs.String("token-file", "", "write the newly minted grc_... token to this file (mode 0600)")

	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("usage: grain registry rotate-credential [flags] <name>")
	}
	name := strings.TrimSpace(rest[0])

	regClient, regURL, err := tf.client(ctx, c)
	if err != nil {
		return err
	}
	resp, err := regClient.RotateCredential(ctx, name, api.RotateCredentialRequest{
		Label:          strings.TrimSpace(*label),
		CredID:         strings.TrimSpace(*credID),
		RevokePrevious: *revokePrev,
		OverlapSeconds: int(overlap.Seconds()),
	})
	if err != nil {
		return err
	}
	if *tokenFile != "" && resp.RegistrationToken != "" {
		if err := writeTokenFile(*tokenFile, resp.RegistrationToken); err != nil {
			return fmt.Errorf("writing -token-file: %w", err)
		}
	}
	if out.json {
		out.encode(resp)
		return nil
	}
	fmt.Printf("rotated credential %s for %s\n", resp.Credential.CredentialID, name)
	if *tokenFile != "" {
		fmt.Printf("registration token written to %s\n", *tokenFile)
	} else {
		fmt.Printf("registration token (shown once): %s\n", resp.RegistrationToken)
	}
	printConnectedConfigSnippet(regURL, name)
	return nil
}

func cmdRegistryUpgrade(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain registry upgrade", flag.ContinueOnError)
	tf := bindRegistryTargetFlags(fs)
	ver := fs.String("version", "", "target grain release version (defaults to registry's current pinned release)")

	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("usage: grain registry upgrade [-version <vX.Y.Z>] <name>")
	}
	name := strings.TrimSpace(rest[0])

	regClient, _, err := tf.client(ctx, c)
	if err != nil {
		return err
	}
	updated, err := regClient.UpgradeEntry(ctx, name, api.UpgradeEntryRequest{
		TargetVersion: strings.TrimSpace(*ver),
	})
	if err != nil {
		return err
	}
	if out.json {
		out.encode(updated)
		return nil
	}
	fmt.Printf("requested upgrade for %s (target_version=%s, state=%s)\n", updated.Name, updated.TargetVersion, updated.State)
	return nil
}

func cmdRegistryResize(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain registry resize", flag.ContinueOnError)
	tf := bindRegistryTargetFlags(fs)
	shapeFlag := fs.String("shape", "", "target VM shape name")
	ver := fs.String("version", "", "optional target grain version to apply during replacement")

	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) < 1 || len(rest) > 2 {
		return errors.New("usage: grain registry resize -shape <shape> [-version <vX.Y.Z>] <name>")
	}
	name := strings.TrimSpace(rest[0])
	shape := strings.TrimSpace(*shapeFlag)
	if len(rest) == 2 && shape == "" {
		shape = strings.TrimSpace(rest[1])
	}
	if shape == "" {
		return errors.New("resize requires -shape <shape>")
	}

	regClient, _, err := tf.client(ctx, c)
	if err != nil {
		return err
	}
	updated, err := regClient.ReplaceEntry(ctx, name, api.ReplaceEntryRequest{
		Shape:         shape,
		TargetVersion: strings.TrimSpace(*ver),
	})
	if err != nil {
		return err
	}
	if out.json {
		out.encode(updated)
		return nil
	}
	fmt.Printf("initiated resize/replace for %s to shape %s (state=%s, step=%s)\n",
		updated.Name, updated.Shape, updated.State, updated.ReplaceStep)
	return nil
}

func cmdRegistryDelete(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain registry delete", flag.ContinueOnError)
	tf := bindRegistryTargetFlags(fs)
	confirm := fs.String("confirm", "", "type the entry name to confirm deletion")
	yes := fs.Bool("yes", false, "confirm deletion without repeating the entry name")
	deleteDisk := fs.Bool("delete-disk", false, "immediately purge the instance database/disk instead of retaining it for the 30-day restore window")

	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("usage: grain registry delete [-confirm <name>] [-yes] [-delete-disk] <name>")
	}
	name := strings.TrimSpace(rest[0])
	confirmVal := strings.TrimSpace(*confirm)
	if *yes && confirmVal == "" {
		confirmVal = name
	}
	if confirmVal == "" {
		return fmt.Errorf("deletion requires -confirm %s (or -yes)", name)
	}
	if !strings.EqualFold(confirmVal, name) {
		return fmt.Errorf("-confirm %q does not match entry name %q", confirmVal, name)
	}

	regClient, _, err := tf.client(ctx, c)
	if err != nil {
		return err
	}
	if err := regClient.DeleteEntry(ctx, name, api.DeleteEntryRequest{
		Confirm:     confirmVal,
		ConfirmName: confirmVal,
		DeleteDisk:  *deleteDisk,
	}); err != nil {
		return err
	}
	if out.json {
		out.encode(map[string]any{"deleted": true, "name": name})
		return nil
	}
	fmt.Printf("deleted entry %s\n", name)
	return nil
}

func cmdRegistryRestore(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain registry restore", flag.ContinueOnError)
	tf := bindRegistryTargetFlags(fs)

	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("usage: grain registry restore <name>")
	}
	name := strings.TrimSpace(rest[0])

	regClient, _, err := tf.client(ctx, c)
	if err != nil {
		return err
	}
	restored, err := regClient.RestoreEntry(ctx, name)
	if err != nil {
		return err
	}
	if out.json {
		out.encode(restored)
		return nil
	}
	fmt.Printf("restored entry %s (state=%s)\n", restored.Name, restored.State)
	return nil
}

func cmdRegistryTransfer(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain registry transfer", flag.ContinueOnError)
	tf := bindRegistryTargetFlags(fs)
	to := fs.String("to", "", "email of the new individual owner to offer transfer to")
	ownerGroup := fs.String("owner-group", "", "optional co-owning group to set alongside transfer")
	clearGroup := fs.Bool("clear-owner-group", false, "clear the entry's co-owning group")

	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("usage: grain registry transfer -to <email> [-owner-group <group>] [-clear-owner-group] <name>")
	}
	name := strings.TrimSpace(rest[0])
	target := strings.TrimSpace(*to)
	if target == "" {
		return errors.New("transfer requires -to <email>")
	}

	regClient, _, err := tf.client(ctx, c)
	if err != nil {
		return err
	}
	updated, err := regClient.OfferTransfer(ctx, name, api.TransferRequest{
		To:              target,
		NewOwner:        target,
		OwnerGroup:      strings.TrimSpace(*ownerGroup),
		ClearOwnerGroup: *clearGroup,
	})
	if err != nil {
		return err
	}
	if out.json {
		out.encode(updated)
		return nil
	}
	fmt.Printf("offered ownership transfer of %s to %s\n", updated.Name, updated.PendingOwner)
	return nil
}

func cmdRegistryAcceptTransfer(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain registry accept-transfer", flag.ContinueOnError)
	tf := bindRegistryTargetFlags(fs)

	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("usage: grain registry accept-transfer <name>")
	}
	name := strings.TrimSpace(rest[0])

	regClient, _, err := tf.client(ctx, c)
	if err != nil {
		return err
	}
	updated, err := regClient.AcceptTransfer(ctx, name)
	if err != nil {
		return err
	}
	if out.json {
		out.encode(updated)
		return nil
	}
	fmt.Printf("accepted ownership transfer of %s (owner=%s)\n", updated.Name, updated.Owner)
	return nil
}
