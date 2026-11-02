package sftp

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"io"
	"net"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"gitverse.ru/pkg-ru/imager/adapters/storage/remote"
	"gitverse.ru/pkg-ru/imager/domain/object"
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
	// openFailures — сколько раз Open должен вернуть ошибку соединения.
	openFailures int
	// fileStatFailures — сколько раз Stat открытого файла вернёт ошибку.
	fileStatFailures int
	// readDirErr — если задана, ReadDir возвращает её.
	readDirErr error
	// removeErr / removeDirErr — если заданы, соответствующие операции
	// возвращают их (кроме os.ErrNotExist-семантики).
	removeErr    error
	removeDirErr error
	// closes — число вызовов Close (освобождение соединения).
	closes int
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

// fakeRemoteFile — in-memory remoteFile: поток, метаданные и закрытие.
type fakeRemoteFile struct {
	name     string
	r        *bytes.Reader
	isDir    bool
	statErr  error
	closeErr error
	closed   bool
}

func (f *fakeRemoteFile) Read(p []byte) (int, error) { return f.r.Read(p) }

func (f *fakeRemoteFile) Seek(offset int64, whence int) (int64, error) {
	return f.r.Seek(offset, whence)
}

func (f *fakeRemoteFile) Close() error {
	f.closed = true
	return f.closeErr
}

func (f *fakeRemoteFile) Stat() (os.FileInfo, error) {
	if f.statErr != nil {
		return nil, f.statErr
	}
	return fakeFileInfo{name: f.name, size: int64(f.r.Size()), isDir: f.isDir}, nil
}

