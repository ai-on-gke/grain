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

const LET_GO_ON = ["wheel", "touchstart", "keydown", "pointerdown"];

// keepInView holds el where scrollIntoView just put it while the page
// above it is still drawing. A section whose cards load at different times
// otherwise pushes a deep-linked card back out of view: Capabilities draws
// its policy card at once and its server list a moment later, above it,
// which left /settings/capabilities#capability-policy -- where every
// capability picker's policy link goes -- scrolled to the server list. It
// lets go after ms, or as soon as the reader scrolls, presses a key or
// clicks, and returns a function that lets go at once.
export function keepInView(el, scrollOptions, ms = 5000) {
  const top = el.getBoundingClientRect().top;
  const observer = new MutationObserver(() => {
    if (!el.isConnected) return;
    if (Math.abs(el.getBoundingClientRect().top - top) > 4) {
      el.scrollIntoView?.(scrollOptions);
    }
  });
  observer.observe(document.body, { childList: true, subtree: true });
  const stop = () => {
    observer.disconnect();
    clearTimeout(timer);
    LET_GO_ON.forEach((type) => window.removeEventListener(type, stop, true));
  };
  const timer = setTimeout(stop, ms);
  LET_GO_ON.forEach((type) => window.addEventListener(type, stop, true));
  return stop;
}
