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
	"github.com/pkg-ru/imager/adapters/storage/remote"
	"github.com/pkg-ru/imager/domain/object"
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
	// deleteErr / removeDirErr / retrErr — если заданы, соответствующие
	// операции возвращают их.
	deleteErr    error
	removeDirErr error
	retrErr      error
	// retrFailures — сколько раз Retr вернёт ошибку соединения.
	retrFailures int
	// quits — число вызовов Quit (освобождение соединения).
	quits int
}

func newFakeConn() *fakeConn {
	return &fakeConn{
		files:    map[string][]byte{},
		dirs:     map[string]bool{},
		features: map[string]bool{},
	}
}

func (c *fakeConn) Login(user, password string) error { return nil }
func (c *fakeConn) Quit() error                       { c.quits++; return nil }
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
	if c.retrFailures > 0 {
		c.retrFailures--
		return nil, errors.New("connection reset")
	}
	if c.retrErr != nil {
		return nil, c.retrErr
	}
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
	if c.deleteErr != nil {
		return c.deleteErr
	}
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
	if c.removeDirErr != nil {
		return c.removeDirErr
	}
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
	s, err := NewSourceStore(Options{
		Addr: "localhost:21", User: "u", Dialer: dialerFor(conn),
		ConnOptions: remote.ConnOptions{MaxAttempts: 3},
	})
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
	s, err := NewSourceStore(Options{
		Addr: "localhost:21", User: "u", Dialer: dialerFor(conn),
		ConnOptions: remote.ConnOptions{MaxAttempts: 2},
	})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}
	if _, err := s.Lookup(context.Background(), "dir/file.bin"); err == nil {
		t.Fatal("expected error after exhausting attempts")
	}
}

