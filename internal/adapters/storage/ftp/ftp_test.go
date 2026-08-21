package ftp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/jlaffaye/ftp"
	"github.com/pkg-ru/imager/internal/domain/object"
)

// fakeConn — минимальный in-memory FTP-сервер, реализующий интерфейс conn.
type fakeConn struct {
	files    map[string][]byte
	dirs     map[string]bool
	features map[string]bool
	// listFailures — сколько раз List должен вернуть ошибку соединения
	// перед успехом (для проверки retry).
	listFailures int
}

func newFakeConn() *fakeConn {
	return &fakeConn{
		files:    map[string][]byte{},
		dirs:     map[string]bool{},
		features: map[string]bool{},
	}
}

func (c *fakeConn) Login(user, password string) error { return nil }
func (c *fakeConn) Quit() error                       { return nil }
func (c *fakeConn) Feature(cmd string) bool {
	// Явно заданные значения имеют приоритет.
	if v, ok := c.features[cmd]; ok {
		return v
	}
	// Базовые команды RFC 959 считаются поддерживаемыми по умолчанию.
	switch cmd {
	case "STOR", "RNFR", "RNTO", "DELE", "MKD":
		return true
	}
	return false
}
func (c *fakeConn) List(path string) ([]*ftp.Entry, error) {
	if c.listFailures > 0 {
		c.listFailures--
		return nil, errors.New("connection reset")
	}
	if data, ok := c.files[path]; ok {
		return []*ftp.Entry{{Name: path, Type: ftp.EntryTypeFile, Size: uint64(len(data))}}, nil
	}
	if c.dirs[path] {
		return []*ftp.Entry{{Name: path, Type: ftp.EntryTypeFolder}}, nil
	}
	return nil, nil
}
func (c *fakeConn) Retr(path string) (io.ReadCloser, error) {
	data, ok := c.files[path]
	if !ok {
		return nil, errors.New("no such file")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}
func (c *fakeConn) Stor(path string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	c.files[path] = data
	return nil
}
func (c *fakeConn) Rename(from, to string) error {
	data, ok := c.files[from]
	if !ok {
		return errors.New("no such file")
	}
	delete(c.files, from)
	c.files[to] = data
	return nil
}
func (c *fakeConn) Delete(path string) error {
	delete(c.files, path)
	return nil
}
func (c *fakeConn) MakeDir(path string) error {
	c.dirs[path] = true
	return nil
}

func dialerFor(c *fakeConn) func(ctx context.Context, addr string, tls bool) (conn, error) {
	return func(ctx context.Context, addr string, tls bool) (conn, error) {
		return c, nil
	}
}

func TestFTPSourceLookupOpen(t *testing.T) {
	conn := newFakeConn()
	conn.files["dir/file.bin"] = []byte("source payload")

	s, err := NewSourceStore(Options{Addr: "localhost:21", User: "u", Dialer: dialerFor(conn)})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}

	key := object.ObjectKey("dir/file.bin")
	meta, err := s.Lookup(context.Background(), key)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if meta.Size != int64(len("source payload")) {
		t.Fatalf("size = %d", meta.Size)
	}

	art, err := s.Open(context.Background(), key)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer art.Close()
	got, err := io.ReadAll(art)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "source payload" {
		t.Fatalf("got %q", got)
	}
}

func TestFTPSourceNotFound(t *testing.T) {
	conn := newFakeConn()
	s, err := NewSourceStore(Options{Addr: "localhost:21", User: "u", Dialer: dialerFor(conn)})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}
	if _, err := s.Lookup(context.Background(), "missing.jpg"); !errors.Is(err, object.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestFTPResultStorePlainFTPAllowed(t *testing.T) {
	conn := newFakeConn()
	r, err := NewResultStore(Options{Addr: "localhost:21", User: "u", Password: "p", Dialer: dialerFor(conn)})
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}
	ctx := context.Background()
	key := object.ObjectKey("out/result.bin")
	payload := []byte("result payload")

	if err := r.Publish(ctx, key, bytes.NewReader(payload), object.PublishOptions{}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	meta, err := r.Lookup(ctx, key)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if meta.Size != int64(len(payload)) {
		t.Fatalf("size = %d, want %d", meta.Size, len(payload))
	}
	art, err := r.Open(ctx, key)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer art.Close()
	got, err := io.ReadAll(art)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("read mismatch")
	}
}