func (c *fakeClient) Open(p string) (remoteFile, error) {
	if c.openFailures > 0 {
		c.openFailures--
		return nil, errors.New("connection reset")
	}
	if c.dirs[p] {
		return &fakeRemoteFile{name: path.Base(p), r: bytes.NewReader(nil), isDir: true}, nil
	}
	data, ok := c.files[p]
	if !ok {
		return nil, os.ErrNotExist
	}
	f := &fakeRemoteFile{name: path.Base(p), r: bytes.NewReader(data)}
	if c.fileStatFailures > 0 {
		c.fileStatFailures--
		f.statErr = errors.New("stat failed")
	}
	return f, nil
}
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
	if c.removeErr != nil {
		return c.removeErr
	}
	delete(c.files, p)
	return nil
}
func (c *fakeClient) RemoveDirectory(p string) error {
	if c.removeDirErr != nil {
		return c.removeDirErr
	}
	delete(c.dirs, p)
	return nil
}
func (c *fakeClient) ReadDir(p string) ([]os.FileInfo, error) {
	if c.readDirErr != nil {
		return nil, c.readDirErr
	}
	var out []os.FileInfo
	seenDir := map[string]bool{}
	for k, v := range c.files {
		if path.Dir(k) == p {
			out = append(out, fakeFileInfo{name: path.Base(k), size: int64(len(v))})
		} else if strings.HasPrefix(k, p+"/") {
			// Вложенный файл: его родительский каталог — подкаталог p.
			rest := strings.TrimPrefix(k, p+"/")
			if idx := strings.Index(rest, "/"); idx >= 0 {
				dir := rest[:idx]
				if !seenDir[dir] {
					seenDir[dir] = true
					out = append(out, fakeFileInfo{name: dir, isDir: true})
				}
			}
		}
	}
	// Явно зарегистрированные подкаталоги.
	for d := range c.dirs {
		if path.Dir(d) == p {
			name := path.Base(d)
			if !seenDir[name] {
				seenDir[name] = true
				out = append(out, fakeFileInfo{name: name, isDir: true})
			}
		}
	}
	return out, nil
}
func (c *fakeClient) Close() error {
	c.closes++
	return nil
}

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
	// ssh.FingerprintSHA256 уже возвращает строку с префиксом "SHA256:";
	// ожидаемый fingerprint задаётся в том же формате (одинарный префикс).
	actual := ssh.FingerprintSHA256(pub)
	fp := actual // совпадает

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
	s, err := NewSourceStore(Options{
		Addr: "h:22", User: "u", Client: c,
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

// TestSFTPLookupRetryExhausted проверяет, что при исчерпании попыток
// возвращается последняя ошибка соединения.
func TestSFTPLookupRetryExhausted(t *testing.T) {
	c := newFakeClient()
	c.files["dir/file.bin"] = []byte("payload")
	c.statFailures = 100
	s, err := NewSourceStore(Options{
		Addr: "h:22", User: "u", Client: c,
		ConnOptions: remote.ConnOptions{MaxAttempts: 2},
	})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}
	if _, err := s.Lookup(context.Background(), "dir/file.bin"); err == nil {
		t.Fatal("expected error after exhausting attempts")
	}
}

// TestSFTPDeleteByPrefix проверяет рекурсивное пакетное удаление каталога
// ассетов: удаляются только ключи с границей '/', возвращается число
// удалённых файлов, операция идемпотентна.
func TestSFTPDeleteByPrefix(t *testing.T) {
	c := newFakeClient()
	c.dirs["photo-jpg"] = true
	c.files["photo-jpg/thumb.webp"] = []byte("a")
	c.files["photo-jpg/preview.webp"] = []byte("b")
	c.files["photo-jpg2/thumb.webp"] = []byte("c")
	c.files["other/x.webp"] = []byte("d")
	r, err := NewResultStore(Options{Addr: "h:22", User: "u", Client: c})
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
		if _, ok := c.files[k]; ok {
			t.Errorf("file %q not deleted", k)
		}
	}
	// Соседние префиксы не тронуты.
	if _, ok := c.files["photo-jpg2/thumb.webp"]; !ok {
		t.Error("photo-jpg2/thumb.webp unexpectedly deleted")
	}
	if _, ok := c.files["other/x.webp"]; !ok {
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

// TestClientAdapterOpenFile — clientAdapter делегирует Open/OpenFile
// реальному *sftp.Client. Сервер — in-memory SFTP (pkg/sftp) поверх net.Pipe,
// без сети и реального SSH.
func TestClientAdapterOpenFile(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	srv := sftp.NewRequestServer(serverConn, sftp.InMemHandler())
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = clientConn.Close()
	})

	cl, err := sftp.NewClientPipe(clientConn, clientConn)
	if err != nil {
		t.Fatalf("NewClientPipe: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close() })

	a := &clientAdapter{Client: cl}

	w, err := a.OpenFile("/hello.txt", os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if _, err := io.WriteString(w, "hi"); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("WriteCloser.Close: %v", err)
	}

	f, err := a.Open("/hello.txt")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = f.Close() }()
	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "hi" {
		t.Errorf("read = %q, want hi", got)
	}
	if info, err := f.Stat(); err != nil {
		t.Fatalf("Stat: %v", err)
	} else if info.Size() != 2 {
		t.Errorf("size = %d, want 2", info.Size())
	}
}

// closeFunc — io.Closer на основе функции.
type closeFunc func() error

func (f closeFunc) Close() error { return f() }

// TestSFTPOpenBuffered — SourceStore.Open материализует объект в
// перематываемый буфер с метаданными.
func TestSFTPOpenBuffered(t *testing.T) {
	c := newFakeClient()
	c.files["dir/file.bin"] = []byte("payload")
	s, err := NewSourceStore(Options{Addr: "h:22", User: "u", Client: c})
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
	// Артефакт перематываемый.
	if _, err := art.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	again, err := io.ReadAll(art)
	if err != nil {
		t.Fatalf("ReadAll after seek: %v", err)
	}
	if string(again) != "payload" {
		t.Errorf("read after seek = %q, want payload", again)
	}
	if err := art.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// TestSFTPOpenRoot — Root добавляется к ключу при открытии.
func TestSFTPOpenRoot(t *testing.T) {
	c := newFakeClient()
	c.files["base/x.bin"] = []byte("x")
	s, err := NewSourceStore(Options{Addr: "h:22", User: "u", Client: c, Root: "base"})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}
	art, err := s.Open(context.Background(), "x.bin")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = art.Close() }()
	got, err := io.ReadAll(art)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "x" {
		t.Errorf("read = %q, want x", got)
	}
}

// TestSFTPOpenNotFound — отсутствующий ключ и каталог дают ErrNotFound.
func TestSFTPOpenNotFound(t *testing.T) {
	c := newFakeClient()
	c.dirs["folder"] = true
	s, err := NewSourceStore(Options{Addr: "h:22", User: "u", Client: c})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}
	ctx := context.Background()
	if _, err := s.Open(ctx, "missing.bin"); !errors.Is(err, object.ErrNotFound) {
		t.Errorf("Open(missing) err = %v, want ErrNotFound", err)
	}
	if _, err := s.Open(ctx, "folder"); !errors.Is(err, object.ErrNotFound) {
		t.Errorf("Open(dir) err = %v, want ErrNotFound", err)
	}
}

