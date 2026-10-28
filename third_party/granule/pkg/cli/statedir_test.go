package cli

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/granule/pkg/vmspec"
)

// asRoot / asUser make defaultStateDir take the branch a test is after,
// whichever user the test suite itself happens to run as -- CI runs it
// both as an unprivileged user and (in a container) as root, and neither
// branch is reachable by asking the real euid.
func asRoot(t *testing.T, stateDir string) {
	t.Helper()
	setEUID(t, 0)
	old := rootStateDir
	rootStateDir = stateDir
	t.Cleanup(func() { rootStateDir = old })
}

func asUser(t *testing.T, home string) {
	t.Helper()
	setEUID(t, 1000)
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "")
}

func setEUID(t *testing.T, uid int) {
	t.Helper()
	old := geteuid
	geteuid = func() int { return uid }
	t.Cleanup(func() { geteuid = old })
}

// TestDefaultStateDir covers where each kind of caller is sent when
// -state-dir isn't given: root to the node's shared directory, which is
// what it has always been, and anyone else somewhere they can actually
// create.
func TestDefaultStateDir(t *testing.T) {
	t.Run("root keeps the shared directory", func(t *testing.T) {
		setEUID(t, 0)
		// Set even though root ignores it: a root shell that inherited
		// XDG_STATE_HOME must not scatter a node's VM state into
		// somebody's home directory.
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		if got := defaultStateDir(); got != rootStateDir {
			t.Errorf("defaultStateDir() = %q, want %q", got, rootStateDir)
		}
	})

	t.Run("XDG_STATE_HOME when it is set", func(t *testing.T) {
		setEUID(t, 1000)
		xdg := t.TempDir()
		t.Setenv("HOME", t.TempDir())
		t.Setenv("XDG_STATE_HOME", xdg)
		want := filepath.Join(xdg, "granule", "vms")
		if got := defaultStateDir(); got != want {
			t.Errorf("defaultStateDir() = %q, want %q", got, want)
		}
	})

	t.Run("the home directory when it is not", func(t *testing.T) {
		home := t.TempDir()
		asUser(t, home)
		want := filepath.Join(home, ".local", "state", "granule", "vms")
		if got := defaultStateDir(); got != want {
			t.Errorf("defaultStateDir() = %q, want %q", got, want)
		}
	})

	// The XDG spec says a relative XDG_STATE_HOME is to be ignored, and
	// a relative state directory would put a VM's state wherever the
	// command happened to be run from.
	t.Run("a relative XDG_STATE_HOME is ignored", func(t *testing.T) {
		setEUID(t, 1000)
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("XDG_STATE_HOME", "relative/state")
		want := filepath.Join(home, ".local", "state", "granule", "vms")
		if got := defaultStateDir(); got != want {
			t.Errorf("defaultStateDir() = %q, want %q", got, want)
		}
	})

	// Nothing to follow the caller to: the answer falls back to what it
	// was before it followed anyone, and ensureStateDir is what says it
	// can't be written to.
	t.Run("no home to fall back on", func(t *testing.T) {
		setEUID(t, 1000)
		t.Setenv("HOME", "")
		t.Setenv("XDG_STATE_HOME", "")
		if got := defaultStateDir(); got != rootStateDir {
			t.Errorf("defaultStateDir() = %q, want %q", got, rootStateDir)
		}
	})
}

// TestVMCreate_DefaultStateDirIsWritableByItsCaller is the point of the
// default following the caller: "granule vm create" with no -state-dir
// has to work for someone who isn't root, where it used to fail on a
// /var/lib/granule/vms they cannot create.
func TestVMCreate_DefaultStateDirIsWritableByItsCaller(t *testing.T) {
	withFakeDocker(t)
	home := t.TempDir()
	asUser(t, home)

	if _, stderr, err := runVMArgs(t, "create", "web", "--backend", "docker"); err != nil {
		t.Fatalf("create with no -state-dir: error = %v, stderr = %s", err, stderr)
	}

	stateDir := filepath.Join(home, ".local", "state", "granule", "vms")
	if _, err := vmspec.Load(stateDir, "web"); err != nil {
		t.Fatalf("saved state not found under %s: %v", stateDir, err)
	}

	// And every other subcommand defaults to the same place, or the VM
	// would be one only "vm create" can see.
	out, _, err := runVMArgs(t, "list")
	if err != nil {
		t.Fatalf("list with no -state-dir: %v", err)
	}
	if !strings.Contains(out, "web") {
		t.Errorf("vm list = %q, want the VM just created", out)
	}
	if _, stderr, err := runVMArgs(t, "update", "web", "--cpus", "4"); err != nil {
		t.Fatalf("update with no -state-dir: error = %v, stderr = %s", err, stderr)
	}
	if _, stderr, err := runVMArgs(t, "delete", "web"); err != nil {
		t.Fatalf("delete with no -state-dir: error = %v, stderr = %s", err, stderr)
	}
}

