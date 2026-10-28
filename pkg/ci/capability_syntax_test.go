package ci

import (
	"strings"
	"testing"
)

// The parser moved to pkg/workflows, which may not import pkg/model, so
// grain's capability grammar reaches it through workflows.Capabilities,
// installed by this package's init. These pin that every grain binary --
// all of which link pkg/ci -- still parses `capabilities:` the way it did
// before the move: a malformed reference is refused at parse, and a
// reference grant with arguments decodes.

func TestParseRefusesAMalformedCapabilityReference(t *testing.T) {
	_, err := Parse(".grain/workflows/a.yml", []byte("on: push\njobs:\n  one:\n    capabilities:\n      - https://example.com/capabilities/gcp/#gcp-key\n    steps:\n      - run: make test\n"))
	if err == nil || !strings.Contains(err.Error(), "capabilities[0]") {
		t.Fatalf("a malformed capability reference parsed: %v", err)
	}
}

func TestParseDecodesACapabilityGrantWithArguments(t *testing.T) {
	w := mustParse(t, ".grain/workflows/a.yml", "on: push\njobs:\n  one:\n    capabilities:\n      - capability: https://example.com/capabilities/gcp#gcp-key\n        with: {account: infra-staging}\n    steps:\n      - run: make test\n")
	got := w.Jobs[0].Capabilities
	if len(got) != 1 || !strings.HasPrefix(got[0], "https://example.com/capabilities/gcp#gcp-key") || !strings.Contains(got[0], "infra-staging") {
		t.Fatalf("capabilities = %q, want the grant with its arguments", got)
	}
}
