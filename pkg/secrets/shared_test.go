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

package secrets

import (
	"context"
	"testing"
)

// TestOpenSharedUnwrapsAKMSWrappedKeyOncePerProcess: readers that open
// the store per call (a repository token on every sync) must not cost a
// Cloud KMS decrypt each time.
func TestOpenSharedUnwrapsAKMSWrappedKeyOncePerProcess(t *testing.T) {
	s, fake, _ := wrappedStore(t)
	cfg := Config{File: s.File(), KeyFile: s.KeyFile()}
	before := fake.Decrypts()
	for i := 0; i < 5; i++ {
		v, err := OpenShared(cfg).Resolve(context.Background(), "github/token")
		if err != nil || v != "ghp_example" {
			t.Fatalf("resolve %d = %q, %v", i, v, err)
		}
	}
	if got := fake.Decrypts() - before; got != 1 {
		t.Fatalf("5 OpenShared reads made %d KMS decrypts, want 1", got)
	}
}

// TestOpenSharedDoesNotCacheAStoreKMSRefused: a store opened while KMS
// refuses is opened afresh next time, so it recovers once KMS answers.
func TestOpenSharedDoesNotCacheAStoreKMSRefused(t *testing.T) {
	s, fake, _ := wrappedStore(t)
	cfg := Config{File: s.File(), KeyFile: s.KeyFile()}
	fake.Deny(true)
	if _, err := OpenShared(cfg).Resolve(context.Background(), "github/token"); err == nil {
		t.Fatal("resolved through a KMS that refuses")
	}
	fake.Deny(false)
	if v, err := OpenShared(cfg).Resolve(context.Background(), "github/token"); err != nil || v != "ghp_example" {
		t.Fatalf("after KMS answers again = %q, %v", v, err)
	}
}
