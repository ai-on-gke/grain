package codex

import (
	"errors"
	"testing"

	"github.com/bwsalmon/grain/pkg/agent"
)

func TestModelIncompatibilityFailureClassifiesAccountRefusal(t *testing.T) {
	err := errors.New(`exit status 1 (stderr: The model "gpt-5.1-codex" is not available with your ChatGPT account)`)
	got := modelIncompatibilityFailure("gpt-5.1-codex", "failed prompt", err)
	if got == nil {
		t.Fatal("modelIncompatibilityFailure returned nil")
	}
	if got.Model != "gpt-5.1-codex" || got.Framework != "codex" {
		t.Fatalf("incompatibility = %+v", got)
	}
	if got.Message != "failed prompt exit status 1 (stderr: The model \"gpt-5.1-codex\" is not available with your ChatGPT account)" {
		t.Fatalf("unexpected Message: %q", got.Message)
	}
	if found, ok := agent.ModelIncompatibility(got); !ok || found != got {
		t.Fatal("typed incompatibility was not discoverable")
	}
}

func TestModelIncompatibilityFailureIgnoresOrdinaryFailure(t *testing.T) {
	if got := modelIncompatibilityFailure("gpt-5", "repository test failed", errors.New("exit status 1")); got != nil {
		t.Fatalf("ordinary failure classified as %+v", got)
	}
}
