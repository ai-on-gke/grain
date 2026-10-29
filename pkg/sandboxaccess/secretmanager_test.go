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

package sandboxaccess

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fakeSecretManager serves the two Secret Manager calls the store makes
// for one secret.
func fakeSecretManager(t *testing.T, secret string) (*httptest.Server, *[]string) {
	t.Helper()
	var versions []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/"+secret+"/versions/latest:access":
			if len(versions) == 0 {
				http.Error(w, `{"error":"NOT_FOUND"}`, http.StatusNotFound)
				return
			}
			var p secretPayload
			p.Payload.Data = versions[len(versions)-1]
			_ = json.NewEncoder(w).Encode(p)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/"+secret+":addVersion":
			var p secretPayload
			if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			versions = append(versions, p.Payload.Data)
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &versions
}

func TestLoadOrCreateCAThroughSecretManager(t *testing.T) {
	const secret = "projects/p/secrets/grain-sandbox-ca"
	srv, versions := fakeSecretManager(t, secret)
	store := SecretManagerStore{
		Secret:   secret,
		Endpoint: srv.URL,
		Token:    func(context.Context) (string, error) { return "tok", nil },
	}
	ctx := context.Background()
	first, err := LoadOrCreateCA(ctx, store, "dep", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(*versions) != 1 {
		t.Fatalf("saved %d versions, want 1", len(*versions))
	}
	second, err := LoadOrCreateCA(ctx, store, "dep", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(*versions) != 1 {
		t.Fatalf("a second start saved another CA")
	}
	if !bytes.Equal(first.PEM(), second.PEM()) {
		t.Fatal("the reloaded CA differs from the saved one")
	}
}

func TestParseCARejectsMismatchedKey(t *testing.T) {
	a, err := NewCA("dep", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewCA("dep", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseCA(append(a.CertPEM(), b.keyPEM...)); err == nil {
		t.Fatal("ParseCA accepted a key that is not the certificate's")
	}
}
