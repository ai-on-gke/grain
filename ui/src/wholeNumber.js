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

// wholeNumber reads what a type="number" box holds as the whole number
// it means, or NaN when it holds none.
//
// Not parseInt: a number input accepts exponent notation, so "1e3" is a
// valid entry for 1000 that the browser lets through -- and parseInt
// stops at the "e" and reads it as 1, which filed a task asked to run
// at priority 1000 at priority 1, and an 8e3 MiB sandbox at 8 MiB,
// without a word. Number reads the whole string the way the input
// itself does; anything that is not a whole number (empty, "2.5", "abc")
// is NaN, which every caller already treats as "not set".
export function wholeNumber(value) {
  const s = String(value ?? "").trim();
  if (s === "") return NaN;
  const n = Number(s);
  return Number.isInteger(n) ? n : NaN;
}

// taskPriority reads a Task priority box as the priority it files: a
// whole number from 1 up, or NaN. Bounded at Number.MAX_SAFE_INTEGER as
// well: past it the number a box holds is no longer the one typed
// (9007199254740993 was filed as 9007199254740992), and past int64 the
// server refused it with Go's own JSON decoding error rather than
// anything about priority (2026-11-02 walkthrough).
export function taskPriority(value) {
  const n = wholeNumber(value);
  return n >= 1 && Number.isSafeInteger(n) ? n : NaN;
}

// PRIORITY_REFUSAL is what every Task priority box says under a value it
// will not save.
export const PRIORITY_REFUSAL =
  "a whole number, 1 or more; default 5, higher numbers run first";
