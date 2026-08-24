// Package sftp реализует storage.SourceStore и storage.ResultStore поверх
// SFTP (SSH File Transfer Protocol) через github.com/pkg/sftp.
//
// ResultStore использует temp-upload + rename для атомарной публикации:
// содержимое загружается во временный файл в том же каталоге, затем
// переименовывается в целевой ключ. NoOverwrite реализуется через
// эксклюзивное создание (O_EXCL) при rename.
//
// Секреты (пароль/ключ) не передаются в URI/логи: задаются отдельными
// полями конфигурации.
package sftp

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"strings"
	"time"

	"github.com/pkg-ru/imager/internal/adapters/storage/remote"
	"github.com/pkg-ru/imager/internal/application/ports/storage"
	"github.com/pkg-ru/imager/internal/domain/object"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// client — узкий интерфейс SFTP-клиента, используемый адаптером. Выделен,
// чтобы тесты могли подставлять in-memory реализацию без реального сервера.
type client interface {
	Stat(path string) (os.FileInfo, error)
	Open(path string) (*sftp.File, error)
	OpenFile(path string, f int) (io.WriteCloser, error)
	MkdirAll(path string) error
	PosixRename(oldname, newname string) error
	Remove(path string) error
	ReadDir(path string) ([]os.FileInfo, error)
	Close() error
}

// Options — параметры SFTP-адаптера.
type Options struct {
	// Addr — адрес сервера "host:port".
	Addr string
	// User — имя пользователя.
	User string
	// Password — пароль (если используется password auth).
	Password string
	// PrivateKey — содержимое приватного ключа (если используется key auth).
	PrivateKey []byte
	// Root — корневой каталог внутри SFTP (пусто = домашний каталог).
	Root string
	// SpoolDir — каталог временных spool (пусто = os.TempDir).
	SpoolDir string
	// SpoolMaxBytes — максимальный размер source spool (0 = без лимита).
	SpoolMaxBytes int64
	// Pool — общий бюджет памяти процесса для spillable-буферов.
	// Если nil, буферы работают только через память без spill.
	Pool *remote.BufferPool
	// DialTimeout — таймаут установки SSH-соединения.
	DialTimeout time.Duration
	// ReadTimeout — таймаут операции (0 = без ограничения).
	ReadTimeout time.Duration
	// MaxAttempts — максимальное число попыток операции (0 = 1).
	MaxAttempts int
	// MaxIdleConns — максимальное число idle-соединений в пуле
	// (0 = не держать соединение между операциями).
	MaxIdleConns int
	// MaxConns — максимальное число одновременных соединений в пуле
	// (0 = 2). Позволяет конкурентным операциям работать параллельно.
	MaxConns int
	// IdleConnTimeout — таймаут idle-соединений (0 = без ограничения).
	IdleConnTimeout time.Duration
	// HostKeyFingerprint — ожидаемый SHA-256 fingerprint host key
	// (например "SHA256:..."). Пусто = InsecureIgnoreHostKey (небезопасно).
	HostKeyFingerprint string
	// Client — опциональный уже созданный SFTP-клиент (для тестов).
	Client client
}

func (o Options) validate() error {
	if o.Addr == "" {
		return fmt.Errorf("sftp: empty addr")
	}
	if o.User == "" {
		return fmt.Errorf("sftp: empty user")
	}
	if o.Password == "" && len(o.PrivateKey) == 0 && o.Client == nil {
		return fmt.Errorf("sftp: no auth method (password or private key) and no client")
	}
	return nil
}