// TestSFTPOpenSpoolLimit — превышение spool-лимита даёт ErrQuota.
func TestSFTPOpenSpoolLimit(t *testing.T) {
	c := newFakeClient()
	c.files["big.bin"] = []byte("0123456789")
	s, err := NewSourceStore(Options{
		Addr: "h:22", User: "u", Client: c, SpoolMaxBytes: 3,
	})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}
	if _, err := s.Open(context.Background(), "big.bin"); !errors.Is(err, object.ErrQuota) {
		t.Fatalf("Open err = %v, want ErrQuota", err)
	}
}

// TestSFTPOpenRetry — ошибка соединения при Open повторяется.
func TestSFTPOpenRetry(t *testing.T) {
	c := newFakeClient()
	c.files["x.bin"] = []byte("data")
	c.openFailures = 1
	s, err := NewSourceStore(Options{
		Addr: "h:22", User: "u", Client: c,
		ConnOptions: remote.ConnOptions{MaxAttempts: 2},
	})
	if err != nil {
		t.Fatalf("NewSourceStore: %v", err)
	}
	art, err := s.Open(context.Background(), "x.bin")
	if err != nil {
		t.Fatalf("Open after retry: %v", err)
	}
	defer func() { _ = art.Close() }()
}

// TestSFTPResultOpen — ResultStore.Open работает через тот же буфер.
func TestSFTPResultOpen(t *testing.T) {
	c := newFakeClient()
	c.files["r.bin"] = []byte("result")
	r, err := NewResultStore(Options{Addr: "h:22", User: "u", Client: c})
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

// TestSFTPReadStream — ReadStream отдаёт поток без материализации;
// соединение живёт до Close и освобождается ровно один раз.
func TestSFTPReadStream(t *testing.T) {
	c := newFakeClient()
	c.files["s.bin"] = []byte("stream")
	r, err := NewResultStore(Options{Addr: "h:22", User: "u", Client: c})
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}
	st, err := r.ReadStream(context.Background(), "s.bin")
	if err != nil {
		t.Fatalf("ReadStream: %v", err)
	}
	if meta := st.Metadata(); meta.Key != "s.bin" || meta.Size != int64(len("stream")) {
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
	// Повторное закрытие безопасно (sync.Once), соединение не закрывается дважды.
	if err := st.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	if c.closes != 1 {
		t.Errorf("client closes = %d, want 1", c.closes)
	}
}

// TestSFTPReadStreamNotFound — отсутствующий объект и каталог → ErrNotFound.
func TestSFTPReadStreamNotFound(t *testing.T) {
	c := newFakeClient()
	c.dirs["folder"] = true
	r, err := NewResultStore(Options{Addr: "h:22", User: "u", Client: c})
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}
	ctx := context.Background()
	if _, err := r.ReadStream(ctx, "missing.bin"); !errors.Is(err, object.ErrNotFound) {
		t.Errorf("ReadStream(missing) err = %v, want ErrNotFound", err)
	}
	if _, err := r.ReadStream(ctx, "folder"); !errors.Is(err, object.ErrNotFound) {
		t.Errorf("ReadStream(dir) err = %v, want ErrNotFound", err)
	}
}

// TestSFTPReadStreamStatError — ошибка Stat открытого файла маппится в
// ErrUnavailable без ретрая.
func TestSFTPReadStreamStatError(t *testing.T) {
	c := newFakeClient()
	c.files["s.bin"] = []byte("x")
	c.fileStatFailures = 1
	r, err := NewResultStore(Options{Addr: "h:22", User: "u", Client: c})
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}
	if _, err := r.ReadStream(context.Background(), "s.bin"); !errors.Is(err, object.ErrUnavailable) {
		t.Fatalf("ReadStream err = %v, want ErrUnavailable", err)
	}
}

