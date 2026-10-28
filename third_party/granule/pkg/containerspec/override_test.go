package containerspec

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func baked() Spec {
	return Spec{
		Rootfs:     RootfsDir,
		Entrypoint: []string{"/docker-entrypoint.sh"},
		Cmd:        []string{"nginx", "-g", "daemon off;"},
		Env:        []string{"PATH=/usr/bin:/bin", "NGINX_VERSION=1.27", "MODE=prod"},
		WorkingDir: "/srv",
		User:       "nginx",
		HaltOnExit: true,
	}
}

// The merge follows `docker run`: a new entrypoint drops the image's
// command unless a command is given too, a command alone keeps the
// image's entrypoint, environment entries are set over the image's by
// key, and user and working directory replace the image's.
func TestApplyMergesLikeDockerRun(t *testing.T) {
	for _, tc := range []struct {
		name string
		o    Override
		want func(*Spec)
	}{
		{"nothing", Override{}, func(*Spec) {}},
		{"cmd only keeps the entrypoint", Override{Cmd: []string{"nginx", "-t"}}, func(s *Spec) {
			s.Cmd = []string{"nginx", "-t"}
		}},
		{"entrypoint resets cmd", Override{Entrypoint: []string{"/bin/sh"}}, func(s *Spec) {
			s.Entrypoint = []string{"/bin/sh"}
			s.Cmd = nil
		}},
		{"entrypoint and cmd", Override{Entrypoint: []string{"/bin/sh"}, Cmd: []string{"-c", "true"}}, func(s *Spec) {
			s.Entrypoint = []string{"/bin/sh"}
			s.Cmd = []string{"-c", "true"}
		}},
		{"empty entrypoint clears it", Override{Entrypoint: []string{}, Cmd: []string{"/bin/true"}}, func(s *Spec) {
			s.Entrypoint = []string{}
			s.Cmd = []string{"/bin/true"}
		}},
		{"empty cmd clears it", Override{Cmd: []string{}}, func(s *Spec) {
			s.Cmd = []string{}
		}},
		{"env by key", Override{Env: []string{"MODE=dev", "EXTRA=1", "EMPTY="}}, func(s *Spec) {
			s.Env = []string{"PATH=/usr/bin:/bin", "NGINX_VERSION=1.27", "MODE=dev", "EXTRA=1", "EMPTY="}
		}},
		{"user and workdir", Override{User: "1000:1000", WorkingDir: "/tmp"}, func(s *Spec) {
			s.User = "1000:1000"
			s.WorkingDir = "/tmp"
		}},
		{"stdin and tty", Override{Stdin: boolPtr(true), Tty: boolPtr(true)}, func(s *Spec) {
			s.OpenStdin = true
			s.Tty = true
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := baked()
			tc.want(&want)
			if got := baked().Apply(tc.o); !reflect.DeepEqual(got, want) {
				t.Errorf("Apply(%+v) =\n%+v\nwant\n%+v", tc.o, got, want)
			}
		})
	}
}

// The baked spec is a value the caller still holds; merging into it must
// not write through its slices.
func TestApplyLeavesTheBakedSpecAlone(t *testing.T) {
	s := baked()
	_ = s.Apply(Override{Env: []string{"MODE=dev"}, Cmd: []string{"x"}})
	if !reflect.DeepEqual(s, baked()) {
		t.Errorf("Apply changed its receiver: %+v", s)
	}
}

