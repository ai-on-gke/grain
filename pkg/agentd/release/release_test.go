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

package release

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEmbeddedPublicKeyLoadsAndRoundTrips(t *testing.T) {
	pub, err := EmbeddedPublicKey()
	if err != nil {
		t.Fatalf("EmbeddedPublicKey: %v", err)
	}
	pemBytes, err := EmbeddedPublicKeyPEM()
	if err != nil {
		t.Fatalf("EmbeddedPublicKeyPEM: %v", err)
	}
	parsed, err := ParsePublicKey(string(pemBytes))
	if err != nil {
		t.Fatalf("ParsePublicKey(PEM): %v", err)
	}
	if !bytes.Equal(pub, parsed) {
		t.Fatalf("round-trip public key mismatch: %x vs %x", pub, parsed)
	}
	// Also parses ed25519:<base64> spelling.
	parsed2, err := ParsePublicKey(PublicKeyString(pub))
	if err != nil || !bytes.Equal(pub, parsed2) {
		t.Fatalf("ParsePublicKey(ed25519:...): %v", err)
	}
}

func TestSignVerifyAndTamperRefusal(t *testing.T) {
	pub, priv, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	binary := []byte("fake-static-elf-binary-bytes")
	m, err := NewManifestForBinary("v3.5.0", "linux/amd64", binary, nil)
	if err != nil {
		t.Fatalf("NewManifestForBinary: %v", err)
	}
	if _, err := VerifyBinary(m, binary, pub); err == nil {
		t.Fatal("unsigned manifest passed VerifyBinary")
	}

	signed, err := SignManifest(m, priv, nil)
	if err != nil {
		t.Fatalf("SignManifest: %v", err)
	}
	raw, err := json.MarshalIndent(signed, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := ParseManifest(raw)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	gotPub, err := VerifyBinary(decoded, binary, pub)
	if err != nil {
		t.Fatalf("VerifyBinary: %v", err)
	}
	if !bytes.Equal(gotPub, pub) {
		t.Errorf("resolved signer %x, want %x", gotPub, pub)
	}

	// Wrong pinned key refuses.
	otherPub, _, _ := GenerateKeyPair()
	if _, err := VerifyBinary(decoded, binary, otherPub); err == nil {
		t.Error("VerifyBinary accepted manifest signed by unpinned key")
	}

	// Tampered binary bytes refused.
	if _, err := VerifyBinary(decoded, append([]byte(nil), "tampered"...), pub); err == nil {
		t.Error("VerifyBinary accepted tampered binary")
	}

	// Tampered version in manifest refused.
	bad := decoded
	bad.Version = "v3.5.1"
	if _, err := VerifyBinary(bad, binary, pub); err == nil {
		t.Error("VerifyBinary accepted tampered manifest version")
	}

	// Unknown JSON field refused (blocks duplicate/smuggled field attacks).
	withUnknown := strings.TrimSuffix(strings.TrimSpace(string(raw)), "}") + `,"extra":"nope"}`
	if _, err := ParseManifest([]byte(withUnknown)); err == nil {
		t.Error("ParseManifest accepted unknown JSON field")
	}
}

func TestKeyRotationOldKeySignsNewForwardOnly(t *testing.T) {
	pubA, privA, _ := GenerateKeyPair()
	pubB, privB, _ := GenerateKeyPair()
	pubC, privC, _ := GenerateKeyPair()

	t1 := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 12, 10, 12, 0, 0, 0, time.UTC)

	rotAB, err := SignKeyRotation(privA, pubB, t1)
	if err != nil {
		t.Fatalf("SignKeyRotation A->B: %v", err)
	}
	rotBC, err := SignKeyRotation(privB, pubC, t2)
	if err != nil {
		t.Fatalf("SignKeyRotation B->C: %v", err)
	}

	binary := []byte("grain-agentd-v3.8.0-binary")
	m, err := NewManifestForBinary("v3.8.0", "linux/arm64", binary, []string{"start"})
	if err != nil {
		t.Fatal(err)
	}
	signed, err := SignManifest(m, privC, []KeyRotation{rotAB, rotBC})
	if err != nil {
		t.Fatalf("SignManifest with A->B->C rotations: %v", err)
	}

	// Host pinned to A walks A->B->C and resolves C.
	for name, pinned := range map[string][]byte{
		"pinned-to-oldest-A": pubA,
		"pinned-to-middle-B": pubB,
		"pinned-to-newest-C": pubC,
	} {
		t.Run(name, func(t *testing.T) {
			resolved, err := VerifyBinary(signed, binary, pinned)
			if err != nil {
				t.Fatalf("VerifyBinary(%s): %v", name, err)
			}
			if !bytes.Equal(resolved, pubC) {
				t.Errorf("resolved %s, want newest key C (%s)", KeyID(resolved), KeyID(pubC))
			}
		})
	}

	// Retired key A can NEVER sign a manifest accepted by a host already pinned to B or C.
	signedByRetiredA, err := SignManifest(m, privA, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBinary(signedByRetiredA, binary, pubB); err == nil {
		t.Error("host pinned to B accepted manifest signed by retired key A")
	}
	if _, err := VerifyBinary(signedByRetiredA, binary, pubC); err == nil {
		t.Error("host pinned to C accepted manifest signed by retired key A")
	}

	// Forged rotation (not signed by A) is rejected.
	forged := rotAB
	forgedSig := make([]byte, 64)
	forged.Signature = "ed25519:" + base64.StdEncoding.EncodeToString(forgedSig)
	badSigned := signed
	badSigned.Rotations = []KeyRotation{forged, rotBC}
	if _, err := VerifyBinary(badSigned, binary, pubA); err == nil {
		t.Error("VerifyBinary accepted forged rotation")
	}
}

func TestOpenSSLInteroperability(t *testing.T) {
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl not installed")
	}
	dir := t.TempDir()
	privPath := filepath.Join(dir, "priv.pem")
	pubPath := filepath.Join(dir, "pub.pem")

	if out, err := exec.Command("openssl", "genpkey", "-algorithm", "Ed25519", "-out", privPath).CombinedOutput(); err != nil {
		t.Fatalf("openssl genpkey: %v\n%s", err, out)
	}
	if out, err := exec.Command("openssl", "pkey", "-in", privPath, "-pubout", "-out", pubPath).CombinedOutput(); err != nil {
		t.Fatalf("openssl pkey -pubout: %v\n%s", err, out)
	}

	privRaw, _ := os.ReadFile(privPath)
	pubRaw, _ := os.ReadFile(pubPath)
	priv, err := ParsePrivateKey(string(privRaw))
	if err != nil {
		t.Fatalf("ParsePrivateKey(openssl PEM): %v", err)
	}
	pub, err := ParsePublicKey(string(pubRaw))
	if err != nil {
		t.Fatalf("ParsePublicKey(openssl PEM): %v", err)
	}

	// Sign in OpenSSL, verify in Go.
	binary := []byte("agentd-openssl-interop")
	m, err := NewManifestForBinary("v3.6.0", "linux/amd64", binary, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.KeyID = KeyID(pub)
	m.Signature = formatSignature(make([]byte, 64))
	msg, err := m.CanonicalText()
	if err != nil {
		t.Fatal(err)
	}
	msgPath := filepath.Join(dir, "msg.txt")
	sigPath := filepath.Join(dir, "sig.bin")
	if err := os.WriteFile(msgPath, []byte(msg), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("openssl", "pkeyutl", "-sign", "-inkey", privPath, "-rawin", "-in", msgPath, "-out", sigPath).CombinedOutput(); err != nil {
		t.Fatalf("openssl pkeyutl -sign: %v\n%s", err, out)
	}
	sigBytes, _ := os.ReadFile(sigPath)
	m.Signature = "ed25519:" + base64.StdEncoding.EncodeToString(sigBytes)
	if _, err := VerifyBinary(m, binary, pub); err != nil {
		t.Fatalf("Go VerifyBinary failed on OpenSSL signature: %v", err)
	}

	// Sign in Go, verify with OpenSSL against FormatPublicKeyPEM (with # comment header).
	goSigned, err := SignManifest(m, priv, nil)
	if err != nil {
		t.Fatal(err)
	}
	formattedPub, err := FormatPublicKeyPEM(pub)
	if err != nil {
		t.Fatal(err)
	}
	commentedPubPath := filepath.Join(dir, "agentd-release.pub")
	if err := os.WriteFile(commentedPubPath, formattedPub, 0o644); err != nil {
		t.Fatal(err)
	}
	goSigBytes, err := parseSignature(goSigned.Signature)
	if err != nil {
		t.Fatal(err)
	}
	goSigPath := filepath.Join(dir, "gosig.bin")
	if err := os.WriteFile(goSigPath, goSigBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("openssl", "pkeyutl", "-verify", "-pubin", "-inkey", commentedPubPath, "-rawin", "-in", msgPath, "-sigfile", goSigPath).CombinedOutput(); err != nil {
		t.Fatalf("openssl pkeyutl -verify failed on Go signature + commented PEM: %v\n%s", err, out)
	}
}
