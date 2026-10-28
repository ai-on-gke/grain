package kubectlgate

import "testing"

// The strings are the ones the failing runs actually reported (task 831's
// runs 831-33 to 831-35), and the konnectivity variants of the same dial.
func TestIsTransientExecStartMatchesFailuresBeforeTheCommandStarted(t *testing.T) {
	for _, c := range []struct {
		stderr string
		want   bool
	}{
		{"error: unable to upgrade connection: error dialing backend: dial timeout, backstop", true},
		{"error: unable to upgrade connection: error dialing backend: dial timeout", true},
		{"error: unable to upgrade connection: error dialing backend: dial tcp 10.128.0.7:10250: connect: connection refused", true},
		{"error: unable to upgrade connection: error dialing backend: EOF", true},
		{"error: unable to upgrade connection: error dialing backend: No agent available", true},
		{"E1026 18:45:02.123456 1 v2.go:105] EOF\nerror: unable to upgrade connection: error dialing backend: dial timeout, backstop", true},
		{"error: Timeout occurred", true},
		{"Unable to connect to the server: dial tcp 136.70.201.174:443: connect: connection timed out", true},
		{"Unable to connect to the server: net/http: TLS handshake timeout", true},
		// The API server answered and refused: the same answer every time.
		{"error: unable to upgrade connection: container not found (\"sandbox\")", false},
		{"error: unable to upgrade connection: pod does not exist", false},
		{"error: unable to upgrade connection: Forbidden", false},
		{`Error from server (NotFound): pods "p" not found`, false},
		{"chmod: cannot access '.gitconfig': No such file or directory", false},
		{"", false},
	} {
		if got := IsTransientExecStart(c.stderr); got != c.want {
			t.Errorf("IsTransientExecStart(%q) = %v, want %v", c.stderr, got, c.want)
		}
	}
}

// What a sandbox's setup reports is the exec's stderr wrapped in the
// errors of everything that called it.
func TestIsTransientTransportReadsTheExecFailureInsideASetupError(t *testing.T) {
	setup := `orchestrator: configuring git credentials for run 831-35: orchestrator: configuring git credentials ` +
		`in granule sandbox pod "grain-granule-831-35": gitcredentials: writing remote .git-credentials and .gitconfig: ` +
		`error: unable to upgrade connection: error dialing backend: dial timeout, backstop`
	if !IsTransientTransport(setup) {
		t.Errorf("IsTransientTransport(%q) = false, want true", setup)
	}
	for _, text := range []string{
		`orchestrator: cloning acme/widgets: fatal: repository not found`,
		`orchestrator: configuring git credentials: error: unable to upgrade connection: container not found ("sandbox")`,
		`no credential named "gcp-key"`,
	} {
		if IsTransientTransport(text) {
			t.Errorf("IsTransientTransport(%q) = true, want false", text)
		}
	}
}
