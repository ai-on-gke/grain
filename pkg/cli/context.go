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
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Named contexts (design/spec/external-cli/external-cli.spec.md#XC-1,
// #XC-2): a workstation that talks to more than one deployment names
// each one once -- its server, how the CLI signs in to it, and where its
// credential is kept -- in config.json, and picks one per invocation
// with -context or $GRAIN_CONTEXT, or for every invocation with `grain
// context use`. config.json holds no secret: a credential's locator only
// (credentials.go), so the file can be read, diffed and backed up.

// contextEnvVar names the context for one shell.
const contextEnvVar = "GRAIN_CONTEXT"

// contextConfig is config.json.
type contextConfig struct {
	Current  string                 `json:"current,omitempty"`
	Contexts map[string]*cliContext `json:"contexts"`
}

// cliContext is one deployment config.json names.
type cliContext struct {
	Server string `json:"server"`
	// Auth is how the CLI signs in: "external-token", "access-code",
	// "google-id-token", "local", "none", or a name a wrapper registered
	// with RegisterCredentialSource.
	Auth     string `json:"auth,omitempty"`
	Audience string `json:"audience,omitempty"`
	// Credential is where the secret is kept ("keyring:grain/<ctx>" or
	// "file:<ctx>"), never the secret.
	Credential string `json:"credential,omitempty"`
	// TokenID is the external agent credential `grain login
	// --issue-token` minted, which `grain logout` revokes.
	TokenID string `json:"tokenId,omitempty"`
}

// grainConfigDir is $XDG_CONFIG_HOME/grain, else ~/.config/grain.
func grainConfigDir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return filepath.Join(dir, "grain"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding the grain config directory: %w", err)
	}
	return filepath.Join(home, ".config", "grain"), nil
}

