#!/usr/bin/env bash
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# docs/test-plans/2026-11-16.md §8: /next/repos and the repository page
# against a real `grain daemon -sandbox-port-base=0` over SQLite. Reuses the
# 2026-11-13 next-ui-complete harness (ghsim with five acme/* repos, iapsim,
# start_daemon, api) and adds the two-repository seed and RBAC principals
# this walkthrough needs.

HERE16=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=../2026-11-13-next-ui-complete/env.sh
source "$HERE16/../2026-11-13-next-ui-complete/env.sh"

# seed_repos configures acme/widgets and acme/api as the deployment's two
# repositories, and a repo-scoped viewer and contributor.
seed_repos() {
	api POST /api/pipeline-pause '{"paused":true,"reason":"walkthrough seeding"}' >/dev/null
	api POST /api/repos '{"repo":"acme/widgets"}'
	echo
	api POST /api/repos '{"repo":"acme/api"}'
	echo
	api POST /api/access/principals '{"entry":"user:viewer@example.com","role":"grain/viewer","reason":"walkthrough reader"}'
	echo
	api POST /api/access/principals '{"entry":"user:contributor@example.com","role":"grain/contributor","reason":"walkthrough writer"}'
	echo
}
