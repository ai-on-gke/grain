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

// connections.go implements `grain connections import <file>`: the one
// way to give a deployment its git and model connections without the UI,
// for a headless or scripted install (scripts/setup.sh and `grain init`
// both seed through it).
//
// The file is an import format only. Grain never reads it at runtime:
// this command records what it says as the deployment's connections, in
// the store, the same records Settings edits, and the file can be
// deleted afterwards. That is what replaced the
// secrets/github/credentials.json ladder, which grain did read on every
// git request (docs/single-tenant-reset.md, stage 2).
//
// Like `grain secrets`, it works on a data dir on this host rather than
// through a running daemon. A daemon already running on that data dir
// picks the connections up on its next settings refresh.
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

	"gopkg.in/yaml.v3"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres"
	"github.com/ai-on-gke/grain/pkg/secrets"
)

const connectionsUsage = `usage: grain connections -data-dir DIR import <file>

Records the git and model connections a seed file describes as this
deployment's connections. Grain never reads the file again: it can be
deleted once this has run. Running it twice with the same file changes
nothing the second time.

The file is YAML (JSON is YAML too):

  git:                            # git connections, most specific scope wins
    - credential: bot             # a credential name under <data-dir>/secrets/github,
                                  # or "anonymous" for none at all
      host: github.com            # optional; the deployment's default host
      scope: ""                   # "" (the host default), "owner" or "owner/repo"
      token_file: /run/bot.token  # optional; stored as secrets/github/bot.token
  models:                         # model connections, matched by id
    - id: gemini
      framework: antigravity      # antigravity, claude, codex or custom
      models: [gemini-3.1-pro]    # optional pin; empty means discover
      credential_file: /run/key   # optional; stored as this connection's key
  default_model: gemini-3.1-pro   # optional; the deployment's default model

A git entry replaces the connection already recorded for its host and
scope. A model entry replaces the connection with its id; every other
connection is kept. Model connections need a data dir the daemon has
already started on once: they are recorded on the settings row it seeds.

-data-dir defaults to $GRAIN_DATA_DIR. The store is the one the daemon on
that data dir opens: -db-driver and -db-dsn, $GRAIN_DB_DRIVER and
$GRAIN_DB_DSN, or what the data dir records.
`

// defaultModelSeed accepts a single model string, a comma-separated list,
// or a YAML sequence of model names under `default_model`.
type defaultModelSeed string

func (d *defaultModelSeed) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.SequenceNode {
		var list []string
		if err := value.Decode(&list); err != nil {
			return err
		}
		*d = defaultModelSeed(model.JoinDefaultModels(list))
		return nil
	}
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	*d = defaultModelSeed(model.NormalizeDefaultModel(s))
	return nil
}

// connectionSeed is the file `grain connections import` reads.
type connectionSeed struct {
	Git           []gitConnectionSeed   `yaml:"git"`
	Models        []modelConnectionSeed `yaml:"models"`
	DefaultModel  defaultModelSeed      `yaml:"default_model"`
	DefaultModels []string              `yaml:"default_models"`
	Relay         any                   `yaml:"relay"`
	RelaySettings any                   `yaml:"relay_settings"`
}

type gitConnectionSeed struct {
	Credential string `yaml:"credential"`
	Host       string `yaml:"host"`
	Scope      string `yaml:"scope"`
	// TokenFile, when set, is copied to the credential's own token file,
	// which is where a connection's credential is read from.
	TokenFile string `yaml:"token_file"`
}

type modelConnectionSeed struct {
	ID             string   `yaml:"id"`
	Name           string   `yaml:"name"`
	Framework      string   `yaml:"framework"`
	Models         []string `yaml:"models"`
	ExcludedModels []string `yaml:"excluded_models"`
	AuthMode       string   `yaml:"auth_mode"`
	Command        string   `yaml:"command"`
	CLIPath        string   `yaml:"cli_path"`
	Concurrency    int      `yaml:"concurrency"`
	Disabled       bool     `yaml:"disabled"`
	// CredentialFile, when set, is stored as the connection's own key in
	// the encrypted secrets store.
	CredentialFile string `yaml:"credential_file"`
}

func connectionsCmd(args []string) {
	if err := runConnections(context.Background(), args, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "grain connections: "+err.Error())
		os.Exit(1)
	}
}

