package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/bwsalmon/grain/pkg/capability/protocol"
	"github.com/bwsalmon/grain/pkg/ui"
)

const capabilityServersUsage = `usage: grain capability-servers <list|add|remove|enable|disable|bind-secret> [args]

  list
  add <capability_id> --command <cmd> [--arg <a>]... [--env NAME=VALUE]... [--disabled]
  add <capability_id> --url <serverUrl> [--header NAME=VALUE]... [--disabled]
  remove|enable|disable <capability_id>

grain's own in-tree servers (gcp, git, model-proxy, self-debug, skill,
host-script) are listed as built-in under https://grain.dev/capabilities/<name>
without anyone adding them. A built-in can be configured and disabled, but
not removed: disable it instead.
  bind-secret <capability_id> (--env NAME | --header NAME) --secret <name>[/<key>] [--prefix <text>] [--value-stdin]

Values that are credentials belong in the secrets store: write them as
$secret:<name>, or bind them with bind-secret, which can store the value
read from stdin as it binds it. A value is never printed back.`

// cmdCapabilityServers is `grain capability-servers`: the deployment's
// mcpServers (docs/capabilities-spec.md §3.1), through
// /api/capability-servers.
func cmdCapabilityServers(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	if len(args) == 0 {
		return errors.New(capabilityServersUsage)
	}
	sub, subArgs := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		fmt.Println(capabilityServersUsage)
		return nil
	case "list", "ls":
		list, err := c.ListCapabilityServers(ctx)
		if err != nil {
			return err
		}
		printCapabilityServers(out, list)
		return nil
	case "add", "set":
		return cmdCapabilityServersAdd(ctx, c, out, subArgs)
	case "remove", "rm", "enable", "disable":
		fs := flag.NewFlagSet("grain capability-servers "+sub, flag.ContinueOnError)
		rest, err := parseArgs(fs, subArgs)
		if err != nil {
			return err
		}
		if len(rest) != 1 {
			return fmt.Errorf("usage: grain capability-servers %s <capability_id>", sub)
		}
		action := sub
		if action == "rm" {
			action = "remove"
		}
		list, err := c.CapabilityServerAction(ctx, action, rest[0])
		if err != nil {
			return err
		}
		printCapabilityServers(out, list)
		return nil
	case "bind-secret":
		return cmdCapabilityServersBind(ctx, c, out, subArgs)
	default:
		return fmt.Errorf("unknown capability-servers subcommand %q\n%s", sub, capabilityServersUsage)
	}
}

func cmdCapabilityServersAdd(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain capability-servers add", flag.ContinueOnError)
	command := fs.String("command", "", "stdio server: the executable (a bare name on PATH, a clean absolute path, or a path relative to grain/config)")
	serverURL := fs.String("url", "", "HTTP server: its control-plane serverUrl")
	disabled := fs.Bool("disabled", false, "save the entry disabled")
	var argv, env, headers stringList
	fs.Var(&argv, "arg", "an argument to --command (repeatable)")
	fs.Var(&env, "env", "NAME=VALUE for a stdio server's environment (repeatable)")
	fs.Var(&headers, "header", "NAME=VALUE sent to an HTTP server (repeatable)")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return errors.New(capabilityServersUsage)
	}
	cfg := protocol.ServerConfig{Command: *command, Args: argv, ServerURL: *serverURL, Disabled: *disabled}
	if cfg.Env, err = pairs("--env", env); err != nil {
		return err
	}
	if cfg.Headers, err = pairs("--header", headers); err != nil {
		return err
	}
	list, err := c.SetCapabilityServer(ctx, ui.SetCapabilityServerRequest{ID: rest[0], Config: cfg})
	if err != nil {
		return err
	}
	printCapabilityServers(out, list)
	return nil
}

func cmdCapabilityServersBind(ctx context.Context, c *ui.HTTPClient, out *printer, args []string) error {
	fs := flag.NewFlagSet("grain capability-servers bind-secret", flag.ContinueOnError)
	env := fs.String("env", "", "the env entry to bind")
	header := fs.String("header", "", "the header to bind")
	secret := fs.String("secret", "", "the secret name, <name> or <secret>/<key>")
	prefix := fs.String("prefix", "", `literal text before the reference, as in "Bearer "`)
	valueStdin := fs.Bool("value-stdin", false, "store the secret's value, read from stdin, before binding it")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 || (*env == "") == (*header == "") || *secret == "" {
		return errors.New(capabilityServersUsage)
	}
	req := ui.BindCapabilityServerSecretRequest{ID: rest[0], Kind: "env", Name: *env, Secret: *secret, Prefix: *prefix}
	if *header != "" {
		req.Kind, req.Name = "header", *header
	}
	if *valueStdin {
		v, err := readSecretStdin()
		if err != nil {
			return err
		}
		req.Value = v
	}
	list, err := c.BindCapabilityServerSecret(ctx, req)
	if err != nil {
		return err
	}
	printCapabilityServers(out, list)
	return nil
}

func pairs(flagName string, in []string) (map[string]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(in))
	for _, kv := range in {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("%s %q is not NAME=VALUE", flagName, kv)
		}
		out[k] = v
	}
	return out, nil
}

func printCapabilityServers(out *printer, list []ui.CapabilityServer) {
	if out.json {
		out.encode(list)
		return
	}
	if len(list) == 0 {
		fmt.Println("No capability servers configured.")
		return
	}
	for _, s := range list {
		state := "unknown"
		if s.Status != nil {
			state = string(s.Status.State)
		} else if s.Config.Disabled {
			state = "disabled"
		}
		label := s.ID
		if s.Builtin != nil {
			label += " (built-in " + s.Builtin.Name + ")"
		}
		fmt.Printf("- %s\n    state: %s\n", label, state)
		if len(s.Config.Aliases) > 0 {
			fmt.Printf("    also answers to: %s\n", strings.Join(s.Config.Aliases, ", "))
		}
		if s.Config.Stdio() {
			fmt.Printf("    command: %s %s\n", s.Config.Command, strings.Join(s.Config.Args, " "))
		} else {
			fmt.Printf("    serverUrl: %s\n", s.Config.ServerURL)
		}
		for _, sec := range s.Secrets {
			have := "missing"
			if sec.Present {
				have = "set"
			}
			fmt.Printf("    secret %s: %s\n", sec.Name, have)
		}
		if s.Status != nil {
			if len(s.Status.Methods) > 0 {
				fmt.Printf("    methods: %s\n", strings.Join(s.Status.Methods, ", "))
			}
			if s.Status.Error != "" {
				fmt.Printf("    error: %s\n", s.Status.Error)
			}
			if s.Status.Reason != "" {
				fmt.Printf("    needs configuring: %s\n", s.Status.Reason)
			}
		}
	}
}

// readSecretStdin reads a secret's value from stdin, dropping one
// trailing newline (what `echo` and a terminal add) and nothing else.
func readSecretStdin() (string, error) {
	data, err := io.ReadAll(bufio.NewReader(os.Stdin))
	if err != nil {
		return "", fmt.Errorf("reading the secret value from stdin: %w", err)
	}
	v := strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	if v == "" {
		return "", errors.New("--value-stdin read an empty value")
	}
	return v, nil
}