func TestSetEnvEntryCollapsesDuplicates(t *testing.T) {
	got := setEnvEntry([]string{"A=1", "B=2", "A=3"}, "A=new")
	if want := []string{"A=new", "B=2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestParseOverride(t *testing.T) {
	o, err := ParseOverride([]byte(`{"entrypoint":["/bin/sh"],"cmd":null,"env":["A=1"],"user":"web","working_dir":"/app"}`))
	if err != nil {
		t.Fatal(err)
	}
	if o.Cmd != nil || o.Entrypoint[0] != "/bin/sh" || o.User != "web" || o.WorkingDir != "/app" {
		t.Errorf("parsed %+v", o)
	}
	o, err = ParseOverride([]byte(`{"cmd":[]}`))
	if err != nil || o.Cmd == nil || len(o.Cmd) != 0 {
		t.Errorf("an empty cmd must parse as given-and-empty, got %#v, %v", o.Cmd, err)
	}

	for _, tc := range []struct{ in, want string }{
		{`{"workdir":"/app"}`, "unknown field"},
		{`{"working_dir":"app"}`, "absolute"},
		{`{"env":["NOEQUALS"]}`, "KEY=VALUE"},
		{`{"env":["=x"]}`, "KEY=VALUE"},
		{`{} {}`, "trailing"},
		{`not json`, "parsing"},
	} {
		if _, err := ParseOverride([]byte(tc.in)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("ParseOverride(%s) = %v, want an error mentioning %q", tc.in, err, tc.want)
		}
	}
}

// What the host writes the guest reads back, including the distinction
// between a command not given and one given empty.
func TestOverrideDiskRoundTrips(t *testing.T) {
	o := Override{Entrypoint: []string{"/bin/app"}, Cmd: []string{}, Env: []string{"A=b c"}, User: "1:2", WorkingDir: "/w"}
	data, err := EncodeOverrideDisk(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(data)%sectorSize != 0 {
		t.Errorf("the disk is %d bytes, not a whole number of sectors", len(data))
	}
	got, ok, err := DecodeOverrideDisk(bytes.NewReader(data))
	if err != nil || !ok {
		t.Fatalf("DecodeOverrideDisk: ok=%v err=%v", ok, err)
	}
	if !reflect.DeepEqual(got, o) {
		t.Errorf("round trip gave %#v, want %#v", got, o)
	}

	if _, ok, err := DecodeOverrideDisk(bytes.NewReader(make([]byte, 4096))); ok || err != nil {
		t.Errorf("a zeroed disk was taken for an override: ok=%v err=%v", ok, err)
	}
}

func TestOverrideDiskRefusesWhatTheGuestWouldNotRead(t *testing.T) {
	if _, err := EncodeOverrideDisk(Override{Env: []string{"BIG=" + strings.Repeat("x", maxOverrideSize)}}); err == nil {
		t.Error("an override larger than the guest reads was encoded")
	}
}

// fakeBlock lays out /sys/block and /dev the way a guest shows its virtio
// disks: name -> serial ("" for no serial file) and contents.
func fakeBlock(t *testing.T, disks map[string][2]string) (sysBlock, dev string) {
	t.Helper()
	root := t.TempDir()
	sysBlock, dev = filepath.Join(root, "sys"), filepath.Join(root, "dev")
	for name, d := range disks {
		dir := filepath.Join(sysBlock, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if d[0] != "" {
			if err := os.WriteFile(filepath.Join(dir, "serial"), []byte(d[0]+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.MkdirAll(dev, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dev, name), []byte(d[1]), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return sysBlock, dev
}

func overrideDisk(t *testing.T, o Override) string {
	t.Helper()
	data, err := EncodeOverrideDisk(o)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestFindOverrideDevice(t *testing.T) {
	want := Override{Cmd: []string{"serve"}}
	disk := overrideDisk(t, want)

	t.Run("by serial", func(t *testing.T) {
		sys, dev := fakeBlock(t, map[string][2]string{
			"vda": {"", "root filesystem"},
			"vdb": {"data", OverrideMagic + "\n{\"cmd\":[\"decoy\"]}"},
			"vdc": {OverrideSerial, disk},
		})
		got, found, err := FindOverrideDevice(sys, dev)
		if err != nil || !found || !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v found=%v err=%v, want %+v", got, found, err, want)
		}
	})
	t.Run("by magic when there is no serial", func(t *testing.T) {
		sys, dev := fakeBlock(t, map[string][2]string{
			"vda": {"", "root filesystem"},
			"vdb": {"", disk},
			"sda": {"", disk},
		})
		got, found, err := FindOverrideDevice(sys, dev)
		if err != nil || !found || !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v found=%v err=%v, want %+v", got, found, err, want)
		}
	})
	t.Run("none", func(t *testing.T) {
		sys, dev := fakeBlock(t, map[string][2]string{"vda": {"", "root filesystem"}})
		if _, found, err := FindOverrideDevice(sys, dev); found || err != nil {
			t.Errorf("found=%v err=%v on a guest with no override", found, err)
		}
		if _, found, err := FindOverrideDevice(filepath.Join(sys, "missing"), dev); found || err != nil {
			t.Errorf("found=%v err=%v with no /sys/block at all", found, err)
		}
	})
	t.Run("corrupt", func(t *testing.T) {
		sys, dev := fakeBlock(t, map[string][2]string{"vdb": {OverrideSerial, OverrideMagic + "\n{\"working_dir\":\"rel\"}"}})
		if _, found, err := FindOverrideDevice(sys, dev); !found || err == nil {
			t.Errorf("found=%v err=%v, want the bad override reported", found, err)
		}
	})
}

func writeSpec(t *testing.T, path string, s Spec) {
	t.Helper()
	data, err := s.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadEffective(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "container.json")
	writeSpec(t, config, baked())
	effective := filepath.Join(dir, "run", "container.json")
	sys, dev := fakeBlock(t, map[string][2]string{"vdb": {OverrideSerial, overrideDisk(t, Override{User: "web"})}})

	// Before granule-container has run, the override device is merged
	// here too.
	got, err := LoadEffective(config, effective, sys, dev)
	if err != nil {
		t.Fatal(err)
	}
	if got.User != "web" {
		t.Errorf("user = %q, want the override's", got.User)
	}

	// Once it has, what it wrote is what the container is.
	ran := baked()
	ran.Cmd = []string{"what", "ran"}
	if err := os.MkdirAll(filepath.Dir(effective), 0o755); err != nil {
		t.Fatal(err)
	}
	writeSpec(t, effective, ran)
	got, err = LoadEffective(config, effective, sys, dev)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Cmd, ran.Cmd) || got.User != "nginx" {
		t.Errorf("LoadEffective = %+v, want the effective spec as written", got)
	}

	// An override that would leave nothing to run is refused.
	sys, dev = fakeBlock(t, map[string][2]string{"vdb": {OverrideSerial, overrideDisk(t, Override{Entrypoint: []string{}})}})
	if _, err := LoadEffective(config, "", sys, dev); err == nil {
		t.Error("an override clearing the whole command was accepted")
	}
}

func boolPtr(b bool) *bool { return &b }

// Stdin and Tty are switches an override can turn either way, and one
// that only sets them is still an override worth applying.
func TestOverrideSwitchesStdinAndTty(t *testing.T) {
	o, err := ParseOverride([]byte(`{"stdin":true,"tty":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if o.IsZero() {
		t.Fatal("an override setting stdin is not zero")
	}
	s := baked()
	s.Tty = true
	got := s.Apply(o)
	if !got.OpenStdin || got.Tty {
		t.Fatalf("got OpenStdin=%v Tty=%v, want true, false", got.OpenStdin, got.Tty)
	}
	if again := got.Apply(Override{}); !again.OpenStdin {
		t.Fatal("an override that says nothing about stdin turned it off")
	}
}
