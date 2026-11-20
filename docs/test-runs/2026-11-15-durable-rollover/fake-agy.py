#!/usr/bin/env python3
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

"""Records each run's prompt, then hands it to the health walkthrough's fake agy.

Prompts land in $WALK_DIR/runs/prompt-<n>.txt in dispatch order, so a
walkthrough can check what a successor's run was told.
"""

import os
import subprocess
import sys

sys.dont_write_bytecode = True

HERE = os.path.dirname(os.path.abspath(__file__))
REAL = os.path.join(HERE, "..", "2026-11-13-grain-health-exit-codes", "fake-agy.py")

args = sys.argv[1:]
if any(a in ("--help", "--version", "models") for a in args):
    sys.exit(subprocess.call([sys.executable, "-B", REAL] + args))

prompt = sys.stdin.read()
runs = os.path.join(os.environ.get("WALK_DIR", "/tmp/walk-health"), "runs")
os.makedirs(runs, exist_ok=True)
n = len([f for f in os.listdir(runs) if f.startswith("prompt-")])
with open(os.path.join(runs, f"prompt-{n:03d}.txt"), "w", encoding="utf-8") as f:
    f.write(prompt)
sys.exit(subprocess.run([sys.executable, "-B", REAL] + args, input=prompt, text=True).returncode)
