package s3

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/shitamachi/forgelet/internal/run/model"
)

// Store provides S3-backed presigned URLs with repository scoping.
type Store struct {
	client *minio.Client
	bucket string
}

// New creates a Store. endpoint like "localhost:9000", useSSL false for MinIO.
func New(endpoint, accessKey, secretKey, bucket string, useSSL bool) (*Store, error) {
	c, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("s3: new client: %w", err)
	}
	return &Store{client: c, bucket: bucket}, nil
}

// EnsureBucket creates bucket if not exists.
func (s *Store) EnsureBucket(ctx context.Context) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("s3: bucket exists: %w", err)
	}
	if !exists {
		if err := s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{}); err != nil {
			return fmt.Errorf("s3: make bucket: %w", err)
		}
	}
	return nil
}

func sanitizePrefix(key string) string {
	sanitized := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, key)
	if len(sanitized) > 100 {
		sanitized = sanitized[:100]
	}
	sanitized = strings.Trim(sanitized, "-")
	if sanitized == "" {
		sanitized = "key"
	}
	return sanitized
}

func sanitizeKey(key string) string {
	h := sha256.Sum256([]byte(key))
	hash := hex.EncodeToString(h[:])[:8]
	return sanitizePrefix(key) + "-" + hash
}

func cacheKey(repo model.RepositoryRef, key string) string {
	return fmt.Sprintf("%s/%s/%s/cache/%s", repo.Provider, repo.Owner, repo.Name, sanitizeKey(key))
}

func artifactKey(repo model.RepositoryRef, runID model.RunID, name string) string {
	safe := name
	return fmt.Sprintf("%s/%s/%s/artifacts/%s/%s.tar.gz", repo.Provider, repo.Owner, repo.Name, runID, safe)
}

// PresignedGet returns a GET URL for object.
func (s *Store) PresignedGet(ctx context.Context, objectKey string, expiry time.Duration) (string, error) {
	u, err := s.client.PresignedGetObject(ctx, s.bucket, objectKey, expiry, url.Values{})
	if err != nil {
		return "", fmt.Errorf("s3: presign get %s: %w", objectKey, err)
	}
	return u.String(), nil
}

// PresignedPut returns a PUT URL for object.
func (s *Store) PresignedPut(ctx context.Context, objectKey string, expiry time.Duration) (string, error) {
	u, err := s.client.PresignedPutObject(ctx, s.bucket, objectKey, expiry)
	if err != nil {
		return "", fmt.Errorf("s3: presign put %s: %w", objectKey, err)
	}
	return u.String(), nil
}

// Exists reports whether object exists.
func (s *Store) Exists(ctx context.Context, objectKey string) (bool, error) {
	_, err := s.client.StatObject(ctx, s.bucket, objectKey, minio.StatObjectOptions{})
	if err != nil {
		resp := minio.ToErrorResponse(err)
		if resp.Code == "NoSuchKey" {
			return false, nil
		}
		if strings.Contains(err.Error(), "NoSuchKey") || strings.Contains(err.Error(), "not found") {
			return false, nil
		}
		return false, fmt.Errorf("s3: stat %s: %w", objectKey, err)
	}
	return true, nil
}

// CacheResolve checks exact key then restore-keys prefixes. Returns hit flag and object key that hit.
func (s *Store) CacheResolve(ctx context.Context, repo model.RepositoryRef, key string, restoreKeys []string) (hit bool, hitKey string, err error) {
	ck := cacheKey(repo, key)
	ok, err := s.Exists(ctx, ck)
	if err != nil {
		return false, "", err
	}
	if ok {
		return true, ck, nil
	}
	for _, rk := range restoreKeys {
		rk = strings.TrimSpace(rk)
		if rk == "" {
			continue
		}
		rkExact := cacheKey(repo, rk)
		ok, err := s.Exists(ctx, rkExact)
		if err != nil {
			return false, "", err
		}
		if ok {
			return true, rkExact, nil
		}
		// Prefix fallback: list objects with prefix of sanitized restore-key
		prefix := fmt.Sprintf("%s/%s/%s/cache/%s", repo.Provider, repo.Owner, repo.Name, sanitizePrefix(rk))
		opts := minio.ListObjectsOptions{Prefix: prefix, Recursive: true}
		for obj := range s.client.ListObjects(ctx, s.bucket, opts) {
			if obj.Err != nil {
				continue
			}
			return true, obj.Key, nil
		}
	}
	return false, "", nil
}

// CachePutURL returns presigned PUT URL for cache key.
func (s *Store) CachePutURL(ctx context.Context, repo model.RepositoryRef, key string) (string, error) {
	return s.PresignedPut(ctx, cacheKey(repo, key), 10*time.Minute)
}

// CacheGetURL returns presigned GET URL for given object key.
func (s *Store) CacheGetURL(ctx context.Context, objectKey string) (string, error) {
	return s.PresignedGet(ctx, objectKey, 10*time.Minute)
}

// ArtifactPutURL returns presigned PUT for artifact.
func (s *Store) ArtifactPutURL(ctx context.Context, repo model.RepositoryRef, runID model.RunID, name string) (string, error) {
	return s.PresignedPut(ctx, artifactKey(repo, runID, name), 10*time.Minute)
}

// ArtifactGetURL returns presigned GET for artifact.
func (s *Store) ArtifactGetURL(ctx context.Context, repo model.RepositoryRef, runID model.RunID, name string) (string, error) {
	return s.PresignedGet(ctx, artifactKey(repo, runID, name), 10*time.Minute)
}
