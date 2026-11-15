// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cli

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/secrets/kmsfake"
)

const testKMSKey = "projects/example-project/locations/us-central1/keyRings/grain/cryptoKeys/secrets-key"

func TestStateKeyWrapAndUnwrapWithNoDaemonRunning(t *testing.T) {
	kmsfake.Start(t)
	t.Setenv(serverEnvVar, closedServer(t))
	dataDir := t.TempDir()
	t.Setenv(dataDirEnvVar, dataDir)
	store := daemon.OpenSecrets(dataDir)
	if err := store.Set("github", "token", []byte("ghp_example")); err != nil {
		t.Fatal(err)
	}
	pub, _ := store.PublicKey()
	keyFile := daemon.SecretsConfig(dataDir).KeyFile

	if err := stateKeyWrap(context.Background(), dataDir, "wrap", nil, &bytes.Buffer{}); err == nil {
		t.Fatal("wrap with no -kms succeeded")
	}
	var out bytes.Buffer
	if err := stateKeyWrap(context.Background(), dataDir, "wrap", []string{"-kms", testKMSKey}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "wrapped "+keyFile) || !strings.Contains(out.String(), testKMSKey) {
		t.Fatalf("wrap printed %q", out.String())
	}
	if data, _ := os.ReadFile(keyFile); !secrets.IsWrappedKey(string(data)) {
		t.Fatalf("the key file is not wrapped:\n%s", data)
	}
	var status bytes.Buffer
	reportSecretsKey(dataDir, &status)
	if !strings.Contains(status.String(), "wrapped by Cloud KMS key "+testKMSKey) || !strings.Contains(status.String(), pub) {
		t.Fatalf("state status says %q", status.String())
	}
	if v, err := daemon.OpenSecrets(dataDir).Resolve(context.Background(), "github/token"); err != nil || v != "ghp_example" {
		t.Fatalf("Resolve through the wrapped key = %q, %v", v, err)
	}

	out.Reset()
	if err := stateKeyWrap(context.Background(), dataDir, "unwrap", nil, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "plaintext key file again") {
		t.Fatalf("unwrap printed %q", out.String())
	}
	if k, err := secrets.ReadKeyFile(keyFile); err != nil || k.Public() != pub {
		t.Fatalf("after unwrap the key file reads as %v, %v", k.Public(), err)
	}
}
