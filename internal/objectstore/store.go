// Package objectstore keeps large, already-redacted outputs out of SQLite
// rows in a local content-addressed store. Objects are named by the SHA-256
// of their uncompressed bytes, stored gzip-compressed, written atomically and
// verified on every read. Callers must redact content before Put.
package objectstore

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	DigestAlgorithm = "sha256"
	// MaximumObjectBytes bounds one uncompressed object so reads cannot be
	// turned into decompression bombs.
	MaximumObjectBytes = 64 << 20
)

var (
	// ErrNotFound reports that no object has the requested digest.
	ErrNotFound = errors.New("object not found")
	// ErrIntegrity reports a stored object whose content no longer matches
	// its digest or reference.
	ErrIntegrity = errors.New("object integrity violation")
	// ErrInvalidInput reports a rejected digest, media type or size.
	ErrInvalidInput = errors.New("invalid object input")
)

var (
	digestPattern    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	mediaTypePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9!#$&^_.+-]{0,63}/[a-z0-9][a-z0-9!#$&^_.+-]{0,63}$`)
)

// Ref is what an event stores instead of the object content.
type Ref struct {
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	MediaType string `json:"media_type"`
}

type Store struct {
	root string
}

// Open uses root as the object directory, creating it with owner-only
// permissions when missing.
func Open(root string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("open object store: root is required")
	}
	if err := os.MkdirAll(filepath.Join(root, DigestAlgorithm), 0o700); err != nil {
		return nil, fmt.Errorf("open object store %q: %w", root, err)
	}
	return &Store{root: root}, nil
}

// Digest returns the content address of data.
func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return DigestAlgorithm + ":" + hex.EncodeToString(sum[:])
}

func validateDigest(digest string) error {
	if !digestPattern.MatchString(digest) {
		return fmt.Errorf("digest %q must match %s: %w", digest, digestPattern, ErrInvalidInput)
	}
	return nil
}

func (store *Store) path(digest string) string {
	hexDigest := strings.TrimPrefix(digest, DigestAlgorithm+":")
	return filepath.Join(store.root, DigestAlgorithm, hexDigest[:2], hexDigest+".gz")
}

// Put stores data and returns its reference. Storing identical content again
// reuses the existing object.
func (store *Store) Put(data []byte, mediaType string) (Ref, error) {
	if !mediaTypePattern.MatchString(mediaType) {
		return Ref{}, fmt.Errorf("put object: media type %q must match %s: %w", mediaType, mediaTypePattern, ErrInvalidInput)
	}
	if len(data) > MaximumObjectBytes {
		return Ref{}, fmt.Errorf("put object: %d bytes exceeds %d: %w", len(data), MaximumObjectBytes, ErrInvalidInput)
	}
	ref := Ref{Digest: Digest(data), Size: int64(len(data)), MediaType: mediaType}
	path := store.path(ref.Digest)

	if _, err := os.Stat(path); err == nil {
		return ref, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Ref{}, fmt.Errorf("put object %s: %w", ref.Digest, err)
	}
	if err := writeAtomically(path, data); err != nil {
		return Ref{}, fmt.Errorf("put object %s: %w", ref.Digest, err)
	}
	return ref, nil
}

// writeAtomically compresses data into a temporary file in the target
// directory, syncs it and renames it into place, so readers never observe a
// partial object. Concurrent writers of one digest produce identical content.
func writeAtomically(path string, data []byte) (err error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".tmp-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			file.Close()
			os.Remove(file.Name())
		}
	}()

	writer := gzip.NewWriter(file)
	if _, err := writer.Write(data); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	if handle, err := os.Open(directory); err == nil {
		handle.Sync()
		handle.Close()
	}
	return nil
}

// Get returns the content of ref after checking its digest and size.
func (store *Store) Get(ref Ref) ([]byte, error) {
	data, err := store.read(ref.Digest)
	if err != nil {
		return nil, fmt.Errorf("get object %s: %w", ref.Digest, err)
	}
	if int64(len(data)) != ref.Size {
		return nil, fmt.Errorf("get object %s: size %d does not match reference size %d: %w", ref.Digest, len(data), ref.Size, ErrIntegrity)
	}
	return data, nil
}

func (store *Store) read(digest string) ([]byte, error) {
	if err := validateDigest(digest); err != nil {
		return nil, err
	}
	file, err := os.Open(store.path(digest))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	reader, err := gzip.NewReader(file)
	if err != nil {
		return nil, fmt.Errorf("decompress: %w: %w", ErrIntegrity, err)
	}
	var buffer bytes.Buffer
	if _, err := io.Copy(&buffer, io.LimitReader(reader, MaximumObjectBytes+1)); err != nil {
		return nil, fmt.Errorf("decompress: %w: %w", ErrIntegrity, err)
	}
	if buffer.Len() > MaximumObjectBytes {
		return nil, fmt.Errorf("decompressed size exceeds %d: %w", MaximumObjectBytes, ErrIntegrity)
	}
	if Digest(buffer.Bytes()) != digest {
		return nil, fmt.Errorf("content digest mismatch: %w", ErrIntegrity)
	}
	return buffer.Bytes(), nil
}

// Has reports whether an object with digest exists, without reading it.
func (store *Store) Has(digest string) (bool, error) {
	if err := validateDigest(digest); err != nil {
		return false, fmt.Errorf("has object: %w", err)
	}
	_, err := os.Stat(store.path(digest))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("has object %s: %w", digest, err)
	}
	return true, nil
}

// Verify reads every stored object and checks that its content matches the
// digest in its file name. It returns the number of objects checked.
func (store *Store) Verify() (int, error) {
	checked := 0
	base := filepath.Join(store.root, DigestAlgorithm)
	err := filepath.WalkDir(base, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".tmp-") {
			return nil
		}
		hexDigest, ok := strings.CutSuffix(entry.Name(), ".gz")
		digest := DigestAlgorithm + ":" + hexDigest
		if !ok || validateDigest(digest) != nil || filepath.Base(filepath.Dir(path)) != hexDigest[:2] {
			return fmt.Errorf("unexpected file %q: %w", path, ErrIntegrity)
		}
		if _, err := store.read(digest); err != nil {
			return fmt.Errorf("object %s: %w", digest, err)
		}
		checked++
		return nil
	})
	if err != nil {
		return checked, fmt.Errorf("verify object store %q: %w", store.root, err)
	}
	return checked, nil
}
