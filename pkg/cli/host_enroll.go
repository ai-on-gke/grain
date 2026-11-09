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
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/ai-on-gke/grain/pkg/version"
)

// isHostdRegistryVerb reports whether `grain hostd <args>` is asking for one
// of the remote host registry/handshake subcommands (`pending`, `accept`,
// `reject`, `rekey`, `codes`, `invite`, `rotate-code`, `revoke-code`) rather
// than starting the local hostd process server.
func isHostdRegistryVerb(args []string) bool {
	return len(args) > 0 && hostRegistryVerb(args[0])
}

func parseHostLabelFlags(csv string, repeated stringList) (map[string]string, bool, error) {
	var all []string
	if strings.TrimSpace(csv) != "" {
		for _, item := range strings.Split(csv, ",") {
			if s := strings.TrimSpace(item); s != "" {
				all = append(all, s)
			}
		}
	}
	all = append(all, repeated...)
	if len(all) == 0 {
		return nil, false, nil
	}
	m, err := pairs("-labels", all)
	if err != nil {
		return nil, false, err
	}
	return m, true, nil
}

func cmdHostPending(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain host pending", flag.ContinueOnError)
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return errors.New("usage: grain host pending")
	}
	pending, err := c.ListPendingHostJoins(ctx)
	if err != nil {
		return err
	}
	if out.json {
		out.encode(pending)
		return nil
	}
	if len(pending) == 0 {
		fmt.Println("No pending host join requests.")
		return nil
	}
	for _, p := range pending {
		codeLabel := p.EnrollmentCode
		if p.RekeyHostd != "" {
			codeLabel = "rekey:" + p.RekeyHostd
		}
		clis := "-"
		if len(p.CLIs) > 0 {
			clis = strings.Join(p.CLIs, ",")
		}
		fmt.Printf("%s\thost %s (%s/%s)\tenrolled by %s (local %s)\tclis %s\tkey %s\tused %s\texpires %s\n",
			p.ID, p.Hostname, p.OS, p.Arch,
			ownerOrNone(p.EnrolledBy), ownerOrNone(p.LocalUser),
			clis, p.KeyFingerprint, codeLabel, p.ExpiresAt.UTC().Format(time.RFC3339))
	}
	return nil
}

func cmdHostAccept(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain host accept", flag.ContinueOnError)
	name := fs.String("name", "", "name for the accepted host (defaults to sanitized hostname)")
	labelsCSV := fs.String("labels", "", "comma-separated k=v labels for the accepted host")
	var labelList stringList
	fs.Var(&labelList, "label", "k=v label for the accepted host (repeatable)")
	capacity := fs.Int("capacity", -1, "maximum concurrent agent CLI runs on this host (0 for unlimited)")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("usage: grain host accept [-name <name>] [-labels k=v,...] [-capacity N] <id>")
	}
	req := ui.AcceptHostJoinRequest{Name: strings.TrimSpace(*name)}
	labels, hasLabels, err := parseHostLabelFlags(*labelsCSV, labelList)
	if err != nil {
		return err
	}
	if hasLabels {
		req.Labels = &labels
	}
	if *capacity >= 0 {
		capVal := *capacity
		req.Capacity = &capVal
	}
	h, err := c.AcceptPendingHostJoin(ctx, rest[0], req)
	if err != nil {
		return err
	}
	if out.json {
		out.encode(h)
		return nil
	}
	fmt.Printf("accepted host %s (owner %s, key %s)\n", h.Name, ownerOrNone(h.Owner), h.KeyFingerprint)
	return nil
}

func cmdHostReject(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain host reject", flag.ContinueOnError)
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("usage: grain host reject <id>")
	}
	if err := c.RejectPendingHostJoin(ctx, rest[0]); err != nil {
		return err
	}
	if out.json {
		out.encode(map[string]string{"rejected": rest[0]})
		return nil
	}
	fmt.Printf("rejected pending host join request %s\n", rest[0])
	return nil
}

func cmdHostCodes(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain host codes", flag.ContinueOnError)
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return errors.New("usage: grain host codes")
	}
	codes, err := c.ListHostEnrollmentCodes(ctx)
	if err != nil {
		return err
	}
	if out.json {
		out.encode(codes)
		return nil
	}
	if len(codes) == 0 {
		fmt.Println("No host enrollment codes.")
		return nil
	}
	now := time.Now().UTC()
	for _, code := range codes {
		state := "active"
		if code.RevokedAt != nil {
			state = "revoked"
		} else if code.ExpiresAt != nil && !now.Before(*code.ExpiresAt) {
			state = "expired"
		} else if code.MaxUses > 0 && code.Uses >= code.MaxUses {
			state = "exhausted"
		}
		uses := fmt.Sprintf("%d", code.Uses)
		if code.MaxUses > 0 {
			uses = fmt.Sprintf("%d/%d", code.Uses, code.MaxUses)
		}
		exp := "never"
		if code.ExpiresAt != nil {
			exp = code.ExpiresAt.UTC().Format(time.RFC3339)
		}
		fmt.Printf("%s\t%s\tuses %s\texpires %s\t%s\n", code.Name, state, uses, exp, code.Code)
	}
	return nil
}

