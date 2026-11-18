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

package ui

import "context"

// DoJSON sends an authenticated JSON request to path on the Grain daemon and decodes the response into out.
func (c *HTTPClient) DoJSON(ctx context.Context, method, path string, body, out any) error {
	return c.do(ctx, method, path, body, out)
}
