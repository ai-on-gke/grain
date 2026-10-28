// secrets.go implements `grain secrets`, the CLI's own answer to
// bwsalmon/agents#357: given -data-dir naming the same root a colocated
// `grain daemon` was started with (daemon.go's own -data-dir, "root
// directory for the store, secrets, and sandbox roots"), it edits the
// files under <data-dir>/secrets directly -- no daemon RPC of any kind,
// since pkg/secrets.Store already reads fresh off disk on every Resolve.
// That also means it only ever works when run on the same host as the
// server: -data-dir is a local filesystem path, so pointing it at the
// daemon's own root is only possible from where that root actually
// lives.
//
// It never opens the task store: a secret has nothing to do with a task,
// so main's dispatch routes "secrets" here the same way it already does
// for daemon/ui/mcpserver, ahead of runCLI's own store-connecting setup.
//
// list prints which secrets exist and which keys each one holds, never
// a value -- the same "tell what's set, not what it is" restriction
// pkg/secrets.Store.List and pkg/ui's own secrets endpoints hold to. set
// is the only way a value moves through this command at all, and it
// deliberately never takes one as an argv flag -- anything on a command
// line is readable back by anyone else on this host with a `ps` --
// reading it instead from -value-file or, left unset, from stdin.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/capability/gcpkey"
	"github.com/ai-on-gke/grain/pkg/capability/geminikey"
	"github.com/ai-on-gke/grain/pkg/secrets"
)

const secretsUsage = `usage: grain secrets -data-dir DIR <command> [args]

-data-dir must name the same root a colocated ` + "`grain daemon`" + ` was
started with -- the encrypted secrets file and its key both live in
<data-dir>/secrets, the same paths daemon.go itself resolves. It defaults to $GRAIN_DATA_DIR when that is set, which a host
installed by scripts/setup.sh exports already. This edits files on disk, not
a running daemon, so it only works when run on the same host as the server
(bwsalmon/agents#357).

Commands:
  list                          list every secret and the keys it holds (never values)
  set [-value-file F] S K       write key K's value in secret S, from -value-file or stdin
  delete <secret> [key]         delete one key, or (with no key given) the whole secret
  mint-gemini-key -project P    mint the daemon's own Gemini API key, if it has none yet
  repo [flags] [URL]            name the repository this deployment's encrypted secrets
                                are backed up to, or report the one it uses
`

func secretsCmd(args []string) {
	if err := runSecrets(args); err != nil {
		fmt.Fprintln(os.Stderr, "grain secrets: "+err.Error())
		os.Exit(1)
	}
}

