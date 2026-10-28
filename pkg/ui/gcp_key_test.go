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

package ui_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestGCPKeyHTTPClientAndServer(t *testing.T) {
	srv, client := testServer(t)
	storeSettingsRow(t, client)
	ctx := context.Background()

	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	httpClient := serverAccessClient(srv, ts.URL)

	list, err := httpClient.ListGCPKeys(ctx)
	if err != nil {
		t.Fatalf("ListGCPKeys: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected 0 keys, got %d", len(list))
	}

	created, err := httpClient.SetGCPKey(ctx, ui.SetGCPKeyRequest{
		Name:                "prod",
		ProjectID:           "proj-gamma",
		ServiceAccountEmail: "sa@proj-gamma.iam.gserviceaccount.com",
		ServiceAccountCount: 2,
		MaxKeyAge:           "30m",
	})
	if err != nil {
		t.Fatalf("SetGCPKey: %v", err)
	}
	if created.Name != "prod" || created.MaxKeyAge != 30*time.Minute {
		t.Errorf("unexpected created key: %+v", created)
	}

	got, err := httpClient.GetGCPKey(ctx, "prod")
	if err != nil {
		t.Fatalf("GetGCPKey: %v", err)
	}
	if got.ProjectID != "proj-gamma" {
		t.Errorf("got.ProjectID = %q, want proj-gamma", got.ProjectID)
	}

	if err := httpClient.DeleteGCPKey(ctx, "prod"); err != nil {
		t.Fatalf("DeleteGCPKey: %v", err)
	}

	listAfter, err := httpClient.ListGCPKeys(ctx)
	if err != nil {
		t.Fatalf("ListGCPKeys after delete: %v", err)
	}
	if len(listAfter) != 0 {
		t.Errorf("expected 0 keys after delete, got %d", len(listAfter))
	}
}

// A key named "default" would be capability gcp-key:default, which is
// gcp-key, which always resolves to the deployment's own pool, so it is
// refused; and a description set through the API survives a later save
// that does not mention it.
func TestGCPKeyDefaultNameAndDescription(t *testing.T) {
	client, store, ctx := testClient(t)
	if err := store.PutConfig(ctx, model.Config{PollInterval: model.DefaultPollInterval}); err != nil {
		t.Fatal(err)
	}
	asRoot := actingAs(ctx, "root")
	req := func(name string, desc *string) ui.SetGCPKeyRequest {
		return ui.SetGCPKeyRequest{
			Name:                name,
			ProjectID:           "proj",
			ServiceAccountEmail: "sa@proj.iam.gserviceaccount.com",
			Description:         desc,
		}
	}
	for _, name := range []string{"default", "DEFAULT"} {
		if _, err := client.SetGCPKeyWithUser(asRoot, req(name, nil), "root"); err == nil {
			t.Errorf("saving a key named %q succeeded, want refused", name)
		}
	}

	desc := "deploys the staging stack"
	key, err := client.SetGCPKeyWithUser(asRoot, req("deploy", &desc), "root")
	if err != nil {
		t.Fatal(err)
	}
	if key.Description != desc {
		t.Fatalf("Description = %q, want %q", key.Description, desc)
	}
	if key, err = client.SetGCPKeyWithUser(asRoot, req("deploy", nil), "root"); err != nil {
		t.Fatal(err)
	}
	if key.Description != desc {
		t.Errorf("save without a description: Description = %q, want %q kept", key.Description, desc)
	}
	empty := ""
	if key, err = client.SetGCPKeyWithUser(asRoot, req("deploy", &empty), "root"); err != nil {
		t.Fatal(err)
	}
	if key.Description != "" {
		t.Errorf("save with an empty description: Description = %q, want cleared", key.Description)
	}
}

// A task asking for gcp-key:<name> is refused at filing when the
// deployment has no key of that name, and filed once it has one.
func TestFilingGCPKeyChecksTheDeploymentsKeys(t *testing.T) {
	client, store, ctx := testClient(t)
	caps := &[]string{"gcp-key:deploy"}
	_, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "deploy", Capabilities: caps})
	if err == nil || !strings.Contains(err.Error(), "this deployment has not configured") {
		t.Fatalf("filing with a key nobody configured: err = %v, want the deployment's missing-key refusal", err)
	}
	if err := store.PutGCPKey(ctx, model.GCPKey{
		Name: "deploy", ProjectID: "proj", ServiceAccountEmail: "sa@proj.iam.gserviceaccount.com",
	}); err != nil {
		t.Fatal(err)
	}
	for _, team := range []string{"", "ops"} {
		task, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "deploy", Capabilities: caps})
		if err != nil {
			t.Fatalf("filing in team %q with the deployment's key: %v", team, err)
		}
		stored, err := store.GetTask(ctx, task.ID)
		if err != nil || stored == nil {
			t.Fatalf("reading task %s: %v", task.ID, err)
		}
		found := false
		for _, g := range stored.Grants {
			found = found || g.Capability == "gcp-key:deploy"
		}
		if !found {
			t.Errorf("team %q task grants = %+v, want gcp-key:deploy", team, stored.Grants)
		}
	}
}
