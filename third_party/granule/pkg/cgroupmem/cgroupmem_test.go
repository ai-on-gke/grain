package cgroupmem

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadV2(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "memory.events"), "low 0\nhigh 0\nmax 12\noom 2\noom_kill 1\noom_group_kill 0\n")
	write(t, filepath.Join(root, "memory.max"), "17716740096\n")
	write(t, filepath.Join(root, "memory.peak"), "17716740096\n")
	s, err := ReadAt(root)
	if err != nil {
		t.Fatal(err)
	}
	if s.OOMKills != 1 || s.LimitBytes != 17716740096 || s.PeakBytes != 17716740096 {
		t.Errorf("ReadAt = %+v", s)
	}
}

func TestReadV2Unlimited(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "memory.events"), "oom_kill 0\n")
	write(t, filepath.Join(root, "memory.max"), "max\n")
	s, err := ReadAt(root)
	if err != nil {
		t.Fatal(err)
	}
	if s != (Stats{}) {
		t.Errorf("ReadAt = %+v, want all zero", s)
	}
}

func TestReadV1(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "memory", "memory.oom_control"), "oom_kill_disable 0\nunder_oom 0\noom_kill 3\n")
	write(t, filepath.Join(root, "memory", "memory.limit_in_bytes"), "9223372036854771712\n")
	s, err := ReadAt(root)
	if err != nil {
		t.Fatal(err)
	}
	if s.OOMKills != 3 || s.LimitBytes != 0 {
		t.Errorf("ReadAt = %+v, want 3 kills and no limit", s)
	}
}

func TestReadNoController(t *testing.T) {
	if _, err := ReadAt(t.TempDir()); err == nil {
		t.Error("ReadAt on an empty directory succeeded")
	}
}

func TestWatch(t *testing.T) {
	root := t.TempDir()
	old := Root
	Root = root
	t.Cleanup(func() { Root = old })

	events := filepath.Join(root, "memory.events")
	write(t, events, "oom_kill 2\n")
	write(t, filepath.Join(root, "memory.max"), "1073741824\n")

	check := Watch()
	if err := check(); err != nil {
		t.Fatalf("no kill yet, got %v", err)
	}
	write(t, events, "oom_kill 3\n")
	err := check()
	if !errors.Is(err, ErrOOMKilled) {
		t.Fatalf("after a kill, got %v", err)
	}
	for _, want := range []string{"VM killed: host memory limit, cgroup OOM", "1024 MiB", "from 2 to 3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q does not say %q", err, want)
		}
	}
}

func TestWatchUnreadable(t *testing.T) {
	old := Root
	Root = t.TempDir()
	t.Cleanup(func() { Root = old })
	if err := Watch()(); err != nil {
		t.Errorf("Watch with no cgroup = %v, want nil", err)
	}
}
