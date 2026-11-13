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

package agentd

// `grain-agentd install`: set grain-agentd up for the user running it,
// with no root -- the binary, an agents-only allowlist filled in from the
// controller's /hostd/install template, whatever a registered transport
// prepares, the credential, and a systemd --user unit -- and start it.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"text/template"
	"time"

	"golang.org/x/term"

	"github.com/ai-on-gke/grain/pkg/agentd/release"
	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/version"
)

// UnitName is the systemd --user unit install writes.
const UnitName = Program + ".service"

var (
	agyPathsMu sync.RWMutex
	agyPaths   []string
)

// RegisterAgyPath adds path to the offline fallback's candidate list for agy.
//
// Deprecated: register candidate paths on the controller via
// hostd.RegisterCLICandidates (or ui.RegisterHostCLICandidates) so new paths
// reach hosts as data through /hostd/install without a new grain-agentd binary.
func RegisterAgyPath(path string) {
	agyPathsMu.Lock()
	defer agyPathsMu.Unlock()
	agyPaths = append(agyPaths, path)
}

// executable and lookPath are seams for tests.
var (
	executable = func(path string) bool {
		st, err := os.Stat(path)
		return err == nil && !st.IsDir() && st.Mode()&0o111 != 0
	}
	lookPath = exec.LookPath
)

// systemctl and loginctl run those commands; seams for tests.
var (
	systemctl = func(stderr io.Writer, args ...string) error {
		c := exec.Command("systemctl", append([]string{"--user"}, args...)...)
		c.Stdout, c.Stderr = stderr, stderr
		return c.Run()
	}
	loginctl = func(args ...string) ([]byte, error) {
		return exec.Command("loginctl", args...).Output()
	}
)

// offlineAllowlistTemplate is the minimal fallback kept in the binary when an
// install runs offline and cannot reach /hostd/install on the controller. It
// carries no CLI-specific closed variable lists: this binary supports
// FeatureEnvOpen ("*"), and agy's per-run staged HOME is the one identity
// variable that requires an exact name.
func offlineAllowlistTemplate() hostd.AllowlistTemplate {
	agyPathsMu.RLock()
	extraAgy := slices.Clone(agyPaths)
	agyPathsMu.RUnlock()
	agyCandidates := make([]string, 0, len(extraAgy)+2)
	for _, p := range extraAgy {
		p = strings.TrimSpace(p)
		if p != "" && !slices.Contains(agyCandidates, p) {
			agyCandidates = append(agyCandidates, p)
		}
	}
	agyCandidates = append(agyCandidates, "agy", "~/.local/bin/agy")
	return hostd.AllowlistTemplate{
		Version: hostd.AllowlistTemplateVersion,
		CLIs: []hostd.CLITemplateEntry{
			{
				Name:       "agy",
				Aliases:    []string{"antigravity"},
				Candidates: agyCandidates,
				Env:        []string{"HOME"},
				PassEnv:    slices.Clone(hostd.ProxyVars),
			},
			{
				Name:       "claude",
				Candidates: []string{"claude", "~/.local/bin/claude"},
				PassEnv:    slices.Clone(hostd.ProxyVars),
			},
			{
				Name:       "codex",
				Candidates: []string{"codex", "~/.local/bin/codex"},
				PassEnv:    slices.Clone(hostd.ProxyVars),
			},
		},
	}
}

func resolveInstallTemplate(ctx context.Context, serverURL, iapAudience string) (tmpl hostd.AllowlistTemplate, offline bool, fetchErr error) {
	var idTok func(context.Context) (string, error)
	if iapAudience != "" {
		idTok = hostd.NewIAPTokenProvider(iapAudience).Token
	}
	fetched, err := hostd.FetchAllowlistTemplate(ctx, hostd.FetchTemplateOptions{
		ServerURL: serverURL,
		IDToken:   idTok,
		Timeout:   5 * time.Second,
	})
	if err == nil {
		return fetched, false, nil
	}
	if raw := strings.TrimSpace(os.Getenv(hostd.AgentdTemplateEnv)); raw != "" {
		if parsed, parseErr := hostd.ParseAllowlistTemplate([]byte(raw)); parseErr == nil {
			if parsed.Server == "" || strings.TrimRight(parsed.Server, "/") == strings.TrimRight(serverURL, "/") {
				return parsed, false, nil
			}
		}
	}
	return offlineAllowlistTemplate(), true, err
}

