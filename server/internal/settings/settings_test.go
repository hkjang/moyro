package settings

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/hkjang/moyro/server/internal/secrets"
)

type memoryRepo struct {
	mu   sync.Mutex
	rows map[string]Record
}

type failingBatchRepo struct{ *memoryRepo }

func (r *failingBatchRepo) PutBatch(_ context.Context, _ []Record) ([]Record, error) {
	return nil, errors.New("injected batch failure")
}

func newMemoryRepo() *memoryRepo { return &memoryRepo{rows: map[string]Record{}} }

func rowKey(section, key string) string { return section + "\x00" + key }

func (r *memoryRepo) Get(_ context.Context, section, key string) (Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.rows[rowKey(section, key)]
	if !ok {
		return Record{}, ErrNotFound
	}
	return cloneRecord(record), nil
}

func (r *memoryRepo) List(_ context.Context, section string) ([]Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []Record{}
	for _, record := range r.rows {
		if record.Section == section {
			out = append(out, cloneRecord(record))
		}
	}
	return out, nil
}

func (r *memoryRepo) Put(_ context.Context, record Record, expected *int64) (Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := rowKey(record.Section, record.Key)
	current := r.rows[key].Revision
	if expected != nil && *expected != current {
		return Record{}, ErrRevisionConflict
	}
	record.Revision = current + 1
	r.rows[key] = cloneRecord(record)
	return cloneRecord(record), nil
}

func (r *memoryRepo) PutBatch(_ context.Context, records []Record) ([]Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	next := make(map[string]Record, len(r.rows)+len(records))
	for key, record := range r.rows {
		next[key] = cloneRecord(record)
	}
	out := make([]Record, len(records))
	for i, record := range records {
		key := rowKey(record.Section, record.Key)
		record.Revision = next[key].Revision + 1
		next[key] = cloneRecord(record)
		out[i] = cloneRecord(record)
	}
	r.rows = next
	return out, nil
}

func (r *memoryRepo) Delete(_ context.Context, section, key string, expected *int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := rowKey(section, key)
	record, ok := r.rows[k]
	if !ok {
		return ErrNotFound
	}
	if expected != nil && *expected != record.Revision {
		return ErrRevisionConflict
	}
	delete(r.rows, k)
	return nil
}

func cloneRecord(r Record) Record {
	r.ValueJSON = append([]byte(nil), r.ValueJSON...)
	r.Ciphertext = append([]byte(nil), r.Ciphertext...)
	r.Nonce = append([]byte(nil), r.Nonce...)
	return r
}

func newTestService(t *testing.T) (*Service, *memoryRepo) {
	t.Helper()
	cipher, err := secrets.New(bytes.Repeat([]byte{0x25}, 32))
	if err != nil {
		t.Fatal(err)
	}
	repo := newMemoryRepo()
	service, err := New(repo, cipher)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return time.UnixMilli(1234) }
	return service, repo
}

func TestPutJSONAndOptimisticConcurrency(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	zero := int64(0)
	setting, err := svc.PutJSON(ctx, "site", "name", "Moyro", "admin", &zero)
	if err != nil {
		t.Fatal(err)
	}
	if setting.Revision != 1 || string(setting.Value) != `"Moyro"` || setting.SecretConfigured {
		t.Fatalf("setting = %#v", setting)
	}
	if _, err := svc.PutJSON(ctx, "site", "name", "lost update", "admin", &zero); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("conflict error = %v", err)
	}
	one := int64(1)
	updated, err := svc.PutJSON(ctx, "site", "name", "Moyro 2", "admin", &one)
	if err != nil || updated.Revision != 2 {
		t.Fatalf("update = %#v, %v", updated, err)
	}
}

func TestSecretIsRedactedAndBoundToRow(t *testing.T) {
	svc, repo := newTestService(t)
	ctx := context.Background()
	setting, err := svc.PutSecret(ctx, "oidc", "client-secret", []byte("top-secret"), "admin", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !setting.SecretConfigured || setting.Value != nil {
		t.Fatalf("secret leaked in setting: %#v", setting)
	}
	plain, revision, err := svc.RevealSecret(ctx, "oidc", "client-secret")
	if err != nil || string(plain) != "top-secret" || revision != 1 {
		t.Fatalf("RevealSecret() = %q, %d, %v", plain, revision, err)
	}
	// Copying authenticated ciphertext under a different logical key fails.
	record, _ := repo.Get(ctx, "oidc", "client-secret")
	record.Key = "other-secret"
	_, _ = repo.Put(ctx, record, nil)
	if _, _, err := svc.RevealSecret(ctx, "oidc", "other-secret"); err == nil {
		t.Fatal("row-swapped ciphertext decrypted")
	}
}

func TestPutJSONAndSecretCommitsOneRedactedBundle(t *testing.T) {
	svc, _ := newTestService(t)
	rows, err := svc.PutJSONAndOptionalSecret(
		context.Background(), "oidc", "provider", map[string]any{"enabled": true},
		"client-secret", []byte("bundle-secret"), "admin",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Revision != 1 || rows[1].Revision != 1 {
		t.Fatalf("bundle metadata = %#v", rows)
	}
	plain, _, err := svc.RevealSecret(context.Background(), "oidc", "client-secret")
	if err != nil || string(plain) != "bundle-secret" {
		t.Fatalf("secret = %q, %v", plain, err)
	}
	public, err := svc.Get(context.Background(), "oidc", "provider")
	if err != nil || string(public.Value) != `{"enabled":true}` {
		t.Fatalf("public setting = %#v, %v", public, err)
	}
}

func TestPutJSONAndSecretDoesNotFallBackToPartialWrites(t *testing.T) {
	cipher, err := secrets.New(bytes.Repeat([]byte{0x35}, 32))
	if err != nil {
		t.Fatal(err)
	}
	repo := &failingBatchRepo{memoryRepo: newMemoryRepo()}
	svc, err := New(repo, cipher)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.PutJSONAndOptionalSecret(
		context.Background(), "ai", "provider", map[string]any{"enabled": true},
		"api-key", []byte("secret"), "admin",
	)
	if err == nil {
		t.Fatal("injected atomic batch failure was ignored")
	}
	if len(repo.rows) != 0 {
		t.Fatalf("partial settings were written: %#v", repo.rows)
	}
}

func TestValidationAndExplicitSecretClear(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	if _, err := svc.PutJSON(ctx, "Bad Section", "name", true, "", nil); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("invalid name error = %v", err)
	}
	if _, err := svc.PutSecret(ctx, "oidc", "secret", nil, "", nil); err == nil {
		t.Fatal("empty secret accepted")
	}
	setting, err := svc.PutSecret(ctx, "oidc", "secret", []byte("value"), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	revision := setting.Revision
	if err := svc.Delete(ctx, "oidc", "secret", &revision); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get(ctx, "oidc", "secret"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete error = %v", err)
	}
}