func (o Options) dial() (client, error) {
	if o.Client != nil {
		return o.Client, nil
	}
	var auths []ssh.AuthMethod
	if o.Password != "" {
		auths = append(auths, ssh.Password(o.Password))
	}
	if len(o.PrivateKey) > 0 {
		signer, err := ssh.ParsePrivateKey(o.PrivateKey)
		if err != nil {
			return nil, fmt.Errorf("sftp: parse private key: %w", err)
		}
		auths = append(auths, ssh.PublicKeys(signer))
	}
	hostKeyCallback, err := o.hostKeyCallback()
	if err != nil {
		return nil, err
	}
	cfg := &ssh.ClientConfig{
		User:            o.User,
		Auth:            auths,
		HostKeyCallback: hostKeyCallback,
		Timeout:         o.DialTimeout,
	}
	conn, err := ssh.Dial("tcp", o.Addr, cfg)
	if err != nil {
		return nil, fmt.Errorf("sftp: dial: %w", err)
	}
	client, err := sftp.NewClient(conn)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("sftp: new client: %w", err)
	}
	return &clientAdapter{Client: client}, nil
}

// hostKeyCallback возвращает callback проверки host key. Если задан
// HostKeyFingerprint (SHA256:...), проверяется точное совпадение. Иначе
// используется InsecureIgnoreHostKey — это небезопасно и должно быть
// заменено на fingerprint в production (см. docs/PRODUCTION.md).
func (o Options) hostKeyCallback() (ssh.HostKeyCallback, error) {
	if o.HostKeyFingerprint == "" {
		return ssh.InsecureIgnoreHostKey(), nil
	}
	expected := strings.TrimPrefix(o.HostKeyFingerprint, "SHA256:")
	if expected == "" {
		return nil, fmt.Errorf("sftp: invalid host-key-fingerprint %q", o.HostKeyFingerprint)
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		actual := ssh.FingerprintSHA256(key)
		if subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) != 1 {
			return fmt.Errorf("sftp: host key mismatch: got %s, want %s", actual, o.HostKeyFingerprint)
		}
		return nil
	}, nil
}

// clientAdapter адаптирует *sftp.Client к интерфейсу client, приводя
// OpenFile к io.WriteCloser.
type clientAdapter struct {
	*sftp.Client
}

func (a *clientAdapter) OpenFile(path string, f int) (io.WriteCloser, error) {
	return a.Client.OpenFile(path, f)
}

func (o Options) key(key object.ObjectKey) (string, error) {
	k, err := remote.CanonicalKey(key)
	if err != nil {
		return "", remote.Unsafe(key, err)
	}
	if o.Root == "" {
		return k, nil
	}
	return path.Join(o.Root, k), nil
}

// statWithClient выполняет Stat с переданным клиентом (из пула). Возвращает
// сырую ошибку соединения без маппинга, чтобы вызывающий мог отличить
// client-ошибку (требует retry) от бизнес-ошибки. NotFound для каталога
// возвращается сразу.
func (o Options) statWithClient(ctx context.Context, key object.ObjectKey, cl client) (object.ObjectMetadata, error) {
	full, err := o.key(key)
	if err != nil {
		return object.ObjectMetadata{}, err
	}
	info, err := cl.Stat(full)
	if err != nil {
		return object.ObjectMetadata{}, err
	}
	if info.IsDir() {
		return object.ObjectMetadata{}, remote.NotFound(key)
	}
	return object.ObjectMetadata{
		Key:     key,
		Size:    info.Size(),
		ModTime: info.ModTime(),
	}, nil
}

// isClientErr отличает ошибки соединения (требуют discard и повторной
// попытки) от бизнес-ошибок; общая логика вынесена в remote.IsConnErr.
// Ошибки соединения — сырые SSH/IO, бизнес-ошибки мапятся через
// MapError/NotFound.
func isClientErr(err error) bool {
	return remote.IsConnErr(err)
}

// attempts возвращает число попыток операции (>= 1).
func (o Options) attempts() int {
	return remote.Attempts(o.MaxAttempts)
}

// withTimeout оборачивает ctx таймаутом операции, если задан ReadTimeout.
// Возвращает cancel-функцию, которую вызывающий обязан вызвать (defer cancel()).
func (o Options) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return remote.WithOpTimeout(ctx, o.ReadTimeout)
}