func install(args []string, stdout, stderr io.Writer) int {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(stderr, Program+" install: "+err.Error())
		return 2
	}
	p := pathsFor(home)
	fs := flag.NewFlagSet(Program+" install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := fs.String("server", "", "the grain daemon's URL as this machine reaches it (required)")
	joinCode := fs.String("join", strings.TrimSpace(os.Getenv(JoinCodeEnv)), "enroll via an enrollment code from Settings -> Hosts -> Invite hosts ($"+JoinCodeEnv+")")
	name := fs.String("name", "", "this host's registry name (Settings -> Hosts); optional")
	iapAudience := fs.String("iap-audience", "", "the OAuth client ID of the IAP in front of the daemon, for an IAP URL")
	noStart := fs.Bool("no-start", false, "write everything, but do not enable or start the service")
	registerTransportFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if geteuid() == 0 {
		fmt.Fprintln(stderr, Program+" install: run this as yourself, not root: "+Program+" runs as the user whose agent CLIs it starts")
		return 2
	}
	u, err := parseServer(*server)
	if err != nil {
		fmt.Fprintln(stderr, Program+" install: -server https://DAEMON is required: the grain daemon's URL as this machine reaches it")
		return 2
	}
	if *name != "" && strings.ContainsAny(*name, " \t\n\"'\\$") {
		fmt.Fprintf(stderr, "%s install: -name %q is not a plain name\n", Program, *name)
		return 2
	}
	if *iapAudience != "" && strings.ContainsAny(*iapAudience, " \t\n\"'\\$") {
		fmt.Fprintf(stderr, "%s install: -iap-audience %q is not a plain value\n", Program, *iapAudience)
		return 2
	}
	step := func(format string, a ...any) { fmt.Fprintf(stdout, "==> "+format+"\n", a...) }

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	// 1. The binary.
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, Program+" install: "+err.Error())
		return 1
	}
	if same, _ := sameFile(self, p.Bin); !same {
		if err := copyExecutable(self, p.Bin); err != nil {
			fmt.Fprintln(stderr, Program+" install: copying the binary: "+err.Error())
			return 1
		}
		step("installed %s", p.Bin)
	}

	// 1b. Pin the embedded release public key at ~/.config/grain/agentd-release.pub.
	if st, err := os.Lstat(p.ReleasePub); errors.Is(err, os.ErrNotExist) {
		pub, err := release.EmbeddedPublicKey()
		if err != nil {
			fmt.Fprintf(stderr, "%s install: embedded release key: %v\n", Program, err)
			return 1
		}
		pubPEM, err := release.FormatPublicKeyPEM(pub)
		if err != nil {
			fmt.Fprintf(stderr, "%s install: formatting release key: %v\n", Program, err)
			return 1
		}
		if err := os.MkdirAll(filepath.Dir(p.ReleasePub), 0o700); err != nil {
			fmt.Fprintln(stderr, Program+" install: "+err.Error())
			return 1
		}
		if err := writeFileAtomic(p.ReleasePub, pubPEM, 0o644); err != nil {
			fmt.Fprintln(stderr, Program+" install: writing the release key: "+err.Error())
			return 1
		}
		step("wrote %s (key %s)", p.ReleasePub, release.KeyID(pub))
	} else if err != nil {
		fmt.Fprintln(stderr, Program+" install: "+err.Error())
		return 1
	} else {
		if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() {
			fmt.Fprintf(stderr, "%s install: %s must be a regular file, not a symlink\n", Program, p.ReleasePub)
			return 1
		}
		if _, err := PinnedReleasePublicKey(home); err != nil {
			fmt.Fprintln(stderr, Program+" install: "+err.Error())
			return 1
		}
		step("keeping %s as it is", p.ReleasePub)
	}

	// 2. A registered transport's own preparation, done before contacting the
	// controller so template fetches, enrollment and the first start can use it.
	unit := unitConfig{Bin: p.Bin, Server: *server, Name: *name, IAPAudience: *iapAudience,
		Token: p.Token, Allowlist: p.Allowlist, StateDir: p.StateDir}
	if t := transportFor(u); t != nil && t.Prepare != nil {
		extra, err := t.Prepare(ctx, InstallConfig{Server: u, Home: home, StateDir: p.StateDir})
		if err != nil {
			fmt.Fprintf(stderr, "%s install: %s: %v\n", Program, t.Name, err)
			return 1
		}
		unit.Extra = extra
		step("prepared %s for %s", t.Name, u.Host)
	}

	// 3. The allowlist (prepared from the controller's template before
	// enrollment so any -join request reports the configured agent CLIs).
	if _, err := os.Stat(p.Allowlist); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(p.Stage, 0o700); err != nil {
			fmt.Fprintln(stderr, Program+" install: "+err.Error())
			return 1
		}
		tmpl, offline, fetchErr := resolveInstallTemplate(ctx, *server, *iapAudience)
		if offline {
			step("note: could not reach %s for the allowlist template (%v); using minimal offline fallback", *server, fetchErr)
		}
		resolved := tmpl.ResolveAll(home, lookPath, executable)
		if len(resolved) == 0 {
			fmt.Fprintf(stderr, "%s install: no agent CLI found on this machine (looked for %s); install one first, or write %s by hand\n",
				Program, strings.Join(tmpl.Names(), ", "), p.Allowlist)
			return 1
		}
		peer := hostd.ThisHello(version.Get().Release)
		rendered, err := hostd.RenderAgentdAllowlist(Program, *server, home, p.Stage, resolved, peer)
		if err != nil {
			fmt.Fprintln(stderr, Program+" install: rendering the allowlist: "+err.Error())
			return 1
		}
		step("writing %s:", p.Allowlist)
		fmt.Fprint(stdout, indentLines(rendered, "  "))
		if err := os.MkdirAll(filepath.Dir(p.Allowlist), 0o700); err != nil {
			fmt.Fprintln(stderr, Program+" install: "+err.Error())
			return 1
		}
		if err := writeFileAtomic(p.Allowlist, []byte(rendered), 0o644); err != nil {
			fmt.Fprintln(stderr, Program+" install: writing the allowlist: "+err.Error())
			return 1
		}
		step("wrote %s", p.Allowlist)
	} else {
		step("keeping %s as it is", p.Allowlist)
	}
	if err := checkAgentsOnly(p.Allowlist, owners()); err != nil {
		fmt.Fprintln(stderr, Program+" install: "+err.Error())
		return 1
	}

	// 4. The credential: enroll with -join (generating an Ed25519 keypair +
	// SAS pairing code handshake) or fall back to a per-host token.
	if err := os.MkdirAll(filepath.Dir(p.Token), 0o700); err != nil {
		fmt.Fprintln(stderr, Program+" install: "+err.Error())
		return 1
	}
	_ = os.Chmod(filepath.Dir(p.Token), 0o700)
	if strings.TrimSpace(*joinCode) != "" {
		var idTok func(context.Context) (string, error)
		if *iapAudience != "" {
			idTok = hostd.NewIAPTokenProvider(*iapAudience).Token
		}
		res, err := hostd.Enroll(ctx, hostd.EnrollOptions{
			ServerURL: *server,
			JoinCode:  strings.TrimSpace(*joinCode),
			KeyPath:   p.Key,
			TokenPath: p.Token,
			CLIs:      allowlistOrDetectedCLIs(p.Allowlist, owners()),
			Version:   version.Get().Release,
			IDToken:   idTok,
			Out:       stdout,
		})
		if err != nil {
			fmt.Fprintln(stderr, Program+" install: "+err.Error())
			return 1
		}
		unit.Key = p.Key
		step("enrolled as %s (key %s); saved credential to %s (mode 0600)", res.Name, res.KeyFingerprint, p.Token)
	} else {
		token := strings.TrimSpace(os.Getenv(TokenEnv))
		if token == "" {
			if have, _ := readTokenFile(p.Token); have != "" {
				step("keeping the token in %s (set %s to replace it)", p.Token, TokenEnv)
				if st, err := os.Stat(p.Key); err == nil && st.Mode().IsRegular() {
					unit.Key = p.Key
				}
			} else if token, err = promptToken(); err != nil {
				fmt.Fprintln(stderr, Program+" install: "+err.Error())
				return 2
			}
		}
		if token != "" {
			if err := writeFileAtomic(p.Token, []byte(token+"\n"), 0o600); err != nil {
				fmt.Fprintln(stderr, Program+" install: writing the token: "+err.Error())
				return 1
			}
			step("wrote the token to %s (mode 0600)", p.Token)
		}
	}

	// 5. The unit.
	text, err := renderUnit(unit)
	if err != nil {
		fmt.Fprintln(stderr, Program+" install: "+err.Error())
		return 1
	}
	unitPath := filepath.Join(p.UnitDir, UnitName)
	if err := os.MkdirAll(p.UnitDir, 0o755); err != nil {
		fmt.Fprintln(stderr, Program+" install: "+err.Error())
		return 1
	}
	if err := writeFileAtomic(unitPath, []byte(text), 0o644); err != nil {
		fmt.Fprintln(stderr, Program+" install: writing the unit: "+err.Error())
		return 1
	}
	step("wrote %s", unitPath)
	if *noStart {
		step("-no-start: start it with: systemctl --user enable --now %s", UnitName)
		return 0
	}

	// 6. Start it.
	if err := systemctl(stderr, "daemon-reload"); err != nil {
		fmt.Fprintf(stderr, "%s install: no systemd user session (%v); run it yourself, e.g. in tmux:\n  %s\n", Program, err, unit.commandLine())
		return 1
	}
	if err := systemctl(stderr, "enable", UnitName); err != nil {
		fmt.Fprintln(stderr, Program+" install: systemctl --user enable: "+err.Error())
		return 1
	}
	if err := systemctl(stderr, "restart", UnitName); err != nil {
		fmt.Fprintln(stderr, Program+" install: systemctl --user restart: "+err.Error())
		return 1
	}
	step("started %s: journalctl --user -u %s -f  (look for \"connected to the controller\")", UnitName, Program)
	if user := os.Getenv("USER"); user != "" && !lingering(user) {
		if _, err := loginctl("enable-linger", user); err == nil {
			step("enabled lingering, so %s keeps running after you log out", Program)
		} else {
			step("note: could not enable lingering (loginctl enable-linger may need root here); %s runs only while you are logged in", Program)
		}
	}
	return 0
}

