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

// Package agentd is grain-agentd: a lightweight, rootless grain hostd for a
// machine that is not the daemon's. It runs the agent CLIs a grain daemon
// asks for (agy, claude, codex), as the user who starts it, with no root
// and no capabilities.
//
// It differs from `grain hostd` in four ways:
//
//   - It runs no capability servers. It registers no built-in entries, and
//     it refuses an allowlist whose capabilities: section names anything.
//   - It is configured entirely as the user. Its allowlist may be owned by
//     the user running it (not only root), and every default path is under
//     the user's home or runtime directory.
//   - It dials the daemon itself, directly or through IAP (-iap-audience),
//     as `grain hostd -server` does -- or through a transport a build
//     registers (RegisterTransport) for a daemon behind a proxy core grain
//     knows nothing about.
//   - It also answers the agent hooks the CLIs it starts fork
//     (agy-tool-hook, claude-stop-hook), since a remote run names hostd's
//     own binary for them (agent.HookBinary).
//
// A user-owned allowlist is weaker than a root-owned one: anything running
// as the user can edit it. That is acceptable here only because the
// allowlist can name agent CLIs and nothing else, and an agent CLI already
// runs as the user with the user's shell.
//
// `grain-agentd install -server URL [-join CODE | -name NAME]` sets it up for
// the user running it (install.go): the binary in ~/.local/bin, an allowlist
// filled in from the controller's /hostd/install template for the CLIs present
// on this machine, the credential in ~/.config/grain, and a systemd --user
// unit, started. `grain-agentd allow <name>` adds one CLI entry from the same
// controller template after confirmation on the host (allow.go).
//
// The package stays small on purpose: it links pkg/hostd and the leaf hook
// packages and nothing of the daemon's (no pkg/ui, pkg/daemon, pkg/model
// or store package), which deps_test.go enforces, so a build that vendors
// it -- or adds its own transport and hook registrations around Main --
// carries little.
package agentd

import (
	"context"
	"crypto/ed25519"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent/antigravity/hook"
	"github.com/ai-on-gke/grain/pkg/agent/claude/stophook"
	"github.com/ai-on-gke/grain/pkg/agentd/release"
	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/version"
)

// Program is the command's name, in messages, paths and the unit.
const Program = "grain-agentd"

// TokenEnv is the environment variable a hostd token is read from when
// the token file has none, and install takes one from instead of asking.
const TokenEnv = "GRAIN_HOSTD_TOKEN"

// JoinCodeEnv is the environment variable an enrollment code is read from
// when -join is unset.
const JoinCodeEnv = "GRAIN_HOST_JOIN_CODE"

// Main is the whole of cmd/grain-agentd: args is os.Args, and the result
// is the process's exit status. A build that adds its own transports or
// host-tool grants registers them from init() and calls this.
func Main(args []string) int {
	if len(args) > 1 {
		switch args[1] {
		case hook.Subcommand:
			hook.Run(args[2:], os.Stdin, os.Stdout, os.Stderr)
			return 0
		case stophook.Subcommand:
			stophook.Run(args[2:], os.Stdin, os.Stdout, os.Stderr)
			return 0
		case "install":
			return install(args[2:], os.Stdout, os.Stderr)
		case "allow":
			return allow(args[2:], os.Stdin, os.Stdout, os.Stderr)
		case "hostd":
			// Accepted for symmetry with `grain hostd`.
			return run(args[2:], os.Stdout, os.Stderr)
		}
	}
	var rest []string
	if len(args) > 1 {
		rest = args[1:]
	}
	return run(rest, os.Stdout, os.Stderr)
}

// paths is where grain-agentd keeps everything, under the user's home.
type paths struct {
	Bin, Token, Key, ReleasePub, Allowlist, StateDir, Stage, UnitDir string
}

func pathsFor(home string) paths {
	return paths{
		Bin:        filepath.Join(home, ".local", "bin", Program),
		Token:      filepath.Join(home, ".config", "grain", "agentd-token"),
		Key:        filepath.Join(home, ".config", "grain", "agentd-key"),
		ReleasePub: filepath.Join(home, ".config", "grain", release.DefaultConfigFileName),
		Allowlist:  filepath.Join(home, ".config", "grain", "agentd.yaml"),
		StateDir:   filepath.Join(home, ".local", "state", Program),
		Stage:      filepath.Join(home, ".grain", "hostd-stage"),
		UnitDir:    filepath.Join(home, ".config", "systemd", "user"),
	}
}

