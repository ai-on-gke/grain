package containerspec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rootfsWithAccounts is a wrapped image's own /etc/passwd and /etc/group
// -- the only place the names in an image's USER mean anything.
func rootfsWithAccounts(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	passwd := "root:x:0:0:root:/root:/bin/sh\n" +
		"# a comment, and a line with too few fields\n" +
		"broken:x:\n" +
		"app:x:1000:2000:the workload:/srv/app:/sbin/nologin\n"
	group := "root:x:0:\nwww-data:x:33:\n"
	if err := os.WriteFile(filepath.Join(dir, "etc", "passwd"), []byte(passwd), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "etc", "group"), []byte(group), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestResolveUser(t *testing.T) {
	rootfs := rootfsWithAccounts(t)
	for _, tc := range []struct {
		user string
		want Credential
	}{
		// No USER at all: nothing is set, and the container runs as what
		// started it.
		{"", Credential{}},
		{"app", Credential{UID: 1000, GID: 2000, Home: "/srv/app", Set: true}},
		{"1000", Credential{UID: 1000, GID: 2000, Home: "/srv/app", Set: true}},
		// A numeric user with no entry of its own is not an error: a
		// distroless image has no passwd file to have one in.
		{"65532", Credential{UID: 65532, Set: true}},
		{"1000:33", Credential{UID: 1000, GID: 33, Home: "/srv/app", Set: true}},
		{"app:www-data", Credential{UID: 1000, GID: 33, Home: "/srv/app", Set: true}},
		{"root", Credential{UID: 0, GID: 0, Home: "/root", Set: true}},
	} {
		got, err := ResolveUser(rootfs, tc.user)
		if err != nil {
			t.Errorf("ResolveUser(%q): %v", tc.user, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ResolveUser(%q) is %+v, want %+v", tc.user, got, tc.want)
		}
	}
}

func TestResolveUserRefusesNamesTheImageDoesNotHave(t *testing.T) {
	rootfs := rootfsWithAccounts(t)
	for _, tc := range []struct{ user, want string }{
		{"nobody", "not in its own /etc/passwd"},
		{"app:nogroup", "not in its own /etc/group"},
		{":1000", "names no user"},
		{"app:", "names no group"},
	} {
		_, err := ResolveUser(rootfs, tc.user)
		if err == nil {
			t.Errorf("ResolveUser(%q) accepted it", tc.user)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("ResolveUser(%q) said %q, want something about %q", tc.user, err, tc.want)
		}
	}
}

// An image with no accounts files at all is ordinary -- distroless images
// have none -- so numbers still resolve and names still fail clearly.
func TestResolveUserWithoutAccountFiles(t *testing.T) {
	rootfs := t.TempDir()
	got, err := ResolveUser(rootfs, "1000:1000")
	if err != nil {
		t.Fatalf("ResolveUser: %v", err)
	}
	if want := (Credential{UID: 1000, GID: 1000, Set: true}); got != want {
		t.Errorf("ResolveUser is %+v, want %+v", got, want)
	}
	if _, err := ResolveUser(rootfs, "app"); err == nil {
		t.Error("a name resolved in an image with no passwd file")
	}
}
