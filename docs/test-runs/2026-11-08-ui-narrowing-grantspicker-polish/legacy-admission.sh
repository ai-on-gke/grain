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

# legacy-admission.sh DB: deals admission over the seed rows the way
# seed.mjs does and blanks it on every row seed.mjs left unset
# (floor(seed index / 4) % 4 == 3), so the store holds legacy "" rows the way a
# pre-admission install's tasks read -- the API cannot file one, since
# POST /api/tasks defaults admission to standard.
set -euo pipefail
db="${1:?usage: legacy-admission.sh path/to/grain.db}"
sqlite3 "$db" "UPDATE task SET admission = CASE CAST(substr(title, 6, 4) AS INTEGER) / 4 % 4 WHEN 0 THEN 'standard' WHEN 1 THEN 'bypass' WHEN 2 THEN 'durable' ELSE '' END WHERE title LIKE 'seed %'; -- re-deals the tiers the way seed.mjs does (idempotent)"
sqlite3 "$db" "SELECT quote(admission), count(*) FROM task GROUP BY 1;"
