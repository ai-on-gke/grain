package sysstat

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadPressureFromAFakeProc(t *testing.T) {
	root := t.TempDir()
	write := func(name, text string) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("vmstat", "nr_free_pages 123\npswpin 4096\npswpout 9000\noom_kill 3\n")
	write("pressure/cpu", "some avg10=12.50 avg60=3.00 avg300=1.00 total=100\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n")
	write("pressure/memory", "some avg10=7.25 avg60=1.00 avg300=0.10 total=55\nfull avg10=2.00 avg60=0.50 avg300=0.05 total=20\n")
	write("pressure/io", "some avg10=1.00 avg60=0.00 avg300=0.00 total=1\nfull avg10=0.50 avg60=0.00 avg300=0.00 total=1\n")
	defer func(was string) { procRoot = was }(procRoot)
	procRoot = root

	p, err := ReadPressure()
	if err != nil {
		t.Fatal(err)
	}
	want := Pressure{CPUSome: 12.5, MemorySome: 7.25, MemoryFull: 2, IOSome: 1, IOFull: 0.5, Known: true, SwapIns: 4096, OOMKills: 3}
	if p != want {
		t.Fatalf("got %+v, want %+v", p, want)
	}

	// A kernel without PSI still reports its counters.
	if err := os.RemoveAll(filepath.Join(root, "pressure")); err != nil {
		t.Fatal(err)
	}
	p, err = ReadPressure()
	if err != nil || p.Known || p.OOMKills != 3 {
		t.Fatalf("without /proc/pressure: %+v, %v", p, err)
	}
}
