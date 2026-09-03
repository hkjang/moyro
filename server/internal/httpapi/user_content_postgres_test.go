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

// A member can declare any Content-Type on their multipart part, so the stored
// MIME is attacker-controlled. These routes serve the bytes back from the same
// origin as the webapp and are not covered by the SPA's CSP, so an inline
// image/svg+xml response would be a stored same-origin script.
func TestUploadedBytesAreNeverServedAsScriptableDocumentsPostgres(t *testing.T) {
	db := newOperationsTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate user content schema: %v", err)
	}

	const payload = `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(document.cookie)</script></svg>`
	storageRoot := t.TempDir()
	svgPath := filepath.Join(storageRoot, "payload.svg")
	if err := os.WriteFile(svgPath, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UnixMilli()
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO users (id, username, email, password_hash, roles, create_at, update_at, picture)
		VALUES ('content-member', 'content-member', 'content-member@example.test', 'unused', 'system_user', $1, $1, 'content-svg')
	`, now); err != nil {
		t.Fatalf("seed uploader: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO file_infos
			(id, user_id, path, thumbnail_path, name, extension, size, mime_type, create_at, update_at)
		VALUES ('content-svg', 'content-member', $2, '', $3, 'svg', $4, 'image/svg+xml', $1, $1)
	`, now, svgPath, `pay"load.svg`, len(payload)); err != nil {
		t.Fatalf("seed svg upload: %v", err)
	}

	h := &handlers{
		auth:  auth.New(db, []byte("user-content-test-signing-key"), time.Hour, nil),
		files: files.New(db, files.NewFSStorage(storageRoot)),
	}

	// /preview must not fall back to the original for a scriptable type. The
	// old `image/*` gate let this through with Content-Type: image/svg+xml.
	preview := httptest.NewRecorder()
	h.filePreview(preview, filePreviewRequest(ctx, "content-member", "content-svg"))
	if preview.Code != http.StatusNotFound {
		t.Fatalf("svg preview status = %d, body=%s, want 404", preview.Code, preview.Body.String())
	}
	if preview.Body.String() == payload {
		t.Fatal("svg bytes escaped through the preview endpoint")
	}

	// The avatar route resolves users.picture to the same file_infos row.
	avatar := httptest.NewRecorder()
	h.getUserImage(avatar, userImageRequest(ctx, "content-member", "content-member"))
	if avatar.Code != http.StatusOK {
		t.Fatalf("avatar status = %d, body=%s, want 200", avatar.Code, avatar.Body.String())
	}
	if got := avatar.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("avatar Content-Type = %q, want application/octet-stream", got)
	}
	if got := avatar.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("avatar X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := avatar.Header().Get("Content-Disposition"); got != `attachment; filename="pay_load.svg"; filename*=UTF-8''pay%22load.svg` {
		t.Fatalf("avatar Content-Disposition = %q, want an escaped attachment", got)
	}

	// Download stays reachable — it just can never render in place, and the
	// quote in the stored name must not close the header's quoted-string.
	download := httptest.NewRecorder()
	h.downloadFile(download, filePreviewRequest(ctx, "content-member", "content-svg"))
	if download.Code != http.StatusOK || download.Body.String() != payload {
		t.Fatalf("download = (%d, %q), want the stored bytes", download.Code, download.Body.String())
	}
	if got := download.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("download X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := download.Header().Get("Content-Disposition"); got != `attachment; filename="pay_load.svg"; filename*=UTF-8''pay%22load.svg` {
		t.Fatalf("download Content-Disposition = %q, want an escaped attachment", got)
	}
}

func userImageRequest(ctx context.Context, actorID, targetID string) *http.Request {
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("userID", targetID)
	ctx = context.WithValue(ctx, chi.RouteCtxKey, routeContext)
	ctx = context.WithValue(ctx, userIDKey, actorID)
	return httptest.NewRequest(http.MethodGet, "/api/v4/users/"+targetID+"/image", nil).WithContext(ctx)
}
