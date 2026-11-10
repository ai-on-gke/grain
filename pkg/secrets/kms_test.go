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

package secrets

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testKMSKey = "projects/example-project/locations/us-central1/keyRings/grain/cryptoKeys/secrets-key"

func wrappedStore(t *testing.T) (*Store, *FakeKMS, string) {
	t.Helper()
	fake := StartFakeKMS(t)
	dir := t.TempDir()
	s := New(dir)
	if err := s.Set("github", "token", []byte("ghp_example")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WrapKeyWithKMS(context.Background(), testKMSKey, nil); err != nil {
		t.Fatalf("wrap: %v", err)
	}
	return s, fake, dir
}

func TestWrapKeyWithKMSWritesTheWrappedFormAndANewStoreUnwrapsIt(t *testing.T) {
	s, fake, dir := wrappedStore(t)
	data, err := os.ReadFile(s.KeyFile())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "grain-secret-key-kms-v1:"+testKMSKey+":") {
		t.Fatalf("key file is not wrapped by %s:\n%s", testKMSKey, data)
	}
	pub, _ := s.PublicKey()
	if strings.Contains(string(data), "grain-secret-key-v1:") {
		t.Fatal("the wrapped key file still carries the plaintext key")
	}
	if info, err := ReadKeyFileInfo(s.KeyFile()); err != nil || info.Protection != ProtectionKMS || info.Public != pub {
		t.Fatalf("ReadKeyFileInfo = %+v, %v", info, err)
	}
	if _, err := ReadKeyFile(s.KeyFile()); err == nil {
		t.Fatal("ReadKeyFile parsed a wrapped key as a plaintext one")
	}

	before := fake.Decrypts()
	reopened := New(dir)
	for range 3 {
		if v, err := reopened.Resolve(context.Background(), "github/token"); err != nil || v != "ghp_example" {
			t.Fatalf("Resolve through the wrapped key = %q, %v", v, err)
		}
	}
	if got := fake.Decrypts() - before; got != 1 {
		t.Fatalf("a store unwrapped %d times across three resolves, want once", got)
	}
	p := reopened.KeyProtection()
	if p.Mode != ProtectionKMS || p.KMSKey != testKMSKey || p.LastUnwrap.IsZero() || p.Error != "" {
		t.Fatalf("KeyProtection = %+v", p)
	}
}

func TestAWrappedKeyIsRewrappedAtLoadWhenKMSHasRotated(t *testing.T) {
	s, fake, dir := wrappedStore(t)
	old, _ := os.ReadFile(s.KeyFile())
	fake.Rotate()
	if _, err := New(dir).Resolve(context.Background(), "github/token"); err != nil {
		t.Fatal(err)
	}
	now, _ := os.ReadFile(s.KeyFile())
	if string(now) == string(old) {
		t.Fatal("the key file was not rewrapped after the KMS key rotated")
	}
	w, err := ParseWrappedKey(string(now))
	if err != nil {
		t.Fatal(err)
	}
	if _, usedPrimary, err := UnwrapKey(context.Background(), DefaultKMSClient(), w); err != nil || !usedPrimary {
		t.Fatalf("rewrapped key: usedPrimary=%v err=%v, want it under the primary", usedPrimary, err)
	}
}

func TestAStoreWhoseKeyKMSRefusesStaysOpenSaysWhyAndRetriesWithBackoff(t *testing.T) {
	_, fake, dir := wrappedStore(t)
	fake.Deny(true)
	s := New(dir)
	clock := time.Now()
	s.now = func() time.Time { return clock }
	_, err := s.Resolve(context.Background(), "github/token")
	if !errors.Is(err, ErrKMS) || !strings.Contains(err.Error(), "KMS permission denied for "+testKMSKey) {
		t.Fatalf("Resolve with KMS refusing = %v, want a permission-denied error naming the key", err)
	}
	if p := s.KeyProtection(); !strings.Contains(p.Error, "permission denied") {
		t.Fatalf("KeyProtection.Error = %q", p.Error)
	}
	calls := fake.Decrypts()
	if _, err := s.Resolve(context.Background(), "github/token"); !errors.Is(err, ErrKMS) {
		t.Fatalf("second Resolve = %v", err)
	}
	if fake.Decrypts() != calls {
		t.Fatal("a resolve inside the backoff window called KMS again")
	}
	if s.KeyRetryIn() <= 0 {
		t.Fatal("KeyRetryIn is 0 while waiting on KMS")
	}

	fake.Deny(false)
	clock = clock.Add(maxKeyBackoff)
	if v, err := s.Resolve(context.Background(), "github/token"); err != nil || v != "ghp_example" {
		t.Fatalf("Resolve once KMS answers = %q, %v", v, err)
	}
	if p := s.KeyProtection(); p.Error != "" || p.LastUnwrap.IsZero() {
		t.Fatalf("KeyProtection after recovery = %+v", p)
	}
}

