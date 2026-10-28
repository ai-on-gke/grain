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

// Converts a browser File into the base64 upload shape the API expects
// (ui.AttachmentUpload: filename, contentType, content) -- the one place
// a File's bytes are actually read, so a test can mock this module the
// same way it already mocks api.js instead of exercising FileReader for
// real (bwsalmon/agents#522).
export default function fileToAttachment(file) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onerror = () => reject(reader.error);
    reader.onload = () => {
      // reader.result is "data:<mime>;base64,<data>" -- everything after
      // the first comma is the part the API wants.
      const content = reader.result.slice(reader.result.indexOf(",") + 1);
      resolve({
        filename: file.name || "image.png",
        contentType: file.type || "application/octet-stream",
        content,
      });
    };
    reader.readAsDataURL(file);
  });
}