// PinnedReleasePublicKey loads the release public key pinned at
// ~/.config/grain/agentd-release.pub, falling back to the public key embedded
// in this grain-agentd binary when the file is absent. Symlinks and group- or
// world-writable files are refused.
func PinnedReleasePublicKey(home string) (ed25519.PublicKey, error) {
	path := pathsFor(home).ReleasePub
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return release.EmbeddedPublicKey()
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file, not a symlink", path)
	}
	if st.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("%s is writable by group or others (mode %04o)", path, st.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	pub, err := release.ParsePublicKey(string(raw))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return pub, nil
}

// owners are the uids grain-agentd accepts as its allowlist's owner: the
// user running it, and root.
func owners() []uint32 { return []uint32{uint32(os.Getuid()), 0} }

// geteuid is os.Geteuid; a seam so tests can stand in for root.
var geteuid = os.Geteuid

func run(args []string, stdout, stderr io.Writer) int {
	home, _ := os.UserHomeDir()
	p := pathsFor(home)
	fs := flag.NewFlagSet(Program, flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := fs.String("server", "", "the grain daemon's URL as this machine reaches it: https://grain.example.com, an IAP URL, or http://127.0.0.1:4242")
	tokenFile := fs.String("token-file", p.Token, "the file holding this host's credential (Settings -> Hosts); $"+TokenEnv+" is read when it is absent")
	keyFile := fs.String("key-file", p.Key, "the file holding this host's Ed25519 private key (mode 0600), generated by -join or -rekey")
	joinCode := fs.String("join", "", "enroll this host using an enrollment code from Settings -> Hosts -> Invite hosts ($"+JoinCodeEnv+")")
	rekeyHost := fs.String("rekey", "", "migrate an existing per-host token for the named host to a key-bound credential, accepted by the deployment owner")
	joinOnly := fs.Bool("join-only", false, "with -join or -rekey: exit 0 once accepted and the credential is saved, without starting the server")
	name := fs.String("name", "", "the registry name this host expects to be (Settings -> Hosts); optional, the daemon names it from its token")
	allowlist := fs.String("allowlist", p.Allowlist, "the agent CLIs this host agent may run (clis: only); owned by you or root, and not group- or world-writable")
	socket := fs.String("socket", defaultSocket(), "the local unix socket the host agent also listens on (mode 0600)")
	stateDir := fs.String("state-dir", p.StateDir, "where the host agent records the process groups it started")
	iapAudience := fs.String("iap-audience", "", "the OAuth client ID of the IAP in front of the daemon, for an IAP URL")
	build := fs.Bool("build", false, "print this binary's build and exit")
	check := fs.Bool("check", false, "check the allowlist, print the agent CLIs it offers, and exit")
	drain := fs.Bool("drain", false, "ask the host agent serving -socket to drain, and exit: one of this build's protocol cuts over (it keeps taking starts "+
		"and exits the moment no agent CLI runs, refusing new starts only if no such moment comes within the allowlist's drain_cap); "+
		"any other refuses new starts and waits for its agent CLIs (up to drain_cap). Either then exits for systemd to start "+
		"the binary ~/.local/bin/grain-agentd names. SIGUSR1 to the host agent drains")
	registerTransportFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *build {
		fmt.Fprintln(stdout, version.Get().Release)
		return 0
	}
	if geteuid() == 0 {
		fmt.Fprintln(stderr, Program+": refusing to run as root; run it as the user whose agent CLIs it starts")
		return 2
	}
	if *drain {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := askDrain(ctx, hostd.Client{Socket: *socket, Build: version.Get().Release}); err != nil {
			fmt.Fprintln(stderr, Program+": "+err.Error())
			return 1
		}
		return 0
	}
	if *check {
		if err := checkAgentsOnly(*allowlist, owners()); err != nil {
			fmt.Fprintln(stderr, Program+": "+err.Error())
			return 2
		}
		a, _ := hostd.LoadAllowlist(*allowlist, owners())
		for _, o := range a.Offers() {
			fmt.Fprintf(stdout, "%s\t%s\t%s\n", o.Kind, o.Name, o.Path)
		}
		return 0
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	join := strings.TrimSpace(*joinCode)
	if join == "" && *rekeyHost == "" && *joinOnly {
		join = strings.TrimSpace(os.Getenv(JoinCodeEnv))
	}
	if join != "" || strings.TrimSpace(*rekeyHost) != "" {
		if *server == "" {
			fmt.Fprintln(stderr, Program+": -server is required: the grain daemon's URL as this machine reaches it")
			return 2
		}
		if _, err := parseServer(*server); err != nil {
			fmt.Fprintln(stderr, Program+": "+err.Error())
			return 2
		}
		var curTok string
		if strings.TrimSpace(*rekeyHost) != "" {
			var err error
			curTok, err = readToken(*tokenFile)
			if err != nil {
				fmt.Fprintln(stderr, Program+": "+err.Error())
				return 2
			}
		}
		var idTok func(context.Context) (string, error)
		if *iapAudience != "" {
			idTok = hostd.NewIAPTokenProvider(*iapAudience).Token
		}
		clis := allowlistOrDetectedCLIs(*allowlist, owners())
		if _, err := hostd.Enroll(ctx, hostd.EnrollOptions{
			ServerURL:    *server,
			JoinCode:     join,
			RekeyHostd:   strings.TrimSpace(*rekeyHost),
			CurrentToken: curTok,
			KeyPath:      *keyFile,
			TokenPath:    *tokenFile,
			CLIs:         clis,
			Version:      version.Get().Release,
			IDToken:      idTok,
			Out:          stderr,
		}); err != nil {
			fmt.Fprintln(stderr, Program+": "+err.Error())
			return 1
		}
		if *joinOnly {
			return 0
		}
	}

	if err := checkAgentsOnly(*allowlist, owners()); err != nil {
		fmt.Fprintln(stderr, Program+": "+err.Error())
		return 2
	}
	if *server == "" {
		fmt.Fprintln(stderr, Program+": -server is required: the grain daemon's URL as this machine reaches it")
		return 2
	}
	token, err := readToken(*tokenFile)
	if err != nil {
		fmt.Fprintln(stderr, Program+": "+err.Error())
		return 2
	}
	u, err := parseServer(*server)
	if err != nil {
		fmt.Fprintln(stderr, Program+": "+err.Error())
		return 2
	}

	var privKey ed25519.PrivateKey
	if *keyFile != "" {
		if _, statErr := os.Stat(*keyFile); statErr == nil {
			k, loadErr := hostd.LoadPrivateKey(*keyFile)
			if loadErr != nil {
				fmt.Fprintln(stderr, Program+": reading -key-file: "+loadErr.Error())
				return 2
			}
			privKey = k
		} else if !errors.Is(statErr, os.ErrNotExist) {
			fmt.Fprintln(stderr, Program+": reading -key-file: "+statErr.Error())
			return 2
		}
	}

	var tokMu sync.RWMutex
	curToken := token
	getToken := func() string {
		tokMu.RLock()
		defer tokMu.RUnlock()
		return curToken
	}

	host, _ := os.Hostname()
	cfg := DialConfig{
		Server: u, Token: token, TokenFunc: getToken, PrivateKey: privKey,
		Name: *name, Host: host, Build: version.Get().Release,
		IAPAudience: *iapAudience, StateDir: *stateDir, Home: home,
	}
	dial, closeDial, err := dialerFor(ctx, cfg)
	if err != nil {
		fmt.Fprintln(stderr, Program+": "+err.Error())
		return 2
	}
	defer closeDial()
	srv := &hostd.Server{
		Socket:          *socket,
		Allowlist:       *allowlist,
		AllowlistOwners: owners(),
		StateDir:        *stateDir,
		Build:           version.Get().Release,
		Remote:          dial,
		AgentsOnly:      true,
		RenewName:       *name,
		RenewKey:        privKey,
		CurrentToken:    getToken,
		RenewedAt:       hostd.TokenFileIssuedAt(*tokenFile),
		OnTokenRenewed: func(next string, _ time.Time) {
			tokMu.Lock()
			curToken = next
			tokMu.Unlock()
			if *tokenFile != "" {
				_ = hostd.WriteSecretFile0600(*tokenFile, []byte(next+"\n"))
			}
		},
		// No CurrentLink: there is no installer's versions directory, so a
		// remote run's hooks name this binary itself (Hello.Grain falls back
		// to os.Executable). No Builtins: agent CLIs and nothing else.
	}
	usr1 := make(chan os.Signal, 1)
	signal.Notify(usr1, syscall.SIGUSR1)
	defer signal.Stop(usr1)
	go drainOnSignal(usr1, srv)
	if err := srv.Serve(ctx); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(stderr, Program+": "+err.Error())
		return 1
	}
	return 0
}

// askDrain is -drain: the request `grain host -drain` makes, for a host
// agent that has no `grain` binary beside it. One speaking this build's
// protocol is cut over -- grain-agentd has no in-place re-exec, so a
// handover would fall back to the same cutover -- and any other is
// drained, so its runs end on the build they started on. Without this
// flag (and SIGUSR1, drainOnSignal) nothing short of the daemon could
// drain a grain-agentd: the 2026-11-13 walkthrough's `grain-agentd
// -drain` was refused as an unknown flag, and SIGUSR1 was ignored
// (docs/test-runs/2026-11-13-hosts-env-drain.md, F1).
func askDrain(ctx context.Context, c hostd.Client) error {
	if h, err := c.Hello(ctx); err == nil && h.Protocol == hostd.ProtocolVersion {
		return c.Cutover(ctx)
	}
	return c.Drain(ctx)
}

// drainOnSignal drains srv on every signal sig delivers, as `grain host`
// does on SIGUSR1: new starts are refused, the running agent CLIs finish
// (up to drain_cap), and Serve returns for systemd to restart the unit.
func drainOnSignal(sig <-chan os.Signal, srv interface{ Drain() }) {
	for range sig {
		srv.Drain()
	}
}

// parseServer reads -server: an http or https URL with a host.
func parseServer(server string) (*url.URL, error) {
	u, err := url.Parse(server)
	if server == "" || err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("-server %q is not a URL like https://grain.example.com", server)
	}
	return u, nil
}