// TestVMCreate_RootDefaultStateDirIsTheSharedOne is the other half: a
// node's VMs are the node's, so root's default has to stay the shared
// directory even when the environment carries an XDG_STATE_HOME (a sudo
// that kept it, or a root login shell that sets one).
func TestVMCreate_RootDefaultStateDirIsTheSharedOne(t *testing.T) {
	withFakeDocker(t)
	shared := filepath.Join(t.TempDir(), "var-lib-kontur-vms")
	xdg := t.TempDir()
	asRoot(t, shared)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", xdg)

	if _, stderr, err := runVMArgs(t, "create", "web", "--backend", "docker"); err != nil {
		t.Fatalf("create with no -state-dir: error = %v, stderr = %s", err, stderr)
	}
	if _, err := vmspec.Load(shared, "web"); err != nil {
		t.Fatalf("saved state not found under %s: %v", shared, err)
	}
	if _, err := os.Stat(filepath.Join(xdg, "granule")); !os.IsNotExist(err) {
		t.Errorf("root's create wrote into %s (stat err = %v), want the shared directory only", xdg, err)
	}
}

// TestStateDirNote is the cost of the default following the caller: a VM
// created without sudo has containers a "sudo granule" can see and a
// spec it cannot, and the plain "not found" reads as "my VM is gone".
func TestStateDirNote(t *testing.T) {
	t.Run("an unprivileged caller is pointed at root's", func(t *testing.T) {
		asUser(t, t.TempDir())
		note := stateDirNote(defaultStateDir())
		if !strings.Contains(note, rootStateDir) || !strings.Contains(note, "sudo") {
			t.Errorf("note = %q, want it to name %s and sudo", note, rootStateDir)
		}
	})

	t.Run("an explicit -state-dir gets no guessing", func(t *testing.T) {
		asUser(t, t.TempDir())
		if note := stateDirNote(filepath.Join(t.TempDir(), "vms")); note != "" {
			t.Errorf("note = %q for an explicit directory, want none", note)
		}
	})

	t.Run("root outside sudo has nobody to point at", func(t *testing.T) {
		asRoot(t, t.TempDir())
		t.Setenv("SUDO_USER", "")
		if note := stateDirNote(defaultStateDir()); note != "" {
			t.Errorf("note = %q, want none when there is no invoking user to name", note)
		}
	})

	t.Run("root under sudo names the invoking user's", func(t *testing.T) {
		asRoot(t, t.TempDir())
		// The user the test process itself runs as: the note is built
		// by looking up whoever SUDO_USER names, and that is the one
		// name a machine running this test is sure to resolve.
		me, err := user.Current()
		if err != nil || me.HomeDir == "" {
			t.Skipf("no current user to look up: %v", err)
		}
		if _, err := user.Lookup(me.Username); err != nil {
			t.Skipf("current user %q is not resolvable by name: %v", me.Username, err)
		}
		t.Setenv("SUDO_USER", me.Username)
		note := stateDirNote(defaultStateDir())
		if !strings.Contains(note, filepath.Join(".local", "state", "granule", "vms")) {
			t.Errorf("note = %q, want it to name the invoking user's state directory", note)
		}
	})
}

