// Package secrets provides domain-separated encryption and keyed hashing from
// Moyro's single boot-time ENCRYPTION_KEY. It intentionally contains no storage
// logic so ciphertext can live in PostgreSQL without coupling callers to a
// particular schema.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
)

const (
	MasterKeySize = 32
	Version       = 1
)

var (
	ErrInvalidMasterKey = errors.New("secrets: master key must be exactly 32 non-zero bytes")
	ErrInvalidEnvelope  = errors.New("secrets: invalid encrypted envelope")
	ErrUnknownKey       = errors.New("secrets: ciphertext was encrypted by an unknown key")
	ErrEmptyContext     = errors.New("secrets: encryption context is required")
)

// Envelope is safe to persist as three columns. Context is intentionally not
// included: callers derive it from stable row identity (for example,
// "settings/oidc/client_secret") so moving ciphertext to another row fails.
type Envelope struct {
	Version    int
	KeyID      string
	Nonce      []byte
	Ciphertext []byte
}

// Manager owns immutable derived keys. Construct one at process startup and
// share it; cipher.AEAD and HMAC key bytes are safe for concurrent reads.
type Manager struct {
	keyID   string
	aead    cipher.AEAD
	hashKey []byte
}

func New(master []byte) (*Manager, error) {
	if len(master) != MasterKeySize || allZero(master) {
		return nil, ErrInvalidMasterKey
	}
	aeadKey, err := derive(master, "moyro/settings/aead/v1", 32)
	if err != nil {
		return nil, err
	}
	hashKey, err := derive(master, "moyro/credential-hmac/v1", 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(aeadKey)
	if err != nil {
		return nil, fmt.Errorf("secrets: create AES cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secrets: create GCM: %w", err)
	}
	sum := sha256.Sum256(master)
	return &Manager{
		keyID:   "v1-" + hex.EncodeToString(sum[:8]),
		aead:    aead,
		hashKey: hashKey,
	}, nil
}

// NewBase64 is convenient outside config.Load and accepts padded or raw
// standard base64. It does not accept URL-base64 to keep operator format
// unambiguous.
func NewBase64(encoded string) (*Manager, error) {
	key, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		key, err = base64.RawStdEncoding.Strict().DecodeString(encoded)
	}
	if err != nil {
		return nil, fmt.Errorf("secrets: decode master key: %w", err)
	}
	return New(key)
}

func (m *Manager) KeyID() string {
	if m == nil {
		return ""
	}
	return m.keyID
}

// Seal encrypts plaintext with fresh randomness and authenticates context as
// additional data. Context must identify both the domain and logical row.
func (m *Manager) Seal(context string, plaintext []byte) (Envelope, error) {
	if m == nil || m.aead == nil {
		return Envelope{}, ErrInvalidMasterKey
	}
	if context == "" {
		return Envelope{}, ErrEmptyContext
	}
	nonce := make([]byte, m.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Envelope{}, fmt.Errorf("secrets: generate nonce: %w", err)
	}
	ciphertext := m.aead.Seal(nil, nonce, plaintext, []byte(context))
	return Envelope{
		Version:    Version,
		KeyID:      m.keyID,
		Nonce:      nonce,
		Ciphertext: ciphertext,
	}, nil
}

// Open authenticates and decrypts an envelope. No partial plaintext is
// returned on any error.
func (m *Manager) Open(context string, envelope Envelope) ([]byte, error) {
	if m == nil || m.aead == nil {
		return nil, ErrInvalidMasterKey
	}
	if context == "" {
		return nil, ErrEmptyContext
	}
	if envelope.Version != Version || len(envelope.Nonce) != m.aead.NonceSize() || len(envelope.Ciphertext) < m.aead.Overhead() {
		return nil, ErrInvalidEnvelope
	}
	if envelope.KeyID != m.keyID {
		return nil, ErrUnknownKey
	}
	plain, err := m.aead.Open(nil, envelope.Nonce, envelope.Ciphertext, []byte(context))
	if err != nil {
		return nil, fmt.Errorf("%w: authentication failed", ErrInvalidEnvelope)
	}
	return plain, nil
}

// Encrypt and Decrypt expose a column-oriented shape used by settings.Service.
func (m *Manager) Encrypt(context string, plaintext []byte) (keyID string, nonce, ciphertext []byte, err error) {
	env, err := m.Seal(context, plaintext)
	if err != nil {
		return "", nil, nil, err
	}
	return env.KeyID, env.Nonce, env.Ciphertext, nil
}

func (m *Manager) Decrypt(context, keyID string, nonce, ciphertext []byte) ([]byte, error) {
	return m.Open(context, Envelope{Version: Version, KeyID: keyID, Nonce: nonce, Ciphertext: ciphertext})
}

// Digest returns a domain-separated HMAC suitable for database lookup of a
// high-entropy bearer secret. Purpose prevents a digest copied between token
// classes from authenticating in the other class.
func (m *Manager) Digest(purpose string, secret []byte) ([]byte, error) {
	if m == nil || len(m.hashKey) == 0 {
		return nil, ErrInvalidMasterKey
	}
	if purpose == "" {
		return nil, ErrEmptyContext
	}
	mac := hmac.New(sha256.New, m.hashKey)
	_, _ = mac.Write([]byte(purpose))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(secret)
	return mac.Sum(nil), nil
}

func (m *Manager) VerifyDigest(purpose string, secret, expected []byte) bool {
	actual, err := m.Digest(purpose, secret)
	return err == nil && hmac.Equal(actual, expected)
}

func derive(master []byte, purpose string, size int) ([]byte, error) {
	out, err := hkdf.Key(sha256.New, master, []byte("moyro/secrets/v1"), purpose, size)
	if err != nil {
		return nil, fmt.Errorf("secrets: derive %s: %w", purpose, err)
	}
	return out, nil
}

func allZero(b []byte) bool {
	var combined byte
	for _, v := range b {
		combined |= v
	}
	return combined == 0
}
