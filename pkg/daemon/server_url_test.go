package daemon

import "testing"

// daemonServerURL is what a dispatched run's own mcpserver is told to
// reach this daemon at, derived from -ui-addr rather than configured
// separately -- so the interesting cases are the addresses a real
// deployment actually passes, including the ones there is no useful URL
// for at all.
func TestDaemonServerURL(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		uiAddr string
		want   string
	}{
		{"loopback, the default", "127.0.0.1:8420", "http://127.0.0.1:8420"},
		{"every interface", ":8420", "http://127.0.0.1:8420"},
		{"every interface, spelled out", "0.0.0.0:8420", "http://127.0.0.1:8420"},
		{"every interface, v6", "[::]:8420", "http://127.0.0.1:8420"},
		{"a named host", "grain.internal:9000", "http://grain.internal:9000"},
		// No UI server at all, and a port only the listener knows: both
		// leave a run without the tool rather than with a wrong address.
		{"no ui server", "", ""},
		{"an ephemeral port", "127.0.0.1:0", ""},
		{"not an address", "8420", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := daemonServerURL(tt.uiAddr); got != tt.want {
				t.Errorf("daemonServerURL(%q) = %q, want %q", tt.uiAddr, got, tt.want)
			}
		})
	}
}
