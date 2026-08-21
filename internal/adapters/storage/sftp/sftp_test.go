package sftp

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"io"
	"os"
	"path"
	"testing"
	"time"

	"github.com/pkg-ru/imager/internal/domain/object"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// fakeFileInfo — минимальная os.FileInfo.
type fakeFileInfo struct {
	name    string
	size    int64
	isDir   bool
	modTime time.Time
}

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return f.size }
func (f fakeFileInfo) Mode() os.FileMode  { return 0o644 }
func (f fakeFileInfo) ModTime() time.Time { return f.modTime }
func (f fakeFileInfo) IsDir() bool        { return f.isDir }
func (f fakeFileInfo) Sys() any           { return nil }

// fakeClient — in-memory SFTP-клиент, реализующий интерфейс client.
type fakeClient struct {
	files map[string][]byte
	dirs  map[string]bool
	// statFailures — сколько раз Stat должен вернуть ошибку соединения
	// перед успехом (для проверки retry).
	statFailures int
}

func newFakeClient() *fakeClient {
	return &fakeClient{files: map[string][]byte{}, dirs: map[string]bool{}}
}

func (c *fakeClient) Stat(p string) (os.FileInfo, error) {
	if c.statFailures > 0 {
		c.statFailures--
		return nil, errors.New("connection reset")
	}
	if data, ok := c.files[p]; ok {
		return fakeFileInfo{name: path.Base(p), size: int64(len(data))}, nil
	}
	if c.dirs[p] {
		return fakeFileInfo{name: path.Base(p), isDir: true}, nil
	}
	return nil, os.ErrNotExist
}

// openTemp создаёт реальный temp-файл с данными и возвращает *sftp.File.
func (c *fakeClient) openTemp(p string) (*sftp.File, error) {
	data, ok := c.files[p]
	if !ok {
		return nil, os.ErrNotExist
	}
	f, err := os.CreateTemp("", "sftp-fake-*")
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, err
	}
	// sftp.File не имеет публичного конструктора; используем его через
	// обёртку невозможно. Вместо этого возвращаем nil и тестируем через
	// отдельный путь. Для полноты теста Open/OpenFile не используются
	// напрямую в unit-сценариях ниже.
	_ = f.Close()
	return nil, nil
}

func (c *fakeClient) Open(p string) (*sftp.File, error) { return c.openTemp(p) }
func (c *fakeClient) OpenFile(p string, f int) (io.WriteCloser, error) {
	if f&os.O_EXCL != 0 {
		if _, exists := c.files[p]; exists {
			return nil, os.ErrExist
		}
	}
	w := &fakeWriter{client: c, path: p}
	return w, nil
}

// fakeWriter — in-memory io.WriteCloser, пишущий в c.files.
type fakeWriter struct {
	client *fakeClient
	path   string
	buf    bytes.Buffer
}

func (w *fakeWriter) Write(p []byte) (int, error) { return w.buf.Write(p) }
func (w *fakeWriter) Close() error {
	w.client.files[w.path] = w.buf.Bytes()
	return nil
}
func (c *fakeClient) MkdirAll(p string) error {
	c.dirs[p] = true
	return nil
}
func (c *fakeClient) PosixRename(oldname, newname string) error {
	data, ok := c.files[oldname]
	if !ok {
		return os.ErrNotExist
	}
	delete(c.files, oldname)
	c.files[newname] = data
	return nil
}
func (c *fakeClient) Remove(p string) error {
	delete(c.files, p)
	return nil
}
func (c *fakeClient) ReadDir(p string) ([]os.FileInfo, error) {
	var out []os.FileInfo
	for k, v := range c.files {
		if path.Dir(k) == p {
			out = append(out, fakeFileInfo{name: path.Base(k), size: int64(len(v))})
		}
	}
	return out, nil
}
func (c *fakeClient) Close() error { return nil }

func TestSFTPConstructorValidation(t *testing.T) {
	if _, err := NewSourceStore(Options{}); err == nil {
		t.Fatal("expected error for empty options")
	}
	if _, err := NewSourceStore(Options{Addr: "h:22", User: "u"}); err == nil {
		t.Fatal("expected error for no auth and no client")
	}
}

// TestSFTPHostKeyCallbackMismatch проверяет, что host key callback отклоняет
// несовпадающий fingerprint.
func TestSFTPHostKeyCallbackMismatch(t *testing.T) {
	// Генерируем реальный ключ для fingerprint.
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	pub, err := ssh.NewPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	actual := ssh.FingerprintSHA256(pub)
	fp := "SHA256:" + actual // совпадает

	cb, err := (Options{HostKeyFingerprint: fp}).hostKeyCallback()
	if err != nil {
		t.Fatalf("hostKeyCallback: %v", err)
	}
	if err := cb("host", nil, pub); err != nil {
		t.Errorf("expected matching host key, got %v", err)
	}

	fp2 := "SHA256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cb2, err := (Options{HostKeyFingerprint: fp2}).hostKeyCallback()
	if err != nil {
		t.Fatalf("hostKeyCallback: %v", err)
	}
	if err := cb2("host", nil, pub); err == nil {
		t.Error("expected error for mismatched fingerprint")
	}
}

