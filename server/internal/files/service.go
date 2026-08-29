package files

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // decoder registration
	"image/jpeg"
	_ "image/png"  // decoder registration
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/hkjang/moyro/server/internal/store"
	xdraw "golang.org/x/image/draw"
)

type FileInfo struct {
	ID            string `json:"id"`
	UserID        string `json:"user_id"`
	PostID        string `json:"post_id"`
	ChannelID     string `json:"channel_id"`
	Path          string `json:"-"`              // filesystem path, not exposed
	ThumbnailPath string `json:"-"`              // filesystem path for thumbnail (if any)
	HasThumbnail  bool   `json:"has_thumbnail"`  // convenience flag so clients skip the thumbnail URL when absent
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	Name          string `json:"name"`
	Extension     string `json:"extension"`
	Size          int64  `json:"size"`
	MimeType      string `json:"mime_type"`
	CreateAt      int64  `json:"create_at"`
	UpdateAt      int64  `json:"update_at"`
	DeleteAt      int64  `json:"delete_at"`
}

// Storage persists the raw bytes of an uploaded file. Backed by local
// filesystem now; swap for S3/MinIO later without touching the service.
type Storage interface {
	Write(id, name string, src io.Reader) (path string, size int64, err error)
	Open(path string) (io.ReadCloser, error)
	Remove(path string) error
}

type FSStorage struct{ Root string }

func NewFSStorage(root string) *FSStorage { return &FSStorage{Root: root} }

func (s *FSStorage) Write(id, name string, src io.Reader) (string, int64, error) {
	dir := filepath.Join(s.Root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, err
	}
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	n, err := io.Copy(f, src)
	if err != nil {
		return "", 0, err
	}
	return path, n, nil
}

func (s *FSStorage) Open(path string) (io.ReadCloser, error) { return os.Open(path) }
func (s *FSStorage) Remove(path string) error                { return os.Remove(path) }

type Service struct {
	db      *store.DB
	storage Storage
}

func New(db *store.DB, storage Storage) *Service { return &Service{db: db, storage: storage} }

// Upload streams the file into storage, writes a file_infos row, and
// returns the resulting FileInfo. channelID may be empty (global upload).
//
// Images get an async thumbnail: we kick off a goroutine that re-reads the
// stored bytes, resamples to 360px longest edge, writes the JPEG alongside
// the original, and UPDATEs the row with the thumbnail path + dimensions.
// Clients poll via /files/{id}/info or the WS refresh to pick up changes.
func (s *Service) Upload(ctx context.Context, userID, channelID, name, mime string, src io.Reader) (*FileInfo, error) {
	id := uuid.NewString()
	safeName := sanitize(name)
	path, size, err := s.storage.Write(id, safeName, src)
	if err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(safeName)), ".")

	fi := &FileInfo{
		ID:        id,
		UserID:    userID,
		ChannelID: channelID,
		Path:      path,
		Name:      safeName,
		Extension: ext,
		Size:      size,
		MimeType:  mime,
		CreateAt:  now,
		UpdateAt:  now,
	}
	var chArg any
	if channelID != "" {
		chArg = channelID
	}
	if _, err := s.db.Pool.Exec(ctx, `
		INSERT INTO file_infos (id, user_id, post_id, channel_id, path, name, extension, size, mime_type, create_at, update_at)
		VALUES ($1,$2,NULL,$3,$4,$5,$6,$7,$8,$9,$9)
	`, id, userID, chArg, path, safeName, ext, size, mime, now); err != nil {
		_ = s.storage.Remove(path)
		return nil, err
	}

	// Fire-and-forget thumbnail for image uploads. Runs with a fresh
	// background context so the client response isn't held up by disk
	// I/O; best-effort — failures leave thumbnail_path empty.
	if isImageMIME(mime) {
		go s.generateThumbnailAsync(id, path)
	}
	return fi, nil
}

// isImageMIME returns true for the MIME types we know we can decode. We
// don't attempt SVG (no decoder) or non-standard formats.
func isImageMIME(mime string) bool {
	switch strings.ToLower(mime) {
	case "image/jpeg", "image/jpg", "image/png", "image/gif":
		return true
	}
	return false
}

// generateThumbnailAsync produces a 360px-longest-edge JPEG thumbnail and
// records the path + original dimensions on the file_infos row. Best-effort:
// any decode/encode error silently leaves thumbnail_path empty.
func (s *Service) generateThumbnailAsync(fileID, origPath string) {
	defer func() { _ = recover() }() // never crash the server on a bad image
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	rc, err := s.storage.Open(origPath)
	if err != nil {
		return
	}
	defer rc.Close()
	img, _, err := image.Decode(rc)
	if err != nil {
		return
	}
	bounds := img.Bounds()
	ow, oh := bounds.Dx(), bounds.Dy()
	if ow <= 0 || oh <= 0 {
		return
	}

	const maxEdge = 360
	nw, nh := ow, oh
	if ow > maxEdge || oh > maxEdge {
		if ow >= oh {
			nw = maxEdge
			nh = (oh * maxEdge) / ow
		} else {
			nh = maxEdge
			nw = (ow * maxEdge) / oh
		}
		if nh < 1 {
			nh = 1
		}
		if nw < 1 {
			nw = 1
		}
	}

	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, bounds, xdraw.Over, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 82}); err != nil {
		return
	}
	thumbPath, _, err := s.storage.Write(fileID, "thumb.jpg", &buf)
	if err != nil {
		return
	}

	// Persist the thumbnail path + dimensions. We store the *original*
	// width/height so clients can compute aspect ratio without decoding.
	_, _ = s.db.Pool.Exec(ctx, `
		UPDATE file_infos
		SET thumbnail_path=$1, width=$2, height=$3, update_at=$4
		WHERE id=$5
	`, thumbPath, ow, oh, time.Now().UnixMilli(), fileID)
}

