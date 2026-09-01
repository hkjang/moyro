package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/hkjang/moyro/server/internal/auth"
	"github.com/hkjang/moyro/server/internal/files"
	"github.com/hkjang/moyro/server/internal/store"
)

func TestGuestWithoutDownloadGrantCannotUsePreviewOriginalFallbackPostgres(t *testing.T) {
	db := newOperationsTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate file preview schema: %v", err)
	}

	storageRoot := t.TempDir()
	originalPath := filepath.Join(storageRoot, "original.png")
	thumbnailPath := filepath.Join(storageRoot, "thumb.jpg")
	if err := os.WriteFile(originalPath, []byte("original-image-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(thumbnailPath, []byte("bounded-thumbnail-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UnixMilli()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users
			(id, username, email, password_hash, roles, create_at, update_at, guest_expires_at, guest_file_download)
		VALUES ('preview-guest', 'preview-guest', 'preview-guest@example.test', 'unused', 'system_guest', $1, $1, $2, FALSE)
	`, now, now+int64(time.Hour/time.Millisecond)); err != nil {
		t.Fatalf("seed file preview guest: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO file_infos
			(id, user_id, path, thumbnail_path, name, extension, size, mime_type, create_at, update_at)
		VALUES
			('preview-original-only', 'preview-guest', $2, '', 'original.png', 'png', 20, 'image/png', $1, $1),
			('preview-with-thumbnail', 'preview-guest', $2, $3, 'thumbnail.png', 'png', 20, 'image/png', $1, $1)
	`, now, originalPath, thumbnailPath); err != nil {
		t.Fatalf("seed file preview files: %v", err)
	}

	h := &handlers{
		auth:  auth.New(db, []byte("file-preview-test-signing-key"), time.Hour, nil),
		files: files.New(db, files.NewFSStorage(storageRoot)),
	}

	denied := httptest.NewRecorder()
	h.filePreview(denied, filePreviewRequest(ctx, "preview-guest", "preview-original-only"))
	if denied.Code != http.StatusForbidden {
		t.Fatalf("original fallback status = %d, body=%s, want 403", denied.Code, denied.Body.String())
	}
	if got := denied.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("denied preview Cache-Control = %q, want private, no-store", got)
	}
	if got := denied.Body.String(); got == "original-image-bytes" {
		t.Fatal("original bytes escaped through the preview endpoint")
	}

	preview := httptest.NewRecorder()
	h.filePreview(preview, filePreviewRequest(ctx, "preview-guest", "preview-with-thumbnail"))
	if preview.Code != http.StatusOK || preview.Body.String() != "bounded-thumbnail-bytes" {
		t.Fatalf("thumbnail preview = (%d, %q), want bounded thumbnail", preview.Code, preview.Body.String())
	}
	if got := preview.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("preview Cache-Control = %q, want private, no-store", got)
	}

	thumbnail := httptest.NewRecorder()
	h.fileThumbnail(thumbnail, filePreviewRequest(ctx, "preview-guest", "preview-with-thumbnail"))
	if thumbnail.Code != http.StatusOK || thumbnail.Body.String() != "bounded-thumbnail-bytes" {
		t.Fatalf("thumbnail response = (%d, %q), want bounded thumbnail", thumbnail.Code, thumbnail.Body.String())
	}
	if got := thumbnail.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("thumbnail Cache-Control = %q, want private, no-store", got)
	}
}

func filePreviewRequest(ctx context.Context, actorID, fileID string) *http.Request {
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("fileID", fileID)
	ctx = context.WithValue(ctx, chi.RouteCtxKey, routeContext)
	ctx = context.WithValue(ctx, userIDKey, actorID)
	return httptest.NewRequest(http.MethodGet, "/api/v4/files/"+fileID+"/preview", nil).WithContext(ctx)
}
