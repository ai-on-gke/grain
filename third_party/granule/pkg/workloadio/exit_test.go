package workloadio

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// report sends r to the collector listening beside vsockSocket, the way
// cloud-hypervisor hands it a guest's connection.
func report(t *testing.T, vsockSocket string, r Report) {
	t.Helper()
	conn, err := net.Dial("unix", ExitSocket(vsockSocket))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := SendReport(conn, r); err != nil {
		t.Fatal(err)
	}
}

func TestExitCollector(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "vsock.sock")
	// A socket left by a previous boot is replaced rather than refused.
	if err := os.WriteFile(ExitSocket(sock), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := ListenExit(sock)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Observed(); got != (Observed{}) {
		t.Fatalf("before any report: %+v", got)
	}

	report(t, sock, Report{Event: EventStarted})
	if got := c.Observed(); got != (Observed{Started: true}) {
		t.Fatalf("after start: %+v", got)
	}
	report(t, sock, Report{Event: EventExited, ExitCode: 3})
	if got := c.Observed(); got != (Observed{Started: true, Exited: true, Code: 3}) {
		t.Fatalf("after exit: %+v", got)
	}
	// A restart clears the earlier run's status.
	report(t, sock, Report{Event: EventStarted})
	if got := c.Observed(); got != (Observed{Started: true}) {
		t.Fatalf("after restart: %+v", got)
	}
	// An event from a newer guest is acknowledged and changes nothing.
	report(t, sock, Report{Event: "paused"})
	report(t, sock, Report{Event: EventExited, ExitCode: 0})

	if got := c.Close(); got != (Observed{Started: true, Exited: true, Code: 0}) {
		t.Fatalf("at close: %+v", got)
	}
	if _, err := os.Stat(ExitSocket(sock)); !os.IsNotExist(err) {
		t.Errorf("socket still there after Close: %v", err)
	}
}

func TestSendReportNeedsAcknowledgement(t *testing.T) {
	host, guest := net.Pipe()
	go func() {
		buf := make([]byte, 512)
		host.Read(buf)
		host.Write([]byte("{\"ok\":false}\n"))
		host.Close()
	}()
	if err := SendReport(guest, Report{Event: EventExited, ExitCode: 1}); err == nil {
		t.Error("a refused report counted as delivered")
	}

	silent, guest2 := net.Pipe()
	go func() {
		buf := make([]byte, 512)
		silent.Read(buf)
		silent.Close()
	}()
	if err := SendReport(guest2, Report{Event: EventExited, ExitCode: 1}); err == nil {
		t.Error("a report nobody acknowledged counted as delivered")
	}
}

func TestObservedStatus(t *testing.T) {
	for _, tc := range []struct {
		name     string
		o        Observed
		wrapped  bool
		want     ExitStatus
		wantOK   bool
		reasonOK bool
	}{
		{name: "exited", o: Observed{Started: true, Exited: true, Code: 137}, want: ExitStatus{ExitCode: 137, Reported: true}, wantOK: true},
		{name: "exited zero, declared", o: Observed{Started: true, Exited: true}, wrapped: true, want: ExitStatus{ExitCode: 0, Reported: true}, wantOK: true},
		{name: "died running", o: Observed{Started: true}, want: ExitStatus{ExitCode: ExitUnreported}, wantOK: true, reasonOK: true},
		{name: "declared, never started", wrapped: true, want: ExitStatus{ExitCode: ExitUnreported}, wantOK: true, reasonOK: true},
		{name: "not a wrapped VM"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := tc.o.Status(tc.wrapped)
			if ok != tc.wantOK || got.ExitCode != tc.want.ExitCode || got.Reported != tc.want.Reported || (got.Reason != "") != tc.reasonOK {
				t.Errorf("Status = %+v, %v; want %+v, %v (reason given: %v)", got, ok, tc.want, tc.wantOK, tc.reasonOK)
			}
		})
	}
}

func TestWriteExitFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "exit")
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := ExitStatus{ExitCode: ExitUnreported, Reason: "gone"}
	if err := WriteExitFile(path, want); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got ExitStatus
	if err := json.Unmarshal(data, &got); err != nil || got != want {
		t.Fatalf("file holds %q (%v), want %+v", data, err, want)
	}
	if !strings.Contains(string(data), `"reported":false`) {
		t.Errorf("file %q does not say the status was unreported", data)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("temporary files left behind: %v", entries)
	}
}
