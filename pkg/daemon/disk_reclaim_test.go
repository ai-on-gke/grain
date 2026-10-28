package daemon

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestDockerPrunerRemovesOldGuestTagsUntaggedDigestsAndBuildCache(t *testing.T) {
	const (
		guestRepo     = "ghcr.io/bwsalmon/grain/guest"
		grainRepo     = "ghcr.io/bwsalmon/grain/grain"
		activeSandbox = guestRepo + ":sha-active@sha256:activeguest"
		activeGrain   = grainRepo + ":main"
	)

	var (
		calls   []string
		removed []string
	)
	pruner := &DockerPruner{
		ActiveSandboxImage: activeSandbox,
		ActiveGrainImage:   activeGrain,
		Run: func(_ context.Context, args ...string) (string, error) {
			cmd := strings.Join(args, " ")
			calls = append(calls, cmd)
			switch {
			case len(args) >= 1 && args[0] == "ps":
				return guestRepo + ":sha-running\n" + grainRepo + ":main\n", nil
			case len(args) >= 4 && args[0] == "image" && args[1] == "ls" && args[len(args)-1] == guestRepo:
				return strings.Join([]string{
					guestRepo + ":sha-active " + guestRepo + "@sha256:activeguest sha256:1111",
					guestRepo + ":previous " + guestRepo + "@sha256:prevguest sha256:2222",
					guestRepo + ":sha-running " + guestRepo + "@sha256:runningguest sha256:3333",
					guestRepo + ":sha-old1 " + guestRepo + "@sha256:old1 sha256:4444",
					guestRepo + ":inputs-abc " + guestRepo + "@sha256:old2 sha256:5555",
					guestRepo + ":<none> " + guestRepo + "@sha256:untaggedguest sha256:6666",
					guestRepo + ":<none> " + guestRepo + "@<none> sha256:0000",
				}, "\n"), nil
			case len(args) >= 4 && args[0] == "image" && args[1] == "ls" && args[len(args)-1] == grainRepo:
				return strings.Join([]string{
					grainRepo + ":main " + grainRepo + "@sha256:maingrain sha256:7777",
					grainRepo + ":previous " + grainRepo + "@sha256:prevgrain sha256:8888",
					grainRepo + ":<none> " + grainRepo + "@sha256:oldmaingrain sha256:9999",
				}, "\n"), nil
			case len(args) == 3 && args[0] == "image" && args[1] == "rm":
				removed = append(removed, args[2])
				return "Untagged: " + args[2] + "\n", nil
			case cmd == "image prune -f":
				return "Deleted Images:\nuntagged: sha256:deadbeef\n\nTotal reclaimed space: 4.5GB\n", nil
			case cmd == "builder prune -f":
				return "ID\t\tRECLAIMABLE\tSIZE\t\tLAST ACCESSED\nabc123\t\ttrue\t\t2.5GB\t\t2 days ago\nTotal:\t2.5GB\nTotal reclaimed space: 2.5GB\n", nil
			default:
				return "", nil
			}
		},
	}

	detail, err := pruner.Prune(context.Background())
	if err != nil {
		t.Fatalf("Prune returned unexpected error: %v", err)
	}

	wantRemoved := []string{
		guestRepo + ":sha-old1",
		guestRepo + ":inputs-abc",
		guestRepo + "@sha256:untaggedguest",
		grainRepo + "@sha256:oldmaingrain",
	}
	if strings.Join(removed, "\n") != strings.Join(wantRemoved, "\n") {
		t.Errorf("removed images = %v, want %v", removed, wantRemoved)
	}
	for _, kept := range []string{
		guestRepo + ":sha-active",
		guestRepo + "@sha256:activeguest",
		guestRepo + ":previous",
		guestRepo + ":sha-running",
		grainRepo + ":main",
		grainRepo + ":previous",
	} {
		for _, r := range removed {
			if r == kept {
				t.Errorf(" unexpectedly removed preserved image %q", kept)
			}
		}
	}

	var sawImagePrune, sawBuilderPrune bool
	for _, c := range calls {
		if c == "image prune -f" {
			sawImagePrune = true
		}
		if c == "builder prune -f" {
			sawBuilderPrune = true
		}
	}
	if !sawImagePrune {
		t.Errorf("calls %v did not include 'image prune -f'", calls)
	}
	if !sawBuilderPrune {
		t.Errorf("calls %v did not include 'builder prune -f'", calls)
	}
	if !strings.Contains(detail, "4 unused Docker image(s)") || !strings.Contains(detail, "GiB of dangling Docker images and build cache") {
		t.Errorf("detail = %q, want removed image count and reclaimed GiB", detail)
	}
}