func contextConfigPath() (string, error) {
	dir, err := grainConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// loadContexts reads config.json; a missing one is no contexts at all.
func loadContexts() (*contextConfig, error) {
	cfg := &contextConfig{Contexts: map[string]*cliContext{}}
	path, err := contextConfigPath()
	if err != nil {
		return cfg, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if cfg.Contexts == nil {
		cfg.Contexts = map[string]*cliContext{}
	}
	return cfg, nil
}

// saveContexts writes config.json atomically, mode 0600 in a 0700
// directory, refusing a context whose credential is anything but a
// locator -- a secret pasted into the wrong field stays out of the file.
func saveContexts(cfg *contextConfig) error {
	for name, c := range cfg.Contexts {
		if c.Credential != "" && !strings.HasPrefix(c.Credential, "keyring:") && !strings.HasPrefix(c.Credential, "file:") {
			return fmt.Errorf("context %q: credential must be a keyring: or file: locator, not a secret", name)
		}
	}
	path, err := contextConfigPath()
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateFile(path, append(raw, '\n'))
}

// writePrivateFile writes data to path through a temporary file renamed
// into place, mode 0600, creating its directory mode 0700.
func writePrivateFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// selectedContext is the context one invocation runs in: its name, its
// entry (nil for none), and what chose it.
type selectedContext struct {
	name   string
	ctx    *cliContext
	source string
}

// selectContext resolves -context, then $GRAIN_CONTEXT, then config.json's
// current (#XC-2). Naming a context config.json does not have is a usage
// error; a current that names one is ignored, as no context.
func selectContext(flagName string) (selectedContext, error) {
	cfg, err := loadContexts()
	if err != nil {
		return selectedContext{}, err
	}
	name, source := strings.TrimSpace(flagName), "-context"
	if name == "" {
		name, source = strings.TrimSpace(os.Getenv(contextEnvVar)), "$"+contextEnvVar
	}
	if name != "" {
		c, ok := cfg.Contexts[name]
		if !ok {
			return selectedContext{}, unknownContext(name, source)
		}
		return selectedContext{name: name, ctx: c, source: source}, nil
	}
	if c, ok := cfg.Contexts[cfg.Current]; ok && cfg.Current != "" {
		return selectedContext{name: cfg.Current, ctx: c, source: "current"}, nil
	}
	return selectedContext{}, nil
}

func unknownContext(name, source string) error {
	where := ""
	if source != "" {
		where = " (from " + source + ")"
	}
	return &cliError{exit: exitUsage, code: "unknown_context", fix: "grain context list",
		err: fmt.Errorf("no context named %q%s in the grain config: `grain context list` shows the ones there are", name, where)}
}

// serverSourceFor is serverSource with sel's context between
// $GRAIN_SERVER and /etc/grain/cli.env (#XC-3).
func serverSourceFor(sel selectedContext) (url, source string) {
	if v := strings.TrimSpace(os.Getenv(serverEnvVar)); v != "" {
		return v, "$" + serverEnvVar
	}
	if sel.ctx != nil && strings.TrimSpace(sel.ctx.Server) != "" {
		return strings.TrimSpace(sel.ctx.Server), "context " + sel.name
	}
	if env := readCLIEnv(); env.server != "" {
		return env.server, env.path
	}
	return defaultServerURL, "the built-in default"
}

// resolveTarget selects the context -context names (selectContext) and,
// unless -server was given, sets *server from it (serverSourceFor):
// runCLI's own -server default was computed before the command line
// could name a context. source is what the server came from.
func resolveTarget(fs *flag.FlagSet, server *string, contextName string) (selectedContext, string, error) {
	sel, err := selectContext(contextName)
	if err != nil {
		return sel, "", err
	}
	if flagWasSet(fs, "server") {
		return sel, "-server", nil
	}
	var source string
	*server, source = serverSourceFor(sel)
	return sel, source, nil
}

// envContext is the context $GRAIN_CONTEXT or config.json selects, for
// -server's default before the command line is read; the command line's
// own -context is resolved after it, in runCLI.
func envContext() selectedContext {
	sel, _ := selectContext("")
	return sel
}

// cmdContext is `grain context list|use|show|delete`.
func cmdContext(out *printer, sel selectedContext, args []string) error {
	if len(args) == 0 {
		return usageErrorf("usage: grain context list | use <name> | show [<name>] | delete <name>")
	}
	fs := flag.NewFlagSet("context "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	rest, err := parseArgs(fs, args[1:])
	if err != nil {
		return usageErrorf("%v", err)
	}
	cfg, err := loadContexts()
	if err != nil {
		return err
	}
	named := func() (string, error) {
		if len(rest) != 1 {
			return "", usageErrorf("usage: grain context %s <name>", args[0])
		}
		if _, ok := cfg.Contexts[rest[0]]; !ok {
			return "", unknownContext(rest[0], "")
		}
		return rest[0], nil
	}
	switch args[0] {
	case "list":
		if len(rest) != 0 {
			return usageErrorf("usage: grain context list")
		}
		names := make([]string, 0, len(cfg.Contexts))
		for n := range cfg.Contexts {
			names = append(names, n)
		}
		sort.Strings(names)
		if out.json {
			type row struct {
				Name    string `json:"name"`
				Current bool   `json:"current"`
				cliContext
			}
			rows := []row{}
			for _, n := range names {
				rows = append(rows, row{Name: n, Current: n == cfg.Current, cliContext: *cfg.Contexts[n]})
			}
			out.encode(rows)
			return nil
		}
		for _, n := range names {
			mark := " "
			if n == cfg.Current {
				mark = "*"
			}
			c := cfg.Contexts[n]
			fmt.Printf("%s %s\t%s\t%s\n", mark, n, c.Server, c.Auth)
		}
		return nil
	case "use":
		name, err := named()
		if err != nil {
			return err
		}
		cfg.Current = name
		if err := saveContexts(cfg); err != nil {
			return err
		}
		if out.json {
			out.encode(map[string]string{"current": name})
		} else {
			fmt.Printf("using context %s (%s)\n", name, cfg.Contexts[name].Server)
		}
		return nil
	case "show":
		name := sel.name
		if len(rest) > 0 {
			if name, err = named(); err != nil {
				return err
			}
		}
		if name == "" {
			return usageErrorf("no context is selected: name one, or `grain context use <name>`")
		}
		c := cfg.Contexts[name]
		if c == nil {
			return unknownContext(name, sel.source)
		}
		if out.json {
			out.encode(struct {
				Name string `json:"name"`
				cliContext
			}{name, *c})
			return nil
		}
		fmt.Printf("name: %s\nserver: %s\nauth: %s\n", name, c.Server, c.Auth)
		for _, kv := range [][2]string{{"audience", c.Audience}, {"credential", c.Credential}, {"tokenId", c.TokenID}} {
			if kv[1] != "" {
				fmt.Printf("%s: %s\n", kv[0], kv[1])
			}
		}
		return nil
	case "delete":
		name, err := named()
		if err != nil {
			return err
		}
		if err := newCredentialStore().delete(name, cfg.Contexts[name].Credential); err != nil {
			return err
		}
		delete(cfg.Contexts, name)
		if cfg.Current == name {
			cfg.Current = ""
		}
		if err := saveContexts(cfg); err != nil {
			return err
		}
		if out.json {
			out.encode(map[string]string{"deleted": name})
		} else {
			fmt.Printf("deleted context %s\n", name)
		}
		return nil
	}
	return usageErrorf("unknown context command %q: list, use, show or delete", args[0])
}
