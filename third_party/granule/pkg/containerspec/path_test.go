package containerspec

import (
	"os"
	"path/filepath"
	"testing"
)

// imageWithOutside lays out a wrapped filesystem and, beside it, a
// directory standing in for the guest around it -- the thing nothing
// done on the image's behalf may touch.
func imageWithOutside(t *testing.T) (rootfs, outside string) {
	t.Helper()
	dir := t.TempDir()
	rootfs = filepath.Join(dir, "container", "rootfs")
	outside = filepath.Join(dir, "guest-etc")
	for _, d := range []string{rootfs, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return rootfs, outside
}

func assertEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("%s gained %s from something done on the image's behalf", dir, e.Name())
	}
}

func TestMkdirInDoesNotClimbOutOfTheImage(t *testing.T) {
	rootfs, _ := imageWithOutside(t)
	for _, vol := range []string{"../../escape", "/../../../escape", "data/../../../escape"} {
		dir, _, err := MkdirIn(rootfs, vol, 0o755)
		if err != nil {
			t.Fatalf("MkdirIn(%q): %v", vol, err)
		}
		if dir != "/escape" {
			t.Errorf("MkdirIn(%q) resolved to %q, want /escape", vol, dir)
		}
		if _, err := os.Stat(filepath.Join(rootfs, "escape")); err != nil {
			t.Errorf("MkdirIn(%q) did not create the directory inside the image: %v", vol, err)
		}
		for _, escaped := range []string{
			filepath.Join(rootfs, "..", "escape"),
			filepath.Join(rootfs, "..", "..", "escape"),
		} {
			if _, err := os.Stat(escaped); err == nil {
				t.Errorf("MkdirIn(%q) created %s, outside the image", vol, escaped)
			}
		}
	}
}

func TestMkdirInFollowsPlantedSymlinksInsideTheImage(t *testing.T) {
	rootfs, outside := imageWithOutside(t)
	// One absolute link naming the guest's directory by its real path,
	// and one relative link climbing out to it: both mean a directory
	// inside the image once the chroot has happened, and neither may be
	// followed onto the guest from out here.
	if err := os.Symlink(outside, filepath.Join(rootfs, "abs")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../guest-etc", filepath.Join(rootfs, "rel")); err != nil {
		t.Fatal(err)
	}

	dir, created, err := MkdirIn(rootfs, "/abs/data", 0o755)
	if err != nil {
		t.Fatalf("MkdirIn(/abs/data): %v", err)
	}
	if want := filepath.Join(outside, "data"); dir != want || !created {
		t.Errorf("MkdirIn(/abs/data) = %q, %v; want %q, true", dir, created, want)
	}
	if _, err := os.Stat(filepath.Join(rootfs, outside, "data")); err != nil {
		t.Errorf("the directory was not created inside the image: %v", err)
	}

	dir, _, err = MkdirIn(rootfs, "/rel/data", 0o755)
	if err != nil {
		t.Fatalf("MkdirIn(/rel/data): %v", err)
	}
	if dir != "/guest-etc/data" {
		t.Errorf("MkdirIn(/rel/data) = %q, want /guest-etc/data", dir)
	}

	// The link itself, as a volume: the directory to chown is the one
	// it resolves to in the image, never the guest's.
	dir, created, err = MkdirIn(rootfs, "/abs", 0o755)
	if err != nil {
		t.Fatalf("MkdirIn(/abs): %v", err)
	}
	if dir != outside || created {
		t.Errorf("MkdirIn(/abs) = %q, %v; want %q, false", dir, created, outside)
	}
	assertEmpty(t, outside)
}