// SourceStore — SFTP-реализация storage.SourceStore (read-only).
type SourceStore struct {
	store
}

// NewSourceStore создаёт SFTP SourceStore.
func NewSourceStore(opts Options) (*SourceStore, error) {
	s, err := newStore(opts)
	if err != nil {
		return nil, err
	}
	return &SourceStore{store: s}, nil
}

// Lookup возвращает метаданные исходного объекта. При ошибке соединения
// выполняется повторная попытка с новым соединением (до MaxAttempts).
func (s *SourceStore) Lookup(ctx context.Context, key object.ObjectKey) (object.ObjectMetadata, error) {
	return withRetry(ctx, &s.store, func(cl *pooledClient) (object.ObjectMetadata, error, error) {
		meta, err := s.opts.statWithClient(ctx, key, cl.client)
		switch {
		case err == nil:
			return meta, nil, nil
		case os.IsNotExist(err):
			return object.ObjectMetadata{}, nil, remote.NotFound(key)
		case !isClientErr(err):
			return object.ObjectMetadata{}, nil, remote.MapError("sftp stat", err)
		default:
			return object.ObjectMetadata{}, err, remote.MapError("sftp stat", err)
		}
	})
}

// openBuffered открывает файл по полному пути full и материализует его в
// spillable буфер. role используется только в тексте ошибки quota
// ("source"/"result").
func (s *store) openBuffered(ctx context.Context, full string, key object.ObjectKey, role string) (object.Artifact, error) {
	return withRetry(ctx, s, func(cl *pooledClient) (object.Artifact, error, error) {
		f, err := cl.Open(full)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, nil, remote.NotFound(key)
			}
			return nil, err, remote.MapError("sftp open", err)
		}
		defer f.Close()

		info, err := f.Stat()
		if err != nil {
			return nil, nil, remote.MapError("sftp stat", err)
		}
		if info.IsDir() {
			return nil, nil, remote.NotFound(key)
		}

		buf, err := remote.NewBuffer(remote.BufferOptions{
			Pool:     s.opts.Pool,
			Dir:      s.opts.SpoolDir,
			MaxBytes: s.opts.SpoolMaxBytes,
		})
		if err != nil {
			return nil, nil, remote.MapError("sftp buffer", err)
		}
		if _, err := buf.WriteFrom(f, s.opts.SpoolMaxBytes); err != nil {
			_ = buf.Close()
			if errors.Is(err, remote.ErrBufferLimit) {
				return nil, nil, fmt.Errorf("sftp: %s %q exceeds spool limit: %w", role, key, object.ErrQuota)
			}
			return nil, nil, remote.MapError("sftp buffer", err)
		}
		if _, err := buf.Seek(0, io.SeekStart); err != nil {
			_ = buf.Close()
			return nil, nil, remote.MapError("sftp buffer seek", err)
		}
		meta := object.ObjectMetadata{Key: key, Size: info.Size(), ModTime: info.ModTime()}
		return remote.NewBufferArtifact(buf, meta), nil, nil
	})
}

// Open открывает поток исходного объекта через spillable буфер. При ошибке
// соединения выполняется повторная попытка с новым соединением.
func (s *SourceStore) Open(ctx context.Context, key object.ObjectKey) (object.Artifact, error) {
	full, err := s.opts.key(key)
	if err != nil {
		return nil, err
	}
	return s.openBuffered(ctx, full, key, "source")
}

var _ storage.SourceStore = (*SourceStore)(nil)

// ResultStore — SFTP-реализация storage.ResultStore.
type ResultStore struct {
	store
}

// NewResultStore создаёт SFTP ResultStore.
func NewResultStore(opts Options) (*ResultStore, error) {
	s, err := newStore(opts)
	if err != nil {
		return nil, err
	}
	return &ResultStore{store: s}, nil
}

