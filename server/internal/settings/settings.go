// Package settings stores administrator-managed runtime configuration in
// PostgreSQL. Secret values are encrypted before reaching the repository and
// are always redacted from the public read model.
package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

var (
	ErrNotFound         = errors.New("settings: setting not found")
	ErrRevisionConflict = errors.New("settings: revision conflict")
	ErrInvalidName      = errors.New("settings: invalid section or key")
	ErrCipherRequired   = errors.New("settings: secret cipher is required")
)

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)

// Cipher is implemented by secrets.Manager. Keeping the narrow interface here
// makes the service easy to test and prevents storage code from ever seeing the
// master key.
type Cipher interface {
	Encrypt(context string, plaintext []byte) (keyID string, nonce, ciphertext []byte, err error)
	Decrypt(context, keyID string, nonce, ciphertext []byte) ([]byte, error)
}

// Record is the repository representation. Exactly one of ValueJSON or
// Ciphertext is populated, enforced by the database CHECK constraint.
type Record struct {
	Section    string
	Key        string
	ValueJSON  []byte
	Ciphertext []byte
	Nonce      []byte
	KeyID      string
	Revision   int64
	UpdatedBy  string
	UpdatedAt  int64
}

func (r Record) IsSecret() bool { return len(r.Ciphertext) != 0 }

type Repository interface {
	Get(ctx context.Context, section, key string) (Record, error)
	List(ctx context.Context, section string) ([]Record, error)
	Put(ctx context.Context, record Record, expectedRevision *int64) (Record, error)
	Delete(ctx context.Context, section, key string, expectedRevision *int64) error
}

// BatchRepository atomically stores multiple independently keyed settings.
// Provider configuration uses it to keep encrypted credentials and their
// public configuration in one commit.
type BatchRepository interface {
	PutBatch(ctx context.Context, records []Record) ([]Record, error)
}

// Setting is safe to serialize to an administrator. Encrypted data, nonce and
// key identifiers are deliberately absent.
type Setting struct {
	Section          string          `json:"section"`
	Key              string          `json:"key"`
	Value            json.RawMessage `json:"value,omitempty"`
	SecretConfigured bool            `json:"secret_configured"`
	Revision         int64           `json:"revision"`
	UpdatedBy        string          `json:"updated_by,omitempty"`
	UpdatedAt        int64           `json:"update_at"`
}

type Service struct {
	repo   Repository
	cipher Cipher
	now    func() time.Time
}

func New(repo Repository, cipher Cipher) (*Service, error) {
	if repo == nil {
		return nil, errors.New("settings: nil repository")
	}
	return &Service{repo: repo, cipher: cipher, now: time.Now}, nil
}

func (s *Service) Get(ctx context.Context, section, key string) (Setting, error) {
	if err := validatePath(section, key); err != nil {
		return Setting{}, err
	}
	record, err := s.repo.Get(ctx, section, key)
	if err != nil {
		return Setting{}, err
	}
	return redact(record), nil
}

func (s *Service) List(ctx context.Context, section string) ([]Setting, error) {
	if !namePattern.MatchString(section) {
		return nil, ErrInvalidName
	}
	records, err := s.repo.List(ctx, section)
	if err != nil {
		return nil, err
	}
	out := make([]Setting, 0, len(records))
	for _, record := range records {
		out = append(out, redact(record))
	}
	return out, nil
}

// PutJSON replaces a non-secret value. expectedRevision=nil means
// last-writer-wins; a non-nil value enables optimistic concurrency. Pass 0 to
// require that the row does not exist yet.
func (s *Service) PutJSON(ctx context.Context, section, key string, value any, actorID string, expectedRevision *int64) (Setting, error) {
	if err := validatePath(section, key); err != nil {
		return Setting{}, err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return Setting{}, fmt.Errorf("settings: encode value: %w", err)
	}
	record, err := s.repo.Put(ctx, Record{
		Section: section, Key: key, ValueJSON: raw,
		UpdatedBy: actorID, UpdatedAt: s.now().UnixMilli(),
	}, expectedRevision)
	if err != nil {
		return Setting{}, err
	}
	return redact(record), nil
}