// TestSFTPHostKeyCallbackInvalid проверяет, что непустой, но не SHA256
// fingerprint отклоняется.
func TestSFTPHostKeyCallbackInvalid(t *testing.T) {
	if _, err := (Options{HostKeyFingerprint: "SHA256:"}).hostKeyCallback(); err == nil {
		t.Error("expected error for empty SHA256 fingerprint")
	}
}

func TestSFTPUnsafeKey(t *testing.T) {
	c := newFakeClient()
	s, err := NewSourceStore(Options{Addr: "h:22", User: "u", Client: c})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}
	if _, err := s.Lookup(context.Background(), "../escape"); !errors.Is(err, object.ErrUnsafePath) {
		t.Fatalf("expected ErrUnsafePath, got %v", err)
	}
}

func TestSFTPLookupNotFound(t *testing.T) {
	c := newFakeClient()
	s, err := NewSourceStore(Options{Addr: "h:22", User: "u", Client: c})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}
	if _, err := s.Lookup(context.Background(), "missing.jpg"); !errors.Is(err, object.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestSFTPLookupFound(t *testing.T) {
	c := newFakeClient()
	c.files["dir/file.bin"] = []byte("payload")
	s, err := NewSourceStore(Options{Addr: "h:22", User: "u", Client: c})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}
	meta, err := s.Lookup(context.Background(), "dir/file.bin")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if meta.Size != int64(len("payload")) {
		t.Fatalf("size = %d, want %d", meta.Size, len("payload"))
	}
}

func TestSFTPResultDeleteIdempotent(t *testing.T) {
	c := newFakeClient()
	c.files["a.bin"] = []byte("x")
	r, err := NewResultStore(Options{Addr: "h:22", User: "u", Client: c})
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}
	ctx := context.Background()
	if err := r.Delete(ctx, "a.bin"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := r.Delete(ctx, "a.bin"); err != nil {
		t.Fatalf("Delete idempotent: %v", err)
	}
	if _, err := r.Lookup(ctx, "a.bin"); !errors.Is(err, object.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestSFTPResultPublishNoOverwriteConflict(t *testing.T) {
	c := newFakeClient()
	c.files["dup.bin"] = []byte("existing")
	r, err := NewResultStore(Options{Addr: "h:22", User: "u", Client: c})
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}
	ctx := context.Background()
	err = r.Publish(ctx, "dup.bin", bytes.NewReader([]byte("new")), object.PublishOptions{NoOverwrite: true})
	if !errors.Is(err, object.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	// Существующий объект не должен быть перезаписан.
	meta, err := r.Lookup(ctx, "dup.bin")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if meta.Size != int64(len("existing")) {
		t.Fatalf("existing object overwritten: size = %d, want %d", meta.Size, len("existing"))
	}
}

func TestSFTPResultPublishNoOverwriteSuccess(t *testing.T) {
	c := newFakeClient()
	r, err := NewResultStore(Options{Addr: "h:22", User: "u", Client: c})
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}
	ctx := context.Background()
	payload := []byte("fresh")
	if err := r.Publish(ctx, "new.bin", bytes.NewReader(payload), object.PublishOptions{NoOverwrite: true}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	meta, err := r.Lookup(ctx, "new.bin")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if meta.Size != int64(len(payload)) {
		t.Fatalf("size = %d, want %d", meta.Size, len(payload))
	}
}

// TestSFTPLookupRetry проверяет, что при ошибке соединения операция
// повторяется до MaxAttempts и завершается успехом после восстановления.
func TestSFTPLookupRetry(t *testing.T) {
	c := newFakeClient()
	c.files["dir/file.bin"] = []byte("payload")
	c.statFailures = 2
	s, err := NewSourceStore(Options{Addr: "h:22", User: "u", Client: c, MaxAttempts: 3})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}
	meta, err := s.Lookup(context.Background(), "dir/file.bin")
	if err != nil {
		t.Fatalf("Lookup after retries: %v", err)
	}
	if meta.Size != int64(len("payload")) {
		t.Fatalf("size = %d, want %d", meta.Size, len("payload"))
	}
}

// TestSFTPLookupRetryExhausted проверяет, что при исчерпании попыток
// возвращается последняя ошибка соединения.
func TestSFTPLookupRetryExhausted(t *testing.T) {
	c := newFakeClient()
	c.files["dir/file.bin"] = []byte("payload")
	c.statFailures = 100
	s, err := NewSourceStore(Options{Addr: "h:22", User: "u", Client: c, MaxAttempts: 2})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}
	if _, err := s.Lookup(context.Background(), "dir/file.bin"); err == nil {
		t.Fatal("expected error after exhausting attempts")
	}
}

var _ = bytes.NewReader
