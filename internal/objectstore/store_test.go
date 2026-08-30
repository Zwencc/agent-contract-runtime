package objectstore

import (
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func openTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "objects")
	store, err := Open(root)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return store, root
}

func objectFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			files = append(files, path)
		}
		return err
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return files
}

func gzipBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	writer.Write(data)
	writer.Close()
	return buffer.Bytes()
}

func TestPutGetRoundTrip(t *testing.T) {
	store, root := openTestStore(t)
	data := []byte(`{"stdout":"ok"}`)

	ref, err := store.Put(data, "application/json")
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if ref.Digest != Digest(data) || ref.Size != int64(len(data)) || ref.MediaType != "application/json" {
		t.Fatalf("Put() = %+v", ref)
	}
	got, err := store.Get(ref)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("Get() = %q, %v", got, err)
	}
	if ok, err := store.Has(ref.Digest); err != nil || !ok {
		t.Fatalf("Has() = %v, %v", ok, err)
	}

	again, err := store.Put(data, "text/plain")
	if err != nil || again.Digest != ref.Digest {
		t.Fatalf("second Put() = %+v, %v", again, err)
	}
	files := objectFiles(t, root)
	if len(files) != 1 {
		t.Fatalf("store holds %d files, want 1 deduplicated object", len(files))
	}
	info, err := os.Stat(files[0])
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("object mode = %v, %v; want 0600", info.Mode().Perm(), err)
	}
	if info, err := os.Stat(filepath.Join(root, DigestAlgorithm)); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("object directory mode = %v, %v; want 0700", info.Mode().Perm(), err)
	}
}

func TestLargeOutputIsCompressed(t *testing.T) {
	store, root := openTestStore(t)
	data := bytes.Repeat([]byte("go test ./... PASS\n"), 1<<16)

	ref, err := store.Put(data, "text/plain")
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	info, err := os.Stat(objectFiles(t, root)[0])
	if err != nil {
		t.Fatalf("stat object: %v", err)
	}
	if info.Size()*10 > ref.Size {
		t.Fatalf("stored %d bytes for %d bytes of repetitive output", info.Size(), ref.Size)
	}
	if got, err := store.Get(ref); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("Get() of large object failed: %v", err)
	}
}

func TestInvalidInputRejected(t *testing.T) {
	store, _ := openTestStore(t)

	for _, mediaType := range []string{"", "json", "Application/JSON", "text/plain; charset=utf-8", "../x"} {
		if _, err := store.Put([]byte("x"), mediaType); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("Put(media type %q) error = %v, want ErrInvalidInput", mediaType, err)
		}
	}
	if _, err := store.Put(make([]byte, MaximumObjectBytes+1), "application/octet-stream"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Put() of oversized object error = %v, want ErrInvalidInput", err)
	}
	for _, digest := range []string{"", "sha256:abc", "md5:" + strings.Repeat("0", 64), "sha256:../../" + strings.Repeat("0", 58),
		"sha256:" + strings.Repeat("A", 64)} {
		if _, err := store.Get(Ref{Digest: digest}); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("Get(%q) error = %v, want ErrInvalidInput", digest, err)
		}
		if _, err := store.Has(digest); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("Has(%q) error = %v, want ErrInvalidInput", digest, err)
		}
	}
	if _, err := Open(" "); err == nil {
		t.Fatal("Open() with empty root error = nil")
	}
}

func TestMissingObject(t *testing.T) {
	store, _ := openTestStore(t)
	digest := Digest([]byte("never stored"))

	if _, err := store.Get(Ref{Digest: digest, Size: 12}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() error = %v, want ErrNotFound", err)
	}
	if ok, err := store.Has(digest); err != nil || ok {
		t.Fatalf("Has() = %v, %v; want false", ok, err)
	}
}

func TestTamperingDetected(t *testing.T) {
	cases := map[string][]byte{
		"replaced content": nil, // filled with valid gzip of other content below
		"not gzip":         []byte("plain bytes"),
		"truncated gzip":   nil,
	}
	for name, replacement := range cases {
		t.Run(name, func(t *testing.T) {
			store, root := openTestStore(t)
			ref, err := store.Put([]byte("original output"), "text/plain")
			if err != nil {
				t.Fatalf("Put() error = %v", err)
			}
			switch name {
			case "replaced content":
				replacement = gzipBytes(t, []byte("forged output"))
			case "truncated gzip":
				full := gzipBytes(t, []byte("original output"))
				replacement = full[:len(full)-6]
			}
			if err := os.WriteFile(objectFiles(t, root)[0], replacement, 0o600); err != nil {
				t.Fatalf("tamper: %v", err)
			}
			if _, err := store.Get(ref); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("Get() error = %v, want ErrIntegrity", err)
			}
			if _, err := store.Verify(); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("Verify() error = %v, want ErrIntegrity", err)
			}
		})
	}
}

func TestReferenceSizeMismatch(t *testing.T) {
	store, _ := openTestStore(t)
	ref, err := store.Put([]byte("output"), "text/plain")
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	ref.Size++
	if _, err := store.Get(ref); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("Get() with wrong size error = %v, want ErrIntegrity", err)
	}
}

func TestDecompressionBombRejected(t *testing.T) {
	store, _ := openTestStore(t)
	digest := Digest([]byte("bomb"))
	path := store.path(digest)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, gzipBytes(t, make([]byte, MaximumObjectBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(Ref{Digest: digest, Size: 4}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("Get() of oversized object error = %v, want ErrIntegrity", err)
	}
}

func TestConcurrentPutSameContent(t *testing.T) {
	store, root := openTestStore(t)
	data := bytes.Repeat([]byte("shared"), 1000)

	var wait sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := store.Put(data, "text/plain"); err != nil {
				errs <- err
			}
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Put() error = %v", err)
	}
	if files := objectFiles(t, root); len(files) != 1 {
		t.Fatalf("store holds %v, want one object and no temporary files", files)
	}
	if count, err := store.Verify(); err != nil || count != 1 {
		t.Fatalf("Verify() = %d, %v", count, err)
	}
}

func TestVerify(t *testing.T) {
	store, root := openTestStore(t)
	for _, content := range []string{"a", "b", "c"} {
		if _, err := store.Put([]byte(content), "text/plain"); err != nil {
			t.Fatalf("Put() error = %v", err)
		}
	}
	// Leftovers of an interrupted write are ignored.
	directory := filepath.Dir(objectFiles(t, root)[0])
	if err := os.WriteFile(filepath.Join(directory, ".tmp-123"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if count, err := store.Verify(); err != nil || count != 3 {
		t.Fatalf("Verify() = %d, %v; want 3", count, err)
	}

	if err := os.WriteFile(filepath.Join(directory, "stray.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Verify(); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("Verify() with stray file error = %v, want ErrIntegrity", err)
	}
}

func TestMisplacedObjectDetected(t *testing.T) {
	store, root := openTestStore(t)
	ref, err := store.Put([]byte("content"), "text/plain")
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	source := objectFiles(t, root)[0]
	wrongDirectory := filepath.Join(root, DigestAlgorithm, "zz")
	if err := os.MkdirAll(wrongDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(source, filepath.Join(wrongDirectory, filepath.Base(source))); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Verify(); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("Verify() with misplaced object error = %v, want ErrIntegrity", err)
	}
	if _, err := store.Get(ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() of misplaced object error = %v, want ErrNotFound", err)
	}
}
