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
  create            [-registry-url <url>] [-name <name>] [-shape <shape>] [-disk-gb <gb>] [-zone <zone>]
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
  resize            [-registry-url <url>] [-shape <shape>] [-disk-gb <gb>] [-version <vX.Y.Z>] <name>
  delete            [-registry-url <url>] [-confirm <name>] [-yes] [-delete-disk] <name>
  restore           [-registry-url <url>] <name>
  stop              [-registry-url <url>] <name>
  start             [-registry-url <url>] <name>
  transfer          [-registry-url <url>] -to <email> [-owner-group <group>]
                    [-clear-owner-group] <name> | -cancel <name>
  accept-transfer   [-registry-url <url>] <name>
  delegating-apps   list | create -principal <sa-email> [flags] <name> | disable <name> | enable <name>
                    (hub admins; see grain registry delegating-apps help)
  grants            list [-include-revoked] | create -entry <name|*>... [-access <read|write>]
                    [-ttl <duration>] <app> | revoke <grant-id|app>

Self-registration, directory discovery, path routing, and control-plane management
for a Grain Hub (Settings -> Hub; design/design/registry.design.md).`

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
			"base URL of the Grain Hub this daemon self-registers with ($"+daemon.RegistryURLEnv+"; empty disables heartbeats)"+seedNote),
		entry: fs.String("registry-entry", "",
			"entry name this daemon registers and heartbeats as in the Grain Hub ($"+daemon.RegistryEntryEnv+")"+seedNote),
		authMode: fs.String("registry-auth-mode", "",
			"credential mode for registry heartbeats: \"token\" (secret "+secrets.RegistryRegistrationTokenSecret+", -registry-token-file, or $"+daemon.RegistryTokenEnv+") or \"oidc\" (ID token for -registry-oidc-audience)"+seedNote),
		oidcAudience: fs.String("registry-oidc-audience", "",
			"OIDC ID token audience when -registry-auth-mode=oidc (defaults to -registry-url)"+seedNote),
		tokenFile: fs.String("registry-token-file", "",
			"fallback file holding a grc_... registration credential when secret "+secrets.RegistryRegistrationTokenSecret+" is unset ($"+daemon.RegistryTokenEnv+" is also consulted)"),
		displayName: fs.String("registry-display-name", "",
			"human-readable display name advertised in the hub directory"+seedNote),
		purpose: fs.String("registry-purpose", "",
			"short description of this grain deployment's purpose in the hub directory"+seedNote),
		hostKind: fs.String("registry-host-kind", "",
			"connected entry host kind advertised to the hub: \"workstation\", \"kubernetes\", or \"other\" (defaults to the active sandbox backend)"+seedNote),
		listed: fs.Bool("registry-listed", true,
			"include this connected entry in default hub directory listings"+seedNote),
		connect: fs.Bool("registry-connect", false,
			"supervise an outbound wss://<registry-host>/api/entries/<name>/connect tunnel and serve accepted mux streams in-process behind the hub assertion verifier gate ($"+daemon.RegistryConnectEnv+"; requires -ui-registry-issuer, -ui-registry-audience, and -ui-registry-keys-url)"),
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
	case "stop":
		return cmdRegistryStop(ctx, c, out, subArgs)
	case "start":
		return cmdRegistryStart(ctx, c, out, subArgs)
	case "transfer":
		return cmdRegistryTransfer(ctx, c, out, subArgs)
	case "accept-transfer":
		return cmdRegistryAcceptTransfer(ctx, c, out, subArgs)
	case "delegating-apps":
		return cmdRegistryDelegatingApps(ctx, c, out, subArgs)
	case "grants":
		return cmdRegistryGrants(ctx, c, out, subArgs)
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
	regURLFlag := fs.String("registry-url", "", "base URL of the Grain Hub ($"+daemon.RegistryURLEnv+")")
	entryFlag := fs.String("entry", "", "entry name in the Grain Hub ($"+daemon.RegistryEntryEnv+"; or positional <entry>)")
	regTokenFlag := fs.String("registry-token", "", "bearer token for authenticating to the hub ($"+daemon.RegistryTokenEnv+")")
	oidcFlag := fs.Bool("oidc", false, "use OIDC ID tokens for registration and daemon heartbeats instead of minting a grc_... token")
	oidcAudFlag := fs.String("oidc-audience", "", "OIDC ID token audience for the hub (defaults to -registry-url)")
	mintToken := fs.Bool("mint-token", false, "mint a fresh grc_... registration credential for this connected entry")
	rotateToken := fs.Bool("rotate-token", false, "rotate an existing connected entry's grc_... registration credential")
	displayName := fs.String("display-name", "", "human-readable display name in the hub directory")
	purpose := fs.String("purpose", "", "short description of this grain's purpose")
	hostKind := fs.String("host-kind", "", "connected entry host kind: \"workstation\", \"kubernetes\", or \"other\"")
	connectedPrincipal := fs.String("connected-principal", "", "optional workload service account email authorized to connect on behalf of this entry")
	listed := fs.Bool("listed", true, "include this entry in default hub directory listings")
	ownerGroup := fs.String("owner-group", "", "optional group principal that co-owns this entry")
	tagsFlag := fs.String("tags", "", "comma-separated catalog tags")
	var scopeFlags multiStringFlag
	fs.Var(&scopeFlags, "scope", "declared repository scope <owner/repo>[:<path_prefix>[:<primary|secondary>]] (repeatable)")
	standalone := fs.Bool("standalone", false, "register on the hub without writing settings or secrets to a local grain daemon")
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
		wantMint := normCfg.EffectiveAuthMode() == model.RegistryAuthToken || *mintToken
		retargeting := (existingCfg.URL != "" && existingCfg.URL != normCfg.URL) ||
			(existingCfg.Entry != "" && !strings.EqualFold(existingCfg.Entry, normCfg.Entry))
		hasStoredEntryCfg := !*standalone && existingCfg.Entry != "" && !retargeting
		remoteEntry, mintedToken, err = upsertConnectedEntry(
			ctx,
			regClient,
			normCfg,
			effectiveHostKind,
			strings.TrimSpace(*connectedPrincipal),
			explicitScopes,
			passed,
			hasStoredEntryCfg,
			wantMint,
			*rotateToken || *mintToken || *standalone || !daemonHasSecret || retargeting,
		)
		if err != nil {
			return err
		}
		if !*standalone && !hasStoredEntryCfg && remoteEntry != nil {
			inheritExistingEntryConfig(&normCfg, remoteEntry, passed)
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
		entryURL := ""
		if remoteEntry != nil {
			entryURL = remoteEntry.URL
		}
		printConnectedConfigSnippet(normCfg.URL, normCfg.Entry, entryURL, *tokenFile)
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

func inheritExistingEntryConfig(cfg *model.RegistryConfig, remote *api.Entry, passed map[string]bool) {
	if !passed["listed"] {
		cfg.Listed = remote.Listed
	}
	if !passed["display-name"] && cfg.DisplayName == "" {
		cfg.DisplayName = remote.DisplayName
	}
	if !passed["purpose"] && cfg.Purpose == "" {
		cfg.Purpose = remote.Purpose
	}
	if !passed["owner-group"] && cfg.OwnerGroup == "" {
		cfg.OwnerGroup = remote.OwnerGroup
	}
	if !passed["tags"] && len(cfg.Tags) == 0 && len(remote.Tags) > 0 {
		cfg.Tags = slices.Clone(remote.Tags)
	}
}

func upsertConnectedEntry(
	ctx context.Context,
	client *api.Client,
	cfg model.RegistryConfig,
	hostKind api.HostKind,
	connectedPrincipal string,
	scopes []api.ScopeInput,
	passed map[string]bool,
	syncAllCfg bool,
	wantToken bool,
	needFreshToken bool,
) (*api.Entry, string, error) {
	existingEntry, err := client.GetEntry(ctx, cfg.Entry)
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

	var updReq api.UpdateEntryRequest
	needUpdate := false
	if passed["display-name"] || (syncAllCfg && cfg.DisplayName != "") {
		disp := cfg.DisplayName
		updReq.DisplayName = &disp
		needUpdate = true
	}
	if passed["purpose"] || (syncAllCfg && cfg.Purpose != "") {
		purp := cfg.Purpose
		updReq.Purpose = &purp
		needUpdate = true
	}
	if passed["host-kind"] || (syncAllCfg && cfg.HostKind != "") {
		hk := cfg.HostKind
		updReq.HostKind = &hk
		needUpdate = true
	}
	if passed["owner-group"] || (syncAllCfg && cfg.OwnerGroup != "") {
		group := cfg.OwnerGroup
		updReq.OwnerGroup = &group
		needUpdate = true
	}
	if passed["listed"] || syncAllCfg {
		updReq.Listed = &listed
		needUpdate = true
	}
	if passed["tags"] || (syncAllCfg && len(cfg.Tags) > 0) {
		tags := slices.Clone(cfg.Tags)
		if tags == nil {
			tags = []string{}
		}
		updReq.Tags = &tags
		needUpdate = true
	}
	if passed["connected-principal"] || connectedPrincipal != "" {
		cp := connectedPrincipal
		updReq.ConnectedPrincipal = &cp
		needUpdate = true
	}
	updated := existingEntry
	if needUpdate {
		var updErr error
		updated, updErr = client.UpdateEntry(ctx, cfg.Entry, updReq)
		if updErr != nil {
			return nil, "", updErr
		}
	}
	if len(scopes) > 0 {
		scResp, scErr := client.SetScopes(ctx, cfg.Entry, scopes)
		if scErr != nil {
			return nil, "", scErr
		}
		if scResp != nil && updated != nil {
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
	regURLFlag := fs.String("registry-url", "", "base URL of the Grain Hub ($"+daemon.RegistryURLEnv+")")
	entryFlag := fs.String("entry", "", "entry name in the Grain Hub ($"+daemon.RegistryEntryEnv+"; or positional <entry>)")
	regTokenFlag := fs.String("registry-token", "", "bearer token for deleting the entry on the hub ($"+daemon.RegistryTokenEnv+")")
	oidcFlag := fs.Bool("oidc", false, "authenticate remote entry deletion with an OIDC ID token")
	oidcAudFlag := fs.String("oidc-audience", "", "OIDC ID token audience for the hub (defaults to -registry-url)")
	keepRemote := fs.Bool("keep-remote", false, "clear the daemon's local hub settings and credential without deleting the entry on the hub")
	standalone := fs.Bool("standalone", false, "delete the entry on the hub without contacting a local grain daemon")

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
			normURL, urlErr := api.ValidateRegistryURL(regURL, false)
			if urlErr != nil {
				return urlErr
			}
			regURL = normURL
			aud = firstNonEmptyTrimmed(*oidcAudFlag, existingCfg.OIDCAudience, regURL)
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
		fmt.Printf("deleted hub entry %s from %s\n", entryName, regURL)
	case remoteDeleted:
		fmt.Printf("unregistered %s from %s and cleared local hub settings\n", entryName, regURL)
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
		fmt.Println("Hub: disabled (run 'grain registry register -registry-url <url> <entry>' or configure in Settings -> Hub)")
		return
	}
	fmt.Printf("State:       %s\n", snap.State)
	fmt.Printf("Hub:         %s\n", snap.Config.URL)
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
