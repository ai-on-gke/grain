package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bwsalmon/grain/pkg/daemon"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/secrets"
)

func TestCodexModelListerUsesConfiguredBinaryAndKey(t *testing.T) {
	t.Parallel()
	binary := filepath.Join(t.TempDir(), "codex")
	// Speak the stable protocol, without contacting any provider. Exit
	// unless the same UI-managed key and no-environment policy reached us.
	script := `#!/bin/sh
test "$1" = app-server || exit 1
test "$OPENAI_API_KEY" = test-key || exit 2
test "$CODEX_EXEC_SERVER_URL" = none || exit 3
test -f "$CODEX_HOME/environments.toml" || exit 4
read -r line
echo '{"id":1,"result":{}}'
read -r line
read -r line
echo '{"id":2,"result":{"data":[{"id":"alias","model":"earlier-model","displayName":"Earlier model","defaultReasoningEffort":"high","supportedReasoningEfforts":[{"reasoningEffort":"low"},{"reasoningEffort":"high"}]}],"nextCursor":null}}'
`
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	creds := testSecrets(t)
	if err := creds.Set(secrets.AgentBackendCredentialSecret("account"), secrets.AgentCredentialKey, []byte("test-key")); err != nil {
		t.Fatal(err)
	}
	lister := &daemon.CodexModelLister{Live: testLiveConfig(daemon.Config{
		CodexPath:     binary,
		AgentBackends: []model.AgentBackend{{ID: "account", Framework: model.AgentFrameworkCodex, AuthMode: model.CodexAuthAPIKey}},
	}), Creds: creds}
	got, err := lister.ListModels(context.Background(), "account")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Models) != 1 || got.Models[0].ID != "earlier-model" || got.Models[0].DefaultEffort != "high" || len(got.Models[0].SupportedEfforts) != 2 {
		t.Fatalf("catalog: %+v", got)
	}
}