// PutSecret encrypts a non-empty secret and returns only its redacted metadata.
// Clearing a secret is an explicit Delete operation, avoiding accidental
// erasure when a web form submits an empty password field.
func (s *Service) PutSecret(ctx context.Context, section, key string, plaintext []byte, actorID string, expectedRevision *int64) (Setting, error) {
	if err := validatePath(section, key); err != nil {
		return Setting{}, err
	}
	if len(plaintext) == 0 {
		return Setting{}, errors.New("settings: empty secret; use Delete to clear")
	}
	if s.cipher == nil {
		return Setting{}, ErrCipherRequired
	}
	keyID, nonce, ciphertext, err := s.cipher.Encrypt(aad(section, key), plaintext)
	if err != nil {
		return Setting{}, fmt.Errorf("settings: encrypt %s.%s: %w", section, key, err)
	}
	record, err := s.repo.Put(ctx, Record{
		Section: section, Key: key, KeyID: keyID,
		Nonce: nonce, Ciphertext: ciphertext,
		UpdatedBy: actorID, UpdatedAt: s.now().UnixMilli(),
	}, expectedRevision)
	if err != nil {
		return Setting{}, err
	}
	return redact(record), nil
}

// PutJSONAndOptionalSecret atomically replaces a public JSON setting and,
// when plaintext is non-empty, its encrypted companion secret. Empty secret
// input leaves an existing secret untouched and stores only the JSON row.
func (s *Service) PutJSONAndOptionalSecret(
	ctx context.Context,
	section, jsonKey string,
	value any,
	secretKey string,
	plaintext []byte,
	actorID string,
) ([]Setting, error) {
	if err := validatePath(section, jsonKey); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("settings: encode value: %w", err)
	}
	now := s.now().UnixMilli()
	records := []Record{{
		Section: section, Key: jsonKey, ValueJSON: raw,
		UpdatedBy: actorID, UpdatedAt: now,
	}}
	if len(plaintext) > 0 {
		if err := validatePath(section, secretKey); err != nil {
			return nil, err
		}
		if s.cipher == nil {
			return nil, ErrCipherRequired
		}
		keyID, nonce, ciphertext, err := s.cipher.Encrypt(aad(section, secretKey), plaintext)
		if err != nil {
			return nil, fmt.Errorf("settings: encrypt %s.%s: %w", section, secretKey, err)
		}
		records = append(records, Record{
			Section: section, Key: secretKey, KeyID: keyID, Nonce: nonce, Ciphertext: ciphertext,
			UpdatedBy: actorID, UpdatedAt: now,
		})
	}
	if len(records) == 1 {
		record, err := s.repo.Put(ctx, records[0], nil)
		if err != nil {
			return nil, err
		}
		return []Setting{redact(record)}, nil
	}
	repo, ok := s.repo.(BatchRepository)
	if !ok {
		return nil, errors.New("settings: repository does not support atomic batch writes")
	}
	stored, err := repo.PutBatch(ctx, records)
	if err != nil {
		return nil, err
	}
	out := make([]Setting, 0, len(stored))
	for _, record := range stored {
		out = append(out, redact(record))
	}
	return out, nil
}

// RevealSecret is for trusted internal consumers such as the OIDC and AI
// provider factories. HTTP handlers must use Get/List instead.
func (s *Service) RevealSecret(ctx context.Context, section, key string) ([]byte, int64, error) {
	if err := validatePath(section, key); err != nil {
		return nil, 0, err
	}
	if s.cipher == nil {
		return nil, 0, ErrCipherRequired
	}
	record, err := s.repo.Get(ctx, section, key)
	if err != nil {
		return nil, 0, err
	}
	if !record.IsSecret() {
		return nil, record.Revision, errors.New("settings: requested value is not secret")
	}
	plain, err := s.cipher.Decrypt(aad(section, key), record.KeyID, record.Nonce, record.Ciphertext)
	if err != nil {
		return nil, record.Revision, fmt.Errorf("settings: decrypt %s.%s: %w", section, key, err)
	}
	return plain, record.Revision, nil
}

func (s *Service) Delete(ctx context.Context, section, key string, expectedRevision *int64) error {
	if err := validatePath(section, key); err != nil {
		return err
	}
	return s.repo.Delete(ctx, section, key, expectedRevision)
}

func redact(record Record) Setting {
	setting := Setting{
		Section: record.Section, Key: record.Key,
		SecretConfigured: record.IsSecret(), Revision: record.Revision,
		UpdatedBy: record.UpdatedBy, UpdatedAt: record.UpdatedAt,
	}
	if !record.IsSecret() {
		setting.Value = append(json.RawMessage(nil), record.ValueJSON...)
	}
	return setting
}

func validatePath(section, key string) error {
	if !namePattern.MatchString(section) || !namePattern.MatchString(key) {
		return ErrInvalidName
	}
	return nil
}

func aad(section, key string) string {
	return "moyro/settings/v1/" + section + "/" + key
}