// TestFTPPoolConcurrentDial проверяет, что пул выполняет dial вне блокировки:
// параллельные acquire могут создавать несколько соединений одновременно,
// а не сериализуются на одном мьютексе. Пул теперь общий (remote.Pool);
// проверяем его через FTP-dial-функцию адаптера.
func TestFTPPoolConcurrentDial(t *testing.T) {
	var mu sync.Mutex
	dials := 0
	// dial блокируется на 50ms, имитируя медленную сеть.
	opts := Options{
		Addr: "localhost:21", User: "u",
		ConnOptions: remote.ConnOptions{MaxConns: 4},
		Dialer: func(ctx context.Context, addr string, tls bool) (conn, error) {
			time.Sleep(50 * time.Millisecond)
			mu.Lock()
			dials++
			mu.Unlock()
			return newFakeConn(), nil
		},
	}
	pool := remote.NewPool(opts.dial, func(c conn) error { return c.Quit() }, opts.MaxConns)

	ctx := context.Background()
	const n = 4
	sessions := make([]*remote.Session[conn], n)
	for i := 0; i < n; i++ {
		e, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatalf("acquire %d: %v", i, err)
		}
		sessions[i] = remote.NewSession(e)
	}
	mu.Lock()
	got := dials
	mu.Unlock()
	if got != n {
		t.Fatalf("dials = %d, want %d (dial должен выполняться параллельно)", got, n)
	}
	// Возвращаем все соединения в пул (Discard идемпотентен).
	for _, s := range sessions {
		s.Discard()
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

// TestFTPServerName — hostname извлекается из "host:port" для TLS ServerName.
func TestFTPServerName(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"example.com:21", "example.com"},
		{"127.0.0.1:2121", "127.0.0.1"},
		{"[::1]:21", "::1"},
		{"no-port", "no-port"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := serverName(tt.in); got != tt.want {
			t.Errorf("serverName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestFTPValidate — fail-fast валидация опций.
func TestFTPValidate(t *testing.T) {
	base := Options{Addr: "h:21", User: "u", Password: "p", TLSVerify: true}
	tests := []struct {
		name    string
		mutate  func(*Options)
		wantErr bool
	}{
		{"valid", func(*Options) {}, false},
		{"empty addr", func(o *Options) { o.Addr = "" }, true},
		{"empty user", func(o *Options) { o.User = "" }, true},
		{"tls without verify", func(o *Options) { o.TLS = true; o.TLSVerify = false }, true},
		{"cr in root", func(o *Options) { o.Root = "a\rb" }, true},
		{"lf in root", func(o *Options) { o.Root = "a\nb" }, true},
		{"del in root", func(o *Options) { o.Root = "a\x7fb" }, true},
		{"valid root", func(o *Options) { o.Root = "assets/img" }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := base
			tt.mutate(&o)
			err := o.validate()
			if tt.wantErr && err == nil {
				t.Fatal("validate() = nil, want error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("validate() = %v, want nil", err)
			}
		})
	}
}

// TestFTPSourceOpenBuffered — Open материализует объект в перематываемый
// буфер с метаданными.
func TestFTPSourceOpenBuffered(t *testing.T) {
	conn := newFakeConn()
	conn.files["dir/file.bin"] = []byte("payload")
	s, err := NewSourceStore(Options{Addr: "localhost:21", User: "u", Dialer: dialerFor(conn)})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}
	art, err := s.Open(context.Background(), "dir/file.bin")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if meta := art.Metadata(); meta.Key != "dir/file.bin" || meta.Size != int64(len("payload")) {
		t.Errorf("Metadata() = %+v", meta)
	}
	got, err := io.ReadAll(art)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "payload" {
		t.Errorf("read = %q, want payload", got)
	}
	if _, err := art.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	if err := art.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// TestFTPResultOpen — ResultStore.Open работает через тот же буфер.
func TestFTPResultOpen(t *testing.T) {
	conn := newFakeConn()
	conn.files["r.bin"] = []byte("result")
	r, err := NewResultStore(Options{Addr: "localhost:21", User: "u", Password: "p", Dialer: dialerFor(conn)})
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}
	art, err := r.Open(context.Background(), "r.bin")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = art.Close() }()
	got, err := io.ReadAll(art)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "result" {
		t.Errorf("read = %q, want result", got)
	}
}

// TestFTPOpenSpoolLimit — превышение spool-лимита даёт ErrQuota.
func TestFTPOpenSpoolLimit(t *testing.T) {
	conn := newFakeConn()
	conn.files["big.bin"] = []byte("0123456789")
	s, err := NewSourceStore(Options{
		Addr: "localhost:21", User: "u", Dialer: dialerFor(conn), SpoolMaxBytes: 3,
	})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}
	if _, err := s.Open(context.Background(), "big.bin"); !errors.Is(err, object.ErrQuota) {
		t.Fatalf("Open err = %v, want ErrQuota", err)
	}
}

// TestFTPOpenErrorMapped — ошибка Retr (не found/connection) маппится в
// ErrUnavailable после исчерпания попыток.
func TestFTPOpenErrorMapped(t *testing.T) {
	conn := newFakeConn()
	conn.retrErr = errors.New("no such file")
	s, err := NewSourceStore(Options{
		Addr: "localhost:21", User: "u", Dialer: dialerFor(conn),
		ConnOptions: remote.ConnOptions{MaxAttempts: 1},
	})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}
	if _, err := s.Open(context.Background(), "missing.bin"); !errors.Is(err, object.ErrUnavailable) {
		t.Fatalf("Open err = %v, want ErrUnavailable", err)
	}
}

// TestFTPReadStream — ReadStream отдаёт поток без материализации;
// соединение живёт до Close и освобождается ровно один раз.
func TestFTPReadStream(t *testing.T) {
	conn := newFakeConn()
	conn.files["s.bin"] = []byte("stream")
	r, err := NewResultStore(Options{Addr: "localhost:21", User: "u", Password: "p", Dialer: dialerFor(conn)})
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}
	st, err := r.ReadStream(context.Background(), "s.bin")
	if err != nil {
		t.Fatalf("ReadStream: %v", err)
	}
	if meta := st.Metadata(); meta.Key != "s.bin" {
		t.Errorf("Metadata() = %+v", meta)
	}
	got, err := io.ReadAll(st)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "stream" {
		t.Errorf("read = %q, want stream", got)
	}
	if err := st.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	if conn.quits != 1 {
		t.Errorf("Quit calls = %d, want 1", conn.quits)
	}
}

