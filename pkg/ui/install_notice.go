package ui

// InstallNotice is something about how this deployment is installed that
// its operator has to fix, on GET /api/config as installNotice: today an
// install still in host mode, which v2.3.0 removed (pkg/daemon's
// installnotice.go).
type InstallNotice struct {
	// Kind names the case: "host-mode".
	Kind string `json:"kind"`
	// Message is the notice itself, the sentence the daemon logs.
	Message string `json:"message"`
	// Command is what fixes it.
	Command string `json:"command,omitempty"`
	// Signals are what the daemon saw that says so.
	Signals []string `json:"signals,omitempty"`
	// AffectedCLIs are the agent CLIs that cannot run because of it, one
	// line each: "claude: no host binary, ...".
	AffectedCLIs []string `json:"affectedClis,omitempty"`
	// DocsURL is where the fix is written up.
	DocsURL string `json:"docsUrl,omitempty"`
}
