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

package cli

// This deployment's Actions cache: the listener in front of
// pkg/actionscache, and the per-job grant a CI sandbox is built with.
//
// It is published exactly as the package cache above it is
// (pkg/cli/packagecache.go): one HTTP listener through the Fabric every
// sandbox-facing service goes through (sandboxnet.go), which binds the
// address sandboxes reach this deployment at where there is one and
// localhost where there is not, with the port chosen by the kernel. A
// sandbox needs no route, no mount and no image change to reach it,
// which is what makes it work the same on a granule guest and in a
// container.
//
// Every failure here is logged and left nil. A deployment with no
// Actions cache is the deployment grain has always been: act runs its
// own cache server inside each job's sandbox and everything exported
// into it dies with the machine. That is a slow CI, not a broken one,
// and it is not a reason to refuse to start.
