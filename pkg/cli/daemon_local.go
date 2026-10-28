package cli

import (
	"flag"

	"github.com/bwsalmon/grain/pkg/daemon"
)

// localFlags defines the daemon's direct local-configuration flags, which
// are aliases over settings it already has (daemon.LocalSettings), and
// returns what folds them into the parsed config.
func localFlags(fs *flag.FlagSet) func(daemon.Config, map[string]bool) (daemon.Config, error) {
	var repos stringSliceFlag
	fs.Var(&repos, "repo", "a repository this workstation works on, as a local checkout's path or owner/name or URL; repeatable. "+
		"Each is added to the -policy file's repos, added to the default team's target repositories when "+
		"that list restricts them, and the first is -default-target-repo unless that is set")
	sandboxBackend := fs.String("sandbox-backend", "", "container, granule, kubernetes, kubernetes-granule, host or none: "+
		"the same as -container-sandboxes, -granule-sandboxes, -k8s-sandboxes, -k8s-granule-sandboxes, -host-sandboxes or -disable-native-sandboxes")
	modelBackend := fs.String("model-backend", "", "agy, claude or codex: the same as -agent-framework (agy is antigravity)")
	return func(cfg daemon.Config, passed map[string]bool) (daemon.Config, error) {
		return daemon.ApplyLocalSettings(cfg, daemon.LocalSettings{
			Repos: repos, SandboxBackend: *sandboxBackend, ModelBackend: *modelBackend,
		}, passed)
	}
}