func cmdHostInvite(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain host invite", flag.ContinueOnError)
	name := fs.String("name", "", "create a new enrollment code with this name (if omitted, shows the active snippet)")
	expires := fs.String("expires", "", "expiry duration (e.g. 24h, 168h) or RFC3339 timestamp when creating a code")
	maxUses := fs.Int("max-uses", 0, "maximum number of join requests this code may file (0 for unlimited)")
	labelsCSV := fs.String("labels", "", "default comma-separated k=v labels for hosts enrolled with this code")
	var labelList stringList
	fs.Var(&labelList, "label", "default k=v label for hosts enrolled with this code (repeatable)")
	capacity := fs.Int("capacity", 0, "default capacity for hosts enrolled with this code")
	allowCSV := fs.String("allow", "", "comma-separated allowed principals (user:<email>, group:<email>, github:<login>, org:<org>)")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		return errors.New("usage: grain host invite [-name <code-name>] [-expires <dur>] [-max-uses N] [-labels k=v,...] [-capacity N] [-allow user:...,group:...]")
	}
	codeName := strings.TrimSpace(*name)
	if codeName == "" && len(rest) == 1 {
		codeName = strings.TrimSpace(rest[0])
	}
	var chosen *ui.HostEnrollmentCodeView
	if codeName != "" {
		labels, _, err := parseHostLabelFlags(*labelsCSV, labelList)
		if err != nil {
			return err
		}
		var allowed []string
		for _, item := range strings.Split(*allowCSV, ",") {
			if s := strings.TrimSpace(item); s != "" {
				allowed = append(allowed, s)
			}
		}
		created, err := c.CreateHostEnrollmentCode(ctx, ui.CreateHostEnrollmentCodeRequest{
			Name:              codeName,
			ExpiresIn:         strings.TrimSpace(*expires),
			MaxUses:           *maxUses,
			DefaultLabels:     labels,
			DefaultCapacity:   *capacity,
			AllowedPrincipals: allowed,
		})
		if err != nil {
			return err
		}
		chosen = &created
	} else {
		codes, err := c.ListHostEnrollmentCodes(ctx)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		for i := range codes {
			c0 := codes[i]
			if c0.RevokedAt == nil && (c0.ExpiresAt == nil || now.Before(*c0.ExpiresAt)) && (c0.MaxUses <= 0 || c0.Uses < c0.MaxUses) {
				chosen = &codes[i]
				break
			}
		}
		if chosen == nil {
			return errors.New("no active enrollment code; create one with `grain host invite -name default`")
		}
	}
	if out.json {
		out.encode(chosen)
		return nil
	}
	fmt.Printf("enrollment code %s (%s)\n", chosen.Name, chosen.Code)
	printHostdInstall(chosen.Install)
	return nil
}

func cmdHostRotateCode(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain host rotate-code", flag.ContinueOnError)
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("usage: grain host rotate-code <code-name>")
	}
	rotated, err := c.RotateHostEnrollmentCode(ctx, rest[0])
	if err != nil {
		return err
	}
	if out.json {
		out.encode(rotated)
		return nil
	}
	fmt.Printf("rotated enrollment code %s (%s)\n", rotated.Name, rotated.Code)
	printHostdInstall(rotated.Install)
	return nil
}

func cmdHostRevokeCode(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain host revoke-code", flag.ContinueOnError)
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("usage: grain host revoke-code <code-name>")
	}
	revoked, err := c.RevokeHostEnrollmentCode(ctx, rest[0])
	if err != nil {
		return err
	}
	if out.json {
		out.encode(revoked)
		return nil
	}
	fmt.Printf("revoked enrollment code %s\n", revoked.Name)
	return nil
}

func cmdHostRekey(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain host rekey", flag.ContinueOnError)
	tokenFile := fs.String("token-file", "", "file holding the host's current token, and where the new key-bound credential is saved ($GRAIN_HOSTD_TOKEN when unset)")
	keyFile := fs.String("key-file", "", "file where the host's Ed25519 private key (mode 0600) will be generated or loaded")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New("usage: grain host rekey [-token-file <path>] [-key-file <path>] <host-name>")
	}
	hostName := strings.TrimSpace(rest[0])
	var curTok string
	if *tokenFile != "" {
		if t, err := daemon.ReadTokenFile(*tokenFile); err == nil {
			curTok = t
		}
	}
	if curTok == "" {
		curTok = strings.TrimSpace(os.Getenv("GRAIN_HOSTD_TOKEN"))
	}
	if curTok == "" {
		return errors.New("rekey needs the host's current token (-token-file or $GRAIN_HOSTD_TOKEN)")
	}
	kf := strings.TrimSpace(*keyFile)
	if kf == "" {
		if *tokenFile != "" {
			kf = filepath.Join(filepath.Dir(*tokenFile), "hostd-key")
		} else {
			kf = filepath.Join(defaultHostdStateDir(), "hostd-key")
		}
	}
	tf := strings.TrimSpace(*tokenFile)
	if tf == "" {
		tf = filepath.Join(defaultHostdStateDir(), "hostd-token")
	}
	res, err := hostd.Enroll(ctx, hostd.EnrollOptions{
		ServerURL:    c.BaseURL,
		RekeyHostd:   hostName,
		CurrentToken: curTok,
		KeyPath:      kf,
		TokenPath:    tf,
		CLIs:         hostd.DetectAgentCLIs(),
		Version:      version.Get().Release,
		HTTPClient:   c.HTTP,
		Out:          os.Stderr,
	})
	if err != nil {
		return err
	}
	if out.json {
		out.encode(map[string]any{
			"name":            res.Name,
			"key_fingerprint": res.KeyFingerprint,
			"key_file":        kf,
			"token_file":      tf,
			"expires_at":      res.ExpiresAt,
		})
		return nil
	}
	fmt.Printf("rekeyed host %s (key %s); key saved to %s, credential saved to %s\n", res.Name, res.KeyFingerprint, kf, tf)
	return nil
}

func formatHostLabels(m map[string]string) string {
	var labels []string
	for k, v := range m {
		labels = append(labels, k+"="+v)
	}
	sort.Strings(labels)
	return strings.Join(labels, ",")
}