func TestAnUnreachableKMSIsReportedAsUnreachable(t *testing.T) {
	_, fake, dir := wrappedStore(t)
	fake.Close()
	_, err := New(dir).Resolve(context.Background(), "github/token")
	if !errors.Is(err, ErrKMS) || !strings.Contains(err.Error(), "KMS unreachable for "+testKMSKey) {
		t.Fatalf("Resolve with KMS down = %v", err)
	}
}

func TestAWrappedKeyDoesNotUnwrapUnderAnotherDeploymentsPublicKey(t *testing.T) {
	s, _, _ := wrappedStore(t)
	data, _ := os.ReadFile(s.KeyFile())
	w, err := ParseWrappedKey(string(data))
	if err != nil {
		t.Fatal(err)
	}
	other, _ := GenerateKey()
	w.Public = other.Public()
	if _, _, err := UnwrapKey(context.Background(), DefaultKMSClient(), w); err == nil {
		t.Fatal("a wrapped key unwrapped under a public key it was not wrapped for")
	}
}

func TestUnwrapKeyFromKMSPutsThePlaintextKeyBack(t *testing.T) {
	s, _, dir := wrappedStore(t)
	pub, _ := s.PublicKey()
	rep, err := s.UnwrapKeyFromKMS(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Protection.Mode != ProtectionFile || rep.Public != pub {
		t.Fatalf("report = %+v", rep)
	}
	k, err := ReadKeyFile(filepath.Join(dir, DefaultKeyFileName))
	if err != nil || k.Public() != pub {
		t.Fatalf("the unwrapped key file reads as %v, %v", k.Public(), err)
	}
	if _, err := s.UnwrapKeyFromKMS(context.Background(), nil); err == nil {
		t.Fatal("unwrapping a plaintext key file succeeded")
	}
}

func TestWrapRefusesWhenKMSCannotRoundTripAndLeavesTheFile(t *testing.T) {
	fake := StartFakeKMS(t)
	s := New(t.TempDir())
	if err := s.Set("a", "b", []byte("c")); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(s.KeyFile())
	fake.Deny(true)
	if _, err := s.WrapKeyWithKMS(context.Background(), testKMSKey, nil); err == nil {
		t.Fatal("wrap succeeded with KMS refusing")
	}
	fake.Deny(false)
	fake.CorruptDecrypts(true)
	if _, err := s.WrapKeyWithKMS(context.Background(), testKMSKey, nil); err == nil {
		t.Fatal("wrap succeeded although the round trip came back different")
	}
	after, _ := os.ReadFile(s.KeyFile())
	if string(before) != string(after) {
		t.Fatal("a failed wrap changed the key file")
	}
	if _, err := s.WrapKeyWithKMS(context.Background(), "projects/p/keyRings/r", nil); err == nil {
		t.Fatal("wrap accepted a name that is not a CryptoKey")
	}
}

func TestWrapReportsPlaintextArchivesLeftBehind(t *testing.T) {
	StartFakeKMS(t)
	dir := t.TempDir()
	s := New(dir)
	first, _ := s.PublicKey()
	_ = first
	other, _ := GenerateKey()
	if err := s.ImportKey(other); err != nil {
		t.Fatal(err)
	}
	rep, err := s.WrapKeyWithKMS(context.Background(), testKMSKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.PlaintextCopies) != 1 || !strings.Contains(rep.PlaintextCopies[0], RetiredKeyMarker) {
		t.Fatalf("PlaintextCopies = %v, want the archive import left", rep.PlaintextCopies)
	}
	if len(rep.Notes) == 0 {
		t.Fatal("no note about the escrow")
	}
}

func TestParseWrappedKeyRefusesMalformedFiles(t *testing.T) {
	k, _ := GenerateKey()
	good := WrappedKey{KMSKey: testKMSKey, Ciphertext: []byte("ct"), Public: k.Public()}.String()
	if _, err := ParseWrappedKey(good); err != nil {
		t.Fatalf("ParseWrappedKey(good) = %v", err)
	}
	for _, bad := range []string{
		"grain-secret-key-kms-v1:" + testKMSKey + ":Y3Q=",
		"grain-secret-key-kms-v1:not-a-key:Y3Q=\n" + k.Public(),
		"grain-secret-key-kms-v1:" + testKMSKey + ":!!!\n" + k.Public(),
		"grain-secret-key-kms-v1:" + testKMSKey + ":Y3Q=\ngrain-secret-pub-v1:short",
	} {
		if _, err := ParseWrappedKey(bad); err == nil {
			t.Errorf("ParseWrappedKey(%q) succeeded", bad)
		}
	}
}
