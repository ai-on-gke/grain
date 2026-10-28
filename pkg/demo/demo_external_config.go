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
	"errors"
	"strings"

	"github.com/ai-on-gke/grain/pkg/ui"
)

// externalConfigRepo is `grain demo -external-config-repo`: a deployment
// whose configuration is managed in an external config repository, so
// every configuration write is refused with the exact edit to commit
// there instead (pkg/ui/config_refusal.go, docs/external-config-repo.md).
//
// It is the StateRepo a daemon wires, cut down to what that refusal and
// Settings → Config Repo read: where the repository is. There is no
// working tree behind it, so it has nothing to sync, browse or change,
// and says so rather than pretending.
type externalConfigRepo struct {
	remote string
	branch string
}

// errDemoConfigRepo is every operation that would need a real
// repository.
var errDemoConfigRepo = errors.New("grain demo: -external-config-repo names a repository and nothing more; there is no working tree here to sync, browse or reconfigure")

func newExternalConfigRepo(remote, branch string) *externalConfigRepo {
	if strings.TrimSpace(branch) == "" {
		branch = "main"
	}
	return &externalConfigRepo{remote: strings.TrimSpace(remote), branch: branch}
}

// ExternalConfigRepo is what Client.externalConfigRepo asks first.
func (r *externalConfigRepo) ExternalConfigRepo() (string, string) { return r.remote, r.branch }

func (r *externalConfigRepo) Status(context.Context) (ui.StateRepoStatus, error) {
	return ui.StateRepoStatus{Available: true, Mode: "external", Remote: r.remote, Branch: r.branch}, nil
}

func (r *externalConfigRepo) DismissResetNotices(ctx context.Context) (ui.StateRepoStatus, error) {
	return r.Status(ctx)
}

func (r *externalConfigRepo) ImportSecretsKey(context.Context, string) (ui.StateRepoStatus, error) {
	return ui.StateRepoStatus{}, errDemoConfigRepo
}

func (r *externalConfigRepo) SetRemote(context.Context, ui.StateRepoRemoteRequest) (ui.StateRepoStatus, error) {
	return ui.StateRepoStatus{}, errDemoConfigRepo
}

func (r *externalConfigRepo) SetSecretsRepo(context.Context, ui.SecretsRepoRequest) (ui.StateRepoStatus, error) {
	return ui.StateRepoStatus{}, errDemoConfigRepo
}

func (r *externalConfigRepo) Sync(context.Context) (ui.StateRepoStatus, error) {
	return ui.StateRepoStatus{}, errDemoConfigRepo
}

func (r *externalConfigRepo) Tree(context.Context) (ui.StateRepoTree, error) {
	return ui.StateRepoTree{}, errDemoConfigRepo
}

func (r *externalConfigRepo) File(context.Context, string, int64) (ui.StateRepoFile, error) {
	return ui.StateRepoFile{}, errDemoConfigRepo
}
