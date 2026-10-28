package main

import (
	"strings"
	"testing"
)

func TestServeRequiresBothSecretsInEnvironment(t *testing.T) {
	t.Setenv(webhookSecretEnvKey, "")
	t.Setenv(relaySecretEnvKey, "")
	if err := serve([]string{"-addr", "127.0.0.1:0"}); err == nil || !strings.Contains(err.Error(), webhookSecretEnvKey) {
		t.Fatalf("expected missing %s error, got %v", webhookSecretEnvKey, err)
	}

	t.Setenv(webhookSecretEnvKey, "wh_key")
	if err := serve([]string{"-addr", "127.0.0.1:0"}); err == nil || !strings.Contains(err.Error(), relaySecretEnvKey) {
		t.Fatalf("expected missing %s error, got %v", relaySecretEnvKey, err)
	}
}