// checkAgentsOnly loads the allowlist as hostd will and refuses one that
// offers anything but agent CLIs. hostd re-reads the file on every request,
// so this is checked again by the same rule only at the next start; the
// file can only be changed by its owner (you), never by the daemon.
func checkAgentsOnly(path string, owners []uint32) error {
	a, err := hostd.LoadAllowlist(path, owners)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("no allowlist at %s: run `%s allow <name>` or write one with a clis: entry for each agent CLI this machine runs (capabilities: stays empty)", path, Program)
		}
		return err
	}
	if len(a.Capabilities) > 0 {
		names := slices.Sorted(maps.Keys(a.Capabilities))
		return fmt.Errorf("%s names capabilities (%s): %s runs agent CLIs only; remove them, or run `grain host`, which serves capabilities too", path, strings.Join(names, ", "), Program)
	}
	if len(a.CLIs) == 0 {
		return fmt.Errorf("%s offers no agent CLIs: run `%s allow <name>` or add a clis: entry", path, Program)
	}
	return nil
}

// readToken is the hostd token: the token file's, else $GRAIN_HOSTD_TOKEN.
func readToken(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil {
		if t := strings.TrimSpace(string(b)); t != "" {
			return t, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("reading -token-file: %w", err)
	}
	if t := strings.TrimSpace(os.Getenv(TokenEnv)); t != "" {
		return t, nil
	}
	return "", fmt.Errorf("no host token: put it in %s (mode 0600) or $%s", path, TokenEnv)
}

func defaultSocket() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, Program, "agentd.sock")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("%s-%d", Program, os.Getuid()), "agentd.sock")
}

func allowlistOrDetectedCLIs(path string, owners []uint32) []string {
	if a, err := hostd.LoadAllowlist(path, owners); err == nil && len(a.CLIs) > 0 {
		return slices.Sorted(maps.Keys(a.CLIs))
	}
	return hostd.DetectAgentCLIs()
}
