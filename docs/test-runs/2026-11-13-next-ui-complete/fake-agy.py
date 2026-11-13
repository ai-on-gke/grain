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

"""Minimal Antigravity CLI stub for grain daemon walkthroughs without model keys."""

import sys


def main() -> int:
    args = sys.argv[1:]
    if "--help" in args or (args and args[0] == "help"):
        print("Usage: agy [--effort low|medium|high] [models|version]")
        return 0
    if args and args[0] == "models":
        # Deliberately advertise extra CLI catalog models (id<TAB>label, per
        # pkg/agent/antigravity/catalog.go) so §1 item 4 can verify that
        # Sessions' model picker only lists the deployment's configured models.
        rows = [
            ("claude-sonnet-4-6", "Claude Sonnet 4.6"),
            ("gemini-2.5-pro", "Gemini 2.5 Pro"),
            ("unconfigured-catalog-alpha", "Unconfigured Alpha"),
            ("unconfigured-catalog-beta", "Unconfigured Beta"),
        ]
        for model_id, label in rows:
            print(f"{model_id}\t{label}")
        return 0
    if "--version" in args or (args and args[0] == "version"):
        print("fake-agy 1.0.0")
        return 0
    return 0


if __name__ == "__main__":
    sys.exit(main())
