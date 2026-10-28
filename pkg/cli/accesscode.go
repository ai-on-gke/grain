package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/bwsalmon/grain/pkg/ui"
)

const accessCodeUsage = `usage: grain access-code [-data-dir DIR] [show|rotate]

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
	if fs.NArg() > 1 {
		fs.Usage()
		return errors.New("too many arguments")
	}
	if fs.NArg() == 1 {
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
