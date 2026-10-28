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

// statemanager.go manages the controller-local configuration repository
// for the daemon and its UI. One manager owns the open repository used by
// native grain/config pull requests and the export loop. Its lock keeps a
// merged configuration import from racing an on-demand or timer export.
package cli