func runConnections(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("grain connections", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, connectionsUsage) }
	dataDir := fs.String("data-dir", dataDirDefault(), "root directory of the grain installation ($"+dataDirEnvVar+" supplies the default)")
	dbDriver := fs.String("db-driver", "", "the daemon's -db-driver; defaults to $GRAIN_DB_DRIVER, then what the data dir records, then sqlite")
	dbDSN := fs.String("db-dsn", "", "the daemon's -db-dsn; defaults to $GRAIN_DB_DSN, then what the data dir records")
	rest, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if *dataDir == "" {
		fs.Usage()
		return errors.New("-data-dir is required")
	}
	if len(rest) != 2 || rest[0] != "import" {
		fs.Usage()
		return errors.New("usage: grain connections -data-dir DIR import <file>")
	}
	raw, err := os.ReadFile(rest[1])
	if err != nil {
		return err
	}
	seed, err := parseConnectionSeed(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", rest[1], err)
	}
	driver, dsn := *dbDriver, *dbDSN
	if driver == "" || dsn == "" {
		resolvedDriver, resolvedDSN, err := daemon.ResolveDatabase(*dataDir)
		if err != nil {
			return err
		}
		if driver == "" {
			driver = resolvedDriver
		}
		if dsn == "" {
			dsn = resolvedDSN
		}
	}
	// Model connections go on the settings row a daemon's first start
	// writes, so a data dir with no store at all has no daemon behind it
	// yet -- or one on a database it did not record. Either way OpenStore
	// would only leave an empty SQLite store behind for nothing.
	if len(seed.Models) > 0 && (driver == "" || driver == "sqlite") && dsn == "" && !daemon.HasSQLiteStore(*dataDir) {
		return fmt.Errorf("%s has no task store yet (no store/grain.db, and no %s naming another database), "+
			"so there is nowhere to record model connections: if its daemon runs on Postgres, pass "+
			"-db-driver postgres -db-dsn URL (or set $GRAIN_DB_DRIVER and $GRAIN_DB_DSN); "+
			"otherwise start grain daemon on it once, then import again", *dataDir, postgres.DatabaseConfigFile)
	}
	store, db, err := daemon.OpenStore(driver, dsn, *dataDir)
	if err != nil {
		return err
	}
	defer db.Close()
	return importConnections(ctx, store, *dataDir, seed, out)
}

// parseConnectionSeed reads and checks a seed file, so that nothing is
// written for a file with a mistake anywhere in it.
func parseConnectionSeed(raw []byte) (connectionSeed, error) {
	var seed connectionSeed
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&seed); err != nil && !errors.Is(err, io.EOF) {
		return seed, err
	}
	for i, g := range seed.Git {
		conn, err := model.NormalizeTeamGitConnection(model.TeamGitConnection{Credential: g.Credential, Host: g.Host, Scope: g.Scope})
		if err != nil {
			return seed, fmt.Errorf("git[%d]: %w", i, err)
		}
		// "anonymous" is a connection with no credential at all, which a
		// public repository wants; it has no token to store.
		if conn.Credential == "anonymous" {
			if g.TokenFile != "" {
				return seed, fmt.Errorf("git[%d]: the anonymous credential takes no token_file", i)
			}
		} else if err := gitproxy.ValidCredentialName(conn.Credential); err != nil {
			return seed, fmt.Errorf("git[%d]: %w", i, err)
		}
		seed.Git[i].Credential, seed.Git[i].Host, seed.Git[i].Scope = conn.Credential, conn.Host, conn.Scope
	}
	seen := map[string]bool{}
	for i, m := range seed.Models {
		m.ID = strings.TrimSpace(m.ID)
		m.Framework = model.NormalizeAgentFrameworkName(strings.TrimSpace(m.Framework))
		if m.ID == "" || strings.ContainsAny(m.ID, " /\\") {
			return seed, fmt.Errorf("models[%d].id must be a non-empty identifier", i)
		}
		if seen[m.ID] {
			return seed, fmt.Errorf("models: id %q is listed twice", m.ID)
		}
		seen[m.ID] = true
		if !model.ValidAgentFramework(m.Framework) {
			return seed, fmt.Errorf("models[%d].framework must be %s", i, model.AgentFrameworkNames())
		}
		if m.Framework == model.AgentFrameworkCustom && strings.TrimSpace(m.Command) == "" {
			return seed, fmt.Errorf("models[%d].command is required for a custom connection", i)
		}
		seed.Models[i] = m
	}
	if len(seed.DefaultModels) > 0 {
		seed.DefaultModel = defaultModelSeed(model.JoinDefaultModels(seed.DefaultModels))
	} else {
		seed.DefaultModel = defaultModelSeed(model.NormalizeDefaultModel(string(seed.DefaultModel)))
	}
	return seed, nil
}

