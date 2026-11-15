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

// `grain-agentd allow <name>`: fetch the controller's allowlist template from
// /hostd/install, resolve <name>'s executable on this machine, show the host
// owner the entry it will add (emitting only keys the target grain-agentd
// binary advertises), and write it once confirmed on the host.

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/version"
)

func allow(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(stderr, Program+" allow: "+err.Error())
		return 2
	}
	p := pathsFor(home)
	installed := readInstalledUnit(filepath.Join(p.UnitDir, UnitName))

	defaultAllowlistPath := p.Allowlist
	if installed.Allowlist != "" {
		defaultAllowlistPath = installed.Allowlist
	}
	defaultTargetBin := p.Bin
	if installed.Bin != "" {
		defaultTargetBin = installed.Bin
	}

	fs := flag.NewFlagSet(Program+" allow", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: %s allow [flags] <name>\n\n", Program)
		fmt.Fprintf(stderr, "Add one agent CLI entry from the controller's allowlist template to this\nhost's allowlist, after showing the entry and confirming on this host.\n\n")
		fs.PrintDefaults()
	}
	server := fs.String("server", installed.Server, "the grain daemon's URL to fetch the allowlist template from (default: read from "+UnitName+")")
	allowlist := fs.String("allowlist", defaultAllowlistPath, "the allowlist file to update")
	binOverride := fs.String("path", "", "use this executable path instead of searching the template's candidate paths")
	targetBin := fs.String("target-bin", defaultTargetBin, "the installed "+Program+" binary whose supported allowlist keys to respect")
	iapAudience := fs.String("iap-audience", installed.IAPAudience, "the OAuth client ID of the IAP in front of the daemon, for an IAP URL")
	yes := fs.Bool("yes", false, "confirm adding the entry without prompting on this host")
	registerTransportFlags(fs)

	flagArgs, posArgs := splitFlagsAndPositional(fs, args)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	posArgs = append(posArgs, fs.Args()...)

	if geteuid() == 0 {
		fmt.Fprintln(stderr, Program+" allow: run this as yourself, not root: "+Program+" runs as the user whose agent CLIs it starts")
		return 2
	}
	if len(posArgs) != 1 || strings.TrimSpace(posArgs[0]) == "" {
		fs.Usage()
		return 2
	}
	cliName := strings.TrimSpace(posArgs[0])

	serverURL := strings.TrimSpace(*server)
	if serverURL == "" {
		if raw := strings.TrimSpace(os.Getenv(hostd.AgentdTemplateEnv)); raw != "" {
			if parsed, parseErr := hostd.ParseAllowlistTemplate([]byte(raw)); parseErr == nil && parsed.Server != "" {
				serverURL = parsed.Server
			}
		}
	}
	u, err := parseServer(serverURL)
	if err != nil {
		fmt.Fprintf(stderr, "%s allow: -server https://DAEMON is required (none passed and no installed unit at %s)\n",
			Program, filepath.Join(p.UnitDir, UnitName))
		return 2
	}
	if *iapAudience != "" && strings.ContainsAny(*iapAudience, " \t\n\"'\\$") {
		fmt.Fprintf(stderr, "%s allow: -iap-audience %q is not a plain value\n", Program, *iapAudience)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	if t := transportFor(u); t != nil && t.Prepare != nil {
		if _, err := t.Prepare(ctx, InstallConfig{Server: u, Home: home, StateDir: p.StateDir}); err != nil {
			fmt.Fprintf(stderr, "%s allow: %s: %v\n", Program, t.Name, err)
			return 1
		}
	}

	tmpl, err := fetchControllerTemplate(ctx, serverURL, *iapAudience)
	if err != nil {
		fmt.Fprintf(stderr, "%s allow: fetching allowlist template from %s: %v\n", Program, serverURL, err)
		return 1
	}
	entry, ok := tmpl.Lookup(cliName)
	if !ok {
		fmt.Fprintf(stderr, "%s allow: controller has no template for agent CLI %q (available: %s)\n",
			Program, cliName, strings.Join(tmpl.Names(), ", "))
		return 1
	}

	var binPath string
	if strings.TrimSpace(*binOverride) != "" {
		cand := strings.TrimSpace(*binOverride)
		if rest, hasTilde := strings.CutPrefix(cand, "~/"); hasTilde {
			cand = filepath.Join(home, rest)
		}
		if abs, absErr := filepath.Abs(cand); absErr == nil {
			cand = filepath.Clean(abs)
		}
		if !executable(cand) {
			fmt.Fprintf(stderr, "%s allow: -path %q is not an executable file\n", Program, *binOverride)
			return 1
		}
		binPath = cand
	} else {
		binPath = hostd.ResolveCandidate(entry.Candidates, home, lookPath, executable)
		if binPath == "" {
			fmt.Fprintf(stderr, "%s allow: %s binary not found on this machine (checked: %s); install it first or pass -path /path/to/%s\n",
				Program, entry.Name, strings.Join(entry.Candidates, ", "), entry.Name)
			return 1
		}
	}

	var existing []byte
	if _, statErr := os.Lstat(*allowlist); statErr == nil {
		if _, loadErr := hostd.LoadAllowlist(*allowlist, owners()); loadErr != nil {
			fmt.Fprintln(stderr, Program+" allow: "+loadErr.Error())
			return 1
		}
		b, readErr := os.ReadFile(*allowlist)
		if readErr != nil {
			fmt.Fprintln(stderr, Program+" allow: "+readErr.Error())
			return 1
		}
		existing = b
	} else if !errors.Is(statErr, os.ErrNotExist) {
		fmt.Fprintln(stderr, Program+" allow: "+statErr.Error())
		return 1
	}

	peer := targetHelloFor(*targetBin)
	updated, snippet, unchanged, err := hostd.UpsertAgentdAllowlistCLI(
		existing, Program, serverURL, home, p.Stage,
		hostd.ResolvedCLI{Entry: entry, Path: binPath}, peer,
	)
	if err != nil {
		fmt.Fprintln(stderr, Program+" allow: "+err.Error())
		return 1
	}
	step := func(format string, a ...any) { fmt.Fprintf(stdout, "==> "+format+"\n", a...) }
	if unchanged {
		step("%s already allows %s (%s)", *allowlist, entry.Name, binPath)
		return 0
	}

	step("entry for %s to write to %s:", entry.Name, *allowlist)
	fmt.Fprint(stdout, indentLines(snippet, "  "))
	if !*yes {
		confirmed, err := confirmOnHost(stdin, stdout, fmt.Sprintf("Allow %s to run %s (%s)? [y/N]: ", Program, entry.Name, binPath))
		if err != nil {
			fmt.Fprintln(stderr, Program+" allow: "+err.Error())
			return 1
		}
		if !confirmed {
			fmt.Fprintf(stderr, "%s allow: cancelled; %s unchanged\n", Program, *allowlist)
			return 1
		}
	}

	if err := os.MkdirAll(p.Stage, 0o700); err != nil {
		fmt.Fprintln(stderr, Program+" allow: "+err.Error())
		return 1
	}
	if err := os.MkdirAll(filepath.Dir(*allowlist), 0o700); err != nil {
		fmt.Fprintln(stderr, Program+" allow: "+err.Error())
		return 1
	}
	if err := writeFileAtomic(*allowlist, updated, 0o644); err != nil {
		fmt.Fprintln(stderr, Program+" allow: writing the allowlist: "+err.Error())
		return 1
	}
	if err := checkAgentsOnly(*allowlist, owners()); err != nil {
		fmt.Fprintln(stderr, Program+" allow: "+err.Error())
		return 1
	}
	step("allowed %s (%s) in %s", entry.Name, binPath, *allowlist)
	return 0
}

