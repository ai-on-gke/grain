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

// runTimers: the one helper a test that is *about* a timer needs.
//
// Not part of appTestHarness.js because it is not about App at all --
// RepoReleases.test.jsx polls on an interval of its own, and the next
// component that does will want the same three lines.
import { act } from "@testing-library/react";
import { vi } from "vitest";

// runTimers moves a test's fake clock forward by ms, letting React and
// any promise chain a timer started settle before it returns -- so a
// test that is about a timer (App's 3s poll, the 5s its error banner
// stays up for, RepoReleases' own poll) can say "and now it is three
// seconds later" instead of sitting there for three seconds.
//
// It is what those tests use *instead of* waitFor, not beside it:
// waitFor polls on a timer of its own, and under a fake clock that
// timer never fires either, so a waitFor here would hang until the
// suite-wide 20s in vite.config.js killed it. advanceTimersByTimeAsync
// runs the queued callbacks in order and awaits the real microtask
// queue between them, which is what lets a fetch resolved by one timer
// be seen by the next; act() puts React's own re-render inside the same
// step, the way userEvent and render already do.
//
// Only reach for this where a test is *about* the timer. Everything
// else waits on a render, which findBy/waitFor already handle at full
// speed on the real clock. And put the timers back afterwards -- App's
// tests do it in resetAppTestState, everyone else in their own
// afterEach -- or the next test in the file inherits a stopped clock.
export async function runTimers(ms = 0) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}
