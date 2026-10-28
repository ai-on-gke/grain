package daemon

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// sessionStoresOffClaim names each agent session store this daemon keeps
// that a Kubernetes pod restart would lose, with why; nil when every one
// of them is on storage that outlives the pod, or the backend is not
// Kubernetes.
//
// A durable run that is not adopted across a restart is a restart
// suspension, resumed on the new daemon's first cycle into the
// conversation it was in (docs/design/durable-tasks.md#DT-1, case 2), and
// that conversation is only there if the session store survived the
// restart. On Kubernetes that means the data claim -- -data-dir, which
// both shipped manifests (deploy/kubernetes/grain.yaml,
// grain-granule.yaml) mount from the grain-data claim -- or, for a run
// pod, one of its -k8s-run-host-volume claims. The pod's own filesystem
// is gone with the pod. The stores are:
//
//   - agy's and codex's per-task homes (pkg/agent/sessionstore), under
//     -data-dir by construction (agentSessionsDir);
//   - claude's ~/.claude for a run that is the daemon's child: $HOME,
//     or $CLAUDE_CONFIG_DIR, which the manifests put at /data/home;
//   - claude's ~/.claude in a run pod: -run-host-credential-dir, which
//     is the run pod's HOME (newKubeRunHostDriver).
func sessionStoresOffClaim(cfg Config, getenv func(string) string) []string {
	if !cfg.K8sSandboxes && !cfg.K8sGranuleSandboxes {
		return nil
	}
	var out []string
	data := cfg.DataDir
	if dir := agentSessionsDir(data); dir == "" {
		out = append(out, "agy's and codex's session stores: there is no -data-dir to keep them in, so every run gets a temporary home")
	}
	claudeDir := getenv("CLAUDE_CONFIG_DIR")
	if claudeDir == "" {
		if home := getenv("HOME"); home != "" {
			claudeDir = filepath.Join(home, ".claude")
		}
	}
	if cfg.RunHost == nil && cfg.RunHostImage == "" && data != "" && !pathUnder(claudeDir, data) {
		out = append(out, fmt.Sprintf("claude's session store %s is not under -data-dir %s: set HOME under it, as the shipped manifests do (HOME=/data/home)",
			orUnset(claudeDir), data))
	}
	if cfg.RunHostImage != "" && cfg.K8sSandboxes {
		home := cfg.RunHostCredentialDir
		if home == "" {
			home = getenv("HOME")
		}
		var mounts []string
		on := false
		for _, v := range cfg.K8sRunHostVolumes {
			if _, path, ok := strings.Cut(v, ":"); ok {
				mounts = append(mounts, path)
				on = on || pathUnder(home, path)
			}
		}
		if !on {
			out = append(out, fmt.Sprintf("a run pod's claude session store (its HOME, %s) is on none of its -k8s-run-host-volume claims (%s)",
				orUnset(home), orUnset(strings.Join(mounts, ", "))))
		}
	}
	return out
}

// warnSessionStoresOffClaim logs sessionStoresOffClaim's findings once at
// startup: a deployment so configured still runs, but a durable run it
// does not adopt across a restart starts its conversation over.
func warnSessionStoresOffClaim(cfg Config) {
	for _, why := range sessionStoresOffClaim(cfg, os.Getenv) {
		log.Printf("grain daemon: warning: %s -- a durable run this daemon does not adopt across a restart "+
			"will start its conversation over instead of resuming it (docs/design/durable-tasks.md#DT-1)", why)
	}
}

// pathUnder reports whether path is root or inside it.
func pathUnder(path, root string) bool {
	if path == "" || root == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

func orUnset(s string) string {
	if s == "" {
		return "unset"
	}
	return s
}
