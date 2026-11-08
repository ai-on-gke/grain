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

import json,sys
r=json.load(sys.stdin); print("run", r["id"], r["event"], "conclusion:", r.get("conclusion"), "warning:", r.get("warning"))
for j in r["jobs"]:
    print("job", j["name"], j.get("conclusion"), "| error:", j.get("error"), "| warning:", j.get("warning"))
    for s in j.get("steps",[]):
        print("  step", s.get("name","")[:40], s.get("conclusion"))
        for k in ("output","log","tail"):
            if s.get(k): print("   |", s[k][-1500:].replace("\n","\n   | "))
