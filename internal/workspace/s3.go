package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3 stores objects in any S3-compatible bucket (AWS S3, MinIO, Cloudflare
// R2, Backblaze B2, ...). Works by key prefix: agent foo's file bar.pdf is
// at s3://<bucket>/<prefix>/foo/bar.pdf.
//
// Use NewS3 to build one from a config block rather than constructing the
// struct directly — the minio client needs specific endpoint parsing.
type S3 struct {
	client *minio.Client
	bucket string
	prefix string // prepended to every key; can be "" for bucket root
	// warnedNoIfMatch keeps the degradation notice to one line per process —
	// "this bucket answers 412 to If-Match" is a property of the backend, not of
	// the individual write that discovered it.
	warnedNoIfMatch atomic.Bool
}

// S3Config holds the bits NewS3 needs. Field naming follows the fastagent.json
// convention so it round-trips through encoding/json cleanly.
type S3Config struct {
	Endpoint  string `json:"endpoint"`         // e.g. "s3.amazonaws.com", "<acct>.r2.cloudflarestorage.com"
	Region    string `json:"region,omitempty"` // AWS region; "" for R2/MinIO
	Bucket    string `json:"bucket"`           // target bucket
	Prefix    string `json:"prefix,omitempty"` // key prefix; useful for multi-env share
	AccessKey string `json:"accessKey"`
	SecretKey string `json:"secretKey"`
	UseSSL    bool   `json:"useSSL"` // default false — most managed services enforce SSL anyway
}

// NewS3 builds an S3 Store. Returns a wrapped error instead of panicking so
// the gateway can fall back to LocalFS on misconfiguration without crashing.
func NewS3(cfg S3Config) (*S3, error) {
	if cfg.Endpoint == "" || cfg.Bucket == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, fmt.Errorf("s3 config requires endpoint/bucket/accessKey/secretKey")
	}
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("s3 client: %w", err)
	}
	return &S3{
		client: client,
		bucket: cfg.Bucket,
		prefix: strings.Trim(cfg.Prefix, "/"),
	}, nil
}

// key is <prefix>/<agentID>[/projects/<pid>[/<sid>]|/sessions/<sid>]/<path>,
// always with forward slashes. The scope segments come from ScopeSegments —
// the one expression of the layout (docs 10 §4 G23) — so this backend and
// LocalFS cannot drift apart.
func (s *S3) key(agentID, projectID, sessionID, p string) string {
	parts := []string{}
	if s.prefix != "" {
		parts = append(parts, s.prefix)
	}
	parts = append(parts, agentID)
	parts = append(parts, ScopeSegments(projectID, sessionID)...)
	parts = append(parts, path.Clean("/" + p)[1:])
	return strings.Join(parts, "/")
}

// scopePrefix returns the listing prefix. Both empty lists the whole
// agent subtree (admin file browser); project/session set narrows it.
func (s *S3) scopePrefix(agentID, projectID, sessionID string) string {
	parts := []string{}
	if s.prefix != "" {
		parts = append(parts, s.prefix)
	}
	parts = append(parts, agentID)
	parts = append(parts, ScopeSegments(projectID, sessionID)...)
	return strings.Join(parts, "/") + "/"
}

