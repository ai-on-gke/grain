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
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// TestVerifiedServiceAccountCommentIsAServicePrincipal pins the
// 2026-11-02 verified-identity walkthrough's F3: a task a verified
// *.gserviceaccount.com caller filed or closed was attributed to a
// service principal, but its comment on that task was stored as a human
// one, because the comment path rebuilt the author as human from the
// email alone.
func TestVerifiedServiceAccountCommentIsAServicePrincipal(t *testing.T) {
	priv, pemStr := generateECDSATestKey(t)
	assertionKeys(t, priv, pemStr, true)
	ui.SetIAPAudience(tunnelAudience)
	srv, client := testServer(t)
	client.Config.ProvisionedServiceAccounts = []string{"bot@my-proj.iam.gserviceaccount.com"}
	if _, err := client.AllowPrincipal(context.Background(), ui.AllowPrincipalRequest{Entry: "user:alice@example.com", Reason: "test"}); err != nil {
		t.Fatal(err)
	}
	ui.SetAccessCodeForTest(srv, testAccessCode)

	exp := time.Now().Add(5 * time.Minute)
	for _, c := range []struct {
		email string
		want  model.Principal
	}{
		{"Bot@My-Proj.iam.gserviceaccount.com", model.Principal{Kind: model.PrincipalService, ID: "bot@my-proj.iam.gserviceaccount.com", Source: model.SourceIAP}},
		{"alice@example.com", model.Principal{Kind: model.PrincipalHuman, ID: "alice@example.com", Source: model.SourceIAP}},
	} {
		h := map[string]string{
			ui.IAPAssertionHeader: signIAPToken(t, priv, tunnelKid, tunnelAudience, c.email, "s", ui.GoogleIAPIssuer, exp),
			"Content-Type":        "application/json",
		}
		rec := serveFrom(srv, "203.0.113.20:443", http.MethodPost, "/api/tasks", `{"title":"t","body":"b","noRepo":true}`, h)
		if rec.Code != http.StatusCreated {
			t.Fatalf("%s: POST /api/tasks: %d %s", c.email, rec.Code, rec.Body)
		}
		var task struct{ ID string }
		if err := json.Unmarshal(rec.Body.Bytes(), &task); err != nil {
			t.Fatal(err)
		}
		if rec := serveFrom(srv, "203.0.113.20:443", http.MethodPost, "/api/tasks/"+task.ID+"/comments", `{"body":"hello"}`, h); rec.Code != http.StatusOK {
			t.Fatalf("%s: POST comments: %d %s", c.email, rec.Code, rec.Body)
		}
		comments, err := client.Store.Comments(context.Background(), task.ID)
		if err != nil {
			t.Fatal(err)
		}
		last := comments[len(comments)-1]
		if last.Body != "hello" || last.Author.Actor != c.want {
			t.Errorf("%s: comment %q attributed to %+v, want %+v", c.email, last.Body, last.Author.Actor, c.want)
		}
	}
}