func (s *Service) GetInfo(ctx context.Context, id string) (*FileInfo, error) {
	fi := &FileInfo{}
	var postID, channelID *string
	err := s.db.Pool.QueryRow(ctx, `
		SELECT id, user_id, post_id, channel_id, path, name, extension, size, mime_type, create_at, update_at, delete_at,
		       thumbnail_path, width, height
		FROM file_infos WHERE id=$1 AND delete_at=0
	`, id).Scan(&fi.ID, &fi.UserID, &postID, &channelID, &fi.Path, &fi.Name, &fi.Extension, &fi.Size, &fi.MimeType, &fi.CreateAt, &fi.UpdateAt, &fi.DeleteAt,
		&fi.ThumbnailPath, &fi.Width, &fi.Height)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if postID != nil {
		fi.PostID = *postID
	}
	if channelID != nil {
		fi.ChannelID = *channelID
	}
	fi.HasThumbnail = fi.ThumbnailPath != ""
	return fi, nil
}

func (s *Service) Open(ctx context.Context, id string) (io.ReadCloser, *FileInfo, error) {
	fi, err := s.GetInfo(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	rc, err := s.storage.Open(fi.Path)
	if err != nil {
		return nil, nil, err
	}
	return rc, fi, nil
}

// AssociateWithPost binds the given file_ids to a post, but only for
// unattached files owned by the caller. Ids that don't match are silently
// skipped rather than rejected — matches Mattermost's lenient behavior.
func (s *Service) AssociateWithPost(ctx context.Context, ownerID string, fileIDs []string, postID, channelID string) ([]string, error) {
	if len(fileIDs) == 0 {
		return nil, nil
	}
	rows, err := s.db.Pool.Query(ctx, `
		UPDATE file_infos
		SET post_id=$1, channel_id=$2, update_at=$3
		WHERE user_id=$4 AND post_id IS NULL AND id = ANY($5::text[])
		RETURNING id
	`, postID, channelID, time.Now().UnixMilli(), ownerID, fileIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ListForPost returns all file_infos tied to a post.
func (s *Service) ListForPost(ctx context.Context, postID string) ([]FileInfo, error) {
	rows, err := s.db.Pool.Query(ctx, `
		SELECT id, user_id, post_id, channel_id, path, name, extension, size, mime_type, create_at, update_at, delete_at,
		       thumbnail_path, width, height
		FROM file_infos WHERE post_id=$1 AND delete_at=0 ORDER BY create_at ASC
	`, postID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FileInfo{}
	for rows.Next() {
		var fi FileInfo
		var postIDv, channelIDv *string
		if err := rows.Scan(&fi.ID, &fi.UserID, &postIDv, &channelIDv, &fi.Path, &fi.Name, &fi.Extension, &fi.Size, &fi.MimeType, &fi.CreateAt, &fi.UpdateAt, &fi.DeleteAt,
			&fi.ThumbnailPath, &fi.Width, &fi.Height); err != nil {
			return nil, err
		}
		if postIDv != nil {
			fi.PostID = *postIDv
		}
		if channelIDv != nil {
			fi.ChannelID = *channelIDv
		}
		fi.HasThumbnail = fi.ThumbnailPath != ""
		out = append(out, fi)
	}
	return out, rows.Err()
}

// OpenThumbnail returns the thumbnail stream or ErrNotFound if the file has
// no thumbnail yet (pending generation / not an image / generation failed).
func (s *Service) OpenThumbnail(ctx context.Context, id string) (io.ReadCloser, *FileInfo, error) {
	fi, err := s.GetInfo(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if fi.ThumbnailPath == "" {
		return nil, fi, ErrNotFound
	}
	rc, err := s.storage.Open(fi.ThumbnailPath)
	if err != nil {
		return nil, nil, err
	}
	return rc, fi, nil
}

// MarshalFileIDs helps handlers encode file_id slices into posts.file_ids JSONB column.
func MarshalFileIDs(ids []string) ([]byte, error) {
	if ids == nil {
		ids = []string{}
	}
	return json.Marshal(ids)
}

var ErrNotFound = errors.New("file not found")

// sanitize strips path separators to prevent directory traversal while
// keeping a recognisable filename for downloads.
func sanitize(name string) string {
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "..", "_")
	if name == "" {
		return fmt.Sprintf("file-%d", time.Now().UnixNano())
	}
	return name
}
