package oidcauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hkjang/moyro/server/internal/secrets"
	"github.com/hkjang/moyro/server/internal/store"
	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"
)

var ErrInvalidFlow = errors.New("oidc login flow is invalid or expired")

// expiredFlowCleanupLimit bounds the work added to a login request.  Cleanup
// is opportunistic, so a busy installation drains old rows over subsequent
// logins without ever turning one request into an unbounded table sweep.
const expiredFlowCleanupLimit = 256

const maxFlowPolicyBytes = 1 << 20

type Flow struct {
	Nonce              string          `json:"nonce"`
	Verifier           string          `json:"verifier"`
	ReturnTo           string          `json:"return_to"`
	ProviderSnapshotID string          `json:"provider_snapshot_id"`
	ProviderPolicy     json.RawMessage `json:"provider_policy"`
	ExpiresAt          int64           `json:"expires_at"`
}

type FlowStore struct {
	db      *store.DB
	secrets *secrets.Manager
	ttl     time.Duration
	clock   func() time.Time
}

func NewFlowStore(db *store.DB, secretManager *secrets.Manager) (*FlowStore, error) {
	if db == nil || db.Pool == nil || secretManager == nil {
		return nil, errors.New("oidcauth: flow store requires database and secret manager")
	}
	return &FlowStore{db: db, secrets: secretManager, ttl: 10 * time.Minute, clock: time.Now}, nil
}

func (s *FlowStore) Create(ctx context.Context, returnTo, providerSnapshotID string, providerPolicy json.RawMessage) (state string, flow Flow, err error) {
	providerSnapshotID = strings.TrimSpace(providerSnapshotID)
	if err := validateFlowProviderBinding(providerSnapshotID, providerPolicy); err != nil {
		return "", Flow{}, ErrInvalidFlow
	}
	// Cleanup failure must not make a valid SSO login unavailable.  The insert
	// below is still bounded by the primary key and a future login retries the
	// indexed expiry cleanup.
	_, _ = s.DeleteExpired(ctx)

	state, err = randomURLToken(32)
	if err != nil {
		return "", Flow{}, err
	}
	nonce, err := randomURLToken(32)
	if err != nil {
		return "", Flow{}, err
	}
	flow = Flow{
		Nonce: nonce, Verifier: oauth2.GenerateVerifier(), ReturnTo: returnTo,
		ProviderSnapshotID: providerSnapshotID,
		ProviderPolicy:     append(json.RawMessage(nil), providerPolicy...),
		ExpiresAt:          s.clock().Add(s.ttl).UnixMilli(),
	}
	payload, err := json.Marshal(flow)
	if err != nil {
		return "", Flow{}, err
	}
	digest := sha256.Sum256([]byte(state))
	contextName := flowContext(digest[:])
	envelope, err := s.secrets.Seal(contextName, payload)
	if err != nil {
		return "", Flow{}, err
	}
	_, err = s.db.Pool.Exec(ctx, `
		INSERT INTO oidc_auth_flows
		    (state_hash, key_id, nonce, ciphertext, expires_at, create_at)
		VALUES ($1,$2,$3,$4,$5,$6)
	`, digest[:], envelope.KeyID, envelope.Nonce, envelope.Ciphertext,
		flow.ExpiresAt, s.clock().UnixMilli())
	if err != nil {
		return "", Flow{}, fmt.Errorf("oidcauth: store flow: %w", err)
	}
	return state, flow, nil
}

func validateFlowProviderBinding(providerSnapshotID string, providerPolicy json.RawMessage) error {
	if strings.TrimSpace(providerSnapshotID) == "" || len(providerPolicy) == 0 ||
		len(providerPolicy) > maxFlowPolicyBytes || !json.Valid(providerPolicy) {
		return ErrInvalidFlow
	}
	return nil
}

// Consume atomically deletes a one-time state before decryption. A replay sees
// no row even if the original callback later fails token exchange.
func (s *FlowStore) Consume(ctx context.Context, state string) (Flow, error) {
	if state == "" {
		return Flow{}, ErrInvalidFlow
	}
	digest := sha256.Sum256([]byte(state))
	var keyID string
	var nonce, ciphertext []byte
	var expiresAt int64
	err := s.db.Pool.QueryRow(ctx, `
		DELETE FROM oidc_auth_flows
		WHERE state_hash=$1
		RETURNING key_id, nonce, ciphertext, expires_at
	`, digest[:]).Scan(&keyID, &nonce, &ciphertext, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Flow{}, ErrInvalidFlow
	}
	if err != nil {
		return Flow{}, err
	}
	if expiresAt <= s.clock().UnixMilli() {
		return Flow{}, ErrInvalidFlow
	}
	plain, err := s.secrets.Open(flowContext(digest[:]), secrets.Envelope{
		Version: secrets.Version, KeyID: keyID, Nonce: nonce, Ciphertext: ciphertext,
	})
	if err != nil {
		return Flow{}, ErrInvalidFlow
	}
	var flow Flow
	if err := json.Unmarshal(plain, &flow); err != nil || flow.Nonce == "" || flow.Verifier == "" ||
		flow.ProviderSnapshotID == "" || len(flow.ProviderPolicy) == 0 || len(flow.ProviderPolicy) > maxFlowPolicyBytes ||
		!json.Valid(flow.ProviderPolicy) || flow.ExpiresAt != expiresAt {
		return Flow{}, ErrInvalidFlow
	}
	return flow, nil
}

func (s *FlowStore) DeleteExpired(ctx context.Context) (int64, error) {
	tag, err := s.db.Pool.Exec(ctx, `
		DELETE FROM oidc_auth_flows
		WHERE state_hash IN (
			SELECT state_hash
			FROM oidc_auth_flows
			WHERE expires_at <= $1
			ORDER BY expires_at, state_hash
			LIMIT $2
		)
	`, s.clock().UnixMilli(), expiredFlowCleanupLimit)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func flowContext(digest []byte) string {
	return "moyro/oidc-flow/v1/" + hex.EncodeToString(digest)
}

func randomURLToken(size int) (string, error) {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
