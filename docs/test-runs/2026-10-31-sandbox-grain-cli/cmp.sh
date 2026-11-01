#!/bin/sh
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

# cmp.sh <args...>: run `grain <args>` with the placed grain-sandbox and with
# a full cmd/grain copied in, print the first and report whether they match.
a=$(grain "$@" 2>&1); ea=$?
b=$(/home/debian/grain-full "$@" 2>&1); eb=$?
printf '%s\n' "$a" | head -"${CMP_LINES:-25}"
if [ "$a" = "$b" ] && [ $ea = $eb ]; then echo "[exit=$ea; full grain: identical]"; else echo "[exit=$ea; full grain exit=$eb, DIFFERS:]"; printf '%s\n' "$b" | head -"${CMP_LINES:-25}"; fi
