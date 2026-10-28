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

// A phone keyboard is built to help you type prose, and it helps in
// three ways at once: it capitalizes the first letter of a text box, it
// substitutes the word it thinks you meant for the letters you actually
// typed, and it underlines whatever it does not recognise. All three are
// right for the boxes here that hold prose -- a task title, a
// description, a reply in the inbox -- and all three are wrong for every
// box that holds a name something else has to match exactly, which in
// this app is most of them: a repo is bwsalmon/grain and never
// Bwsalmon/grain, a branch is main and never Main, a secret key is token
// and never Token, and a release train's tag is rel.latest and never
// whatever a keyboard decided rel.latest was a typo for.
//
// The first two are the same failure, which is why they are turned off
// together. The box ends up holding a value nobody typed; a hardware
// keyboard is not affected by any of this, so the person who set the
// deployment up on a desktop never sees it happen; and the error
// surfaces later as "repository not found" rather than as anything the
// field itself complains about. The third is only a red squiggle -- not
// a wrong value, but pure noise on a field full of slashes and hyphens,
// and it rides along both because it costs nothing and because a browser
// that does not read autocorrect (a WebKit attribute, standardised
// since) may still take spellcheck as the instruction not to meddle.
//
// identifierField is that opt-out, spread into a field's own input props
// so the intent is named once rather than left as three bare strings at
// each of the fields that want it:
//
//   <TextField label="Base branch" inputProps={identifierField} />
//   <TextField inputProps={{ ...identifierField, "aria-label": label }} />
//   <input {...identifierField} placeholder="owner/name" />
//
// It has to go on the <input> itself, not on the MUI component wrapping
// it: TextField forwards a prop it does not recognise to the FormControl
// <div> around the field, where the attributes mean nothing. Hence
// inputProps (MUI v6's slotProps.htmlInput under its older name, which
// is what the rest of this UI already uses) for a MUI field and a plain
// spread for a bare one.
//
// Nothing here is about the *value* being an identifier -- validation is
// each field's own business, and a field that holds prose (see
// NewTaskOverlay's title, which deliberately does not spread this)
// should keep all three of the keyboard's habits.
export const identifierField = {
  autoCapitalize: "off",
  autoCorrect: "off",
  spellCheck: "false",
};