func runSecrets(args []string) error {
	fs := flag.NewFlagSet("grain secrets", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, secretsUsage) }
	dataDir := fs.String("data-dir", dataDirDefault(), "root directory a colocated `grain daemon` was started with (required; $"+dataDirEnvVar+" supplies the default)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dataDir == "" {
		fs.Usage()
		return errors.New("-data-dir is required")
	}

	rest := fs.Args()
	if len(rest) == 0 {
		fs.Usage()
		return errors.New("a command is required")
	}

	store := daemon.OpenSecrets(*dataDir)
	cmd, cmdArgs := rest[0], rest[1:]
	switch cmd {
	case "list":
		return secretsList(store, cmdArgs)
	case "set":
		return secretsSet(store, cmdArgs)
	case "delete":
		return secretsDelete(store, cmdArgs)
	case "mint-gemini-key":
		return secretsMintGeminiKey(store, *dataDir, cmdArgs)
	case "repo":
		return secretsRepoCommand(context.Background(), *dataDir, os.Stdout, cmdArgs)
	default:
		fs.Usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func secretsList(store *secrets.Store, args []string) error {
	fs := flag.NewFlagSet("grain secrets list", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := noArgs(fs, fs.Args()); err != nil {
		return err
	}
	list, err := store.List()
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Println("no secrets set")
		return nil
	}
	for _, s := range list {
		fmt.Printf("%s: %s\n", s.Name, strings.Join(s.Keys, ", "))
	}
	return nil
}

func secretsSet(store *secrets.Store, args []string) error {
	fs := flag.NewFlagSet("grain secrets set", flag.ContinueOnError)
	valueFile := fs.String("value-file", "", "file holding the exact bytes to write as this key's value; defaults to reading stdin")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 2 {
		fs.Usage()
		return errors.New("usage: grain secrets set [-value-file FILE] <secret> <key>")
	}
	secretName, key := rest[0], rest[1]

	var value []byte
	if *valueFile != "" {
		value, err = os.ReadFile(*valueFile)
	} else {
		value, err = io.ReadAll(os.Stdin)
	}
	if err != nil {
		return fmt.Errorf("reading the value: %w", err)
	}
	if err := store.Set(secretName, key, value); err != nil {
		return err
	}
	fmt.Printf("set %s/%s\n", secretName, key)
	return nil
}

func secretsDelete(store *secrets.Store, args []string) error {
	fs := flag.NewFlagSet("grain secrets delete", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch fs.NArg() {
	case 1:
		secretName := fs.Arg(0)
		if err := store.DeleteSecret(secretName); err != nil {
			return err
		}
		fmt.Printf("deleted %s\n", secretName)
	case 2:
		secretName, key := fs.Arg(0), fs.Arg(1)
		if err := store.DeleteKey(secretName, key); err != nil {
			return err
		}
		fmt.Printf("deleted %s/%s\n", secretName, key)
	default:
		fs.Usage()
		return errors.New("usage: grain secrets delete <secret> [key]")
	}
	return nil
}

// secretsMintGeminiKey mints the daemon's own operating Gemini API key
// using the standing minter credential already in the secrets database,
// and writes it to the file `grain daemon -gemini-api-key-file` reads.
//
// This closes the deploy-time gap terraform/gcp otherwise leaves: a
// deployment whose minter already holds roles/serviceusage.apiKeysAdmin
// (that module's enable_gemini_key, on by default) has, on the host,
// every permission needed to mint this key -- so requiring an operator
// to paste one in by hand before grain-daemon.service will start is a
// step the deployment can take for itself. Nothing here widens any
// grant: it is the same credential, the same API, and the same
// per-service restriction the gemini-key capability already mints under.
//
// Seed-once, deliberately, mirroring scripts/setup.sh's own
// seed_secret and seed_gcp_minter_key: an existing non-empty key file is
// left exactly as it is and reported, never overwritten. A redeploy
// therefore does not mint a fresh key on every convergence pass, and a
// key an operator placed by hand always wins. Rotating means deleting
// the file first (and the old key in GCP, which this does not do for
// you -- the reaper never touches an operating key).
func secretsMintGeminiKey(store *secrets.Store, dataDir string, args []string) error {
	fs := flag.NewFlagSet("grain secrets mint-gemini-key", flag.ContinueOnError)
	project := fs.String("project", "", "GCP project to mint the key in (required)")
	credential := fs.String("credential", gcpkey.DefaultMinterCredential, "name of the standing minter credential in this secrets store")
	out := fs.String("out", "", "file to write the key to; defaults to <data-dir>/secrets/gemini-api-key")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return errors.New("usage: grain secrets mint-gemini-key -project PROJECT [-credential NAME] [-out FILE]")
	}
	if *project == "" {
		return errors.New("-project is required")
	}

	path := *out
	if path == "" {
		path = filepath.Join(dataDir, "secrets", "gemini-api-key")
	}
	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		fmt.Printf("%s already holds a key; leaving it untouched\n", path)
		return nil
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("checking %s: %w", path, err)
	}

	ctx := context.Background()
	name, key, err := geminikey.MintOperatingKey(ctx, store, *credential, *project)
	if err != nil {
		return err
	}

	// Written 0600 through a temporary file in the same directory, so a
	// reader never observes a half-written key and the value never
	// exists at the final path with looser permissions -- the same care
	// pkg/secrets and setup.sh's own seed_secret take with a credential.
	if err := writeSecretFile(path, key); err != nil {
		return err
	}
	fmt.Printf("minted %s and wrote it to %s\n", name, path)
	return nil
}

func writeSecretFile(path, value string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".gemini-api-key-*")
	if err != nil {
		return fmt.Errorf("creating a temporary file next to %s: %w", path, err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("securing %s: %w", tmp.Name(), err)
	}
	if _, err := tmp.WriteString(value); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("moving the key into place at %s: %w", path, err)
	}
	return nil
}
