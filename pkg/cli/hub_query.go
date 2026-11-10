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

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/hub/api"
	"github.com/ai-on-gke/grain/pkg/ui"
)

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

type hubTargetFlags struct {
	url          *string
	token        *string
	oidcAudience *string
}

func bindHubTargetFlags(fs *flag.FlagSet) hubTargetFlags {
	return hubTargetFlags{
		url:          fs.String("hub-url", "", "base URL of the Grain Hub ($"+daemon.HubURLEnv+")"),
		token:        fs.String("hub-token", "", "bearer token for authenticating to the hub ($"+daemon.HubTokenEnv+")"),
		oidcAudience: fs.String("oidc-audience", "", "OIDC ID token audience for the hub (defaults to -hub-url)"),
	}
}

func (tf hubTargetFlags) client(ctx context.Context, c *ui.HTTPClient) (*api.Client, string, error) {
	regURL, urlSource := resolveHubURL(*tf.url, "")
	aud := strings.TrimSpace(*tf.oidcAudience)
	if regURL == "" && c != nil {
		if settings, err := c.GetSettings(ctx); err == nil {
			regURL, urlSource = resolveHubURL("", settings.Registry.URL)
			if aud == "" {
				aud = strings.TrimSpace(settings.Registry.OIDCAudience)
			}
		}
	}
	if regURL == "" {
		return nil, "", errors.New("-hub-url is required (or set $" + daemon.HubURLEnv + ")")
	}
	normURL, err := api.ValidateHubURL(regURL, false)
	if err != nil {
		return nil, "", err
	}
	regURL = normURL
	if aud == "" {
		aud = regURL
	}
	bearer, err := resolveHubCLIBearer(ctx, regURL, aud, *tf.token)
	if err != nil {
		return nil, "", err
	}
	cli, err := newHubAPIClient(c, regURL, bearer)
	if err != nil {
		return nil, "", err
	}
	recordHubTarget(ctx, hubTarget{url: regURL, source: urlSource})
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

func shellQuoteArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\r\n'\"\\$`!&|;<>()*?[]{}#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// hubAssertionIssuer mirrors hub.HubAssertionIssuer, the fixed
// `iss` every registry-minted X-Grain-Registry-Assertion carries.
const hubAssertionIssuer = "grain-registry"

// printConnectedConfigSnippet prints a `grain daemon` command line that
// actually starts the outbound tunnel: `-hub-connect` refuses to start
// without the three -ui-hub-* verifier flags, so the snippet carries
// them (audience = the entry's own URL, keys = the registry's JWKS) along
// with where the registration token lives.
func printConnectedConfigSnippet(hubURL, entryName, entryURL, tokenFile string) {
	fmt.Printf("outbound tunnel config snippet:\n")
	if strings.TrimSpace(tokenFile) == "" {
		tokenFile = "<file holding the grc_... token>"
	}
	keysURL := strings.TrimRight(hubURL, "/") + "/.well-known/jwks.json"
	fmt.Printf("  grain daemon -hub-url %s -hub-entry %s -hub-token-file %s -hub-connect \\\n",
		shellQuoteArg(hubURL), shellQuoteArg(entryName), shellQuoteArg(tokenFile))
	if strings.TrimSpace(entryURL) == "" {
		entryURL = "<https://" + entryName + ".<instance-domain>>"
	}
	fmt.Printf("    -ui-require-verifier=registry -ui-hub-issuer=%s -ui-hub-audience=%s -ui-hub-keys-url=%s\n",
		hubAssertionIssuer, shellQuoteArg(entryURL), shellQuoteArg(keysURL))
}

func cmdHubList(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain hub list", flag.ContinueOnError)
	tf := bindHubTargetFlags(fs)
	all := fs.Bool("all", false, "list all entries across the hub (admin only)")
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
		return errors.New("usage: grain hub list [flags]")
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
		fmt.Println("no hub entries found")
		return nil
	}
	for _, e := range entries {
		urlStr := e.URL
		if urlStr == "" {
			urlStr = "-"
		}
		line := fmt.Sprintf("%-24s %-10s %-10s %-9s listed=%-5t owner=%-24s %s",
			e.Name, e.Kind, e.State, e.Status, e.Listed, e.Owner, urlStr)
		if detail := strings.Join(strings.Fields(e.StatusDetail), " "); detail != "" {
			line += "  detail=" + detail
		}
		fmt.Println(line)
	}
	return nil
}

func cmdHubDirectory(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain hub directory", flag.ContinueOnError)
	tf := bindHubTargetFlags(fs)
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
		return errors.New("usage: grain hub directory [flags] [<query>]")
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

func cmdHubRoute(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain hub route", flag.ContinueOnError)
	tf := bindHubTargetFlags(fs)
	repoFlag := fs.String("repo", "", "repository (<owner/name>)")
	pathFlag := fs.String("path", "", "path within repository")
	includeOffline := fs.Bool("include-offline", false, "include offline candidates in route resolution")

	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) > 2 {
		return errors.New("usage: grain hub route [-repo <owner/name>] [-path <subpath>] [-include-offline] [<repo> [<path>]]")
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

func cmdHubShow(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain hub show", flag.ContinueOnError)
	tf := bindHubTargetFlags(fs)

	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("usage: grain hub show [-hub-url <url>] <name>")
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
	if entry.ReplaceStep != "" {
		fmt.Printf("Step:        %s\n", entry.ReplaceStep)
	}
	if detail := strings.TrimSpace(entry.StatusDetail); detail != "" {
		fmt.Printf("Detail:      %s\n", detail)
	}
	if hDetail := strings.TrimSpace(entry.HealthDetail); hDetail != "" && hDetail != strings.TrimSpace(entry.StatusDetail) {
		fmt.Printf("Health:      %s\n", hDetail)
	}
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
	if entry.BootDiskGB > 0 {
		fmt.Printf("Disk:        %d GB\n", entry.BootDiskGB)
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
