#!/bin/bash
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

# api.sh <data-dir> <port> <METHOD> <path> [json]: one authenticated call,
# printing the status line and the body.
dir=$1 port=$2 m=$3 p=$4 body=$5
curl -sS -X "$m" -H "Authorization: Bearer $(cat "$dir/local-api-token")" -H 'Content-Type: application/json' \
  ${body:+--data "$body"} -w '\nHTTP %{http_code}\n' "http://127.0.0.1:$port$p"
