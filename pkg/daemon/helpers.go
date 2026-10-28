package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model/postgres"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// DefaultSandboxImage is overwritten at link time. The value here is the
// fallback for a build that does not stamp one -- a plain `make build`
// on a laptop, or a `docker build` with no --build-arg -- and names the
// tag CI keeps pointed at main rather than nothing at all, so an
// unstamped binary still resolves to a real, if less precise, image.
var DefaultSandboxImage = "ghcr.io/bwsalmon/grain/guest:latest"

// DefaultK8sSandboxImage is the default container image for plain Kubernetes
// sandbox pods ("kubernetes" backend).
var DefaultK8sSandboxImage = "ghcr.io/bwsalmon/grain/k8s-sandbox:latest"

// timestampSuffix names an archived directory by when it was archived,
// in a form that sorts and that a shell does not have to be quoted for.
func timestampSuffix() string { return time.Now().UTC().Format("20060102-150405") }

// sortedKeys keeps a run's output and its order of writes fixed: a map
// is the right shape for "each repo once, each prefix once", and Go's
// own iteration order would otherwise make two syncs of one unchanged
// file print their lines in different orders.
func SortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func ReadTokenFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading token file: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("token file %s is empty", path)
	}
	return token, nil
}

// actorID resolves who a local, non-REST command (currently just "demo")
// acts as: the -as flag, else the OS user, else ui.DefaultActor's own
// fallback. The task CLI itself no longer has a notion of this: attribution
// is the daemon's own deployment-wide setting now (pkg/ui's HTTPClient.Config
// doc comment), not something a REST caller can override per request.
func ActorID(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return ""
}

// loadDatabaseConfig reads <dataDir>/db-config.json with its password,
// which lives in the secret store rather than the file, put back into
// the DSN. A file an older build wrote with the password still in it
// has the password moved into the secret store first; if that fails the
// file is used as it stands, and the log says it still holds one.
func LoadDatabaseConfig(dataDir string) (*postgres.DatabaseConfig, error) {
	saved, err := postgres.ReadDatabaseConfig(dataDir)
	if err != nil || saved == nil {
		return saved, err
	}
	if saved.HasPlaintextPassword() {
		if _, err := postgres.MigrateDatabaseConfigPassword(dataDir, OpenSecrets(dataDir)); err != nil {
			log.Printf("grain: %s still holds the database password in clear text; moving it into the secret store failed: %v",
				filepath.Join(dataDir, postgres.DatabaseConfigFile), err)
			return saved, nil
		}
		log.Printf("grain: moved the database password out of %s into the secret store (secret %q)",
			filepath.Join(dataDir, postgres.DatabaseConfigFile), postgres.DatabasePasswordSecret)
	} else if saved.PasswordSecret == "" {
		return saved, nil
	}
	return postgres.LoadDatabaseConfig(context.Background(), dataDir, OpenSecrets(dataDir))
}

// localSandboxes, when set, builds the sandbox backend for a daemon whose
// flags select none: orchestrator.HostSandboxes, one plain directory per
// run under -sandbox-dir with no isolation at all.
//
// It is nil in every build a deployment runs, which selects host
// sandboxes by name (-host-sandboxes) instead, behind CheckHostSandboxes'
// guard. This seam is the test-only way to reach the same backend without
// any flag -- set by this package's own tests (local_sandboxes_test.go)
// and by the browser e2e build (local_sandboxes_e2e.go, behind the
// grain_e2e build tag).
var LocalSandboxes func(dir string) (orchestrator.Sandboxes, error)
