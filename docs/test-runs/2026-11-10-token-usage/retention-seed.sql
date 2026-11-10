-- Copyright 2026 Google LLC
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.
-- You may obtain a copy of the License at
--
--     http://www.apache.org/licenses/LICENSE-2.0
--
-- Unless required by applicable law or agreed to in writing, software
-- distributed under the License is distributed on an "AS IS" BASIS,
-- WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
-- See the License for the specific language governing permissions and
-- limitations under the License.

-- retention-seed.sql: two usage rows either side of a 7-day retention,
-- written while the daemon is stopped (SQLite spelling of the timestamps
-- the store itself writes).
INSERT INTO task_run_usage (run_id, task_id, connection_id, model, framework, input_tokens, output_tokens, started_at, finished_at)
VALUES ('old-8d', '2', 'agy-walk', 'fake-model', 'antigravity', 111, 1, datetime('now', '-8 days'), datetime('now', '-8 days')),
       ('old-6d', '2', 'agy-walk', 'fake-model', 'antigravity', 222, 2, datetime('now', '-6 days'), datetime('now', '-6 days'));
