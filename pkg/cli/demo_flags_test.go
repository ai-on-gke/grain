package cli

import (
	"strings"
	"testing"
)

// -external-config-repo is what puts a demo in external config mode,
// with -external-config-branch naming its branch; the branch on its own
// means nothing and is refused rather than silently ignored.
func TestParseDemoFlagsExternalConfigRepo(t *testing.T) {
	opts, err := parseDemoFlags([]string{"-external-config-repo", "https://github.com/acme/grain-config.git", "-external-config-branch", "prod"})
	if err != nil {
		t.Fatalf("parseDemoFlags: %v", err)
	}
	if opts.ExternalConfigRepo != "https://github.com/acme/grain-config.git" || opts.ExternalConfigBranch != "prod" {
		t.Errorf("ExternalConfigRepo, ExternalConfigBranch = %q, %q", opts.ExternalConfigRepo, opts.ExternalConfigBranch)
	}

	opts, err = parseDemoFlags(nil)
	if err != nil {
		t.Fatalf("parseDemoFlags: %v", err)
	}
	if opts.ExternalConfigRepo != "" {
		t.Errorf("a plain demo has ExternalConfigRepo %q, want none", opts.ExternalConfigRepo)
	}

	if _, err := parseDemoFlags([]string{"-external-config-branch", "prod"}); err == nil || !strings.Contains(err.Error(), "-external-config-repo") {
		t.Errorf("-external-config-branch alone: err = %v, want one naming -external-config-repo", err)
	}
}
