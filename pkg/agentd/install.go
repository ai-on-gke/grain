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
// with no root -- the binary, the token, an agents-only allowlist,
// whatever a registered transport prepares, and a systemd --user unit --
// and start it.

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
	"strings"
	"sync"
	"syscall"
	"text/template"

	"golang.org/x/term"

	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/version"
)

// UnitName is the systemd --user unit install writes.
const UnitName = Program + ".service"

var (
	agyPathsMu sync.RWMutex
	agyPaths   []string
)

// RegisterAgyPath adds path to where install looks for agy before $PATH:
// for a build whose machines install agy somewhere of their own. The
// first executable registered path wins.
func RegisterAgyPath(path string) {
	agyPathsMu.Lock()
	defer agyPathsMu.Unlock()
	agyPaths = append(agyPaths, path)
}

// findAgy is where agy is on this machine: a registered path, else agy on
// $PATH, else ~/.local/bin/agy; "" when none is an executable.
func findAgy(home string, isExec func(string) bool) string {
	agyPathsMu.RLock()
	candidates := append([]string(nil), agyPaths...)
	agyPathsMu.RUnlock()
	if p, err := exec.LookPath("agy"); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			candidates = append(candidates, abs)
		}
	}
	candidates = append(candidates, filepath.Join(home, ".local", "bin", "agy"))
	for _, c := range candidates {
		if isExec(c) {
			return c
		}
	}
	return ""
}

// executable reports whether path is an executable regular file; a seam
// for tests.
var executable = func(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir() && st.Mode()&0o111 != 0
}

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

	// 2. The allowlist (prepared before enrollment so any -join request
	// reports the configured agent CLIs).
	if _, err := os.Stat(p.Allowlist); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(p.Stage, 0o700); err != nil {
			fmt.Fprintln(stderr, Program+" install: "+err.Error())
			return 1
		}
		agy := findAgy(home, executable)
		if agy == "" {
			fmt.Fprintf(stderr, "%s install: no agent CLI found (looked for agy on $PATH and in ~/.local/bin); write %s by hand\n", Program, p.Allowlist)
			return 1
		}
		if err := os.MkdirAll(filepath.Dir(p.Allowlist), 0o700); err != nil {
			fmt.Fprintln(stderr, Program+" install: "+err.Error())
			return 1
		}
		if err := writeFileAtomic(p.Allowlist, []byte(defaultAllowlist(p.Stage, *server, agy)), 0o644); err != nil {
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

	// 3. A registered transport's own preparation, done now so enrollment
	// or the first start can use it immediately.
	unit := unitConfig{Bin: p.Bin, Server: *server, Name: *name, IAPAudience: *iapAudience,
		Token: p.Token, Allowlist: p.Allowlist, StateDir: p.StateDir}
	if t := transportFor(u); t != nil && t.Prepare != nil {
		extra, err := t.Prepare(context.Background(), InstallConfig{Server: u, Home: home, StateDir: p.StateDir})
		if err != nil {
			fmt.Fprintf(stderr, "%s install: %s: %v\n", Program, t.Name, err)
			return 1
		}
		unit.Extra = extra
		step("prepared %s for %s", t.Name, u.Host)
	}

	// 4. The credential: enroll with -join (generating an Ed25519 keypair +
	// SAS pairing code handshake) or fall back to a per-host token.
	if err := os.MkdirAll(filepath.Dir(p.Token), 0o700); err != nil {
		fmt.Fprintln(stderr, Program+" install: "+err.Error())
		return 1
	}
	_ = os.Chmod(filepath.Dir(p.Token), 0o700)
	if strings.TrimSpace(*joinCode) != "" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
		defer stop()
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

// readTokenFile is the token path holds, "" when it holds none.
func readTokenFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// defaultAllowlist is a first allowlist: an entry for agy at agyPath,
// confined to the staging root a remote run uses, and an empty
// capabilities section.
func defaultAllowlist(stage, server, agyPath string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s's allowlist: the agent CLIs the grain daemon at\n# %s may start on this machine, as you. %s re-reads it on\n# every request. %s runs agent CLIs only: capabilities: stays empty.\n", Program, server, Program, Program)
	b.WriteString("clis:\n")
	fmt.Fprintf(&b, "  agy:\n    path: %s\n    paths_under: [%s]\n    cwd_under: [%s]\n", agyPath, stage, stage)
	b.WriteString("    env: [HOME, AGY_CLI_DISABLE_AUTO_UPDATE, GEMINI_API_KEY, GOOGLE_API_KEY]\n")
	b.WriteString("    pass_env: [HTTP_PROXY, HTTPS_PROXY, NO_PROXY, http_proxy, https_proxy, no_proxy]\n")
	b.WriteString("capabilities: {}\n")
	return b.String()
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

var unitTemplate = template.Must(template.New("unit").Parse(`[Unit]
Description={{.Program}}: agent CLIs for the grain daemon at {{.Server}}
After=network-online.target

[Service]
ExecStart={{.Command}}
Restart=on-failure
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
