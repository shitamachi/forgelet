package action

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/shitamachi/forgelet/internal/run"
)

// LoadPreloaded consumes optional administrator-owned bundles in the immutable
// executor image. A missing entry retains the normal public download path; an
// advertised but corrupt bundle fails closed. Neither workflow nor repository
// content selects this directory or its archive checksums.
func LoadPreloaded(ctx context.Context, root string, a run.Action, destination string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if root == "" {
		return false, nil
	}
	fixed, err := RemoteReference(a.Repository + "@" + a.SHA)
	if err != nil || fixed.SHA == "" {
		return false, fmt.Errorf("preloaded Action requires immutable source")
	}
	lock, err := os.Open(filepath.Join(root, "manifest.json"))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("open Action bundle manifest: %w", err)
	}
	data, readErr := io.ReadAll(io.LimitReader(lock, (64<<10)+1))
	closeErr := lock.Close()
	if readErr != nil {
		return false, fmt.Errorf("read Action bundle manifest: %w", readErr)
	}
	if closeErr != nil {
		return false, fmt.Errorf("close Action bundle manifest: %w", closeErr)
	}
	if len(data) > 64<<10 {
		return false, fmt.Errorf("action bundle manifest exceeds 64 KiB")
	}
	var checksums map[string]string
	if err := json.Unmarshal(data, &checksums); err != nil {
		return false, fmt.Errorf("decode Action bundle manifest: %w", err)
	}
	if len(checksums) > 128 {
		return false, fmt.Errorf("action bundle manifest exceeds 128 entries")
	}
	expected, ok := checksums[fixed.Repository+"@"+fixed.SHA]
	if !ok {
		return false, nil
	}
	digest, err := hex.DecodeString(expected)
	if err != nil || len(digest) != sha256.Size {
		return true, fmt.Errorf("invalid Action bundle checksum")
	}
	archive, err := os.Open(filepath.Join(root, filepath.FromSlash(fixed.Repository), fixed.SHA+".tar.gz"))
	if err != nil {
		return true, fmt.Errorf("open Action bundle: %w", err)
	}
	defer func() { _ = archive.Close() }()
	info, err := archive.Stat()
	if err != nil {
		return true, fmt.Errorf("stat Action bundle: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > 32<<20 {
		return true, fmt.Errorf("action bundle must be a regular archive of at most 32 MiB")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, sourceContextReader{ctx, archive}); err != nil {
		return true, fmt.Errorf("hash Action bundle: %w", err)
	}
	if hex.EncodeToString(hash.Sum(nil)) != hex.EncodeToString(digest) {
		return true, fmt.Errorf("action bundle checksum mismatch")
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return true, fmt.Errorf("rewind Action bundle: %w", err)
	}
	commit, err := archiveCommitHeader(sourceContextReader{ctx, archive})
	if err != nil {
		return true, fmt.Errorf("verify Action bundle commit: %w", err)
	}
	if commit != fixed.SHA {
		return true, fmt.Errorf("action bundle commit does not match its immutable reference")
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return true, fmt.Errorf("rewind verified Action bundle: %w", err)
	}
	if err := extractArchive(ctx, sourceContextReader{ctx, archive}, destination); err != nil {
		return true, fmt.Errorf("extract Action bundle: %w", err)
	}
	return true, nil
}

type sourceContextReader struct {
	ctx    context.Context
	source io.Reader
}

func (r sourceContextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(data)
}
