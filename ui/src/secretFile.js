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

// Reads a credential out of a file the operator picked, as text, for the
// write-only secret boxes to send (grain/task-160).
//
// The one that made this necessary is `github-app/private-key`: a GitHub
// App's key arrives as a downloaded .pem, and the only way to get it into
// grain was to open it and paste it into a single-line password box.
// Pasting a PEM is where it goes wrong -- a terminal or a chat window
// folds the lines, a password manager strips them, a copy out of a JSON
// blob leaves literal backslash-n behind -- and the result is a value
// that looks stored and fails much later, at the first attempt to sign a
// JWT with it, as "failed to parse private key" from a run that has
// nothing to do with the mistake. A file's bytes are not retyped by
// anything on the way, so the line breaks that PEM is made of survive.
//
// It is the one place a File's bytes are actually read for a secret --
// the same shape (and the same reason) as attachments.js next door: a
// test can mock this module rather than exercise FileReader for real.
// Nothing here sends anything; the caller decides what to do with the
// text, which for both callers today is to put it in the box beside the
// button so it is still the human who presses Set.

// maxSecretFileBytes is a generous cap on what can be loaded this way. A
// PEM key is under 2KB and a service-account JSON about the same, so
// anything approaching this is the wrong file -- picked by mistake out of
// a Downloads folder -- and the cap is what says so here rather than
// letting a megabyte of it travel to the store and sit there as a
// credential nothing can resolve.
export const maxSecretFileBytes = 64 * 1024;

// readSecretFile resolves to the file's text, or rejects with an error
// worded for the person who picked it. The three refusals are the three
// ways the picked file is not a credential at all: too big, empty, or
// binary -- a NUL byte cannot occur in a PEM, a token or a JSON key, and
// a value that has one is a .p12 or an image that would otherwise be
// stored verbatim and fail wherever it was finally resolved.
export default function readSecretFile(file) {
  return new Promise((resolve, reject) => {
    if (file.size > maxSecretFileBytes) {
      reject(
        new Error(
          `${file.name} is ${file.size} bytes -- too big for a credential (the limit is ${maxSecretFileBytes}). Check you picked the right file.`,
        ),
      );
      return;
    }
    const reader = new FileReader();
    reader.onerror = () => reject(reader.error);
    reader.onload = () => {
      const text = reader.result;
      if (text.trim() === "") {
        reject(new Error(`${file.name} is empty.`));
        return;
      }
      if (text.includes("\0")) {
        reject(
          new Error(
            `${file.name} does not look like text -- a key file, a token or a JSON key is what goes here.`,
          ),
        );
        return;
      }
      resolve(text);
    };
    reader.readAsText(file);
  });
}