func (s *S3) Put(ctx context.Context, agentID, projectID, sessionID, p string, r io.Reader, size int64, contentType string) error {
	if contentType == "" {
		contentType = mime.TypeByExtension(filepath.Ext(p))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
	}
	_, err := s.client.PutObject(ctx, s.bucket, s.key(agentID, projectID, sessionID, p), r, size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	return err
}

// PutIfVersion is S3's conditional PUT: the expectation rides the request as
// If-Match (or If-None-Match: * for "must not exist"), so the check and the
// write share one round trip — the window the port's doc promises to close.
//
// That is only as good as the bucket. S3-compatible services split here: Ceph
// RGW — the store behind DigitalOcean Spaces, which is what this deployment
// runs on — implements the create-only form and nothing else, answering 412 to
// every If-Match even when the object still carries exactly the version just
// read from it (measured 2026-09-22 against nyc3 Spaces). Reporting those 412s
// as ErrVersionConflict made every overwrite of an existing path fail while
// every brand-new path succeeded, and the file tools turned that into "another
// writer changed X" — three retries and three refusals in the 09-22 dev
// session.
//
// So a refused precondition is checked against the object before it is
// believed: if the object still carries `expected`, the refusal was about the
// header and the write lands unconditionally — compare-then-write, the strength
// LocalFS has always had. A superseded expectation still comes back as
// ErrVersionConflict, because then the object really has moved on.
func (s *S3) PutIfVersion(ctx context.Context, agentID, projectID, sessionID, p string, r io.Reader, size int64, contentType string, expected Version) error {
	if contentType == "" {
		contentType = mime.TypeByExtension(filepath.Ext(p))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
	}
	opts := minio.PutObjectOptions{ContentType: contentType}
	key := s.key(agentID, projectID, sessionID, p)
	if expected == VersionAbsent {
		// Create-only. S3 spells it If-None-Match: *.
		opts.SetMatchETagExcept("*")
	} else {
		opts.SetMatchETag(string(expected))
	}
	if _, err := s.client.PutObject(ctx, s.bucket, key, r, size, opts); err != nil {
		if !isPreconditionFailed(err) {
			return mapS3Err(err)
		}
		// Create-only is exact on every backend we have met, so a 412 there is
		// the answer it looks like: the key is taken.
		if expected == VersionAbsent || !s.stillHasVersion(ctx, agentID, projectID, sessionID, p, expected) {
			return ErrVersionConflict
		}
		// The object is exactly what we expected, so nothing superseded us. The
		// failed PUT consumed the body; a rewindable one is required to land it.
		seeker, ok := r.(io.Seeker)
		if !ok {
			return fmt.Errorf("workspace put: this backend does not evaluate If-Match and the payload cannot be replayed, so nothing was written")
		}
		if _, err := seeker.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("workspace put: rewind for the unconditional retry: %w", err)
		}
		if s.warnedNoIfMatch.CompareAndSwap(false, true) {
			slog.Warn("this bucket answers 412 to If-Match; overwrites of existing paths degrade to compare-then-write",
				"bucket", s.bucket, "prefix", s.prefix)
		}
		if _, err := s.client.PutObject(ctx, s.bucket, key, r, size, minio.PutObjectOptions{ContentType: contentType}); err != nil {
			return mapS3Err(err)
		}
	}
	return nil
}

// stillHasVersion reports whether the object carries exactly `expected`. It is
// the witness that separates "this backend does not evaluate If-Match" from "we
// were superseded": both arrive as 412 from the PUT, and only the object itself
// tells them apart.
func (s *S3) stillHasVersion(ctx context.Context, agentID, projectID, sessionID, p string, expected Version) bool {
	info, err := s.Stat(ctx, agentID, projectID, sessionID, p)
	return err == nil && info != nil && info.Version == expected
}

// isPreconditionFailed recognises the 412 both AWS S3 and MinIO return for a
// failed If-Match / If-None-Match.
func isPreconditionFailed(err error) bool {
	var resp minio.ErrorResponse
	if errors.As(err, &resp) {
		return resp.StatusCode == http.StatusPreconditionFailed
	}
	return strings.Contains(err.Error(), "PreconditionFailed")
}

func (s *S3) Get(ctx context.Context, agentID, projectID, sessionID, p string) (io.ReadCloser, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, s.key(agentID, projectID, sessionID, p), minio.GetObjectOptions{})
	if err != nil {
		return nil, mapS3Err(err)
	}
	// Minio's GetObject returns a lazy reader — probe with Stat so callers
	// get the NotFound error upfront, not on the first Read.
	if _, statErr := obj.Stat(); statErr != nil {
		obj.Close()
		return nil, mapS3Err(statErr)
	}
	return obj, nil
}

func (s *S3) Stat(ctx context.Context, agentID, projectID, sessionID, p string) (*ObjectInfo, error) {
	info, err := s.client.StatObject(ctx, s.bucket, s.key(agentID, projectID, sessionID, p), minio.StatObjectOptions{})
	if err != nil {
		return nil, mapS3Err(err)
	}
	return &ObjectInfo{
		Path:        p,
		Size:        info.Size,
		ContentType: info.ContentType,
		ModTime:     info.LastModified.UTC(),
		// The ETag is the object's content identity on S3 (and on every
		// S3-compatible service here). The cleanup script already trusts it as
		// a content hash; PutIfVersion promotes it to a write precondition.
		Version: Version(info.ETag),
	}, nil
}