// TestSFTPReadStreamRetry — ошибка соединения при открытии повторяется.
func TestSFTPReadStreamRetry(t *testing.T) {
	c := newFakeClient()
	c.files["s.bin"] = []byte("x")
	c.openFailures = 1
	r, err := NewResultStore(Options{
		Addr: "h:22", User: "u", Client: c,
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

// TestSFTPStreamCloserIdempotent — Close закрывает файл и освобождает
// сессию ровно один раз; ошибка файла возвращается первому вызову.
func TestSFTPStreamCloserIdempotent(t *testing.T) {
	var fileCloses, sessionDiscards int
	wantErr := errors.New("close failed")
	f := closeFunc(func() error {
		fileCloses++
		return wantErr
	})
	sess := remote.NewDirectSession[client](nil, func(client) error {
		sessionDiscards++
		return nil
	})
	c := &sftpStreamCloser{f: f, session: sess}
	if err := c.Close(); !errors.Is(err, wantErr) {
		t.Errorf("first Close = %v, want %v", err, wantErr)
	}
	if err := c.Close(); err != nil {
		t.Errorf("second Close = %v, want nil", err)
	}
	if fileCloses != 1 {
		t.Errorf("file closes = %d, want 1", fileCloses)
	}
	if sessionDiscards != 1 {
		t.Errorf("session discards = %d, want 1", sessionDiscards)
	}
}

// TestSFTPStats — рекурсивная агрегация объектов/байт; .tmp-* пропускаются.
func TestSFTPStats(t *testing.T) {
	c := newFakeClient()
	c.dirs["sub"] = true
	c.dirs["sub/deep"] = true
	c.files["a.bin"] = []byte("aaa")
	c.files["sub/b.bin"] = []byte("bbbb")
	c.files["sub/deep/c.bin"] = []byte("ccccc")
	c.files["sub/.tmp-partial"] = []byte(strings.Repeat("x", 100))
	r, err := NewResultStore(Options{Addr: "h:22", User: "u", Client: c})
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}
	stats, err := r.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Objects != 3 || stats.TotalBytes != 12 {
		t.Errorf("Stats = %+v, want 3 objects / 12 bytes", stats)
	}

	// Root задаёт начало обхода.
	c.files["base/x.bin"] = []byte("x")
	r2, err := NewResultStore(Options{Addr: "h:22", User: "u", Client: c, Root: "base"})
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

// TestSFTPStatsError — ошибка обхода маппится в ErrUnavailable.
func TestSFTPStatsError(t *testing.T) {
	c := newFakeClient()
	c.readDirErr = errors.New("boom")
	r, err := NewResultStore(Options{
		Addr: "h:22", User: "u", Client: c,
		ConnOptions: remote.ConnOptions{MaxAttempts: 1},
	})
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}
	if _, err := r.Stats(context.Background()); !errors.Is(err, object.ErrUnavailable) {
		t.Fatalf("Stats err = %v, want ErrUnavailable", err)
	}
}

// TestSFTPDeleteByPrefixNested — вложенные каталоги удаляются рекурсивно,
// временные файлы публикации пропускаются.
func TestSFTPDeleteByPrefixNested(t *testing.T) {
	c := newFakeClient()
	c.dirs["assets"] = true
	c.dirs["assets/sub"] = true
	c.files["assets/a.webp"] = []byte("a")
	c.files["assets/sub/b.webp"] = []byte("b")
	c.files["assets/.tmp-pub"] = []byte("t")
	c.files["assets/sub/.tmp-pub"] = []byte("t")
	r, err := NewResultStore(Options{Addr: "h:22", User: "u", Client: c})
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
		if _, ok := c.files[k]; ok {
			t.Errorf("file %q not deleted", k)
		}
	}
	// Временные файлы публикации не трогаются.
	for _, k := range []string{"assets/.tmp-pub", "assets/sub/.tmp-pub"} {
		if _, ok := c.files[k]; !ok {
			t.Errorf("temp file %q unexpectedly deleted", k)
		}
	}
	if c.dirs["assets/sub"] {
		t.Error("subdirectory assets/sub not removed")
	}
}

// TestSFTPDeleteByPrefixErrors — ошибки удаления файла/каталога маппятся в
// ErrUnavailable.
func TestSFTPDeleteByPrefixErrors(t *testing.T) {
	ctx := context.Background()
	opts := func(c *fakeClient) Options {
		return Options{
			Addr: "h:22", User: "u", Client: c,
			ConnOptions: remote.ConnOptions{MaxAttempts: 1},
		}
	}

	c1 := newFakeClient()
	c1.files["assets/a.webp"] = []byte("a")
	c1.removeErr = errors.New("remove failed")
	r1, err := NewResultStore(opts(c1))
	if err != nil {
		t.Fatalf("NewResultStore: %v", err)
	}
	if _, err := r1.DeleteByPrefix(ctx, object.ObjectKey("assets/")); !errors.Is(err, object.ErrUnavailable) {
		t.Errorf("remove error = %v, want ErrUnavailable", err)
	}

	c2 := newFakeClient()
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
}
