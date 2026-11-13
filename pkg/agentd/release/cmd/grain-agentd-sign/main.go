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

// Command grain-agentd-sign generates Ed25519 release signing keys, writes and
// signs per-platform grain-agentd release manifests, verifies manifests and
// binaries against a pinned release public key, and signs key-rotation records
// ("the old key signs the new").
//
// It is an offline / CI release tool and never runs inside the controller.
package main

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/agentd/release"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "keygen":
		return cmdKeygen(args[1:], stdout, stderr)
	case "manifest":
		return cmdManifest(args[1:], stdout, stderr)
	case "sign":
		return cmdSign(args[1:], stdout, stderr)
	case "verify":
		return cmdVerify(args[1:], stdout, stderr)
	case "rotate":
		return cmdRotate(args[1:], stdout, stderr)
	case "pubkey-string":
		pub, err := release.ParsePublicKey(os.Getenv("AGENTD_RELEASE_PUBLIC_KEY"))
		if err != nil {
			fmt.Fprintf(stderr, "grain-agentd-sign pubkey-string: %v\n", err)
			return 1
		}
		fmt.Fprint(stdout, release.PublicKeyString(pub))
		return 0
	case "-h", "--help", "help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "grain-agentd-sign: unknown subcommand %q\n", args[0])
		usage(stderr)
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, `Usage: grain-agentd-sign <subcommand> [flags]

Subcommands:
  keygen    Generate an Ed25519 release signing keypair (private PEM + agentd-release.pub)
  manifest  Write (and optionally sign) a per-platform grain-agentd manifest.json
  sign      Sign an existing grain-agentd manifest.json with a release private key
  verify    Verify a manifest.json (and optional binary) against a pinned public key
  rotate    Sign a key-rotation record delegating from an old private key to a new public key`)
}

