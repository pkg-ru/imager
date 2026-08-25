package ftp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"

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
	// deleteNoSuchFile — если true, Delete возвращает 550 (File unavailable)
	// для отсутствующих файлов (проверка идемпотентности Delete).
	deleteNoSuchFile bool
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
		// Перечисляем содержимое каталога: файлы с префиксом path+"/"
		// и подкаталоги, которые не входят в другой каталог.
		base := path
		if base != "" && !strings.HasSuffix(base, "/") {
			base += "/"
		}
		var out []*ftp.Entry
		seenDir := map[string]bool{}
		for k := range c.files {
			if strings.HasPrefix(k, base) {
				name := strings.TrimPrefix(k, base)
				if idx := strings.Index(name, "/"); idx >= 0 {
					dir := name[:idx]
					if !seenDir[dir] {
						seenDir[dir] = true
						out = append(out, &ftp.Entry{Name: dir, Type: ftp.EntryTypeFolder})
					}
					continue
				}
				out = append(out, &ftp.Entry{Name: name, Type: ftp.EntryTypeFile, Size: uint64(len(c.files[k]))})
			}
		}
		for d := range c.dirs {
			if d == path {
				continue
			}
			if strings.HasPrefix(d, base) && !strings.Contains(strings.TrimPrefix(d, base), "/") {
				if !seenDir[d[len(base):]] {
					seenDir[d[len(base):]] = true
					out = append(out, &ftp.Entry{Name: d[len(base):], Type: ftp.EntryTypeFolder})
				}
			}
		}
		return out, nil
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
	if c.deleteNoSuchFile {
		if _, ok := c.files[path]; !ok {
			return &textproto.Error{Code: 550, Msg: "file unavailable"}
		}
	}
	delete(c.files, path)
	return nil
}
func (c *fakeConn) MakeDir(path string) error {
	c.dirs[path] = true
	return nil
}
func (c *fakeConn) RemoveDir(path string) error {
	delete(c.dirs, path)
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

// TestFTPPoolConcurrentDial проверяет, что пул выполняет dial вне блокировки:
// параллельные acquire могут создавать несколько соединений одновременно,
// а не сериализуются на одном мьютексе.
func TestFTPPoolConcurrentDial(t *testing.T) {
	var mu sync.Mutex
	dials := 0
	// dial блокируется на 50ms, имитируя медленную сеть.
	dial := func(ctx context.Context, addr string, tls bool) (conn, error) {
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		dials++
		mu.Unlock()
		return newFakeConn(), nil
	}
	pool := newConnPool(Options{Addr: "localhost:21", User: "u", Dialer: dial, MaxConns: 4})

	ctx := context.Background()
	const n = 4
	conns := make([]*pooledConn, n)
	for i := 0; i < n; i++ {
		c, err := pool.acquire(ctx)
		if err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		conns[i] = c
	}
	mu.Lock()
	got := dials
	mu.Unlock()
	if got != n {
		t.Fatalf("dials = %d, want %d (dial должен выполняться параллельно)", got, n)
	}
	// Возвращаем все соединения в пул (discard, т.к. release-цепочка удалена).
	for _, c := range conns {
		c.discard()
	}
}

// TestFTPDeleteByPrefix проверяет рекурсивное пакетное удаление каталога
// ассетов: удаляются только ключи с границей '/', возвращается число
// удалённых файлов, операция идемпотентна.
func TestFTPDeleteByPrefix(t *testing.T) {
	conn := newFakeConn()
	conn.dirs["photo-jpg"] = true
	conn.files["photo-jpg/thumb.webp"] = []byte("a")
	conn.files["photo-jpg/preview.webp"] = []byte("b")
	conn.files["photo-jpg2/thumb.webp"] = []byte("c")
	conn.files["other/x.webp"] = []byte("d")
	r, err := NewResultStore(Options{Addr: "localhost:21", User: "u", Password: "p", Dialer: dialerFor(conn)})
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}
	ctx := context.Background()

	n, err := r.DeleteByPrefix(ctx, object.ObjectKey("photo-jpg/"))
	if err != nil {
		t.Fatalf("DeleteByPrefix: %v", err)
	}
	if n != 2 {
		t.Fatalf("deleted = %d, want 2", n)
	}
	for _, k := range []string{"photo-jpg/thumb.webp", "photo-jpg/preview.webp"} {
		if _, ok := conn.files[k]; ok {
			t.Errorf("file %q not deleted", k)
		}
	}
	// Соседние префиксы не тронуты.
	if _, ok := conn.files["photo-jpg2/thumb.webp"]; !ok {
		t.Error("photo-jpg2/thumb.webp unexpectedly deleted")
	}
	if _, ok := conn.files["other/x.webp"]; !ok {
		t.Error("other/x.webp unexpectedly deleted")
	}

	// Идемпотентность: повторное удаление возвращает 0.
	n, err = r.DeleteByPrefix(ctx, object.ObjectKey("photo-jpg/"))
	if err != nil {
		t.Fatalf("second DeleteByPrefix: %v", err)
	}
	if n != 0 {
		t.Fatalf("second deleted = %d, want 0", n)
	}
}

// TestFTPPublishTempNameUnique проверяет, что temp-файл публикации имеет
// уникальное имя вида ".tmp-<base>-<UnixNano>" и не остаётся после успешной
// публикации.
func TestFTPPublishTempNameUnique(t *testing.T) {
	conn := newFakeConn()
	r, err := NewResultStore(Options{Addr: "localhost:21", User: "u", Password: "p", Dialer: dialerFor(conn)})
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}
	ctx := context.Background()
	key := object.ObjectKey("out/result.bin")
	if err := r.Publish(ctx, key, bytes.NewReader([]byte("payload")), object.PublishOptions{}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	// Temp-файл не должен остаться после успешной публикации.
	for k := range conn.files {
		if strings.HasPrefix(k, "out/.tmp-") {
			t.Fatalf("temp file left after publish: %q", k)
		}
	}
	// Целевой файл опубликован.
	if _, ok := conn.files["out/result.bin"]; !ok {
		t.Fatal("published file missing")
	}
}

// TestFTPDeleteIdempotent550 проверяет, что ошибка 550 (no such file) при
// Delete маппится в идемпотентный успех (не ошибка).
func TestFTPDeleteIdempotent550(t *testing.T) {
	conn := newFakeConn()
	conn.deleteNoSuchFile = true
	r, err := NewResultStore(Options{Addr: "localhost:21", User: "u", Password: "p", Dialer: dialerFor(conn)})
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}
	ctx := context.Background()
	// Файла нет — Delete должен быть идемпотентным (550 → успех).
	if err := r.Delete(ctx, object.ObjectKey("missing.bin")); err != nil {
		t.Fatalf("Delete missing: expected idempotent success, got %v", err)
	}
}
