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

package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

// prompter asks one question at a time on in/out, or answers every one
// with its default when non-interactive.
type prompter struct {
	in             *bufio.Reader
	out            io.Writer
	nonInteractive bool
}

func (p prompter) ask(question, def string) (string, error) {
	if p.nonInteractive {
		return def, nil
	}
	if def != "" {
		fmt.Fprintf(p.out, "%s [%s]: ", question, def)
	} else {
		fmt.Fprintf(p.out, "%s: ", question)
	}
	line, err := p.in.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		if errors.Is(err, io.EOF) {
			return def, nil
		}
		return "", err
	}
	if line = strings.TrimSpace(line); line == "" {
		return def, nil
	}
	return line, nil
}

func (p prompter) yes(question string, def bool) (bool, error) {
	d := "y/N"
	if def {
		d = "Y/n"
	}
	a, err := p.ask(question+" ("+d+")", "")
	if err != nil || a == "" {
		return def, err
	}
	switch strings.ToLower(a) {
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	}
	return false, fmt.Errorf("%q is not yes or no", a)
}

// splitList reads a comma- or space-separated answer.
func splitList(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
}
