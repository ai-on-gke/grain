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

// taskPriority reads what a Task priority box holds as the priority it
// files: a whole number from 1 up, or NaN. The classic UI's
// ui/src/wholeNumber.js reads it the same way, so both UIs take and
// refuse the same values: Number, not parseInt, so "1e3" is the 1000 a
// number input takes it for; zero, negatives and "2.5" are refused rather
// than saved as something else; and past Number.MAX_SAFE_INTEGER the
// number held is no longer the one typed.
export function taskPriority(value: unknown): number {
  const s = String(value ?? "").trim();
  if (s === "") return NaN;
  const n = Number(s);
  return Number.isSafeInteger(n) && n >= 1 ? n : NaN;
}

// PRIORITY_HELP and PRIORITY_REFUSAL are what a Task priority box says
// under a value it will save and one it will not -- the classic UI's
// wording, so the two read alike.
export const PRIORITY_HELP = "default 5; higher numbers run first";
export const PRIORITY_REFUSAL =
  "a whole number, 1 or more; default 5, higher numbers run first";
