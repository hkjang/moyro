package secrets

import (
	"bytes"
	"encoding/base64"
	"errors"
	"testing"
)

func testManager(t *testing.T, fill byte) *Manager {
	t.Helper()
	m, err := New(bytes.Repeat([]byte{fill}, MasterKeySize))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestSealRoundTripUsesFreshNonce(t *testing.T) {
	m := testManager(t, 0x41)
	a, err := m.Seal("settings/oidc/client-secret", []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Seal("settings/oidc/client-secret", []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a.Nonce, b.Nonce) || bytes.Equal(a.Ciphertext, b.Ciphertext) {
		t.Fatal("two encryptions reused output")
	}
	plain, err := m.Open("settings/oidc/client-secret", a)
	if err != nil || string(plain) != "secret" {
		t.Fatalf("Open() = %q, %v", plain, err)
	}
}

func TestOpenRejectsWrongContextTamperAndKey(t *testing.T) {
	m := testManager(t, 0x31)
	env, err := m.Seal("settings/ai/key", []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Open("settings/oidc/key", env); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("wrong context error = %v", err)
	}
	tampered := env
	tampered.Ciphertext = append([]byte(nil), env.Ciphertext...)
	tampered.Ciphertext[0] ^= 0xff
	if _, err := m.Open("settings/ai/key", tampered); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("tamper error = %v", err)
	}
	other := testManager(t, 0x32)
	if _, err := other.Open("settings/ai/key", env); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("wrong key error = %v", err)
	}
}

func TestDigestIsDomainSeparatedAndVerifiable(t *testing.T) {
	m := testManager(t, 0x55)
	a, _ := m.Digest("api-key/v1", []byte("value"))
	b, _ := m.Digest("webhook/v1", []byte("value"))
	if bytes.Equal(a, b) {
		t.Fatal("purposes produced identical digest")
	}
	if !m.VerifyDigest("api-key/v1", []byte("value"), a) {
		t.Fatal("valid digest rejected")
	}
	if m.VerifyDigest("api-key/v1", []byte("other"), a) {
		t.Fatal("invalid digest accepted")
	}
}

func TestNewRejectsWeakKeysAndAcceptsBase64(t *testing.T) {
	for _, key := range [][]byte{nil, make([]byte, 16), make([]byte, 32)} {
		if _, err := New(key); !errors.Is(err, ErrInvalidMasterKey) {
			t.Fatalf("New(%d bytes) error = %v", len(key), err)
		}
	}
	encoded := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x6a}, 32))
	if _, err := NewBase64(encoded); err != nil {
		t.Fatalf("NewBase64(): %v", err)
	}
}