// TestVMDelete_MissingSpecExplainsTheOtherStateDir is the sharpest edge
// of the default following the caller: "vm delete" treats a spec it
// cannot find as "already gone", so a "sudo granule vm delete web" on a
// VM created without sudo used to print "deleted VM" and exit 0 while
// saying nothing about the state directory it did not look in.
func TestVMDelete_MissingSpecExplainsTheOtherStateDir(t *testing.T) {
	withFakeDocker(t)

	t.Run("a container that is there makes it a fact", func(t *testing.T) {
		asRoot(t, filepath.Join(t.TempDir(), "var-lib-kontur-vms"))
		me := sudoUser(t)

		out, _, err := runVMArgs(t, "delete", "web")
		if err != nil {
			t.Fatalf("delete of a VM whose spec is elsewhere: %v", err)
		}
		if !strings.Contains(out, "granule-vm-web is running") {
			t.Errorf("delete output = %q, want it to say the VM's container is there", out)
		}
		if !strings.Contains(out, userStateDir(me.HomeDir)) {
			t.Errorf("delete output = %q, want it to name %s as where the spec is", out, userStateDir(me.HomeDir))
		}
		// The containers are gone, the spec in the other directory is
		// not: "deleted VM" would be the same lie one directory along.
		if strings.Contains(out, `deleted VM "web"`) {
			t.Errorf("delete output = %q, want it not to claim the VM was deleted outright", out)
		}
		if !strings.Contains(out, "not its saved state") {
			t.Errorf("delete output = %q, want it to say the saved state is still there", out)
		}
	})

	// Nothing running under that name: the VM really may be gone, so the
	// note is a note and the delete stays the no-op it has always been.
	t.Run("no container leaves it a note", func(t *testing.T) {
		asUser(t, t.TempDir())
		t.Setenv("FAKEDOCKER_MISSING", "granule-vm-web")

		out, _, err := runVMArgs(t, "delete", "web")
		if err != nil {
			t.Fatalf("delete of a VM that is not there: %v", err)
		}
		if !strings.Contains(out, rootStateDir) {
			t.Errorf("delete output = %q, want it to point at %s", out, rootStateDir)
		}
		if strings.Contains(out, "the VM exists") {
			t.Errorf("delete output = %q, want no claim about a container that is not there", out)
		}
		if !strings.Contains(out, `deleted VM "web"`) {
			t.Errorf("delete output = %q, want the usual no-op delete", out)
		}
	})

	// An explicit -state-dir is the caller saying where to look, so
	// there is nothing to guess at and nothing to say.
	t.Run("an explicit -state-dir says nothing", func(t *testing.T) {
		asUser(t, t.TempDir())
		dir := filepath.Join(t.TempDir(), "vms")
		calls := callLog(t)

		out, _, err := runVMArgs(t, "delete", "web", "--state-dir", dir)
		if err != nil {
			t.Fatalf("delete with an explicit -state-dir: %v", err)
		}
		if strings.Contains(out, "no saved state") || strings.Contains(out, rootStateDir) {
			t.Errorf("delete output = %q, want no guessing about other directories", out)
		}
		if !strings.Contains(out, `deleted VM "web"`) {
			t.Errorf("delete output = %q, want the plain no-op delete", out)
		}
		// And the extra "is it there?" inspect is not paid for either.
		for _, c := range calls() {
			if len(c) > 0 && c[0] == "inspect" {
				t.Errorf("delete asked docker %v, want no inspect when there is nothing to explain", c)
			}
		}
	})
}

// sudoUser makes the test process look like a root granule invoked
// through sudo by the user running the tests -- the one name a machine
// running this is sure to resolve back to a home directory.
func sudoUser(t *testing.T) *user.User {
	t.Helper()
	me, err := user.Current()
	if err != nil || me.HomeDir == "" {
		t.Skipf("no current user to look up: %v", err)
	}
	if _, err := user.Lookup(me.Username); err != nil {
		t.Skipf("current user %q is not resolvable by name: %v", me.Username, err)
	}
	t.Setenv("SUDO_USER", me.Username)
	return me
}

// TestVMStatus_NotFoundExplainsTheOtherStateDir checks the note reaches
// the message a caller actually sees, rather than only the helper.
func TestVMStatus_NotFoundExplainsTheOtherStateDir(t *testing.T) {
	asUser(t, t.TempDir())
	_, _, err := runVMArgs(t, "status", "nosuch")
	if err == nil {
		t.Fatal("status for a VM that does not exist = nil error, want one")
	}
	if !strings.Contains(err.Error(), rootStateDir) {
		t.Errorf("error = %v, want it to point at %s as the other place to look", err, rootStateDir)
	}
}