// Lookup возвращает метаданные результата. При ошибке соединения выполняется
// повторная попытка с новым соединением.
func (r *ResultStore) Lookup(ctx context.Context, key object.ObjectKey) (object.ObjectMetadata, error) {
	return withRetry(ctx, &r.store, func(cl *pooledClient) (object.ObjectMetadata, error, error) {
		meta, err := r.opts.statWithClient(ctx, key, cl.client)
		switch {
		case err == nil:
			return meta, nil, nil
		case os.IsNotExist(err):
			return object.ObjectMetadata{}, nil, remote.NotFound(key)
		case !isClientErr(err):
			return object.ObjectMetadata{}, nil, remote.MapError("sftp stat", err)
		default:
			return object.ObjectMetadata{}, err, remote.MapError("sftp stat", err)
		}
	})
}

// Open открывает перематываемый поток результата через spillable буфер.
// При ошибке соединения выполняется повторная попытка с новым соединением.
func (r *ResultStore) Open(ctx context.Context, key object.ObjectKey) (object.Artifact, error) {
	full, err := r.opts.key(key)
	if err != nil {
		return nil, err
	}
	return r.openBuffered(ctx, full, key, "result")
}

// ReadStream открывает одноразовый поток результата напрямую из SFTP без
// материализации. Соединение удерживается до закрытия Stream. При ошибке
// соединения выполняется повторная попытка с новым соединением.
func (r *ResultStore) ReadStream(ctx context.Context, key object.ObjectKey) (object.Stream, error) {
	full, err := r.opts.key(key)
	if err != nil {
		return nil, err
	}
	return withRetry(ctx, &r.store, func(cl *pooledClient) (object.Stream, error, error) {
		f, err := cl.Open(full)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, nil, remote.NotFound(key)
			}
			return nil, err, remote.MapError("sftp open", err)
		}
		info, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return nil, nil, remote.MapError("sftp stat", err)
		}
		if info.IsDir() {
			_ = f.Close()
			return nil, nil, remote.NotFound(key)
		}
		meta := object.ObjectMetadata{Key: key, Size: info.Size(), ModTime: info.ModTime()}
		// f закрывается через Stream.Close; соединение возвращается в пул
		// после закрытия потока (cl.discard вызывается в Close).
		return remote.NewStreamArtifact(f, &sftpStreamCloser{f: f, client: cl}, meta), nil, nil
	})
}

// sftpStreamCloser закрывает поток и сбрасывает соединение пула.
type sftpStreamCloser struct {
	f      io.Closer
	client *pooledClient
	once   bool
}

func (c *sftpStreamCloser) Close() error {
	if c.once {
		return nil
	}
	c.once = true
	err := c.f.Close()
	c.client.discard()
	return err
}

// Publish полностью завершает upload до возврата: temp-upload + rename.
//
// Особенность метода: внутренние ошибки (dial/mkdir/create/write/close/
// rename temp) не ретраятся — операция выполняется ровно одной попыткой
// и возвращает ошибку сразу. Это выражено через общий каркас
// withRetryPolicy с политикой neverRetry; работа с src-ридером и очистка
// temp-файлов выполняются внутри одиночной попытки (publishAttempt).
func (r *ResultStore) Publish(ctx context.Context, key object.ObjectKey, src io.Reader, opts object.PublishOptions) error {
	full, err := r.opts.key(key)
	if err != nil {
		return err
	}
	_, err = withRetryPolicy(ctx, &r.store, neverRetry, func(cl *pooledClient) (struct{}, error, error) {
		return struct{}{}, nil, r.publishAttempt(full, key, src, opts, cl)
	})
	return err
}

