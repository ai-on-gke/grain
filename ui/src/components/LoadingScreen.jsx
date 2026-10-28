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

import GrainMark from "./GrainMark.jsx";

// LoadingScreen fills the app shell for the one stretch nothing else can
// render yet: the first load App.jsx makes on mount
// (bwsalmon/agents#555). Even the sidebar needs config's
// targetRepos/capabilities, and the lists fetched beside it are empty
// until they land -- an empty list drawn as a page is a deployment
// claiming it has no boards, no schedules, no tasks (grain/task-235) --
// so there is nothing more specific to show in the meantime and nothing
// honest to say either: just the mark, at the hero size GrainMark.jsx
// already has a denser/jittered rendering tier for (docs/brand.md's
// "hero, marketing, splash" tier) but that nothing had used until now.
const HERO_SIZE = 320;

export default function LoadingScreen() {
  return (
    <div className="loading-screen">
      <GrainMark size={HERO_SIZE} animated title="grain" />
    </div>
  );
}
