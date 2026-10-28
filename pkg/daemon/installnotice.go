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

package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/ai-on-gke/grain/pkg/whererun"
)

// A host-mode install -- the daemon as a plain systemd service on the
// machine, which an installer before v2.3.0 could write -- keeps running
// after an upgrade through the UI or by hand: the old unit's own
// ExecStartPre takes the new binary out of the new image and starts it on
// the bare machine. Only a re-run of scripts/setup.sh migrates it to
// container mode plus grain hostd. Until then it is a daemon on the bare
// machine (whererun), which is offered only the host run location for
// every CLI (clirun.go), and since v2.3.0 there is no per-run docker
// launcher either, so a CLI with no binary on the machine cannot run at
// all. Nothing said so (an operator's workstation, 2026-10-23); this file is
// what says so, at start, in the log, on GET /api/config and in the
// Upgrade pane (hostModeUpgradeRefusal).

// hostModeUnitPath is where every installer wrote grain-daemon.service.
var hostModeUnitPath = "/etc/systemd/system/grain-daemon.service"

// hostModeDocsURL is the migration's own section of the design doc.
const hostModeDocsURL = "https://github.com/ai-on-gke/grain/blob/main/docs/design/cli-run-location.md#migrating-a-host-mode-install"

// hostModeMigrateCommand is what migrates the install.
const hostModeMigrateCommand = "sudo ./scripts/setup.sh (or ./google/scripts/cloudtop-install.sh on a Cloudtop)"

// hostModeProbe is what hostModeInstall reads, so a test can describe a
// process without being one.
type hostModeProbe struct {
	Getenv   func(string) string
	ReadFile func(string) ([]byte, error)
	// Where is whererun's answer for the process.
	Where whererun.Detection
}

func systemHostModeProbe() hostModeProbe {
	return hostModeProbe{Getenv: os.Getenv, ReadFile: os.ReadFile, Where: whererun.Detect()}
}

// hostModeInstall reports whether this daemon is an unmigrated host-mode
// install, and the evidence for it. All of these must hold:
//
//   - it runs on the bare machine (whererun), not in Kubernetes;
//   - it upgrades by pulling release images (-upgrade-image), which the
//     host-mode unit passed and a development daemon -- `grain demo`, a
//     local `grain daemon`, the e2e harness -- or `grain init`'s unit,
//     which builds from a checkout (pkg/hostinit), does not;
//   - and its unit says host mode: systemd started it
//     (INVOCATION_ID) with GRAIN_INSTALL_MODE=host, or
//     grain-daemon.service's ExecStart runs the binary rather than
//     `docker run` / `podman run`, the same reading setup.sh's
//     installed_install_mode makes.
func hostModeInstall(cfg Config, p hostModeProbe) (bool, []string) {
	if !p.Where.Host || KubernetesMode(cfg) || cfg.UpgradeImage == "" {
		return false, nil
	}
	var signals []string
	if p.Getenv("INVOCATION_ID") != "" && p.Getenv("GRAIN_INSTALL_MODE") == "host" {
		signals = append(signals, "started by systemd with GRAIN_INSTALL_MODE=host")
	}
	if p.ReadFile != nil {
		if data, err := p.ReadFile(hostModeUnitPath); err == nil {
			if bin := unitExecStartBinary(string(data)); bin != "" && bin != "docker" && bin != "podman" {
				signals = append(signals, fmt.Sprintf("%s runs %s directly, not in a container", filepath.Base(hostModeUnitPath), bin))
			}
		}
	}
	if len(signals) == 0 {
		return false, nil
	}
	return true, append([]string{"the daemon runs on the bare machine (" + p.Where.Signal + ")"}, signals...)
}

// unitExecStartBinary is the base name of the program a unit's ExecStart
// runs, "" for no ExecStart.
func unitExecStartBinary(unit string) string {
	bin := ""
	for _, line := range strings.Split(unit, "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, "ExecStart=")
		if !ok {
			continue
		}
		rest = strings.TrimLeft(rest, "-@:+!")
		if f := strings.Fields(rest); len(f) > 0 {
			bin = filepath.Base(strings.Trim(f[0], `"'`))
		}
	}
	return bin
}

// hostModeMessage is the notice's sentence, the same in the log, the
// banner and the Upgrade pane.
const hostModeMessage = "this install is still host mode, which v2.3.0 no longer supports: re-run scripts/setup.sh " +
	"to migrate it to container mode plus grain hostd (keeps the data directory and unit name)"

// hostModeInstallNotice is the notice for a host-mode install, nil for
// any other. Affected names the CLIs that cannot run because of it.
func hostModeInstallNotice(cfg Config, p hostModeProbe, runs []CLIRunResolution) *ui.InstallNotice {
	ok, signals := hostModeInstall(cfg, p)
	if !ok {
		return nil
	}
	return HostModeNotice(signals, runs)
}

// HostModeNotice is the notice a host-mode install gets, seen through
// signals, with runs the CLIs' resolutions on it. `grain demo
// -host-mode-install` shows it without being one.
func HostModeNotice(signals []string, runs []CLIRunResolution) *ui.InstallNotice {
	return &ui.InstallNotice{
		Kind:         "host-mode",
		Message:      hostModeMessage,
		Command:      hostModeMigrateCommand,
		Signals:      signals,
		AffectedCLIs: hostModeAffectedCLIs(runs),
		DocsURL:      hostModeDocsURL,
	}
}

// hostModeAffectedCLIs are the CLIs that lost their run location on a
// bare-machine daemon: the only location it offers is host, so a CLI with
// no binary on the machine has nowhere to run.
func hostModeAffectedCLIs(runs []CLIRunResolution) []string {
	var out []string
	for _, r := range runs {
		name := cliBinaryName(r.Framework)
		switch {
		case r.Err != "":
			out = append(out, fmt.Sprintf("%s: %s", name, r.Err))
		case r.Location == model.CLIRunHost && r.HostPath == "":
			out = append(out, name+": no host binary, and no container location on a bare-machine daemon")
		}
	}
	return out
}

// installNotice is the notice run() found at start, for GET /api/config.
var installNotice atomic.Pointer[ui.InstallNotice]

// InstallNotice is what GET /api/config reports as installNotice: nil
// when there is nothing to say.
func InstallNotice() *ui.InstallNotice { return installNotice.Load() }

// checkInstallMode finds out whether this daemon is an unmigrated
// host-mode install, logs the warning once if so, and keeps the notice
// for the UI.
func checkInstallMode(ctx context.Context, cfg Config) {
	n := hostModeInstallNotice(cfg, systemHostModeProbe(), cfg.ResolveCLIRuns(ctx))
	installNotice.Store(n)
	if n == nil {
		return
	}
	log.Printf("level=warning condition=host_mode_install grain daemon: %s -- run %s. Signals: %s", n.Message, n.Command, strings.Join(n.Signals, "; "))
	for _, a := range n.AffectedCLIs {
		log.Printf("level=warning condition=host_mode_install grain daemon: agent CLI %s", a)
	}
}