func (s *S3) List(ctx context.Context, agentID, projectID, sessionID string) ([]ObjectInfo, error) {
	prefix := s.scopePrefix(agentID, projectID, sessionID)
	var out []ObjectInfo
	for obj := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	}) {
		if obj.Err != nil {
			return nil, obj.Err
		}
		rel := strings.TrimPrefix(obj.Key, prefix)
		ctype := obj.ContentType
		if ctype == "" {
			ctype = mime.TypeByExtension(filepath.Ext(rel))
		}
		out = append(out, ObjectInfo{
			Path:        rel,
			Size:        obj.Size,
			ContentType: ctype,
			ModTime:     obj.LastModified.UTC(),
		})
	}
	return out, nil
}

func (s *S3) Delete(ctx context.Context, agentID, projectID, sessionID, p string) error {
	err := s.client.RemoveObject(ctx, s.bucket, s.key(agentID, projectID, sessionID, p), minio.RemoveObjectOptions{})
	if err != nil && !isS3NotFound(err) {
		return err
	}
	return nil
}

// Move relocates every object under the source scope to the destination
// scope. S3 has no native rename: each object is server-side copied
// (CopyObject — bytes never round-trip the gateway) then the original
// is deleted. Refuses to clobber a non-empty destination so a slip
// can't merge two chats' files; returns ErrMoveDestinationExists.
//
// Not atomic: a crash mid-loop leaves the source/dest in a partially
// migrated state. Callers should treat Move as best-effort and fix
// up by re-running it (idempotent — the second call sees a missing
// source and exits cleanly).
func (s *S3) Move(ctx context.Context, agentID, fromProjectID, fromSessionID, toProjectID, toSessionID string) error {
	srcPrefix := s.scopePrefix(agentID, fromProjectID, fromSessionID)
	dstPrefix := s.scopePrefix(agentID, toProjectID, toSessionID)
	if srcPrefix == dstPrefix {
		return nil
	}
	// Destination must be empty.
	for obj := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
		Prefix:    dstPrefix,
		Recursive: true,
		MaxKeys:   1,
	}) {
		if obj.Err != nil {
			return obj.Err
		}
		if obj.Key != "" {
			return ErrMoveDestinationExists
		}
	}
	// Copy each source object to the corresponding destination key,
	// then delete the source.
	for obj := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
		Prefix:    srcPrefix,
		Recursive: true,
	}) {
		if obj.Err != nil {
			return obj.Err
		}
		if obj.Key == "" {
			continue
		}
		dstKey := dstPrefix + strings.TrimPrefix(obj.Key, srcPrefix)
		dstOpt := minio.CopyDestOptions{Bucket: s.bucket, Object: dstKey}
		srcOpt := minio.CopySrcOptions{Bucket: s.bucket, Object: obj.Key}
		if _, err := s.client.CopyObject(ctx, dstOpt, srcOpt); err != nil {
			return fmt.Errorf("s3 copy %s -> %s: %w", obj.Key, dstKey, err)
		}
		if err := s.client.RemoveObject(ctx, s.bucket, obj.Key, minio.RemoveObjectOptions{}); err != nil && !isS3NotFound(err) {
			return fmt.Errorf("s3 remove src %s: %w", obj.Key, err)
		}
	}
	return nil
}

// SignedURL is the main reason we want S3 for cloud deployments: download
// requests can bypass the gateway entirely. TTL is typically a few minutes;
// the browser uses the URL once and discards it.
func (s *S3) SignedURL(ctx context.Context, agentID, projectID, sessionID, p string, ttl time.Duration) (string, error) {
	reqParams := url.Values{}
	u, err := s.client.PresignedGetObject(ctx, s.bucket, s.key(agentID, projectID, sessionID, p), ttl, reqParams)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

// mapS3Err normalises minio's errors to our ErrNotFound so callers can do a
// simple errors.Is check without knowing the SDK.
func mapS3Err(err error) error {
	if err == nil {
		return nil
	}
	if isS3NotFound(err) {
		return ErrNotFound
	}
	return err
}

func isS3NotFound(err error) bool {
	errResp := minio.ToErrorResponse(err)
	if errResp.Code == "NoSuchKey" || errResp.Code == "NoSuchObject" || errResp.Code == "NotFound" {
		return true
	}
	// Some providers wrap it differently.
	return errors.Is(err, ErrNotFound) || strings.Contains(strings.ToLower(err.Error()), "not found")
}
