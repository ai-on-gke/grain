package daemon

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/whererun"
)

// hostModeUnit is grain-daemon.service as the last host-mode setup.sh
// wrote it, cut to the lines that matter here.
const hostModeUnit = `[Service]
Environment=GRAIN_INSTALL_MODE=host
ExecStartPre=+/usr/local/libexec/grain/extract-binary
ExecStart=/usr/local/bin/grain daemon -upgrade-image ghcr.io/bwsalmon/grain/grain
`

// containerUnit is grain-daemon.service as this release's setup.sh
// writes it.
const containerUnit = `[Service]
ExecStart=/usr/bin/docker run --rm --name grain-daemon ghcr.io/bwsalmon/grain/grain daemon
`

// probe is a process: its environment, its unit file (none for ""),
// and whether it runs on the bare machine.
func probe(env map[string]string, unit string, host bool) hostModeProbe {
	where := whererun.Detection{Host: host, Signal: "no container marker found"}
	if !host {
		where.Signal = "/.dockerenv exists"
	}
	return hostModeProbe{
		Getenv: func(k string) string { return env[k] },
		ReadFile: func(path string) ([]byte, error) {
			if path == hostModeUnitPath && unit != "" {
				return []byte(unit), nil
			}
			return nil, fs.ErrNotExist
		},
		Where: where,
	}
}

var (
	hostEnv     = map[string]string{"INVOCATION_ID": "abc", "GRAIN_INSTALL_MODE": "host"}
	imageConfig = Config{UpgradeImage: "ghcr.io/bwsalmon/grain/grain"}
)

// A host-mode install upgraded onto this build by its Upgrade button, or
// by hand, runs, but takes its next upgrade from the installer (task
// 5041); nothing else is refused.
func TestOnlyALeftoverHostModeInstallRefusesUIUpgrades(t *testing.T) {
	got := hostModeUpgradeRefusal(imageConfig, probe(hostEnv, hostModeUnit, true))
	for _, want := range []string{"host mode", "sudo ./scripts/setup.sh", "container mode plus grain hostd", "expects container mode"} {
		if !strings.Contains(got, want) {
			t.Errorf("refusal %q does not say %q", got, want)
		}
	}
	for name, p := range map[string]hostModeProbe{
		"the release container":              probe(map[string]string{"INVOCATION_ID": "abc"}, containerUnit, false),
		"a development daemon":               probe(map[string]string{"GRAIN_INSTALL_MODE": "host"}, "", true),
		"a development daemon under systemd": probe(map[string]string{"INVOCATION_ID": "abc"}, "", true),
	} {
		if got := hostModeUpgradeRefusal(imageConfig, p); got != "" {
			t.Errorf("%s refuses upgrades: %q", name, got)
		}
	}
}

func TestHostModeInstallIsToldApartFromAContainerAndADevelopmentDaemon(t *testing.T) {
	for _, c := range []struct {
		name string
		cfg  Config
		p    hostModeProbe
		want bool
	}{
		{"a host-mode unit", imageConfig, probe(hostEnv, hostModeUnit, true), true},
		{"a host-mode unit from before GRAIN_INSTALL_MODE", imageConfig,
			probe(map[string]string{"INVOCATION_ID": "abc"}, "[Service]\nExecStart=/usr/local/bin/grain daemon\n", true), true},
		{"a host-mode environment with its unit unreadable", imageConfig, probe(hostEnv, "", true), true},
		{"the release container plus hostd", imageConfig, probe(map[string]string{"INVOCATION_ID": "abc"}, containerUnit, false), false},
		{"the release container, its env claiming host", imageConfig, probe(hostEnv, containerUnit, false), false},
		{"a development daemon (no -upgrade-image)", Config{}, probe(hostEnv, hostModeUnit, true), false},
		{"a development daemon with no unit", imageConfig, probe(nil, "", true), false},
		{"a development daemon beside a container unit", imageConfig, probe(nil, containerUnit, true), false},
		{"grain init's unit, which builds from a checkout", Config{UpgradeSrcDir: "/src/grain"}, probe(hostEnv, hostModeUnit, true), false},
		{"Kubernetes", Config{UpgradeImage: "x", K8sSandboxes: true}, probe(hostEnv, hostModeUnit, true), false},
	} {
		got, signals := hostModeInstall(c.cfg, c.p)
		if got != c.want {
			t.Errorf("%s: hostModeInstall = %v (%v), want %v", c.name, got, signals, c.want)
		}
		if got && len(signals) < 2 {
			t.Errorf("%s: signals %v say too little", c.name, signals)
		}
	}
}

func TestTheNoticeNamesTheCLIsThatLostTheirRunLocation(t *testing.T) {
	runs := []CLIRunResolution{
		{Framework: model.AgentFrameworkClaude, Location: model.CLIRunHost},
		{Framework: model.AgentFrameworkCodex, Location: model.CLIRunHost, HostPath: "/usr/local/bin/codex"},
		{Framework: model.AgentFrameworkAntigravity, Location: model.CLIRunHost, HostPath: "/usr/bin/agy", Err: "it broke"},
	}
	n := hostModeInstallNotice(imageConfig, probe(hostEnv, hostModeUnit, true), runs)
	if n == nil {
		t.Fatal("no notice for a host-mode install")
	}
	if !strings.Contains(n.Message, "re-run scripts/setup.sh") || !strings.Contains(n.Message, "v2.3.0") {
		t.Errorf("message %q", n.Message)
	}
	if n.DocsURL == "" || n.Command == "" || n.Kind != "host-mode" {
		t.Errorf("notice %+v lacks its docs link, command or kind", n)
	}
	got := strings.Join(n.AffectedCLIs, "\n")
	if !strings.Contains(got, "claude: no host binary, and no container location on a bare-machine daemon") {
		t.Errorf("affected %q does not name claude", got)
	}
	if !strings.Contains(got, "agy: it broke") {
		t.Errorf("affected %q does not name agy's error", got)
	}
	if strings.Contains(got, "codex") {
		t.Errorf("affected %q names codex, which has a host binary", got)
	}

	if n := hostModeInstallNotice(imageConfig, probe(nil, containerUnit, false), runs); n != nil {
		t.Errorf("the release container got a notice: %+v", n)
	}
}

func TestUnitExecStartBinary(t *testing.T) {
	for unit, want := range map[string]string{
		hostModeUnit:  "grain",
		containerUnit: "docker",
		"[Service]\nExecStart=-/usr/bin/podman run x\n":      "podman",
		"[Service]\nExecStart=\"/opt/grain/grain\" daemon\n": "grain",
		"[Service]\nType=simple\n":                           "",
	} {
		if got := unitExecStartBinary(unit); got != want {
			t.Errorf("unitExecStartBinary(%q) = %q, want %q", unit, got, want)
		}
	}
}