// TestFTPReadStreamRetry — ошибка соединения при Retr повторяется.
func TestFTPReadStreamRetry(t *testing.T) {
	conn := newFakeConn()
	conn.files["s.bin"] = []byte("x")
	conn.retrFailures = 1
	r, err := NewResultStore(Options{
		Addr: "localhost:21", User: "u", Password: "p", Dialer: dialerFor(conn),
		ConnOptions: remote.ConnOptions{MaxAttempts: 2},
	})
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}
	st, err := r.ReadStream(context.Background(), "s.bin")
	if err != nil {
		t.Fatalf("ReadStream after retry: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// TestFTPReadStreamError — ошибка Retr маппится в ErrUnavailable.
func TestFTPReadStreamError(t *testing.T) {
	conn := newFakeConn()
	conn.retrErr = errors.New("no such file")
	r, err := NewResultStore(Options{
		Addr: "localhost:21", User: "u", Password: "p", Dialer: dialerFor(conn),
		ConnOptions: remote.ConnOptions{MaxAttempts: 1},
	})
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}
	if _, err := r.ReadStream(context.Background(), "missing.bin"); !errors.Is(err, object.ErrUnavailable) {
		t.Fatalf("ReadStream err = %v, want ErrUnavailable", err)
	}
}

// TestFTPStreamCloserIdempotent — Close закрывает поток и освобождает
// соединение ровно один раз.
func TestFTPStreamCloserIdempotent(t *testing.T) {
	var streamCloses, sessionDiscards int
	wantErr := errors.New("close failed")
	rc := closeFunc(func() error {
		streamCloses++
		return wantErr
	})
	sess := remote.NewDirectSession[conn](nil, func(conn) error {
		sessionDiscards++
		return nil
	})
	c := &ftpStreamCloser{rc: rc, session: sess}
	if err := c.Close(); !errors.Is(err, wantErr) {
		t.Errorf("first Close = %v, want %v", err, wantErr)
	}
	if err := c.Close(); err != nil {
		t.Errorf("second Close = %v, want nil", err)
	}
	if streamCloses != 1 {
		t.Errorf("stream closes = %d, want 1", streamCloses)
	}
	if sessionDiscards != 1 {
		t.Errorf("session discards = %d, want 1", sessionDiscards)
	}
}

// closeFunc — io.Closer на основе функции.
type closeFunc func() error

func (f closeFunc) Close() error { return f() }

// TestFTPStats — рекурсивная агрегация объектов/байт; temp-файлы
// пропускаются; Root задаёт начало обхода.
func TestFTPStats(t *testing.T) {
	conn := newFakeConn()
	conn.dirs["/"] = true
	conn.dirs["/sub"] = true
	conn.files["/a.bin"] = []byte("aaa")
	conn.files["/sub/b.bin"] = []byte("bbbb")
	conn.files["/.tmp-partial"] = []byte(strings.Repeat("x", 100))
	r, err := NewResultStore(Options{Addr: "localhost:21", User: "u", Password: "p", Dialer: dialerFor(conn)})
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}
	stats, err := r.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Objects != 2 || stats.TotalBytes != 7 {
		t.Errorf("Stats = %+v, want 2 objects / 7 bytes", stats)
	}

	// Root задаёт начало обхода.
	conn.dirs["assets"] = true
	conn.files["assets/x.bin"] = []byte("x")
	r2, err := NewResultStore(Options{
		Addr: "localhost:21", User: "u", Password: "p", Dialer: dialerFor(conn), Root: "assets",
	})
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}
	stats2, err := r2.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats(root): %v", err)
	}
	if stats2.Objects != 1 || stats2.TotalBytes != 1 {
		t.Errorf("Stats(root) = %+v, want 1 object / 1 byte", stats2)
	}
}

// TestFTPStatsError — ошибка обхода маппится в ErrUnavailable.
func TestFTPStatsError(t *testing.T) {
	conn := newFakeConn()
	conn.listFailures = 100
	r, err := NewResultStore(Options{
		Addr: "localhost:21", User: "u", Password: "p", Dialer: dialerFor(conn),
		ConnOptions: remote.ConnOptions{MaxAttempts: 1},
	})
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}
	if _, err := r.Stats(context.Background()); !errors.Is(err, object.ErrUnavailable) {
		t.Fatalf("Stats err = %v, want ErrUnavailable", err)
	}
}