// AllowlistFromTemplate fills in tmpl against the executables on this host and
// renders the first allowlist targeting peer, writing only keys peer advertises.
func AllowlistFromTemplate(tmpl hostd.AllowlistTemplate, peer hostd.Hello, home, stage, server string) (string, error) {
	resolved := tmpl.ResolveAll(home, lookPath, executable)
	if len(resolved) == 0 {
		return "", fmt.Errorf("no agent CLI found (looked for %s)", strings.Join(tmpl.Names(), ", "))
	}
	return hostd.RenderAgentdAllowlist(Program, server, home, stage, resolved, peer)
}

func indentLines(text, prefix string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = prefix + l
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

// readTokenFile is the token path holds, "" when it holds none.
func readTokenFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

type unitConfig struct {
	Bin, Server, Name, IAPAudience, Token, Key, Allowlist, StateDir string
	// Extra are a transport's own flags (Transport.Prepare).
	Extra []string
}

func (u unitConfig) args() []string {
	a := []string{"-server", u.Server, "-token-file", u.Token}
	if u.Key != "" {
		a = append(a, "-key-file", u.Key)
	}
	a = append(a, "-allowlist", u.Allowlist, "-state-dir", u.StateDir)
	if u.Name != "" {
		a = append(a, "-name", u.Name)
	}
	if u.IAPAudience != "" {
		a = append(a, "-iap-audience", u.IAPAudience)
	}
	return append(a, u.Extra...)
}

func (u unitConfig) commandLine() string {
	return u.Bin + " " + strings.Join(u.args(), " ")
}

// unitTemplate restarts grain-agentd whenever it exits, not only on a
// failure: a drain or cutover (OpDrain) ends with grain-agentd exiting 0
// so the binary ~/.local/bin/grain-agentd now names takes over, as
// grain-hostd.service does with Restart=always. With Restart=on-failure a
// drained host stayed offline, and a run whose Exit frame raced the drain
// waited out the daemon's re-attach window
// (docs/test-runs/2026-11-11-host-agent-contract.md). systemctl --user
// stop still stops it: a stop is never restarted.
//
// KillMode=mixed sends SIGTERM to grain-agentd alone, so a stop or restart
// lets Serve mark its running CLIs with killStopping (interrupted) rather
// than racing a cgroup-wide SIGTERM that can reap a CLI as a plain failure
// first, and then sends SIGKILL to anything left in the cgroup once
// grain-agentd exits (or crashes), so stopped or uninstalled units never
// leak unsupervised CLIs. KillMode=process would not keep detached runs
// usable across a PID-changing restart anyway: hostd spawns CLIs directly
// (Wait4 needs ppid == os.Getpid() in canAdoptLiveSpool), holds their
// deadline timers and remote OpListen loopback endpoints in-process, and
// kills unadoptable survivors in ReapRecorded.
var unitTemplate = template.Must(template.New("unit").Parse(`[Unit]
Description={{.Program}}: agent CLIs for the grain daemon at {{.Server}}
After=network-online.target

[Service]
ExecStart={{.Command}}
KillMode=mixed
Restart=always
RestartSec=10

[Install]
WantedBy=default.target
`))

// renderUnit is the systemd --user unit that runs u. It refuses a value
// that would need quoting there rather than quote it.
func renderUnit(u unitConfig) (string, error) {
	for _, v := range append([]string{u.Bin}, u.args()...) {
		if strings.ContainsAny(v, " \t\n\"'\\%$") {
			return "", fmt.Errorf("%q cannot go in a systemd unit as it is (spaces, quotes or %%)", v)
		}
	}
	var b strings.Builder
	err := unitTemplate.Execute(&b, struct{ Program, Server, Command string }{Program, u.Server, u.commandLine()})
	return b.String(), err
}

// promptToken reads the token from the terminal without echoing it.
//
// term.ReadPassword turns echo off in-process and back on when it returns.
// An interrupt while it waits would otherwise end the process with echo
// still off, so the terminal's state is also restored on SIGINT/SIGTERM.
func promptToken() (string, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("no terminal to ask for the host token on: set %s", TokenEnv)
	}
	defer tty.Close()
	fd := int(tty.Fd())
	if state, err := term.GetState(fd); err == nil {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
		stop := make(chan struct{})
		defer func() { signal.Stop(sigs); close(stop) }()
		go func() {
			select {
			case <-sigs:
				_ = term.Restore(fd, state)
				fmt.Fprintln(tty)
				os.Exit(130)
			case <-stop:
			}
		}()
	}
	fmt.Fprint(tty, "Paste this host's token (Settings -> Hosts), then Enter: ")
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(tty)
	if err != nil {
		return "", fmt.Errorf("reading the token: %w", err)
	}
	token := strings.TrimSpace(string(b))
	if token == "" {
		return "", errors.New("no token given")
	}
	return token, nil
}

// writeFileAtomic writes data to path through a temporary file in the same
// directory, renamed into place with mode perm.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// copyExecutable copies src to dest, mode 0755, through a temporary file
// renamed into place.
func copyExecutable(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), "."+filepath.Base(dest)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dest)
}

func sameFile(a, b string) (bool, error) {
	sa, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	sb, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(sa, sb), nil
}

func lingering(user string) bool {
	out, err := loginctl("show-user", user, "-p", "Linger")
	return err == nil && strings.TrimSpace(string(out)) == "Linger=yes"
}
