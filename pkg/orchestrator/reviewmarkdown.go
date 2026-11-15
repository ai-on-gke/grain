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

import (
	"regexp"
	"strings"
)

// findingMarkdown is a review finding's body as Markdown. A reviewing run
// writes its add_review_comment bodies for GitHub, and some write them the
// way GitHub's own HTML reads -- <code>path</code>, <strong>refuse</strong>,
// &quot;held&quot; -- and bullet with "* ". GitHub renders that, but the
// copy grain relays into a task's conversation is read by both UIs: the
// classic UI shows raw HTML as text by design (a comment must never inject
// markup), and ui-ng renders only a few bare inline tags. So the relayed
// copy is put in the Markdown it meant: backticks for code, ** for bold,
// * for emphasis, real quotes, and "- " bullets.
//
// Only the handful of inline tags a finding uses are converted, and only
// in their bare, attribute-free form; anything else is left as written,
// to be shown as text. Fenced code blocks and `code spans` already in the
// body are left exactly as they are: their text is literal there.
// &lt; and &gt; stay encoded, since Markdown renders them as < and > but
// a decoded < would read as the start of a tag.
func findingMarkdown(body string) string {
	if !strings.ContainsAny(body, "<&*") {
		return body
	}
	lines := strings.Split(body, "\n")
	var out []string
	var prose []string
	fence := ""
	flush := func() {
		if len(prose) > 0 {
			out = append(out, strings.Split(proseMarkdown(strings.Join(prose, "\n")), "\n")...)
			prose = nil
		}
	}
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if fence != "" {
			out = append(out, line)
			if strings.HasPrefix(trimmed, fence) && strings.Trim(trimmed, fence[:1]+" \t") == "" {
				fence = ""
			}
			continue
		}
		if m := fenceOpenRE.FindString(trimmed); m != "" {
			flush()
			fence = m
			out = append(out, line)
			continue
		}
		prose = append(prose, line)
	}
	flush()
	return strings.Join(out, "\n")
}

var (
	fenceOpenRE  = regexp.MustCompile("^(```+|~~~+)")
	codeSpanRE   = regexp.MustCompile("`+")
	htmlCodeRE   = regexp.MustCompile(`(?is)<code>(.*?)</code>`)
	htmlStrongRE = regexp.MustCompile(`(?is)<(?:strong|b)>(.*?)</(?:strong|b)>`)
	htmlEmRE     = regexp.MustCompile(`(?is)<(?:em|i)>(.*?)</(?:em|i)>`)
	htmlBreakRE  = regexp.MustCompile(`(?i)<br\s*/?>`)
	starBulletRE = regexp.MustCompile(`(?m)^(\s*)\* `)
	ampRE        = regexp.MustCompile(`&amp;(?:#?[A-Za-z0-9]+;)?`)
)

// proseMarkdown converts the text outside fenced blocks: the code spans
// in it are kept verbatim and everything between them is converted.
func proseMarkdown(s string) string {
	s = starBulletRE.ReplaceAllString(s, "$1- ")
	var b strings.Builder
	for s != "" {
		loc := codeSpanRE.FindStringIndex(s)
		if loc == nil {
			b.WriteString(htmlishMarkdown(s))
			break
		}
		ticks := s[loc[0]:loc[1]]
		end := strings.Index(s[loc[1]:], ticks)
		if end < 0 {
			// An unclosed run of backticks is literal text, not a span.
			b.WriteString(htmlishMarkdown(s[:loc[1]]))
			s = s[loc[1]:]
			continue
		}
		b.WriteString(htmlishMarkdown(s[:loc[0]]))
		closeAt := loc[1] + end + len(ticks)
		b.WriteString(s[loc[0]:closeAt])
		s = s[closeAt:]
	}
	return b.String()
}

func htmlishMarkdown(s string) string {
	s = htmlCodeRE.ReplaceAllStringFunc(s, func(m string) string {
		inner := htmlCodeRE.FindStringSubmatch(m)[1]
		return codeSpan(decodeEntities(inner, true))
	})
	s = htmlStrongRE.ReplaceAllString(s, "**$1**")
	s = htmlEmRE.ReplaceAllString(s, "*$1*")
	s = htmlBreakRE.ReplaceAllString(s, "\n")
	return decodeEntities(s, false)
}

// codeSpan wraps text in enough backticks that none inside it closes the
// span early, padding with a space where CommonMark needs one.
func codeSpan(text string) string {
	longest := 0
	for _, run := range codeSpanRE.FindAllString(text, -1) {
		longest = max(longest, len(run))
	}
	ticks := strings.Repeat("`", longest+1)
	if strings.HasPrefix(text, "`") || strings.HasSuffix(text, "`") {
		return ticks + " " + text + " " + ticks
	}
	return ticks + text + ticks
}

// decodeEntities turns the entities an HTML-minded writer uses for
// punctuation back into the characters. In code (literal text), &lt; and
// &gt; are decoded too; in prose they stay, for the reason above.
func decodeEntities(s string, code bool) string {
	if !strings.Contains(s, "&") {
		return s
	}
	pairs := []string{
		"&quot;", `"`, "&#34;", `"`, "&#39;", "'", "&#x27;", "'", "&apos;", "'",
		"&nbsp;", " ", "&ndash;", "–", "&mdash;", "—", "&hellip;", "…",
	}
	if code {
		pairs = append(pairs, "&lt;", "<", "&gt;", ">", "&amp;", "&")
		return strings.NewReplacer(pairs...).Replace(s)
	}
	s = strings.NewReplacer(pairs...).Replace(s)
	// A bare &amp; is an ampersand, unless decoding it would make the text
	// after it read as an entity it was written to avoid.
	return ampRE.ReplaceAllStringFunc(s, func(m string) string {
		if m != "&amp;" {
			return m
		}
		return "&"
	})
}