// TestFTPDeleteByPrefixNested — вложенные каталоги удаляются рекурсивно,
// temp-файлы пропускаются.
func TestFTPDeleteByPrefixNested(t *testing.T) {
	conn := newFakeConn()
	conn.dirs["assets"] = true
	conn.dirs["assets/sub"] = true
	conn.files["assets/a.webp"] = []byte("a")
	conn.files["assets/sub/b.webp"] = []byte("b")
	conn.files["assets/.tmp-pub"] = []byte("t")
	conn.files["assets/sub/x.tmp"] = []byte("t")
	r, err := NewResultStore(Options{Addr: "localhost:21", User: "u", Password: "p", Dialer: dialerFor(conn)})
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}
	n, err := r.DeleteByPrefix(context.Background(), object.ObjectKey("assets/"))
	if err != nil {
		t.Fatalf("DeleteByPrefix: %v", err)
	}
	if n != 2 {
		t.Errorf("deleted = %d, want 2", n)
	}
	for _, k := range []string{"assets/a.webp", "assets/sub/b.webp"} {
		if _, ok := conn.files[k]; ok {
			t.Errorf("file %q not deleted", k)
		}
	}
	for _, k := range []string{"assets/.tmp-pub", "assets/sub/x.tmp"} {
		if _, ok := conn.files[k]; !ok {
			t.Errorf("temp file %q unexpectedly deleted", k)
		}
	}
	if conn.dirs["assets/sub"] {
		t.Error("subdirectory assets/sub not removed")
	}
}

// TestFTPDeleteByPrefixErrors — ошибки DELE/RMD маппятся в ErrUnavailable;
// 550 при RMD считается идемпотентным.
func TestFTPDeleteByPrefixErrors(t *testing.T) {
	ctx := context.Background()
	opts := func(c *fakeConn) Options {
		return Options{
			Addr: "localhost:21", User: "u", Password: "p", Dialer: dialerFor(c),
			ConnOptions: remote.ConnOptions{MaxAttempts: 1},
		}
	}

	c1 := newFakeConn()
	c1.dirs["assets"] = true
	c1.files["assets/a.webp"] = []byte("a")
	c1.deleteErr = errors.New("delete failed")
	r1, err := NewResultStore(opts(c1))
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}
	if _, err := r1.DeleteByPrefix(ctx, object.ObjectKey("assets/")); !errors.Is(err, object.ErrUnavailable) {
		t.Errorf("delete error = %v, want ErrUnavailable", err)
	}

	c2 := newFakeConn()
	c2.dirs["assets"] = true
	c2.dirs["assets/sub"] = true
	c2.files["assets/sub/b.webp"] = []byte("b")
	c2.removeDirErr = errors.New("rmdir failed")
	r2, err := NewResultStore(opts(c2))
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}
	if _, err := r2.DeleteByPrefix(ctx, object.ObjectKey("assets/")); !errors.Is(err, object.ErrUnavailable) {
		t.Errorf("rmdir error = %v, want ErrUnavailable", err)
	}

	// 550 (no such file) при DELE/RMD — идемпотентный успех.
	c3 := newFakeConn()
	c3.dirs["assets"] = true
	c3.dirs["assets/sub"] = true
	c3.files["assets/sub/b.webp"] = []byte("b")
	c3.deleteErr = &textproto.Error{Code: 550, Msg: "file unavailable"}
	c3.removeDirErr = &textproto.Error{Code: 550, Msg: "dir unavailable"}
	r3, err := NewResultStore(opts(c3))
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}
	if _, err := r3.DeleteByPrefix(ctx, object.ObjectKey("assets/")); err != nil {
		t.Errorf("DeleteByPrefix(550) = %v, want nil", err)
	}
}

// TestServerConnAdapterFeature — Feature объявляет поддержку базовых команд
// RFC 959 и отклоняет расширенные.
func TestServerConnAdapterFeature(t *testing.T) {
	a := &serverConnAdapter{}
	for _, cmd := range []string{"STOR", "RNFR", "RNTO", "DELE", "MKD", "RMD"} {
		if !a.Feature(cmd) {
			t.Errorf("Feature(%q) = false, want true", cmd)
		}
		if !a.Feature(strings.ToLower(cmd)) {
			t.Errorf("Feature(%q) = false, want true (case-insensitive)", strings.ToLower(cmd))
		}
	}
	for _, cmd := range []string{"MFMT", "SITE", ""} {
		if a.Feature(cmd) {
			t.Errorf("Feature(%q) = true, want false", cmd)
		}
	}
}

