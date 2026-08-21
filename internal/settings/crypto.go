// Package settings stores runtime configuration in the database so it can be
// managed from the dashboard rather than by editing a file.
//
// Configuration splits in two, and the split is forced rather than chosen. A
// handful of settings are needed before the database exists -- where the
// database is, what address to listen on -- and those stay in a small
// bootstrap file. Everything else lives here.
package settings

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// ErrNoKey reports that a secret was written or read without an encryption key
// configured.
var ErrNoKey = errors.New("settings: no encryption key is configured")

// ErrDecrypt reports that a stored secret could not be decrypted. In practice
// this means the key changed, so the message says so rather than leaving an
// operator to guess at corruption.
var ErrDecrypt = errors.New(
	"settings: a stored secret could not be decrypted. The encryption key has " +
		"changed or is not the one this database was written with; re-enter the " +
		"affected secrets, or restore the original key")

// Cipher encrypts secret settings at rest.
//
// AES-256-GCM with a random nonce per value. GCM rather than CBC because a
// stored secret must be tamper-evident as well as unreadable: an attacker with
// write access to the database file should not be able to flip bits in an API
// key and have it silently decrypt to something else.
//
// The key never enters the database. That is the whole point -- it is what
// makes a stolen database file useless, and it is why the key lives in the
// environment or a file the way every other credential here does.
type Cipher struct {
	aead cipher.AEAD
}

// NewCipher derives a cipher from a key.
//
// The supplied material is hashed to exactly 32 bytes rather than being used
// raw, so an operator can paste a passphrase or a base64 key and either works.
// This is not a password KDF and is not meant to be: the key is expected to be
// generated, not chosen, and mcpd -init generates one.
func NewCipher(key string) (*Cipher, error) {
	if strings.TrimSpace(key) == "" {
		return nil, ErrNoKey
	}
	if len(key) < 32 {
		return nil, fmt.Errorf(
			"settings: the encryption key must be at least 32 characters; " +
				"generate one with: openssl rand -base64 32")
	}

	sum := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, fmt.Errorf("settings: build cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("settings: build GCM: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt returns base64 ciphertext with the nonce prefixed.
func (c *Cipher) Encrypt(plaintext string) (string, error) {
	if c == nil {
		return "", ErrNoKey
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("settings: system entropy unavailable: %w", err)
	}
	sealed := c.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt reverses Encrypt.
func (c *Cipher) Decrypt(encoded string) (string, error) {
	if c == nil {
		return "", ErrNoKey
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", ErrDecrypt
	}
	if len(raw) < c.aead.NonceSize() {
		return "", ErrDecrypt
	}
	nonce, ciphertext := raw[:c.aead.NonceSize()], raw[c.aead.NonceSize():]

	plaintext, err := c.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		// GCM authentication failed: a wrong key and a tampered value are
		// indistinguishable, and both mean the same thing to an operator.
		return "", ErrDecrypt
	}
	return string(plaintext), nil
}

// GenerateKey returns a new random encryption key.
func GenerateKey() (string, error) {
	buf := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", fmt.Errorf("settings: system entropy unavailable: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// ResolveKey reads the encryption key from a reference.
//
// It accepts the same env:, file: and credential: forms as every other secret
// in mcpd, so the key is configured the way an operator already understands.
func ResolveKey(ref string, credentialsDir string) (string, error) {
	scheme, name, ok := strings.Cut(strings.TrimSpace(ref), ":")
	if !ok {
		return "", fmt.Errorf(
			"settings: encryption key reference %q has no scheme; "+
				"use env:, file: or credential:", ref)
	}

	switch scheme {
	case "env":
		v, present := os.LookupEnv(name)
		if !present || v == "" {
			return "", fmt.Errorf("settings: %s is not set", name)
		}
		return v, nil

	case "file":
		return readKeyFile(name)

	case "credential":
		if credentialsDir == "" {
			return "", fmt.Errorf(
				"settings: %q requests a systemd credential but "+
					"CREDENTIALS_DIRECTORY is unset", ref)
		}
		if strings.ContainsAny(name, `/\`) {
			return "", fmt.Errorf("settings: credential name %q must not contain a path", name)
		}
		return readKeyFile(credentialsDir + "/" + name)

	default:
		return "", fmt.Errorf("settings: unknown key scheme %q", scheme)
	}
}

func readKeyFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("settings: read encryption key from %s: %w", path, err)
	}
	v := strings.TrimRight(string(b), "\r\n")
	if v == "" {
		return "", fmt.Errorf("settings: encryption key file %s is empty", path)
	}
	return v, nil
}
