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

package store

import (
	"context"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/hub/api"
)

// When the hub host sits directly under instance_domain, its label must never
// name an entry: <label>.<instance_domain> is the hub itself.
func TestReservedEntryNamesAreRefused(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	st.SetReservedEntryNames("hubhost")

	if _, err := st.CreateEntry(ctx, CreateEntryInput{Name: "hubhost", Kind: api.KindConnected, OwnerEmail: "alice@example.com"}); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("CreateEntry(hubhost) err = %v, want reserved", err)
	}
	avail, err := st.CheckNameAvailability(ctx, "hubhost", "alice@example.com")
	if err != nil {
		t.Fatalf("CheckNameAvailability: %v", err)
	}
	if avail.Status != api.NameInvalid || !strings.Contains(avail.Reason, "reserved") {
		t.Fatalf("availability of hubhost = %+v, want invalid/reserved", avail)
	}
	if _, err := st.CreateEntry(ctx, CreateEntryInput{Name: "other-node", Kind: api.KindConnected, OwnerEmail: "alice@example.com"}); err != nil {
		t.Fatalf("CreateEntry(other-node): %v", err)
	}
}
