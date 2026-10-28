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

package gitproxy

import "time"

// SetCLIWaitIntervals shortens how often a blocked /cli/ci/wait polls and
// heartbeats, for the test's lifetime.
func SetCLIWaitIntervals(poll, heartbeat time.Duration) (restore func()) {
	oldPoll, oldBeat := cliWaitPoll, cliWaitHeartbeat
	cliWaitPoll, cliWaitHeartbeat = poll, heartbeat
	return func() { cliWaitPoll, cliWaitHeartbeat = oldPoll, oldBeat }
}