func TestDockerPrunerDegradesGracefullyWhenDockerIsUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
		err  error
	}{
		{
			name: "missing binary",
			err:  exec.ErrNotFound,
		},
		{
			name: "daemon not running",
			out:  "Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?",
			err:  errors.New("exit status 1"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &DockerPruner{
				Run: func(context.Context, ...string) (string, error) {
					return tc.out, tc.err
				},
			}
			detail, err := p.Prune(context.Background())
			if err != nil {
				t.Errorf("Prune returned error %v, want graceful nil", err)
			}
			if detail != "" {
				t.Errorf("Prune returned detail %q, want empty", detail)
			}
		})
	}
}

func TestDiskReclaimSweepRunsDockerPrunerStep(t *testing.T) {
	freeMB := 40 * 1024
	totalMB := 443 * 1024
	var logs []string
	pruner := &DockerPruner{
		ActiveSandboxImage: "ghcr.io/bwsalmon/grain/guest:sha-active",
		ActiveGrainImage:   "ghcr.io/bwsalmon/grain/grain:main",
		Run: func(_ context.Context, args ...string) (string, error) {
			cmd := strings.Join(args, " ")
			switch {
			case args[0] == "ps":
				return "", nil
			case len(args) >= 2 && args[0] == "image" && args[1] == "ls":
				if args[len(args)-1] == "ghcr.io/bwsalmon/grain/guest" {
					return "ghcr.io/bwsalmon/grain/guest:sha-old ghcr.io/bwsalmon/grain/guest@sha256:old sha256:1111\n", nil
				}
				return "", nil
			case cmd == "image prune -f":
				freeMB += 30 * 1024
				return "Total reclaimed space: 30GB\n", nil
			case cmd == "builder prune -f":
				return "Total reclaimed space: 0B\n", nil
			default:
				return "", nil
			}
		},
	}

	reclaim := NewDiskReclaim(nil, pruner)
	reclaim.Read = func() model.DiskSpace {
		return model.DiskSpace{Path: "/var/lib/grain", TotalMB: totalMB, FreeMB: freeMB}
	}
	reclaim.FloorOf = model.DefaultDiskFloor
	reclaim.Log = func(format string, args ...any) {
		logs = append(logs, strings.TrimSpace(
			strings.ReplaceAll(format, "\n", " "),
		))
	}
	reclaim.Now = time.Now

	reclaim.Sweep(context.Background())

	if freeMB != 70*1024 {
		t.Errorf("freeMB after sweep = %d, want %d", freeMB, 70*1024)
	}
}

func TestParseDockerReclaimedBytes(t *testing.T) {
	for _, tc := range []struct {
		out  string
		want int64
	}{
		{"Total reclaimed space: 0B\n", 0},
		{"Total reclaimed space: 500MB\n", 500_000_000},
		{"Total reclaimed space: 2.5GB\n", 2_500_000_000},
		{"Total reclaimed space: 1.5GiB\n", int64(1.5 * float64(1<<30))},
		{"Deleted Images:\nuntagged: foo\n\nTotal reclaimed space: 10.2 kB\n", 10_200},
	} {
		if got := parseDockerReclaimedBytes(tc.out); got != tc.want {
			t.Errorf("parseDockerReclaimedBytes(%q) = %d, want %d", tc.out, got, tc.want)
		}
	}
}

// An empty {{.Digest}} lists as a bare `repo@`, which is no reference
// docker image rm accepts; it is left for `docker image prune` instead.
func TestReclaimRemovableImageSkipsEmptyDigest(t *testing.T) {
	const repo = "ghcr.io/bwsalmon/grain/grain"
	if got := reclaimRemovableImage(repo, []string{repo + ":<none>", repo + "@", "0123456789ab"}); got != "" {
		t.Errorf("reclaimRemovableImage(empty digest) = %q, want \"\"", got)
	}
	if got, want := reclaimRemovableImage(repo, []string{repo + ":<none>", repo + "@sha256:1111"}), repo+"@sha256:1111"; got != want {
		t.Errorf("reclaimRemovableImage = %q, want %q", got, want)
	}
}
