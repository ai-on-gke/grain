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

import { Button } from "@mui/material";

// ClosedTasksMore is the end of a task view that can show closed work: the
// live list carries no closed tasks, and the closed ones are paged in
// newest-closed first (closedTasks.js), so this is how a reader reaches
// the ones closed before the pages already read. Nothing is drawn once
// every closed task is in.
export default function ClosedTasksMore({ closed }) {
  if (!closed?.hasMore) return null;
  return (
    <div className="closed-tasks-more">
      <Button
        size="small"
        variant="outlined"
        disabled={closed.loading}
        onClick={() => closed.loadMore()}
      >
        {closed.loading ? "Loading closed tasks…" : "Load older closed tasks"}
      </Button>
    </div>
  );
}