func fetchControllerTemplate(ctx context.Context, serverURL, iapAudience string) (hostd.AllowlistTemplate, error) {
	var idTok func(context.Context) (string, error)
	if iapAudience != "" {
		idTok = hostd.NewIAPTokenProvider(iapAudience).Token
	}
	fetched, err := hostd.FetchAllowlistTemplate(ctx, hostd.FetchTemplateOptions{
		ServerURL: serverURL,
		IDToken:   idTok,
		Timeout:   10 * time.Second,
	})
	if err == nil {
		return fetched, nil
	}
	if raw := strings.TrimSpace(os.Getenv(hostd.AgentdTemplateEnv)); raw != "" {
		if parsed, parseErr := hostd.ParseAllowlistTemplate([]byte(raw)); parseErr == nil {
			if parsed.Server == "" || strings.TrimRight(parsed.Server, "/") == strings.TrimRight(serverURL, "/") {
				return parsed, nil
			}
		}
	}
	return hostd.AllowlistTemplate{}, err
}

func confirmOnHost(in io.Reader, out io.Writer, prompt string) (bool, error) {
	fmt.Fprint(out, prompt)
	if in == nil {
		return false, errors.New("confirmation required on this host (run interactively, or pass -yes)")
	}
	r := bufio.NewReader(in)
	line, err := r.ReadString('\n')
	if err != nil && len(line) == 0 {
		if errors.Is(err, io.EOF) {
			return false, errors.New("confirmation required on this host (run interactively, or pass -yes)")
		}
		return false, err
	}
	ans := strings.ToLower(strings.TrimSpace(line))
	return ans == "y" || ans == "yes", nil
}

