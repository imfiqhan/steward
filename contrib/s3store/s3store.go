// Package s3store implements steward.Storage over any S3-compatible object
// store (AWS S3, MinIO, R2, Backblaze, ...) via the MinIO client.
//
//	store, _ := s3store.New(s3store.Config{
//	    Endpoint:  "s3.amazonaws.com",
//	    AccessKey: os.Getenv("AWS_ACCESS_KEY_ID"),
//	    SecretKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
//	    Bucket:    "my-app-uploads",
//	    BaseURL:   "https://cdn.example.com", // public URL prefix for stored files
//	    UseSSL:    true,
//	})
//	admin, _ := steward.New(steward.Config{Storage: store, ...})
package s3store

import (
	"context"
	"errors"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	steward "github.com/imfiqhan/steward"
)

// Config describes the target bucket.
type Config struct {
	Endpoint  string // host[:port], no scheme
	AccessKey string
	SecretKey string
	Bucket    string
	Region    string
	UseSSL    bool
	// BaseURL is the public prefix used by URL(); files must be readable
	// there (public bucket policy or a CDN in front). When empty, URL()
	// falls back to the endpoint/bucket path.
	//
	// It is only consulted when signing is turned off: by default the panel
	// asks for a presigned URL, which works against a private bucket and needs
	// no public prefix at all.
	BaseURL string
}

// Storage adapts an S3 bucket to steward.Storage.
type Storage struct {
	client  *minio.Client
	bucket  string
	baseURL string
}

var _ steward.Storage = (*Storage)(nil)

// New connects a client (no network call until first use).
func New(cfg Config) (*Storage, error) {
	if cfg.Endpoint == "" || cfg.Bucket == "" {
		return nil, errors.New("s3store: Endpoint and Bucket are required")
	}
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, err
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		scheme := "http"
		if cfg.UseSSL {
			scheme = "https"
		}
		base = scheme + "://" + cfg.Endpoint + "/" + cfg.Bucket
	}
	return &Storage{client: client, bucket: cfg.Bucket, baseURL: base}, nil
}

// Put implements steward.Storage.
func (s *Storage) Put(ctx context.Context, name string, r io.Reader, size int64, contentType string) (string, error) {
	name = strings.TrimLeft(name, "/")
	_, err := s.client.PutObject(ctx, s.bucket, name, r, size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return "", err
	}
	return s.URL(name), nil
}

// Delete implements steward.Storage; deleting a missing object is not an
// error.
func (s *Storage) Delete(ctx context.Context, name string) error {
	return s.client.RemoveObject(ctx, s.bucket, strings.TrimLeft(name, "/"), minio.RemoveObjectOptions{})
}

// URL implements steward.Storage.
func (s *Storage) URL(name string) string {
	segs := strings.Split(strings.TrimLeft(name, "/"), "/")
	for i, seg := range segs {
		segs[i] = url.PathEscape(seg)
	}
	return s.baseURL + "/" + strings.Join(segs, "/")
}

// SignedURL implements steward.SignedURLStorage: a presigned GET good for ttl,
// so the bucket behind it never needs a public read policy.
//
// steward.Admin.StorageURL prefers this over URL when a backend offers it, which
// is what makes a private bucket work without changing anything in a resource.
func (s *Storage) SignedURL(ctx context.Context, name string, ttl time.Duration) (string, error) {
	u, err := s.client.PresignedGetObject(ctx, s.bucket, strings.TrimLeft(name, "/"), ttl, nil)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}
