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

package judge

import (
	"regexp"
	"strings"
)

type DiffChunk struct {
	Content string
}

type JudgePacket struct {
	PacketID        string
	PairID          string
	RootSpecTitle   string
	RootSpecBody    string
	CIConclusion    string
	TestFiles       []string
	Chunks          []DiffChunk
	InjectionFlags  []string
	EstimatedTokens int
}

var blindRegexes = []*regexp.Regexp{
	regexp.MustCompile(`(?i)grain-qa-stage/[a-zA-Z0-9._/-]+`),
	regexp.MustCompile(`(?i)qa-alloc:[a-zA-Z0-9._-]+`),
	regexp.MustCompile(`(?i)golden:[a-z0-9-]+`),
	regexp.MustCompile(`(?i)caller_notes`),
	regexp.MustCompile(`(?i)grain/task-[a-zA-Z0-9._-]+`),
	regexp.MustCompile(`(?i)task-[0-9a-f]{6,}`),
}

func RedactBlindText(s string) string {
	for _, re := range blindRegexes {
		s = re.ReplaceAllString(s, "")
	}
	return s
}

func SplitDiffIntoChunks(squashedDiff string, maxLinesPerChunk int) []DiffChunk {
	if maxLinesPerChunk <= 0 {
		maxLinesPerChunk = 1500
	}
	var chunks []DiffChunk

	lines := strings.SplitAfter(squashedDiff, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	var currentChunk strings.Builder
	var chunkLines int

	var currentFile strings.Builder
	var fileLines int

	flushFile := func() {
		if fileLines == 0 {
			return
		}
		if chunkLines > 0 && chunkLines+fileLines > maxLinesPerChunk {
			chunks = append(chunks, DiffChunk{Content: currentChunk.String()})
			currentChunk.Reset()
			chunkLines = 0
		}
		currentChunk.WriteString(currentFile.String())
		chunkLines += fileLines
		currentFile.Reset()
		fileLines = 0
	}

	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git ") {
			flushFile()
		}
		currentFile.WriteString(line)
		fileLines++
	}
	flushFile()

	if chunkLines > 0 {
		chunks = append(chunks, DiffChunk{Content: currentChunk.String()})
	}

	return chunks
}

var injectionRe = regexp.MustCompile(`(?i)(ignore (all |previous )?instructions|score this (pr|diff|change) (10|9|high)|give (full|maximum) marks|system prompt:|you are a (senior )?(reviewer|judge|rater))`)

func DetectPromptInjection(diff string) []string {
	var flags []string
	lines := strings.Split(diff, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "+") {
			if injectionRe.MatchString(line) {
				flags = append(flags, line)
			}
		}
	}
	return flags
}
