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

package demo

import (
	"context"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// seedDemoDeclarations gives Settings -> Capabilities' Repository
// declarations card (model.DeclarationAcceptance) both of its states: a
// CI job whose GCP key a person accepted, and one that has since widened
// to a host script beside it and waits for a person again.
func seedDemoDeclarations(ctx context.Context, store *model.Store, now time.Time) error {
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	gcp := `https://grain.dev/capabilities/gcp#gcp-key={"permission_set":"artifact-publisher"}`
	hostScript := `https://grain.dev/capabilities/host-script#run={"script":"deploy-preview"}`
	for _, job := range []struct {
		name    string
		widened bool
	}{{"publish-images", false}, {"deploy-preview", true}} {
		if _, _, err := store.NoteCIJobDeclaration(ctx, repo, job.name, "4f9c2a1d7e3b", []string{gcp}, now.Add(-48*time.Hour)); err != nil {
			return err
		}
		if _, err := store.AcceptDeclaration(ctx, model.CIJobDeclarationID(repo, job.name), "alice@example.com",
			[]string{"capabilities:admin"}, []string{gcp}, now.Add(-47*time.Hour)); err != nil {
			return err
		}
		if job.widened {
			if _, _, err := store.NoteCIJobDeclaration(ctx, repo, job.name, "8b1e6d0c2a94", []string{gcp, hostScript}, now.Add(-2*time.Hour)); err != nil {
				return err
			}
		}
	}
	return nil
}