func TestMkdirInReportsWhatItCreated(t *testing.T) {
	rootfs, _ := imageWithOutside(t)
	if err := os.MkdirAll(filepath.Join(rootfs, "srv"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, created, err := MkdirIn(rootfs, "/srv", 0o755); err != nil || created {
		t.Errorf("MkdirIn(/srv) on an existing directory = %v, %v; want false, nil", created, err)
	}
	if _, created, err := MkdirIn(rootfs, "/srv/new/", 0o755); err != nil || !created {
		t.Errorf("MkdirIn(/srv/new/) = %v, %v; want true, nil", created, err)
	}
	if _, created, err := MkdirIn(rootfs, "/", 0o755); err != nil || created {
		t.Errorf("MkdirIn(/) = %v, %v; want false, nil", created, err)
	}
}

func TestMkdirInRefusesAFile(t *testing.T) {
	rootfs, _ := imageWithOutside(t)
	if err := os.WriteFile(filepath.Join(rootfs, "file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := MkdirIn(rootfs, "/file", 0o755); err == nil {
		t.Error("MkdirIn on a regular file succeeded")
	}
	if _, _, err := MkdirIn(rootfs, "/file/sub", 0o755); err == nil {
		t.Error("MkdirIn below a regular file succeeded")
	}
}

func TestMkdirInStopsASymlinkLoop(t *testing.T) {
	rootfs, _ := imageWithOutside(t)
	if err := os.Symlink("/loop", filepath.Join(rootfs, "loop")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := MkdirIn(rootfs, "/loop/x", 0o755); err == nil {
		t.Error("MkdirIn through a symlink loop succeeded")
	}
}

func TestWriteFileInReplacesASymlinkRatherThanFollowingIt(t *testing.T) {
	rootfs, outside := imageWithOutside(t)
	guestFile := filepath.Join(outside, "shadow")
	if err := os.WriteFile(guestFile, []byte("root:secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(rootfs, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(guestFile, filepath.Join(rootfs, "etc", "resolv.conf")); err != nil {
		t.Fatal(err)
	}

	if err := WriteFileIn(rootfs, "/etc/resolv.conf", []byte("nameserver 10.0.0.1\n"), 0o644); err != nil {
		t.Fatalf("WriteFileIn: %v", err)
	}
	if data, _ := os.ReadFile(guestFile); string(data) != "root:secret\n" {
		t.Errorf("the guest's file was overwritten through the image's symlink: %q", data)
	}
	info, err := os.Lstat(filepath.Join(rootfs, "etc", "resolv.conf"))
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("the image's /etc/resolv.conf is not a regular file now: %v, %v", info, err)
	}
	if data, _ := os.ReadFile(filepath.Join(rootfs, "etc", "resolv.conf")); string(data) != "nameserver 10.0.0.1\n" {
		t.Errorf("the image's /etc/resolv.conf holds %q", data)
	}
}

func TestWriteFileInResolvesAPlantedDirectorySymlinkInsideTheImage(t *testing.T) {
	rootfs, outside := imageWithOutside(t)
	if err := os.Symlink(outside, filepath.Join(rootfs, "etc")); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileIn(rootfs, "/etc/hosts", []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatalf("WriteFileIn: %v", err)
	}
	assertEmpty(t, outside)
	if _, err := os.Stat(filepath.Join(rootfs, outside, "hosts")); err != nil {
		t.Errorf("the file did not land where the chroot will find it: %v", err)
	}
}

func TestResolveUserReadsTheImagesPasswdNotTheGuests(t *testing.T) {
	rootfs, outside := imageWithOutside(t)
	guestPasswd := filepath.Join(outside, "passwd")
	if err := os.WriteFile(guestPasswd, []byte("guestonly:x:4242:4242::/home/guestonly:/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(rootfs, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(guestPasswd, filepath.Join(rootfs, "etc", "passwd")); err != nil {
		t.Fatal(err)
	}
	if cred, err := ResolveUser(rootfs, "guestonly"); err == nil {
		t.Errorf("ResolveUser found %+v in the guest's passwd file through the image's symlink", cred)
	}
}

func TestSplit(t *testing.T) {
	parts := split("/a/b/c/")
	if len(parts) != 3 || parts[0] != "a" || parts[1] != "b" || parts[2] != "c" {
		t.Errorf("split(/a/b/c/) = %v", parts)
	}
}
