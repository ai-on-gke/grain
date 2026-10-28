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

// The encryption under the secrets file.
//
// Secrets live in grain's state repository (pkg/staterepo) alongside
// everything else it knows, which is the whole point of that repository
// -- one thing to clone, one thing to back up. That only works if the
// file is unreadable to everyone the repository is readable to, so the
// secrets file is the one file in there that is ciphertext.
//
// The scheme is a sealed box, built out of the standard library alone
// and nothing else: X25519 to an ephemeral key pair, HKDF-SHA256 to turn
// the shared secret into a key, and AES-256-GCM over the plaintext. That
// is the same shape age uses, and it is deliberately not age: adding a
// dependency to encrypt one file is a bigger commitment than fifty lines
// of composition of primitives that ship with the compiler, and the file
// this writes is grain's own -- nothing else has to read it.
//
// The private key is the operator's to manage, and grain never puts a
// copy of it anywhere but the one file it is told to read (never inside
// the repository -- staterepo's own .gitignore names it). Losing it
// means losing every secret in the file: there is no recovery path here
// and there is deliberately no escrow.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The armour a secrets file is written in: a text header of base64
// fields and then the ciphertext, wrapped. Text rather than raw bytes
// because this file lives in a git repository -- a binary blob would
// make every write a full-file diff with nothing readable in it, and
// this way a reviewer can at least see that only the ciphertext moved.
const (
	ArmourBegin = "-----BEGIN GRAIN SECRETS-----"
	ArmourEnd   = "-----END GRAIN SECRETS-----"
	armourBegin = ArmourBegin
	armourEnd   = ArmourEnd
	// keyPrefix and pubPrefix tag the two halves so neither can be
	// mistaken for the other by eye or by a paste into the wrong field --
	// which matters, because one of them is publishable and the other one
	// ends the deployment's confidentiality if it leaks.
	keyPrefix = "grain-secret-key-v1:"
	pubPrefix = "grain-secret-pub-v1:"
	// hkdfInfo domain-separates this use of the shared secret. It is part
	// of the file format: changing it makes every existing file
	// undecryptable.
	hkdfInfo = "grain secrets v1"
)

// ErrNoKey is returned when the private key file is not there. It is
// distinguishable on purpose: a missing key with no secrets file yet is
// a new install, which generates one, and a missing key with a secrets
// file beside it is an operator who has lost their key, which is
// unrecoverable and must say so rather than quietly starting over with a
// new key and an unreadable file.
var ErrNoKey = errors.New("secrets: no private key")

// ErrWrongKey is returned when the file was encrypted to a different
// public key than the one loaded.
var ErrWrongKey = errors.New("secrets: this file is encrypted to a different key")

// Key is the operator's secrets key: the X25519 private key grain
// decrypts with, and whose public half it encrypts to.
type Key struct{ priv *ecdh.PrivateKey }

// GenerateKey mints a new secrets key.
func GenerateKey() (Key, error) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return Key{}, fmt.Errorf("secrets: generating a key: %w", err)
	}
	return Key{priv: priv}, nil
}

// Public renders the public half, which is safe to show, store and paste
// anywhere -- it can encrypt a secret but not read one back.
func (k Key) Public() string {
	if k.priv == nil {
		return ""
	}
	return pubPrefix + base64.StdEncoding.EncodeToString(k.priv.PublicKey().Bytes())
}

// String renders the private half in the form WriteKeyFile writes and
// ParseKey reads. This is the material an operator keeps: whoever holds
// it can read every secret grain has.
func (k Key) String() string {
	if k.priv == nil {
		return ""
	}
	return keyPrefix + base64.StdEncoding.EncodeToString(k.priv.Bytes())
}

// ParseKey reads a key back from the form String renders, tolerating the
// surrounding whitespace a copy and paste picks up.
func ParseKey(s string) (Key, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, keyPrefix) {
		return Key{}, fmt.Errorf("secrets: a key must start with %q", keyPrefix)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(strings.TrimPrefix(s, keyPrefix)))
	if err != nil {
		return Key{}, fmt.Errorf("secrets: the key is not valid base64: %w", err)
	}
	priv, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return Key{}, fmt.Errorf("secrets: the key is not a valid X25519 key: %w", err)
	}
	return Key{priv: priv}, nil
}

// ReadKeyFile loads the key at path, reporting ErrNoKey when there is
// none there yet.
func ReadKeyFile(path string) (Key, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Key{}, fmt.Errorf("%w at %s", ErrNoKey, path)
	}
	if err != nil {
		return Key{}, fmt.Errorf("secrets: reading %s: %w", path, err)
	}
	return ParseKey(string(data))
}

