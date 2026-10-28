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
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// bundleSecretFlags are the flags grain export and grain import share for
// the passphrase or key that protects a migration bundle.
//
// The bundle holds the whole secrets directory, so its passphrase is read
// from a file or from stdin, the same way `grain secrets set` and
// `grain state key import` take their values: anything on a command line
// is readable by anyone else on the host through `ps` and lands in the
// shell's history. -passphrase, -password and -key still parse so that
// existing scripts keep working, but they print a warning saying so.
type bundleSecretFlags struct {
	passphrase     string
	passphraseFile string
	key            string
	keyFile        string

	// stdin is what -passphrase-file - and -key-file - read.
	stdin io.Reader
}

func addBundleSecretFlags(fs *flag.FlagSet) *bundleSecretFlags {
	f := &bundleSecretFlags{stdin: os.Stdin}
	fs.StringVar(&f.passphraseFile, "passphrase-file", "", "file holding the passphrase, or - for stdin")
	fs.StringVar(&f.keyFile, "key-file", "", "file holding a 64-hex-character key, or - for stdin")
	fs.StringVar(&f.passphrase, "passphrase", "", "deprecated: use -passphrase-file")
	fs.StringVar(&f.passphrase, "password", "", "deprecated: use -passphrase-file")
	fs.StringVar(&f.key, "key", "", "deprecated: use -key-file")
	return f
}

const bundleSecretFlagsUsage = `  -passphrase-file FILE   file holding the passphrase; - reads it from stdin
  -key-file FILE          file holding a 32-byte key as 64 hex characters; - reads it from stdin
  -passphrase, -password, -key
                          deprecated: the value is visible to every user on the host through ps
                          and is kept in shell history; use -passphrase-file or -key-file instead
`

// resolve returns the passphrase, or the decoded 32-byte key, the flags
// name. At most one of them is set; both empty means no encryption.
func (f *bundleSecretFlags) resolve(warn io.Writer) (string, []byte, error) {
	if f.passphrase != "" {
		fmt.Fprintln(warn, "warning: -passphrase/-password on the command line is deprecated and visible to other users through ps; use -passphrase-file FILE or -passphrase-file - (stdin)")
	}
	if f.key != "" {
		fmt.Fprintln(warn, "warning: -key on the command line is deprecated and visible to other users through ps; use -key-file FILE or -key-file - (stdin)")
	}
	if f.passphraseFile == "-" && f.keyFile == "-" {
		return "", nil, errors.New("-passphrase-file and -key-file cannot both read stdin")
	}

	pass := f.passphrase
	if f.passphraseFile != "" {
		if pass != "" {
			return "", nil, errors.New("give the passphrase once: -passphrase-file or -passphrase, not both")
		}
		data, err := f.read(f.passphraseFile)
		if err != nil {
			return "", nil, fmt.Errorf("reading passphrase: %w", err)
		}
		pass = strings.TrimRight(string(data), "\r\n")
		if pass == "" {
			return "", nil, fmt.Errorf("passphrase from %s is empty", f.passphraseFile)
		}
	}

	rawKey := f.key
	if f.keyFile != "" {
		if rawKey != "" {
			return "", nil, errors.New("give the key once: -key-file or -key, not both")
		}
		data, err := f.read(f.keyFile)
		if err != nil {
			return "", nil, fmt.Errorf("reading key: %w", err)
		}
		rawKey = string(data)
	}

	if pass != "" && rawKey != "" {
		return "", nil, errors.New("give either a passphrase or a key, not both")
	}
	if rawKey == "" {
		return pass, nil, nil
	}
	key, err := parseBundleKey(rawKey)
	if err != nil {
		return "", nil, err
	}
	return "", key, nil
}

func (f *bundleSecretFlags) read(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(f.stdin)
	}
	return os.ReadFile(path)
}

// parseBundleKey decodes a raw key, which must be exactly 32 bytes as 64
// hex characters. Anything else is an error rather than being run through
// PBKDF2 as a passphrase, which is what older versions did: a typo'd key
// silently became a different, weaker one.
func parseBundleKey(raw string) ([]byte, error) {
	trimmed := strings.TrimSpace(raw)
	decoded, err := hex.DecodeString(trimmed)
	if err != nil || len(decoded) != KeyLength {
		return nil, fmt.Errorf("key must be %d bytes written as %d hex characters (a bundle encrypted by an older grain with a non-hex -key decrypts with that value as -passphrase-file)", KeyLength, 2*KeyLength)
	}
	return decoded, nil
}
