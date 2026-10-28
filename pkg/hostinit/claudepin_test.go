package hostinit

import (
	"os"
	"regexp"
	"testing"
)

// TestClaudeCLIDefaultsMatchDockerfile holds this package's pinned claude
// CLI version and digests to the same values the deployment Dockerfile
// pins -- the same cross-file pinning discipline
// TestEveryBaseImageIsPinnedByDigest (tests/deploy) already holds the
// Dockerfiles themselves to, so the two never quietly drift apart.
func TestClaudeCLIDefaultsMatchDockerfile(t *testing.T) {
	data, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatalf("reading the repository Dockerfile: %v", err)
	}
	text := string(data)

	check := func(argName, want string) {
		re := regexp.MustCompile(`(?m)^ARG ` + argName + `=(\S+)$`)
		m := re.FindStringSubmatch(text)
		if m == nil {
			t.Fatalf("Dockerfile has no ARG %s= line", argName)
		}
		if m[1] != want {
			t.Errorf("Dockerfile's %s=%s does not match hostinit's Default%s=%s -- bump both together",
				argName, m[1], argName, want)
		}
	}
	check("CLAUDE_CLI_VERSION", DefaultClaudeCLIVersion)
	check("CLAUDE_CLI_SHA256_AMD64", DefaultClaudeCLISHA256Amd64)
	check("CLAUDE_CLI_SHA256_ARM64", DefaultClaudeCLISHA256Arm64)
}