// WriteKeyFile writes the key 0600, through a temporary file in the same
// directory so the material never exists at the final path with looser
// permissions and no reader ever sees half a key.
func WriteKeyFile(path string, k Key) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("secrets: preparing %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".secrets-key-*")
	if err != nil {
		return fmt.Errorf("secrets: creating a temporary file next to %s: %w", path, err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("secrets: securing %s: %w", tmp.Name(), err)
	}
	if _, err := tmp.WriteString(k.String() + "\n"); err != nil {
		tmp.Close()
		return fmt.Errorf("secrets: writing %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Encrypt seals plaintext to the key's public half.
//
// A fresh ephemeral key pair per call means a fresh AES key and so a
// fresh nonce space per file, which is what makes the fixed all-zero
// nonce below safe: the key is used for exactly one message, ever.
func Encrypt(k Key, plaintext []byte) ([]byte, error) {
	if k.priv == nil {
		return nil, errors.New("secrets: no key to encrypt to")
	}
	recipient := k.priv.PublicKey()
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("secrets: generating an ephemeral key: %w", err)
	}
	shared, err := eph.ECDH(recipient)
	if err != nil {
		return nil, fmt.Errorf("secrets: deriving the shared secret: %w", err)
	}
	aead, err := newAEAD(shared, eph.PublicKey().Bytes(), recipient.Bytes())
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	sealed := aead.Seal(nil, nonce, plaintext, nil)
	return armour(recipient.Bytes(), eph.PublicKey().Bytes(), sealed), nil
}

// Recipient reports which public key a sealed file was encrypted to, in
// the same form Key.Public renders.
//
// The armour header carries it in clear, which is the point: an operator
// restoring an installation onto a new host, holding several keys, needs
// to be told *which* one this file wants, and answering that must not
// require already being able to read the file. It reveals nothing -- a
// public key encrypts secrets, it does not read them.
func Recipient(data []byte) (string, error) {
	recipient, _, _, err := unarmour(data)
	if err != nil {
		return "", err
	}
	return pubPrefix + base64.StdEncoding.EncodeToString(recipient), nil
}

// Decrypt opens a file Encrypt wrote.
func Decrypt(k Key, data []byte) ([]byte, error) {
	if k.priv == nil {
		return nil, errors.New("secrets: no key to decrypt with")
	}
	recipient, ephBytes, sealed, err := unarmour(data)
	if err != nil {
		return nil, err
	}
	mine := k.priv.PublicKey().Bytes()
	if string(recipient) != string(mine) {
		return nil, ErrWrongKey
	}
	eph, err := ecdh.X25519().NewPublicKey(ephBytes)
	if err != nil {
		return nil, fmt.Errorf("secrets: the file's ephemeral key is not valid: %w", err)
	}
	shared, err := k.priv.ECDH(eph)
	if err != nil {
		return nil, fmt.Errorf("secrets: deriving the shared secret: %w", err)
	}
	aead, err := newAEAD(shared, ephBytes, mine)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	out, err := aead.Open(nil, nonce, sealed, nil)
	if err != nil {
		return nil, fmt.Errorf("secrets: the file did not decrypt (it may be corrupt): %w", err)
	}
	return out, nil
}

// newAEAD derives the message key from the shared secret, binding both
// public keys into the salt so a ciphertext cannot be replayed under a
// different pair of them.
func newAEAD(shared, ephPub, recipientPub []byte) (cipher.AEAD, error) {
	salt := append(append([]byte{}, ephPub...), recipientPub...)
	key, err := hkdf.Key(sha256.New, shared, salt, hkdfInfo, 32)
	if err != nil {
		return nil, fmt.Errorf("secrets: deriving the message key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func armour(recipient, eph, sealed []byte) []byte {
	var b strings.Builder
	b.WriteString(armourBegin + "\n")
	b.WriteString("recipient: " + base64.StdEncoding.EncodeToString(recipient) + "\n")
	b.WriteString("ephemeral: " + base64.StdEncoding.EncodeToString(eph) + "\n")
	b.WriteString("\n")
	body := base64.StdEncoding.EncodeToString(sealed)
	for len(body) > 64 {
		b.WriteString(body[:64] + "\n")
		body = body[64:]
	}
	b.WriteString(body + "\n")
	b.WriteString(armourEnd + "\n")
	return []byte(b.String())
}

func unarmour(data []byte) (recipient, eph, sealed []byte, err error) {
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 4 || strings.TrimSpace(lines[0]) != armourBegin {
		return nil, nil, nil, errors.New("secrets: this is not a grain secrets file")
	}
	var body strings.Builder
	inBody := false
	foundEnd := false
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		switch {
		case line == armourEnd:
			inBody = false
			foundEnd = true
		case line == "":
			inBody = true
		case !inBody && strings.HasPrefix(line, "recipient: "):
			recipient, err = base64.StdEncoding.DecodeString(strings.TrimPrefix(line, "recipient: "))
		case !inBody && strings.HasPrefix(line, "ephemeral: "):
			eph, err = base64.StdEncoding.DecodeString(strings.TrimPrefix(line, "ephemeral: "))
		case inBody:
			body.WriteString(line)
		}
		if err != nil {
			return nil, nil, nil, fmt.Errorf("secrets: the file's header is not valid base64: %w", err)
		}
	}
	if !foundEnd {
		return nil, nil, nil, errors.New("secrets: the file is truncated or corrupted (missing footer)")
	}
	if len(recipient) == 0 || len(eph) == 0 {
		return nil, nil, nil, errors.New("secrets: the file's header is incomplete")
	}
	sealed, err = base64.StdEncoding.DecodeString(body.String())
	if err != nil {
		return nil, nil, nil, fmt.Errorf("secrets: the file's body is not valid base64: %w", err)
	}
	return recipient, eph, sealed, nil
}
