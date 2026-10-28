package containerspec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArgvIsEntrypointThenCommand(t *testing.T) {
	spec := Spec{Entrypoint: []string{"/entry.sh", "--flag"}, Cmd: []string{"serve", "8080"}}
	if got, want := strings.Join(spec.Argv(), " "), "/entry.sh --flag serve 8080"; got != want {
		t.Errorf("Argv is %q, want %q", got, want)
	}
	if got := (Spec{Cmd: []string{"/bin/sh"}}).Argv(); len(got) != 1 || got[0] != "/bin/sh" {
		t.Errorf("an image with only a command runs %v", got)
	}
}

func TestValidateRefusesAnUnrunnableSpec(t *testing.T) {
	valid := Spec{Rootfs: RootfsDir, Cmd: []string{"/bin/sh"}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("a usable spec was refused: %v", err)
	}

	for _, tc := range []struct {
		name string
		edit func(*Spec)
		want string
	}{
		{"no rootfs", func(s *Spec) { s.Rootfs = "" }, "no rootfs"},
		{"relative rootfs", func(s *Spec) { s.Rootfs = "rootfs" }, "not an absolute path"},
		{"nothing to run", func(s *Spec) { s.Cmd = nil }, "neither an entrypoint nor a command"},
		{"relative workdir", func(s *Spec) { s.WorkingDir = "srv" }, "not an absolute path"},
		{"environment that is not KEY=VALUE", func(s *Spec) { s.Env = []string{"BROKEN"} }, "not KEY=VALUE"},
		{"unknown stop signal", func(s *Spec) { s.StopSignal = "SIGCARROT" }, "unknown stop signal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := valid
			tc.edit(&spec)
			err := spec.Validate()
			if err == nil {
				t.Fatal("Validate accepted it")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestLoadRoundTrips(t *testing.T) {
	spec := Spec{
		Image:      "ghcr.io/example/app:v3",
		Rootfs:     RootfsDir,
		Entrypoint: []string{"/entry.sh"},
		Cmd:        []string{"serve"},
		Env:        []string{"PATH=/usr/bin", "LANG=C"},
		WorkingDir: "/srv",
		User:       "app",
		StopSignal: "SIGQUIT",
		HaltOnExit: true,
	}
	data, err := spec.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	path := filepath.Join(t.TempDir(), "container.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if strings.Join(loaded.Argv(), " ") != "/entry.sh serve" || loaded.User != "app" || !loaded.HaltOnExit {
		t.Errorf("the spec did not survive the round trip: %+v", loaded)
	}
}

// A spec that cannot run is caught when it is read, so the guest says
// what is wrong with the file rather than failing somewhere later.
func TestLoadRefusesAnUnusableFile(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct{ name, content, want string }{
		{"not json", "{", "parsing the container spec"},
		{"nothing to run", `{"rootfs":"/var/lib/granule/container/rootfs"}`, "unusable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name+".json")
			if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if err == nil {
				t.Fatal("Load accepted it")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
	if _, err := Load(filepath.Join(dir, "absent.json")); err == nil {
		t.Error("Load accepted a file that is not there")
	}
}

func TestEnvironmentLookup(t *testing.T) {
	spec := Spec{Env: []string{"PATH=/first", "LANG=C", "PATH=/last"}}
	// The last assignment wins, as it does for a process environment.
	if got, _ := spec.LookupEnv("PATH"); got != "/last" {
		t.Errorf("PATH is %q, want the last assignment", got)
	}
	if _, ok := spec.LookupEnv("ABSENT"); ok {
		t.Error("a variable that is not set was found")
	}
	if got := (Spec{}).Path(); got != DefaultPath {
		t.Errorf("an image with no PATH gets %q, want the runtime default", got)
	}
}

func TestParseSignal(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"", 15},
		{"SIGTERM", 15},
		{"sigquit", 3},
		{"QUIT", 3},
		{"9", 9},
	} {
		got, err := ParseSignal(tc.in)
		if err != nil {
			t.Errorf("ParseSignal(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseSignal(%q) is %d, want %d", tc.in, got, tc.want)
		}
	}
	for _, bad := range []string{"SIGCARROT", "0", "200", "-1"} {
		if _, err := ParseSignal(bad); err == nil {
			t.Errorf("ParseSignal(%q) accepted it", bad)
		}
	}
	if got := (Spec{}).StopSignalName(); got != "SIGTERM" {
		t.Errorf("an image with no stop signal is stopped with %q", got)
	}
}