func importConnections(ctx context.Context, store *model.Store, dataDir string, seed connectionSeed, out io.Writer) error {
	githubDir := filepath.Join(dataDir, "secrets", "github")
	for _, g := range seed.Git {
		if g.TokenFile == "" {
			continue
		}
		token, err := os.ReadFile(g.TokenFile)
		if err != nil {
			return fmt.Errorf("git connection %s: %w", g.Credential, err)
		}
		if err := os.MkdirAll(githubDir, 0o700); err != nil {
			return err
		}
		set, err := gitproxy.LoadCredentialSet(githubDir)
		if err != nil {
			return err
		}
		if err := set.SetToken(g.Credential, strings.TrimSpace(string(token))); err != nil {
			return fmt.Errorf("git connection %s: %w", g.Credential, err)
		}
	}
	// The daemon seeds the settings row from its own flags on its first
	// start, and only when there is none: a row written here first would
	// stand in for every one of them. Git connections and the default
	// model are recorded on that row too (since schema 22), and may be
	// recorded before that first start: into a row holding nothing else,
	// which the daemon seeds from its flags, keeping them
	// (model.Config.ConnectionsOnly).
	if len(seed.Models) > 0 {
		if cfg, err := store.GetConfig(ctx); err != nil {
			return err
		} else if cfg == nil {
			return fmt.Errorf("this data dir has no deployment settings yet, so there is nowhere to record " +
				"model connections: start grain daemon on it once, then import again")
		}
	}
	var secretStore *secrets.Store
	for _, m := range seed.Models {
		if m.CredentialFile == "" {
			continue
		}
		value, err := os.ReadFile(m.CredentialFile)
		if err != nil {
			return fmt.Errorf("model connection %s: %w", m.ID, err)
		}
		if secretStore == nil {
			secretStore = daemon.OpenSecrets(dataDir)
		}
		if err := secretStore.Set(secrets.AgentBackendCredentialSecret(m.ID), secrets.AgentCredentialKey, []byte(strings.TrimSpace(string(value)))); err != nil {
			return fmt.Errorf("model connection %s: %w", m.ID, err)
		}
	}

	if len(seed.Models) > 0 {
		cfg, err := store.GetConfig(ctx)
		if err != nil {
			return err
		}
		if cfg == nil {
			return fmt.Errorf("this data dir has no deployment settings yet")
		}
		cfg.AgentBackends = mergeModelConnections(cfg.AgentBackends, seed.Models)
		if err := store.PutConfig(ctx, *cfg); err != nil {
			return fmt.Errorf("recording model connections: %w", err)
		}
	}
	if len(seed.Git) > 0 || seed.DefaultModel != "" {
		// With no row yet, UpdateConfig writes one holding only these,
		// which the daemon's first start seeds the rest of from its
		// flags (model.Config.ConnectionsOnly).
		if err := store.UpdateConfig(ctx, func(cfg *model.Config) error {
			cfg.GitConnections = mergeGitConnections(cfg.GitConnections, seed.Git)
			if seed.DefaultModel != "" {
				cfg.DefaultModel = string(seed.DefaultModel)
			}
			return nil
		}); err != nil {
			return fmt.Errorf("recording git connections: %w", err)
		}
	}
	if seed.Relay != nil || seed.RelaySettings != nil {
		fmt.Fprintln(out, "note: skipping relay settings (the rendezvous relay has been removed)")
	}
	fmt.Fprintf(out, "imported %d git and %d model connections\n", len(seed.Git), len(seed.Models))
	return nil
}

// mergeGitConnections is have with each seeded connection in place of the
// one recorded for the same host and scope, or appended when there is
// none.
func mergeGitConnections(have []model.TeamGitConnection, seed []gitConnectionSeed) []model.TeamGitConnection {
	out := append([]model.TeamGitConnection(nil), have...)
	for _, g := range seed {
		conn := model.TeamGitConnection{Credential: g.Credential, Host: g.Host, Scope: g.Scope}
		replaced := false
		for i, c := range out {
			norm, err := model.NormalizeTeamGitConnection(c)
			if err == nil && norm.Host == conn.Host && norm.Scope == conn.Scope {
				out[i] = conn
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, conn)
		}
	}
	return out
}

// mergeModelConnections is have with each seeded connection in place of
// the one with its id, or appended. A replaced connection keeps its
// discovered models, which are a cache of what its provider serves.
func mergeModelConnections(have []model.AgentBackend, seed []modelConnectionSeed) []model.AgentBackend {
	out := append([]model.AgentBackend(nil), have...)
	for _, m := range seed {
		b := model.AgentBackend{
			ID:             m.ID,
			Name:           strings.TrimSpace(m.Name),
			Framework:      m.Framework,
			Models:         m.Models,
			ExcludedModels: m.ExcludedModels,
			AuthMode:       strings.TrimSpace(m.AuthMode),
			Command:        strings.TrimSpace(m.Command),
			CLIPath:        strings.TrimSpace(m.CLIPath),
			Concurrency:    m.Concurrency,
			Disabled:       m.Disabled,
		}
		replaced := false
		for i, c := range out {
			if c.ID == b.ID {
				b.Discovered = c.Discovered
				out[i] = b
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, b)
		}
	}
	return out
}
