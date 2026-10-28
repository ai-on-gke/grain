// `grain version`: prints the build version information stamped into the binary.
package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/installtrack"
	"github.com/ai-on-gke/grain/pkg/version"
)

type versionOutput struct {
	Release             string   `json:"release,omitempty"`
	Edition             string   `json:"edition,omitempty"`
	Revision            string   `json:"revision,omitempty"`
	CommittedAt         string   `json:"committedAt,omitempty"`
	Modified            bool     `json:"modified,omitempty"`
	Unversioned         bool     `json:"unversioned,omitempty"`
	InstallTrack        string   `json:"installTrack,omitempty"`
	InstallTrackSignals []string `json:"installTrackSignals,omitempty"`
}

func versionCmd(args []string) {
	versionCmdWithProbe(args, installtrack.DefaultProbe())
}

func versionCmdWithProbe(args []string, probe installtrack.Probe) {
	fs := flag.NewFlagSet("grain version", flag.ExitOnError)
	jsonFlag := fs.Bool("json", false, "print version information as JSON")
	trackFlag := fs.Bool("track", false, "print only the detected install track (\"google\" or \"oss\")")
	dataDirFlag := fs.String("data-dir", "", "optional data directory to inspect for install track signals")
	unitFileFlag := fs.String("unit-file", "", "optional systemd unit file to inspect for install track signals")
	if err := fs.Parse(args); err != nil {
		return
	}

	if *unitFileFlag != "" {
		probe.UnitFile = *unitFileFlag
	}
	if *dataDirFlag != "" {
		probe.DataDir = *dataDirFlag
		if _, err := os.Stat(filepath.Join(*dataDirFlag, "grain.db")); err == nil {
			if store, db, err := daemon.OpenStore("sqlite", "", *dataDirFlag); err == nil {
				ctx := context.Background()
				if cfg, err := store.GetConfig(ctx); err == nil {
					probe.Config = cfg
				}
				if repos, err := store.ListRepoConfigs(ctx); err == nil {
					probe.RepoConfigs = repos
				}
				_ = db.Close()
			}
		}
	}

	det := probe.Detect()
	if *trackFlag && !*jsonFlag {
		fmt.Println(det.Track)
		return
	}

	info := version.Get()

	if *jsonFlag {
		out := versionOutput{
			Release:             info.Release,
			Edition:             info.Edition,
			Revision:            info.Revision,
			Modified:            info.Modified,
			Unversioned:         info.Unversioned,
			InstallTrack:        string(det.Track),
			InstallTrackSignals: det.Signals,
		}
		if !info.Time.IsZero() {
			out.CommittedAt = info.Time.UTC().Format(time.RFC3339)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
		return
	}

	fmt.Println(formatVersion(info))
}

func formatVersion(info version.Info) string {
	if info.Revision == "" && info.Release == "" {
		if info.Edition != "" {
			return "dev (" + info.Edition + ", unstamped)"
		}
		return "dev (unstamped)"
	}

	// The release is printed whole, build metadata and all
	// ("v2.3.0-dev.114+abc1234"): the +<sha7> is what says which build of
	// the -dev line this is. One nothing stamped says so, since what Go
	// read off the module in its place is not a version to compare.
	var parts []string
	switch {
	case info.Unversioned && info.Release != "":
		parts = append(parts, info.Release+" (unversioned)")
	case info.Unversioned:
		parts = append(parts, "unversioned")
	case info.Release != "":
		parts = append(parts, info.Release)
	}
	if info.Edition != "" {
		parts = append(parts, info.Edition)
	}
	if info.Revision != "" {
		rev := info.Revision
		if len(rev) > 7 {
			rev = rev[:7]
		}
		if info.Modified {
			rev += "-dirty"
		}
		parts = append(parts, rev)
	}
	if !info.Time.IsZero() {
		parts = append(parts, info.Time.UTC().Format("2006-01-02 15:04Z"))
	}
	return strings.Join(parts, " · ")
}
