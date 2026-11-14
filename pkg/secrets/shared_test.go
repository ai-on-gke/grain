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

// TestOpenSharedDoesNotShareAStoreWithItsOwnKeySource: the cache is keyed
// by file paths alone, so a Config that names its own KeySource must get
// a Store of its own rather than one loading its key from elsewhere.
func TestOpenSharedDoesNotShareAStoreWithItsOwnKeySource(t *testing.T) {
	s, _, _ := wrappedStore(t)
	cfg := Config{File: s.File(), KeyFile: s.KeyFile()}
	shared := OpenShared(cfg)
	if OpenShared(cfg) != shared {
		t.Fatal("OpenShared did not share the store for the same files")
	}
	cfg.KeySource = FileKeySource{Path: s.KeyFile()}
	if OpenShared(cfg) == shared {
		t.Fatal("a Config with its own KeySource was handed the shared store")
	}
}

// TestShareKeyUnwrapsOnceButAuditsOnlyItsOwnResolutions: the daemon's
// store shares OpenShared's key so a wrapped key is unwrapped once per
// process, but what the readers of the shared store resolve is not the
// daemon's to audit.
func TestShareKeyUnwrapsOnceButAuditsOnlyItsOwnResolutions(t *testing.T) {
	s, fake, _ := wrappedStore(t)
	cfg := Config{File: s.File(), KeyFile: s.KeyFile()}
	before := fake.Decrypts()
	shared := OpenShared(cfg)
	own := shared.ShareKey()
	audit := &mockAuditLogger{}
	own.SetAuditLogger(audit)
	ctx := context.Background()
	for _, store := range []*Store{own, shared, own, shared} {
		if v, err := store.Resolve(ctx, "github/token"); err != nil || v != "ghp_example" {
			t.Fatalf("resolve = %q, %v", v, err)
		}
	}
	if got := fake.Decrypts() - before; got != 1 {
		t.Fatalf("two stores sharing a key made %d KMS decrypts, want 1", got)
	}
	if len(audit.entries) != 2 {
		t.Fatalf("audited %d resolutions, want the 2 made through the store it was set on", len(audit.entries))
	}
	// A drop by one is a drop for both: neither keeps a key the other
	// found stale.
	own.forgetKey()
	if _, err := shared.Resolve(ctx, "github/token"); err != nil {
		t.Fatal(err)
	}
	if got := fake.Decrypts() - before; got != 2 {
		t.Fatalf("after one store dropped the key, %d KMS decrypts in all, want 2", got)
	}
}