// targetHelloFor returns the host contract features advertised by targetBin.
// When targetBin is this process's own executable (or does not exist yet), it
// returns hostd.ThisHello without spawning a subprocess; when targetBin is a
// different installed grain-agentd binary, it probes `-check` with an
// `env: ["*"]` allowlist so keys an older host agent rejects are never written.
func targetHelloFor(targetBin string) hostd.Hello {
	targetBin = strings.TrimSpace(targetBin)
	if targetBin == "" || !executable(targetBin) {
		return hostd.ThisHello(version.Get().Release)
	}
	if self, err := os.Executable(); err == nil && sameExecutable(self, targetBin) {
		return hostd.ThisHello(version.Get().Release)
	}
	if probeEnvOpen(targetBin) {
		return hostd.ThisHello("")
	}
	return hostd.Hello{Protocol: hostd.ProtocolVersion}
}

func sameExecutable(a, b string) bool {
	sa, err := os.Stat(a)
	if err != nil {
		return false
	}
	sb, err := os.Stat(b)
	if err != nil {
		return false
	}
	if os.SameFile(sa, sb) {
		return true
	}
	if sa.Size() != sb.Size() {
		return false
	}
	ha, err := fileSHA256(a)
	if err != nil {
		return false
	}
	hb, err := fileSHA256(b)
	return err == nil && ha == hb
}

func fileSHA256(path string) ([32]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return [32]byte{}, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return [32]byte{}, err
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

// probeEnvOpen reports whether bin accepts `env: ["*"]` in `-check`; a seam for tests.
var probeEnvOpen = func(bin string) bool {
	absBin, err := filepath.Abs(bin)
	if err != nil || !filepath.IsAbs(absBin) {
		return false
	}
	absBin = filepath.Clean(absBin)
	tmp, err := os.CreateTemp("", "grain-agentd-probe-*.yaml")
	if err != nil {
		return false
	}
	probePath := tmp.Name()
	defer os.Remove(probePath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return false
	}
	content := fmt.Sprintf("clis:\n  probe:\n    path: %s\n    env: [\"*\"]\n", absBin)
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return false
	}
	if err := tmp.Close(); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, absBin, "-check", "-allowlist", probePath)
	return cmd.Run() == nil
}

type installedUnitInfo struct {
	Bin, Server, Allowlist, IAPAudience string
}

// readInstalledUnit extracts the binary, -server, -allowlist and -iap-audience
// flags from an installed grain-agentd.service unit when present, so
// `grain-agentd allow <name>` works without repeating -server.
func readInstalledUnit(unitPath string) installedUnitInfo {
	data, err := os.ReadFile(unitPath)
	if err != nil {
		return installedUnitInfo{}
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		cmd, ok := strings.CutPrefix(line, "ExecStart=")
		if !ok {
			continue
		}
		fields := strings.Fields(cmd)
		if len(fields) == 0 {
			continue
		}
		info := installedUnitInfo{Bin: fields[0]}
		for i := 1; i < len(fields); i++ {
			switch {
			case fields[i] == "-server" && i+1 < len(fields):
				info.Server = fields[i+1]
				i++
			case strings.HasPrefix(fields[i], "-server="):
				info.Server = strings.TrimPrefix(fields[i], "-server=")
			case fields[i] == "-allowlist" && i+1 < len(fields):
				info.Allowlist = fields[i+1]
				i++
			case strings.HasPrefix(fields[i], "-allowlist="):
				info.Allowlist = strings.TrimPrefix(fields[i], "-allowlist=")
			case fields[i] == "-iap-audience" && i+1 < len(fields):
				info.IAPAudience = fields[i+1]
				i++
			case strings.HasPrefix(fields[i], "-iap-audience="):
				info.IAPAudience = strings.TrimPrefix(fields[i], "-iap-audience=")
			}
		}
		return info
	}
	return installedUnitInfo{}
}

// splitFlagsAndPositional separates flags (and their values) from positional
// arguments so `grain-agentd allow claude -server ...` and
// `grain-agentd allow -server ... claude` both parse cleanly.
func splitFlagsAndPositional(fs *flag.FlagSet, args []string) (flagArgs, posArgs []string) {
	type boolFlag interface{ IsBoolFlag() bool }
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			posArgs = append(posArgs, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			posArgs = append(posArgs, a)
			continue
		}
		flagArgs = append(flagArgs, a)
		trimmed := strings.TrimLeft(a, "-")
		if strings.ContainsRune(trimmed, '=') || trimmed == "h" || trimmed == "help" {
			continue
		}
		f := fs.Lookup(trimmed)
		if f == nil {
			continue
		}
		if bf, ok := f.Value.(boolFlag); ok && bf.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			i++
			flagArgs = append(flagArgs, args[i])
		}
	}
	return flagArgs, posArgs
}