func TestFTPSResultPublishRead(t *testing.T) {
	conn := newFakeConn()
	r, err := NewResultStore(Options{Addr: "localhost:21", User: "u", Password: "p", TLS: true, TLSVerify: true, Dialer: dialerFor(conn)})
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}
	ctx := context.Background()
	key := object.ObjectKey("out/result.bin")
	payload := []byte("result payload")

	if err := r.Publish(ctx, key, bytes.NewReader(payload), object.PublishOptions{}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	meta, err := r.Lookup(ctx, key)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if meta.Size != int64(len(payload)) {
		t.Fatalf("size = %d, want %d", meta.Size, len(payload))
	}
	art, err := r.Open(ctx, key)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer art.Close()
	got, err := io.ReadAll(art)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("read mismatch")
	}
}

func TestFTPResultPublishNoOverwriteConflict(t *testing.T) {
	conn := newFakeConn()
	conn.files["out/result.bin"] = []byte("existing")
	r, err := NewResultStore(Options{Addr: "localhost:21", User: "u", Password: "p", Dialer: dialerFor(conn)})
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}
	err = r.Publish(context.Background(), "out/result.bin", bytes.NewReader([]byte("new")), object.PublishOptions{NoOverwrite: true})
	if !errors.Is(err, object.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	// Временный файл должен быть удалён.
	if _, ok := conn.files["out/result.bin.tmp"]; ok {
		t.Fatal("temp file not cleaned up after conflict")
	}
	// Существующий файл не должен быть перезаписан.
	if string(conn.files["out/result.bin"]) != "existing" {
		t.Fatalf("existing file overwritten: %q", conn.files["out/result.bin"])
	}
}

func TestFTPResultPublishNoOverwriteSuccess(t *testing.T) {
	conn := newFakeConn()
	r, err := NewResultStore(Options{Addr: "localhost:21", User: "u", Password: "p", Dialer: dialerFor(conn)})
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}
	err = r.Publish(context.Background(), "out/result.bin", bytes.NewReader([]byte("new")), object.PublishOptions{NoOverwrite: true})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if string(conn.files["out/result.bin"]) != "new" {
		t.Fatalf("unexpected content: %q", conn.files["out/result.bin"])
	}
	if _, ok := conn.files["out/result.bin.tmp"]; ok {
		t.Fatal("temp file not cleaned up after success")
	}
}

func TestFTPResultPublishMissingCapability(t *testing.T) {
	conn := newFakeConn()
	conn.features["STOR"] = false
	r, err := NewResultStore(Options{Addr: "localhost:21", User: "u", Password: "p", Dialer: dialerFor(conn)})
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}
	err = r.Publish(context.Background(), "out/result.bin", bytes.NewReader([]byte("new")), object.PublishOptions{})
	if !errors.Is(err, object.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

func TestFTPUnsafeKey(t *testing.T) {
	conn := newFakeConn()
	s, err := NewSourceStore(Options{Addr: "localhost:21", User: "u", Dialer: dialerFor(conn)})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}
	if _, err := s.Lookup(context.Background(), "../escape"); !errors.Is(err, object.ErrUnsafePath) {
		t.Fatalf("expected ErrUnsafePath, got %v", err)
	}
}

// TestFTPSVerifyForbidden проверяет, что отключение TLS-верификации для
// FTPS запрещено на этапе валидации.
func TestFTPSVerifyForbidden(t *testing.T) {
	conn := newFakeConn()
	if _, err := NewResultStore(Options{
		Addr: "localhost:21", User: "u", Password: "p",
		TLS: true, TLSVerify: false, Dialer: dialerFor(conn),
	}); err == nil {
		t.Fatal("expected error for tls-verify=false")
	}
}

// TestFTPSourceLookupRetry проверяет, что при ошибке соединения операция
// повторяется до MaxAttempts и завершается успехом после восстановления.
func TestFTPSourceLookupRetry(t *testing.T) {
	conn := newFakeConn()
	conn.files["dir/file.bin"] = []byte("payload")
	conn.listFailures = 2
	s, err := NewSourceStore(Options{Addr: "localhost:21", User: "u", Dialer: dialerFor(conn), MaxAttempts: 3})
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

// TestFTPSourceLookupRetryExhausted проверяет, что при исчерпании попыток
// возвращается последняя ошибка соединения.
func TestFTPSourceLookupRetryExhausted(t *testing.T) {
	conn := newFakeConn()
	conn.files["dir/file.bin"] = []byte("payload")
	conn.listFailures = 100
	s, err := NewSourceStore(Options{Addr: "localhost:21", User: "u", Dialer: dialerFor(conn), MaxAttempts: 2})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}
	if _, err := s.Lookup(context.Background(), "dir/file.bin"); err == nil {
		t.Fatal("expected error after exhausting attempts")
	}
}
