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
	"strings"

	"github.com/ai-on-gke/grain/pkg/registry/api"
	"github.com/ai-on-gke/grain/pkg/ui"
)

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
	diskGB := fs.Int("disk-gb", 0, "boot disk size in GB (20..4000; defaults to shape default)")
	zone := fs.String("zone", "", "optional GCE zone override")
	displayName := fs.String("display-name", "", "human-readable display name in the hub directory")
	purpose := fs.String("purpose", "", "short description of this hosted grain's purpose")
	ownerGroup := fs.String("owner-group", "", "optional co-owning group principal")
	listed := fs.Bool("listed", false, "include this entry in default hub directory listings")
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

	if *diskGB != 0 {
		if err := api.ValidateBootDiskGB(*diskGB); err != nil {
			return err
		}
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
		BootDiskGB:  *diskGB,
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
		resp.RegistrationToken = ""
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
	entryURL := ""
	if e, err := regClient.GetEntry(ctx, name); err == nil && e != nil {
		entryURL = e.URL
	}
	printConnectedConfigSnippet(regURL, name, entryURL, *tokenFile)
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
	diskGB := fs.Int("disk-gb", 0, "boot disk size in GB (20..4000; defaults to shape default)")
	ver := fs.String("version", "", "optional target grain version to apply during replacement")

	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) < 1 || len(rest) > 2 {
		return errors.New("usage: grain registry resize [-shape <shape>] [-disk-gb <gb>] [-version <vX.Y.Z>] <name>")
	}
	name := strings.TrimSpace(rest[0])
	shape := strings.TrimSpace(*shapeFlag)
	if len(rest) == 2 && shape == "" {
		shape = strings.TrimSpace(rest[1])
	}
	if *diskGB != 0 {
		if err := api.ValidateBootDiskGB(*diskGB); err != nil {
			return err
		}
	}
	if shape == "" && *diskGB == 0 {
		return errors.New("resize requires -shape <shape> or -disk-gb <gb>")
	}

	regClient, _, err := tf.client(ctx, c)
	if err != nil {
		return err
	}
	updated, err := regClient.ReplaceEntry(ctx, name, api.ReplaceEntryRequest{
		Shape:         shape,
		BootDiskGB:    *diskGB,
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

// cmdRegistryStop implements `grain registry stop <name>`: it halts a hosted
// instance's VM while the registry keeps its externalised state (database,
// secrets, config repository), so the entry costs no VM compute until it is
// started again or woken by a visit.
func cmdRegistryStop(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	return cmdRegistryPower(ctx, c, out, args, "stop")
}

// cmdRegistryStart implements `grain registry start <name>`: it boots a
// stopped hosted instance back onto a VM with its prior state.
func cmdRegistryStart(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	return cmdRegistryPower(ctx, c, out, args, "start")
}

func cmdRegistryPower(ctx context.Context, c *ui.HTTPClient, out *printer, args []string, verb string) error {
	fs := flag.NewFlagSet("grain registry "+verb, flag.ContinueOnError)
	tf := bindRegistryTargetFlags(fs)

	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 || strings.TrimSpace(rest[0]) == "" {
		return fmt.Errorf("usage: grain registry %s <name>", verb)
	}
	name := strings.TrimSpace(rest[0])

	regClient, _, err := tf.client(ctx, c)
	if err != nil {
		return err
	}
	var updated *api.Entry
	if verb == "stop" {
		updated, err = regClient.StopEntry(ctx, name)
	} else {
		updated, err = regClient.StartEntry(ctx, name)
	}
	if err != nil {
		return err
	}
	if out.json {
		out.encode(updated)
		return nil
	}
	fmt.Printf("requested %s for %s (state=%s)\n", verb, updated.Name, updated.State)
	return nil
}

func cmdRegistryTransfer(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain registry transfer", flag.ContinueOnError)
	tf := bindRegistryTargetFlags(fs)
	to := fs.String("to", "", "email of the new individual owner to offer transfer to")
	ownerGroup := fs.String("owner-group", "", "optional co-owning group to set alongside transfer")
	clearGroup := fs.Bool("clear-owner-group", false, "clear the entry's co-owning group")
	cancel := fs.Bool("cancel", false, "withdraw the entry's pending transfer offer instead of making one")

	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("usage: grain registry transfer -to <email> [-owner-group <group>] [-clear-owner-group] <name> | -cancel <name>")
	}
	name := strings.TrimSpace(rest[0])
	target := strings.TrimSpace(*to)
	if *cancel {
		if target != "" || strings.TrimSpace(*ownerGroup) != "" || *clearGroup {
			return errors.New("transfer -cancel withdraws a pending offer and takes no -to, -owner-group or -clear-owner-group")
		}
	} else if target == "" {
		return errors.New("transfer requires -to <email> (or -cancel to withdraw a pending offer)")
	}

	regClient, _, err := tf.client(ctx, c)
	if err != nil {
		return err
	}
	if *cancel {
		updated, err := regClient.OfferTransfer(ctx, name, api.TransferRequest{Cancel: true})
		if err != nil {
			return err
		}
		if out.json {
			out.encode(updated)
			return nil
		}
		fmt.Printf("cancelled the pending ownership transfer of %s\n", updated.Name)
		return nil
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
