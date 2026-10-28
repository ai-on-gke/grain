package containerspec

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
)

func TestMountTarget(t *testing.T) {
	rootfs := t.TempDir()
	mustMkdir(t, filepath.Join(rootfs, "srv"))
	// An image that ships /data as a symlink to the guest's /etc, and one
	// whose /cache is a relative link: both are followed inside rootfs.
	if err := os.Symlink("/etc", filepath.Join(rootfs, "data")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("srv", filepath.Join(rootfs, "cache")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootfs, "file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/", filepath.Join(rootfs, "top")); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		dest, want string
	}{
		{"/srv", "/srv"},
		{"/var/lib/app", "/var/lib/app"},
		{"/data", "/etc"},
		{"/data/conf", "/etc/conf"},
		{"/cache/", "/srv"},
		{"/../../../mnt", "/mnt"},
		{"/srv/../work", "/work"},
	} {
		got, err := MountTarget(rootfs, Mount{Tag: "t", Destination: tc.dest}, 0o755)
		if err != nil {
			t.Errorf("MountTarget(%q) error = %v", tc.dest, err)
			continue
		}
		if want := filepath.Join(rootfs, tc.want); got != want {
			t.Errorf("MountTarget(%q) = %q, want %q", tc.dest, got, want)
		}
		if info, err := os.Lstat(got); err != nil || !info.IsDir() {
			t.Errorf("MountTarget(%q) left no directory at %s: %v", tc.dest, got, err)
		}
	}
	for _, dest := range []string{"/..", "/top", "/srv/.."} {
		if _, err := MountTarget(rootfs, Mount{Tag: "t", Destination: dest}, 0o755); err == nil {
			t.Errorf("MountTarget(%q) mounts on the container's root, want an error", dest)
		}
	}
	if _, err := MountTarget(rootfs, Mount{Tag: "t", Destination: "/file"}, 0o755); !errors.Is(err, syscall.ENOTDIR) {
		t.Errorf("MountTarget(/file) error = %v, want ENOTDIR", err)
	}
}

func TestMountOrder(t *testing.T) {
	in := []Mount{
		{Tag: "c", Destination: "/data/cache"},
		{Tag: "a", Destination: "/data"},
		{Tag: "d", Destination: "/data/cache/x/"},
		{Tag: "b", Destination: "/logs"},
	}
	var got []string
	for _, m := range MountOrder(in) {
		got = append(got, m.Tag)
	}
	if want := []string{"a", "b", "c", "d"}; !reflect.DeepEqual(got, want) {
		t.Errorf("MountOrder = %v, want %v", got, want)
	}
	if in[0].Tag != "c" {
		t.Errorf("MountOrder reordered its argument")
	}
}

func TestOverrideMounts(t *testing.T) {
	o, err := ParseOverride([]byte(`{"mounts":[{"tag":"data","destination":"/data"},{"tag":"conf","destination":"/etc/app","readonly":true}]}`))
	if err != nil {
		t.Fatalf("ParseOverride error = %v", err)
	}
	if o.IsZero() {
		t.Errorf("an override with mounts IsZero")
	}
	want := []Mount{{Tag: "data", Destination: "/data"}, {Tag: "conf", Destination: "/etc/app", ReadOnly: true}}
	if !reflect.DeepEqual(o.Mounts, want) {
		t.Errorf("Mounts = %+v, want %+v", o.Mounts, want)
	}

	base := Spec{Rootfs: RootfsDir, Cmd: []string{"sh"}, Mounts: []Mount{{Tag: "baked", Destination: "/baked"}}}
	s := base.Apply(o)
	if len(s.Mounts) != 3 || s.Mounts[0].Tag != "baked" || s.Mounts[2].Tag != "conf" {
		t.Errorf("Apply Mounts = %+v, want the spec's then the override's", s.Mounts)
	}
	if len(base.Mounts) != 1 {
		t.Errorf("Apply changed the spec it was called on")
	}
	if err := s.Validate(); err != nil {
		t.Errorf("Validate() = %v", err)
	}
	if err := base.Apply(Override{Mounts: []Mount{{Tag: "x", Destination: "/baked/"}}}).Validate(); err == nil {
		t.Errorf("Validate accepted two mounts on /baked")
	}

	for _, bad := range []string{
		`{"mounts":[{"tag":"data","destination":"data"}]}`,
		`{"mounts":[{"destination":"/data"}]}`,
		`{"mounts":[{"tag":"data","destination":"/"}]}`,
		`{"mounts":[{"tag":"a","destination":"/data"},{"tag":"b","destination":"/data/."}]}`,
		`{"mounts":[{"tag":"a","destination":"/data","options":["ro"]}]}`,
	} {
		if _, err := ParseOverride([]byte(bad)); err == nil {
			t.Errorf("ParseOverride(%s) accepted it", bad)
		}
	}
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}
