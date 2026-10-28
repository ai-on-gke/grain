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
