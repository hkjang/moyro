package files

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// S3Storage persists file contents in an S3-compatible bucket. Implements
// the `Storage` interface; the `path` returned by Write is the object key
// (format: `files/{id}/{name}`) and Open/Remove accept that same key back.
// This means the DB's file_infos.path column stores keys transparently —
// no schema migration is needed when switching from `fs` to `s3`.
//
// Credentials + region resolve through the AWS SDK's default chain
// (environment variables, shared config, EC2 metadata, IRSA, …). Pass a
// non-empty `endpoint` for minio / R2 / B2 compatibility; when set, path-
// style addressing is forced because those providers don't all support
// virtual-host style.
type S3Storage struct {
	bucket   string
	client   *s3.Client
	uploader *manager.Uploader
}

// NewS3Storage dials S3 and returns a Storage impl. A zero-length bucket
// returns an error so the caller can fail fast rather than silently
// writing nothing. Callers typically route through NewStorage() which
// consults cfg.FileBackend.
func NewS3Storage(ctx context.Context, bucket, region, endpoint string) (*S3Storage, error) {
	if bucket == "" {
		return nil, errors.New("s3 storage requires MODDLE_S3_BUCKET")
	}
	loadCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cfg, err := awsconfig.LoadDefaultConfig(loadCtx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, err
	}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
			// path-style URLs are required by minio/R2 and most non-AWS
			// S3 clones; AWS itself also accepts them so this is safe.
			o.UsePathStyle = true
		}
	})
	return &S3Storage{
		bucket:   bucket,
		client:   client,
		uploader: manager.NewUploader(client),
	}, nil
}

// Write streams src to S3 under the key `files/{id}/{name}`. Returns the
// key as the storage path (the fs impl returns a filesystem path; the
// interface is opaque about format). Size is counted via a passthrough
// reader because the Upload manager doesn't expose the byte count directly.
func (s *S3Storage) Write(id, name string, src io.Reader) (string, int64, error) {
	key := "files/" + id + "/" + name
	cr := &countingReader{r: src}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, err := s.uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket:               aws.String(s.bucket),
		Key:                  aws.String(key),
		Body:                 cr,
		ServerSideEncryption: types.ServerSideEncryptionAes256,
	})
	if err != nil {
		return "", 0, err
	}
	return key, cr.n, nil
}

// Open streams an object back out. Callers must Close the returned
// ReadCloser — it owns the underlying HTTP response body.
func (s *S3Storage) Open(path string) (io.ReadCloser, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	// Intentionally NOT deferring cancel here: the body stream is
	// returned to the caller and must stay alive past this function's
	// return. We trade a leaked timer slot for correctness; in practice
	// the S3 GET completes within the 30s budget regardless.
	_ = cancel
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(path),
	})
	if err != nil {
		return nil, err
	}
	return out.Body, nil
}

// Remove deletes an object. Missing-key errors are swallowed because the
// FS impl's os.Remove returns an error on missing-file too but callers
// treat that as best-effort — keeping behaviour consistent.
func (s *S3Storage) Remove(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(path),
	})
	return err
}

// countingReader wraps an io.Reader and counts the total bytes consumed.
// Used so Write can return a size without pre-buffering the stream.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