func cmdKeygen(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	privOut := fs.String("private-key", "agentd-release.key", "output path for the PKCS#8 Ed25519 private key (mode 0600)")
	pubOut := fs.String("public-key", "agentd-release.pub", "output path for the Ed25519 public key (mode 0644)")
	force := fs.Bool("force", false, "overwrite existing key files")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !*force {
		for _, p := range []string{*privOut, *pubOut} {
			if _, err := os.Stat(p); err == nil {
				fmt.Fprintf(stderr, "grain-agentd-sign keygen: %s already exists (pass -force to overwrite)\n", p)
				return 1
			}
		}
	}
	pub, priv, err := release.GenerateKeyPair()
	if err != nil {
		fmt.Fprintf(stderr, "grain-agentd-sign keygen: %v\n", err)
		return 1
	}
	privPEM, err := release.FormatPrivateKeyPEM(priv)
	if err != nil {
		fmt.Fprintf(stderr, "grain-agentd-sign keygen: %v\n", err)
		return 1
	}
	pubPEM, err := release.FormatPublicKeyPEM(pub)
	if err != nil {
		fmt.Fprintf(stderr, "grain-agentd-sign keygen: %v\n", err)
		return 1
	}
	if err := os.MkdirAll(filepath.Dir(*privOut), 0o700); err != nil {
		fmt.Fprintf(stderr, "grain-agentd-sign keygen: %v\n", err)
		return 1
	}
	if err := os.WriteFile(*privOut, privPEM, 0o600); err != nil {
		fmt.Fprintf(stderr, "grain-agentd-sign keygen: %v\n", err)
		return 1
	}
	if err := os.MkdirAll(filepath.Dir(*pubOut), 0o755); err != nil {
		fmt.Fprintf(stderr, "grain-agentd-sign keygen: %v\n", err)
		return 1
	}
	if err := os.WriteFile(*pubOut, pubPEM, 0o644); err != nil {
		fmt.Fprintf(stderr, "grain-agentd-sign keygen: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "generated key_id %s\n  private: %s (keep outside the controller)\n  public:  %s\n", release.KeyID(pub), *privOut, *pubOut)
	return 0
}

func cmdManifest(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("manifest", flag.ContinueOnError)
	fs.SetOutput(stderr)
	binPath := fs.String("binary", "", "path to built grain-agentd binary (required)")
	platform := fs.String("platform", "", "target platform, e.g. linux/amd64 (required)")
	ver := fs.String("version", "", "release version string (defaults to \"unversioned\")")
	minFeatures := fs.String("min-features", "", "comma-separated minimum contract features (defaults to current floor)")
	signingKey := fs.String("signing-key", "", "optional path to Ed25519 private key PEM to sign immediately")
	rotationsFile := fs.String("rotations", "", "optional path to JSON array of KeyRotation records")
	outPath := fs.String("out", "", "output manifest path (defaults to <binary>.manifest.json)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *binPath == "" || *platform == "" {
		fmt.Fprintln(stderr, "grain-agentd-sign manifest: -binary and -platform are required")
		return 2
	}
	rawBin, err := os.ReadFile(*binPath)
	if err != nil {
		fmt.Fprintf(stderr, "grain-agentd-sign manifest: %v\n", err)
		return 1
	}
	var minList []string
	if strings.TrimSpace(*minFeatures) != "" {
		minList = strings.Split(strings.TrimSpace(*minFeatures), ",")
	}
	m, err := release.NewManifestForBinary(*ver, *platform, rawBin, minList)
	if err != nil {
		fmt.Fprintf(stderr, "grain-agentd-sign manifest: %v\n", err)
		return 1
	}
	if *signingKey != "" {
		m, err = signWithFiles(m, *signingKey, *rotationsFile)
		if err != nil {
			fmt.Fprintf(stderr, "grain-agentd-sign manifest: %v\n", err)
			return 1
		}
	}
	dest := *outPath
	if dest == "" {
		dest = *binPath + ".manifest.json"
	}
	if err := writeJSON(dest, m); err != nil {
		fmt.Fprintf(stderr, "grain-agentd-sign manifest: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote %s (sha256 %s, signed=%v)\n", dest, m.SHA256, m.Signature != "")
	return 0
}

func cmdSign(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sign", flag.ContinueOnError)
	fs.SetOutput(stderr)
	manifestPath := fs.String("manifest", "", "path to manifest.json to sign in place (required)")
	signingKey := fs.String("signing-key", "", "path to Ed25519 private key PEM (required)")
	rotationsFile := fs.String("rotations", "", "optional path to JSON array of KeyRotation records")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *manifestPath == "" || *signingKey == "" {
		fmt.Fprintln(stderr, "grain-agentd-sign sign: -manifest and -signing-key are required")
		return 2
	}
	raw, err := os.ReadFile(*manifestPath)
	if err != nil {
		fmt.Fprintf(stderr, "grain-agentd-sign sign: %v\n", err)
		return 1
	}
	m, err := release.ParseManifest(raw)
	if err != nil {
		fmt.Fprintf(stderr, "grain-agentd-sign sign: %v\n", err)
		return 1
	}
	signed, err := signWithFiles(m, *signingKey, *rotationsFile)
	if err != nil {
		fmt.Fprintf(stderr, "grain-agentd-sign sign: %v\n", err)
		return 1
	}
	if err := writeJSON(*manifestPath, signed); err != nil {
		fmt.Fprintf(stderr, "grain-agentd-sign sign: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "signed %s with key %s\n", *manifestPath, signed.KeyID)
	return 0
}

func cmdVerify(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	manifestPath := fs.String("manifest", "", "path to manifest.json (required)")
	binPath := fs.String("binary", "", "optional path to binary to verify SHA-256 and size against")
	pubPath := fs.String("public-key", "", "path to pinned release public key (defaults to embedded release public key)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *manifestPath == "" {
		fmt.Fprintln(stderr, "grain-agentd-sign verify: -manifest is required")
		return 2
	}
	raw, err := os.ReadFile(*manifestPath)
	if err != nil {
		fmt.Fprintf(stderr, "grain-agentd-sign verify: %v\n", err)
		return 1
	}
	m, err := release.ParseManifest(raw)
	if err != nil {
		fmt.Fprintf(stderr, "grain-agentd-sign verify: %v\n", err)
		return 1
	}
	var pinnedPub ed25519.PublicKey
	if *pubPath != "" {
		pubRaw, err := os.ReadFile(*pubPath)
		if err != nil {
			fmt.Fprintf(stderr, "grain-agentd-sign verify: %v\n", err)
			return 1
		}
		pinnedPub, err = release.ParsePublicKey(string(pubRaw))
		if err != nil {
			fmt.Fprintf(stderr, "grain-agentd-sign verify: %v\n", err)
			return 1
		}
	} else {
		pinnedPub, err = release.EmbeddedPublicKey()
		if err != nil {
			fmt.Fprintf(stderr, "grain-agentd-sign verify: %v\n", err)
			return 1
		}
	}
	var signer ed25519.PublicKey
	if *binPath != "" {
		binBytes, err := os.ReadFile(*binPath)
		if err != nil {
			fmt.Fprintf(stderr, "grain-agentd-sign verify: %v\n", err)
			return 1
		}
		signer, err = release.VerifyBinary(m, binBytes, pinnedPub)
		if err != nil {
			fmt.Fprintf(stderr, "grain-agentd-sign verify: %v\n", err)
			return 1
		}
	} else {
		signer, err = release.VerifyManifest(m, pinnedPub)
		if err != nil {
			fmt.Fprintf(stderr, "grain-agentd-sign verify: %v\n", err)
			return 1
		}
	}
	fmt.Fprintf(stdout, "verified %s (%s %s, signer key_id %s)\n", *manifestPath, m.Asset, m.Version, release.KeyID(signer))
	return 0
}

func cmdRotate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("rotate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	oldKeyPath := fs.String("old-private-key", "", "path to outgoing Ed25519 private key PEM (required)")
	newPubPath := fs.String("new-public-key", "", "path to replacement Ed25519 public key PEM (required)")
	chainPath := fs.String("chain", "agentd-rotations.json", "path to JSON rotation chain file to create or append to")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *oldKeyPath == "" || *newPubPath == "" {
		fmt.Fprintln(stderr, "grain-agentd-sign rotate: -old-private-key and -new-public-key are required")
		return 2
	}
	oldRaw, err := os.ReadFile(*oldKeyPath)
	if err != nil {
		fmt.Fprintf(stderr, "grain-agentd-sign rotate: %v\n", err)
		return 1
	}
	oldPriv, err := release.ParsePrivateKey(string(oldRaw))
	if err != nil {
		fmt.Fprintf(stderr, "grain-agentd-sign rotate: %v\n", err)
		return 1
	}
	newRaw, err := os.ReadFile(*newPubPath)
	if err != nil {
		fmt.Fprintf(stderr, "grain-agentd-sign rotate: %v\n", err)
		return 1
	}
	newPub, err := release.ParsePublicKey(string(newRaw))
	if err != nil {
		fmt.Fprintf(stderr, "grain-agentd-sign rotate: %v\n", err)
		return 1
	}
	var chain []release.KeyRotation
	if existing, err := os.ReadFile(*chainPath); err == nil {
		chain, err = release.ParseRotations(existing)
		if err != nil {
			fmt.Fprintf(stderr, "grain-agentd-sign rotate: existing %s: %v\n", *chainPath, err)
			return 1
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stderr, "grain-agentd-sign rotate: %v\n", err)
		return 1
	}
	hop, err := release.SignKeyRotation(oldPriv, newPub, time.Now().UTC())
	if err != nil {
		fmt.Fprintf(stderr, "grain-agentd-sign rotate: %v\n", err)
		return 1
	}
	chain = append(chain, hop)
	rawCheck, _ := json.Marshal(chain)
	if _, err := release.ParseRotations(rawCheck); err != nil {
		fmt.Fprintf(stderr, "grain-agentd-sign rotate: %v\n", err)
		return 1
	}
	if err := writeJSON(*chainPath, chain); err != nil {
		fmt.Fprintf(stderr, "grain-agentd-sign rotate: %v\n", err)
		return 1
	}
	oldPub := oldPriv.Public().(ed25519.PublicKey)
	fmt.Fprintf(stdout, "appended key rotation %s -> %s in %s (%d total hop(s))\n", release.KeyID(oldPub), release.KeyID(newPub), *chainPath, len(chain))
	return 0
}

func signWithFiles(m release.Manifest, keyPath, rotationsPath string) (release.Manifest, error) {
	privRaw, err := os.ReadFile(keyPath)
	if err != nil {
		return release.Manifest{}, err
	}
	priv, err := release.ParsePrivateKey(string(privRaw))
	if err != nil {
		return release.Manifest{}, err
	}
	var rotations []release.KeyRotation
	if rotationsPath != "" {
		rotRaw, err := os.ReadFile(rotationsPath)
		if err != nil {
			return release.Manifest{}, err
		}
		rotations, err = release.ParseRotations(rotRaw)
		if err != nil {
			return release.Manifest{}, err
		}
	}
	return release.SignManifest(m, priv, rotations)
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o644)
}
