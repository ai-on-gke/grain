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

package orchestrator

import "testing"

func TestFindingMarkdown(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, in, want string }{
		{"plain", "nothing to convert", "nothing to convert"},
		{"code", "see <code>a.go:1</code>", "see `a.go:1`"},
		{"code entities", "<code>a &lt;b&gt; &amp;&amp; &quot;c&quot;</code>", "`a <b> && \"c\"`"},
		{"bold and em", "<strong>no</strong> <b>never</b> <em>maybe</em> <i>so</i>", "**no** **never** *maybe* *so*"},
		{"break", "one<br>two<br/>three", "one\ntwo\nthree"},
		{"quotes", "never &quot;held&quot; &#39;x&#39;", "never \"held\" 'x'"},
		{"amp", "a &amp; b, &amp;quot; stays", "a & b, &amp;quot; stays"},
		{"angle brackets stay encoded", "&lt;script&gt;", "&lt;script&gt;"},
		{"star bullets", "* one\n  * two\n*emph*", "- one\n  - two\n*emph*"},
		{"code span verbatim", "`<code>&quot;</code>` and <code>x</code>", "`<code>&quot;</code>` and `x`"},
		{"fence verbatim", "<strong>a</strong>\n```html\n<strong>b</strong> &quot;\n* c\n```\n* d", "**a**\n```html\n<strong>b</strong> &quot;\n* c\n```\n- d"},
		{"attributes left alone", `<code class="x">y</code>`, `<code class="x">y</code>`},
		{"unclosed ticks", "a ` b <code>c</code>", "a ` b `c`"},
	} {
		if got := findingMarkdown(tc.in); got != tc.want {
			t.Errorf("%s: findingMarkdown(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}
