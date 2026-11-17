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
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ai-on-gke/grain/pkg/ui"
)

const accessCodeUsage = `usage: grain access-code [-data-dir DIR] [show|rotate|mode [on|recovery|off]]

Prints this host's grain access code: it is the daemon's one credential, typed into the UI's "Enter
access code" page and sent by the grain CLI as a bearer token. It lives
in DIR/local-api-token (default: $GRAIN_LOCAL_API_TOKEN_FILE, else
local-api-token in $GRAIN_DATA_DIR or /etc/grain/cli.env's), mode 0600, readable only by the account
grain runs as and root -- run this as that account.

The code, with a URL that reaches the daemon, is full owner access:
treat it like a password.

  show     print the code (the default)
  rotate   replace it with a new one, print that, and end every UI
           session and bearer use of the old one within a second
  mode     print the code's mode, or set it (DIR/access-code-mode):
             on        the code works everywhere (the default)
             recovery  once a person with a full role on the allowlist can
                       sign in with a verified identity (IAP or an identity
                       proxy), the code only manages access and settings,
                       and every use raises an Inbox notification
             off       the code is refused everywhere; only a run's own
                       plumbing still uses it
           Recommended: recovery, once your own verified sign-in works.
           Only this command, on this host, changes the mode.
`

// accessCodeCmd is `grain access-code`: show or rotate the access code
// (the local API token, ui/accesscode.go).
func accessCodeCmd(args []string) {
	if err := runAccessCode(args, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "grain access-code:", err)
		os.Exit(1)
	}
}

func runAccessCode(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("access-code", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, accessCodeUsage) }
	dataDir := fs.String("data-dir", "", "the daemon's data directory (default: $"+dataDirEnvVar+")")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	action := "show"
	if fs.NArg() > 1 && !(fs.Arg(0) == "mode" && fs.NArg() == 2) {
		fs.Usage()
		return errors.New("too many arguments")
	}
	if fs.NArg() >= 1 {
		// "mode recovery" is two arguments: it is still the mode action,
		// never "show", which would print the code to somebody who asked
		// to lock it down (docs/test-runs/2026-11-11-caller-identity.md,
		// F3).
		action = fs.Arg(0)
	}
	path := ui.LocalAPITokenFile()
	dir := dataDirDefault()
	if path == "" && dir != "" {
		path = ui.LocalAPITokenPath(dir)
	}
	if strings.TrimSpace(*dataDir) != "" {
		dir = *dataDir
		path = ui.LocalAPITokenPath(dir)
	}
	if path == "" {
		return fmt.Errorf("no access code file: set %s (or %s) to the daemon's, or pass -data-dir", dataDirEnvVar, ui.LocalAPITokenFileEnv)
	}
	switch action {
	case "show":
		b, err := os.ReadFile(path)
		if err != nil {
			return accessCodeFileError(path, err)
		}
		code := strings.TrimSpace(string(b))
		if code == "" {
			return fmt.Errorf("%s is empty; restart the daemon to write a new code, or run `grain access-code rotate`", path)
		}
		fmt.Fprintln(stdout, code)
		if note := accessCodeServerNote(); note != "" {
			fmt.Fprintln(stderr, "grain access-code:", note)
		}
		return nil
	case "rotate":
		if _, err := os.Stat(path); err != nil {
			return accessCodeFileError(path, err)
		}
		code, err := ui.RotateLocalAPIToken(path)
		if err != nil {
			return accessCodeFileError(path, err)
		}
		fmt.Fprintln(stdout, code)
		fmt.Fprintln(stderr, "grain access-code: rotated; the old code and every UI session signed in with it stop working within a second")
		return nil
	case "mode":
		if fs.NArg() == 1 {
			fmt.Fprintln(stdout, ui.ReadAccessCodeMode(path))
			return nil
		}
		mode, err := ui.ParseAccessCodeMode(fs.Arg(1))
		if err != nil {
			return err
		}
		if _, err := os.Stat(path); err != nil {
			return accessCodeFileError(path, err)
		}
		if err := ui.WriteAccessCodeMode(path, mode); err != nil {
			return accessCodeFileError(ui.AccessCodeModePath(path), err)
		}
		fmt.Fprintln(stdout, mode)
		switch mode {
		case ui.AccessCodeRecovery:
			fmt.Fprintln(stderr, "grain access-code: recovery: once a person with a full role on the allowlist can sign in with a verified identity, "+
				"the code only manages access and settings, and each use raises an Inbox notification")
		case ui.AccessCodeOff:
			fmt.Fprintln(stderr, "grain access-code: off: the UI, the CLI and every session refuse the code within a second; "+
				"sign in with a verified identity (grain login), or run `grain access-code mode on` here to undo it")
		}
		return nil
	default:
		fs.Usage()
		return fmt.Errorf("unknown action %q", action)
	}
}

func accessCodeFileError(path string, err error) error {
	switch {
	case errors.Is(err, os.ErrPermission):
		return fmt.Errorf("%s is readable only by the account grain runs as and root (%s owns it): "+
			"run this as that account (sudo -u %s grain access-code) -- %w", path, ui.FileOwner(path), ui.FileOwner(path), err)
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("there is no access code at %s -- the daemon writes it at first start; "+
			"check -data-dir / %s -- %w", path, dataDirEnvVar, err)
	}
	return err
}

// accessCodeServerNote asks the server the CLI would talk to
// (serverSource) whether it takes the code over the network, and says
// so when it does not (-ui-require-verifier / -ui-require-iap). It is a
// note, not an answer: a server that cannot be reached, or one too old to
// say, gets none.
var accessCodeServerNote = func() string {
	server, _ := serverSource()
	client := cliHTTPClient(time.Second, 2*time.Second)
	client.Timeout = 3 * time.Second
	resp, err := client.Get(strings.TrimRight(server, "/") + "/api/auth/access-code")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var status struct {
		RequireVerifier string `json:"requireVerifier"`
		RequireIAP      bool   `json:"requireIAP"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&status) != nil {
		return ""
	}
	req := strings.TrimSpace(status.RequireVerifier)
	if req == "" && status.RequireIAP {
		req = string(ui.AuthMethodIAP)
	}
	if req == "" {
		return ""
	}
	if req == string(ui.AuthMethodIAP) {
		return accessCodeRequireIAPNote
	}
	if !safeVerifierLabel(req) {
		return accessCodeRequireVerifierGenericNote
	}
	return fmt.Sprintf("the server requires %s (-ui-require-verifier=%s): this code is not accepted over the network, "+
		"only from the daemon's own host over loopback; sign in through %s instead", req, req, req)
}

const accessCodeRequireIAPNote = "the server requires IAP (-ui-require-iap): this code is not accepted over the network, " +
	"only from the daemon's own host over loopback (kubectl exec deploy/grain -- grain ...); sign in through IAP instead"

const accessCodeRequireVerifierGenericNote = "the server requires verified sign-in (-ui-require-verifier): this code is not accepted over the network, " +
	"only from the daemon's own host over loopback"

// safeVerifierLabel reports whether s is a plain verifier identifier safe to
// print to the operator's terminal (no control/escape sequences, format
// characters, whitespace, or excessive length).
func safeVerifierLabel(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if r == utf8.RuneError || unicode.IsControl(r) || unicode.IsSpace(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}