// publishAttempt выполняет одну попытку публикации на переданном клиенте.
// Возвращаемая ошибка всегда замаплена (raw = nil): каркас не предпринимает
// повторов независимо от типа ошибки.
func (r *ResultStore) publishAttempt(full string, key object.ObjectKey, src io.Reader, opts object.PublishOptions, cl *pooledClient) error {
	dir := path.Dir(full)
	if err := cl.MkdirAll(dir); err != nil {
		return remote.MapError("sftp mkdir", err)
	}

	if opts.NoOverwrite {
		// Эксклюзивное создание целевого файла (O_EXCL): не перезаписывает
		// существующий объект. Если файл уже существует — ErrConflict.
		dst, err := cl.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
		if err != nil {
			if isExistErr(err) {
				// Бизнес-отказ не закрывает соединение каркасом.
				cl.detach()
				return remote.Conflict(key)
			}
			return remote.MapError("sftp create target", err)
		}
		if _, err := io.Copy(dst, src); err != nil {
			_ = dst.Close()
			_ = cl.Remove(full)
			return remote.MapError("sftp write target", err)
		}
		if err := dst.Close(); err != nil {
			_ = cl.Remove(full)
			return remote.MapError("sftp close target", err)
		}
		return nil
	}

	// Временный файл в том же каталоге для атомарного rename.
	tmpName := path.Join(dir, fmt.Sprintf(".tmp-%s-%d", path.Base(full), time.Now().UnixNano()))
	tmp, err := cl.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return remote.MapError("sftp create temp", err)
	}
	cleanup := func() {
		_ = tmp.Close()
		_ = cl.Remove(tmpName)
	}
	if _, err := io.Copy(tmp, src); err != nil {
		cleanup()
		return remote.MapError("sftp write temp", err)
	}
	if err := tmp.Close(); err != nil {
		_ = cl.Remove(tmpName)
		return remote.MapError("sftp close temp", err)
	}

	if err := cl.PosixRename(tmpName, full); err != nil {
		_ = cl.Remove(tmpName)
		return remote.MapError("sftp rename", err)
	}
	return nil
}

// Delete удаляет объект. Идемпотентно. При ошибке соединения выполняется
// повторная попытка с новым соединением.
func (r *ResultStore) Delete(ctx context.Context, key object.ObjectKey) error {
	full, err := r.opts.key(key)
	if err != nil {
		return err
	}
	_, err = withRetry(ctx, &r.store, func(cl *pooledClient) (struct{}, error, error) {
		err := cl.Remove(full)
		switch {
		case err == nil:
			return struct{}{}, nil, nil
		case os.IsNotExist(err):
			// Идемпотентность: объекта уже нет.
			return struct{}{}, nil, nil
		case !isClientErr(err):
			return struct{}{}, nil, remote.MapError("sftp remove", err)
		default:
			return struct{}{}, err, remote.MapError("sftp remove", err)
		}
	})
	return err
}

// Stats возвращает агрегированную статистику по корню (рекурсивно).
// При ошибке соединения выполняется повторная попытка с новым соединением.
func (r *ResultStore) Stats(ctx context.Context) (object.StoreStats, error) {
	root := r.opts.Root
	if root == "" {
		root = "."
	}
	return withRetry(ctx, &r.store, func(cl *pooledClient) (object.StoreStats, error, error) {
		var stats object.StoreStats
		if err := r.walk(cl.client, root, &stats); err != nil {
			return object.StoreStats{}, err, remote.MapError("sftp walk", err)
		}
		return stats, nil, nil
	})
}

func (r *ResultStore) walk(client client, dir string, stats *object.StoreStats) error {
	entries, err := client.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".tmp-") {
			continue
		}
		full := path.Join(dir, name)
		if e.IsDir() {
			if err := r.walk(client, full, stats); err != nil {
				return err
			}
			continue
		}
		stats.Objects++
		stats.TotalBytes += e.Size()
	}
	return nil
}

var _ storage.ResultStore = (*ResultStore)(nil)

// isExistErr сообщает, является ли ошибка признаком существования файла.
func isExistErr(err error) bool {
	return errors.Is(err, os.ErrExist)
}
