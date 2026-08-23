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

func (o Options) stat(ctx context.Context, key object.ObjectKey) (object.ObjectMetadata, error) {
	full, err := o.key(key)
	if err != nil {
		return object.ObjectMetadata{}, err
	}
	client, err := o.dial()
	if err != nil {
		return object.ObjectMetadata{}, remote.MapError("sftp dial", err)
	}
	defer client.Close()
	info, err := client.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return object.ObjectMetadata{}, remote.NotFound(key)
		}
		return object.ObjectMetadata{}, remote.MapError("sftp stat", err)
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

// isClientErr отличает ошибки соединения (требуют discard) от
// бизнес-ошибок (например, ErrNotFound), при которых соединение
// можно переиспользовать. Все бизнес-ошибки из remote и object
// мапятся через MapError/NotFound; ошибки соединения — сырые SSH/IO.
func isClientErr(err error) bool {
	// ErrNotFound и другие object-ошибки — не client-ошибки.
	if object.IsNotFound(err) || object.IsConflict(err) || object.IsQuota(err) || object.IsUnsafePath(err) || object.IsUnavailable(err) {
		return false
	}
	// Всё остальное (IO, SSH, timeout) — ошибка соединения.
	return err != nil
}

// attempts возвращает число попыток операции (>= 1).
func (o Options) attempts() int {
	n := o.MaxAttempts
	if n < 1 {
		n = 1
	}
	return n
}

// withTimeout оборачивает ctx таймаутом операции, если задан ReadTimeout.
// Возвращает cancel-функцию, которую вызывающий обязан вызвать (defer cancel()).
func (o Options) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if o.ReadTimeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, o.ReadTimeout)
}

// SourceStore — SFTP-реализация storage.SourceStore (read-only).
type SourceStore struct {
	opts Options
	pool *connPool
}

// NewSourceStore создаёт SFTP SourceStore.
func NewSourceStore(opts Options) (*SourceStore, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	var pool *connPool
	if opts.Client == nil {
		pool = newConnPool(opts)
	}
	return &SourceStore{opts: opts, pool: pool}, nil
}

// getClient возвращает клиента из пула или opts.Client для тестов.
func (s *SourceStore) getClient(ctx context.Context) (*pooledClient, error) {
	if s.pool != nil {
		return s.pool.acquire(ctx)
	}
	// Для тестов с fake client используем прямой вызов.
	if s.opts.Client != nil {
		return &pooledClient{client: s.opts.Client}, nil
	}
	c, err := s.opts.dial()
	if err != nil {
		return nil, err
	}
	return &pooledClient{client: c}, nil
}

// Lookup возвращает метаданные исходного объекта. При ошибке соединения
// выполняется повторная попытка с новым соединением (до MaxAttempts).
func (s *SourceStore) Lookup(ctx context.Context, key object.ObjectKey) (object.ObjectMetadata, error) {
	ctx, cancel := s.opts.withTimeout(ctx)
	defer cancel()
	attempts := s.opts.attempts()
	var lastErr error
	for i := 0; i < attempts; i++ {
		cl, err := s.getClient(ctx)
		if err != nil {
			return object.ObjectMetadata{}, remote.MapError("sftp dial", err)
		}
		meta, err := s.opts.statWithClient(ctx, key, cl)
		if err == nil {
			return meta, nil
		}
		if os.IsNotExist(err) {
			return object.ObjectMetadata{}, remote.NotFound(key)
		}
		// Бизнес-ошибка (NotFound и т.п.) — не ретраим.
		if !isClientErr(err) {
			return meta, remote.MapError("sftp stat", err)
		}
		lastErr = remote.MapError("sftp stat", err)
		cl.discard()
		if ctx.Err() != nil {
			break
		}
	}
	return object.ObjectMetadata{}, lastErr
}

// Open открывает поток исходного объекта через spillable буфер. При ошибке
// соединения выполняется повторная попытка с новым соединением.
func (s *SourceStore) Open(ctx context.Context, key object.ObjectKey) (object.Artifact, error) {
	full, err := s.opts.key(key)
	if err != nil {
		return nil, err
	}
	ctx, cancel := s.opts.withTimeout(ctx)
	defer cancel()
	attempts := s.opts.attempts()
	var lastErr error
	for i := 0; i < attempts; i++ {
		cl, err := s.getClient(ctx)
		if err != nil {
			return nil, remote.MapError("sftp dial", err)
		}
		needDiscard := true
		defer func() {
			if needDiscard {
				cl.discard()
			}
		}()

		f, err := cl.Open(full)
		if err != nil {
			if os.IsNotExist(err) {
				needDiscard = false
				return nil, remote.NotFound(key)
			}
			lastErr = remote.MapError("sftp open", err)
			if !isClientErr(err) {
				return nil, lastErr
			}
			cl.discard()
			if ctx.Err() != nil {
				return nil, lastErr
			}
			continue
		}
		defer f.Close()

		info, err := f.Stat()
		if err != nil {
			return nil, remote.MapError("sftp stat", err)
		}
		if info.IsDir() {
			needDiscard = false
			return nil, remote.NotFound(key)
		}

		buf, err := remote.NewBuffer(remote.BufferOptions{
			Pool:     s.opts.Pool,
			Dir:      s.opts.SpoolDir,
			MaxBytes: s.opts.SpoolMaxBytes,
		})
		if err != nil {
			return nil, remote.MapError("sftp buffer", err)
		}
		if _, err := buf.WriteFrom(f, s.opts.SpoolMaxBytes); err != nil {
			_ = buf.Close()
			if errors.Is(err, remote.ErrBufferLimit) {
				return nil, fmt.Errorf("sftp: source %q exceeds spool limit: %w", key, object.ErrQuota)
			}
			return nil, remote.MapError("sftp buffer", err)
		}
		if _, err := buf.Seek(0, io.SeekStart); err != nil {
			_ = buf.Close()
			return nil, remote.MapError("sftp buffer seek", err)
		}
		meta := object.ObjectMetadata{Key: key, Size: info.Size(), ModTime: info.ModTime()}
		needDiscard = false
		return remote.NewBufferArtifact(buf, meta), nil
	}
	return nil, lastErr
}

var _ storage.SourceStore = (*SourceStore)(nil)

// ResultStore — SFTP-реализация storage.ResultStore.
type ResultStore struct {
	opts Options
	pool *connPool
}

// NewResultStore создаёт SFTP ResultStore.
func NewResultStore(opts Options) (*ResultStore, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	var pool *connPool
	if opts.Client == nil {
		pool = newConnPool(opts)
	}
	return &ResultStore{opts: opts, pool: pool}, nil
}

// getClient возвращает клиента из пула или opts.Client для тестов.
func (r *ResultStore) getClient(ctx context.Context) (*pooledClient, error) {
	if r.pool != nil {
		return r.pool.acquire(ctx)
	}
	if r.opts.Client != nil {
		return &pooledClient{client: r.opts.Client}, nil
	}
	c, err := r.opts.dial()
	if err != nil {
		return nil, err
	}
	return &pooledClient{client: c}, nil
}

// Lookup возвращает метаданные результата. При ошибке соединения выполняется
// повторная попытка с новым соединением.
func (r *ResultStore) Lookup(ctx context.Context, key object.ObjectKey) (object.ObjectMetadata, error) {
	ctx, cancel := r.opts.withTimeout(ctx)
	defer cancel()
	attempts := r.opts.attempts()
	var lastErr error
	for i := 0; i < attempts; i++ {
		cl, err := r.getClient(ctx)
		if err != nil {
			return object.ObjectMetadata{}, remote.MapError("sftp dial", err)
		}
		meta, err := r.opts.statWithClient(ctx, key, cl)
		if err == nil {
			return meta, nil
		}
		if os.IsNotExist(err) {
			return object.ObjectMetadata{}, remote.NotFound(key)
		}
		if !isClientErr(err) {
			return meta, remote.MapError("sftp stat", err)
		}
		lastErr = remote.MapError("sftp stat", err)
		cl.discard()
		if ctx.Err() != nil {
			break
		}
	}
	return object.ObjectMetadata{}, lastErr
}

// Open открывает перематываемый поток результата через spillable буфер.
// При ошибке соединения выполняется повторная попытка с новым соединением.
func (r *ResultStore) Open(ctx context.Context, key object.ObjectKey) (object.Artifact, error) {
	full, err := r.opts.key(key)
	if err != nil {
		return nil, err
	}
	ctx, cancel := r.opts.withTimeout(ctx)
	defer cancel()
	attempts := r.opts.attempts()
	var lastErr error
	for i := 0; i < attempts; i++ {
		cl, err := r.getClient(ctx)
		if err != nil {
			return nil, remote.MapError("sftp dial", err)
		}
		needDiscard := true
		defer func() {
			if needDiscard {
				cl.discard()
			}
		}()

		f, err := cl.Open(full)
		if err != nil {
			if os.IsNotExist(err) {
				needDiscard = false
				return nil, remote.NotFound(key)
			}
			lastErr = remote.MapError("sftp open", err)
			if !isClientErr(err) {
				return nil, lastErr
			}
			cl.discard()
			if ctx.Err() != nil {
				return nil, lastErr
			}
			continue
		}
		defer f.Close()

		info, err := f.Stat()
		if err != nil {
			return nil, remote.MapError("sftp stat", err)
		}
		if info.IsDir() {
			needDiscard = false
			return nil, remote.NotFound(key)
		}

		buf, err := remote.NewBuffer(remote.BufferOptions{
			Pool:     r.opts.Pool,
			Dir:      r.opts.SpoolDir,
			MaxBytes: r.opts.SpoolMaxBytes,
		})
		if err != nil {
			return nil, remote.MapError("sftp buffer", err)
		}
		if _, err := buf.WriteFrom(f, r.opts.SpoolMaxBytes); err != nil {
			_ = buf.Close()
			if errors.Is(err, remote.ErrBufferLimit) {
				return nil, fmt.Errorf("sftp: result %q exceeds spool limit: %w", key, object.ErrQuota)
			}
			return nil, remote.MapError("sftp buffer", err)
		}
		if _, err := buf.Seek(0, io.SeekStart); err != nil {
			_ = buf.Close()
			return nil, remote.MapError("sftp buffer seek", err)
		}
		meta := object.ObjectMetadata{Key: key, Size: info.Size(), ModTime: info.ModTime()}
		needDiscard = false
		return remote.NewBufferArtifact(buf, meta), nil
	}
	return nil, lastErr
}

// ReadStream открывает одноразовый поток результата напрямую из SFTP без
// материализации. Соединение удерживается до закрытия Stream. При ошибке
// соединения выполняется повторная попытка с новым соединением.
func (r *ResultStore) ReadStream(ctx context.Context, key object.ObjectKey) (object.Stream, error) {
	full, err := r.opts.key(key)
	if err != nil {
		return nil, err
	}
	ctx, cancel := r.opts.withTimeout(ctx)
	defer cancel()
	attempts := r.opts.attempts()
	var lastErr error
	for i := 0; i < attempts; i++ {
		cl, err := r.getClient(ctx)
		if err != nil {
			return nil, remote.MapError("sftp dial", err)
		}
		needDiscard := true
		defer func() {
			if needDiscard {
				cl.discard()
			}
		}()

		f, err := cl.Open(full)
		if err != nil {
			if os.IsNotExist(err) {
				needDiscard = false
				return nil, remote.NotFound(key)
			}
			lastErr = remote.MapError("sftp open", err)
			if !isClientErr(err) {
				return nil, lastErr
			}
			cl.discard()
			if ctx.Err() != nil {
				return nil, lastErr
			}
			continue
		}
		info, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return nil, remote.MapError("sftp stat", err)
		}
		if info.IsDir() {
			_ = f.Close()
			needDiscard = false
			return nil, remote.NotFound(key)
		}
		meta := object.ObjectMetadata{Key: key, Size: info.Size(), ModTime: info.ModTime()}
		needDiscard = false
		// f закрывается через Stream.Close; соединение возвращается в пул
		// после закрытия потока (cl.discard вызывается в Close).
		return remote.NewStreamArtifact(f, &sftpStreamCloser{f: f, client: cl}, meta), nil
	}
	return nil, lastErr
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
// При ошибке соединения выполняется повторная попытка с новым соединением.
func (r *ResultStore) Publish(ctx context.Context, key object.ObjectKey, src io.Reader, opts object.PublishOptions) error {
	full, err := r.opts.key(key)
	if err != nil {
		return err
	}
	ctx, cancel := r.opts.withTimeout(ctx)
	defer cancel()
	attempts := r.opts.attempts()
	var lastErr error
	for i := 0; i < attempts; i++ {
		cl, err := r.getClient(ctx)
		if err != nil {
			return remote.MapError("sftp dial", err)
		}
		needDiscard := true
		defer func() {
			if needDiscard {
				cl.discard()
			}
		}()

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
					needDiscard = false
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
			needDiscard = false
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
		needDiscard = false
		return nil
	}
	return lastErr
}

// Delete удаляет объект. Идемпотентно. При ошибке соединения выполняется
// повторная попытка с новым соединением.
func (r *ResultStore) Delete(ctx context.Context, key object.ObjectKey) error {
	full, err := r.opts.key(key)
	if err != nil {
		return err
	}
	ctx, cancel := r.opts.withTimeout(ctx)
	defer cancel()
	attempts := r.opts.attempts()
	var lastErr error
	for i := 0; i < attempts; i++ {
		cl, err := r.getClient(ctx)
		if err != nil {
			return remote.MapError("sftp dial", err)
		}
		needDiscard := true
		defer func() {
			if needDiscard {
				cl.discard()
			}
		}()

		err = cl.Remove(full)
		if err != nil {
			if os.IsNotExist(err) {
				needDiscard = false
				return nil
			}
			lastErr = remote.MapError("sftp remove", err)
			if !isClientErr(err) {
				return lastErr
			}
			cl.discard()
			if ctx.Err() != nil {
				return lastErr
			}
			continue
		}
		needDiscard = false
		return nil
	}
	return lastErr
}

// Stats возвращает агрегированную статистику по корню (рекурсивно).
// При ошибке соединения выполняется повторная попытка с новым соединением.
func (r *ResultStore) Stats(ctx context.Context) (object.StoreStats, error) {
	ctx, cancel := r.opts.withTimeout(ctx)
	defer cancel()
	attempts := r.opts.attempts()
	var lastErr error
	for i := 0; i < attempts; i++ {
		cl, err := r.getClient(ctx)
		if err != nil {
			return object.StoreStats{}, remote.MapError("sftp dial", err)
		}
		needDiscard := true
		defer func() {
			if needDiscard {
				cl.discard()
			}
		}()

		root := r.opts.Root
		if root == "" {
			root = "."
		}
		var stats object.StoreStats
		err = r.walk(cl, root, &stats)
		if err != nil {
			lastErr = remote.MapError("sftp walk", err)
			if !isClientErr(err) {
				return object.StoreStats{}, lastErr
			}
			cl.discard()
			if ctx.Err() != nil {
				return object.StoreStats{}, lastErr
			}
			continue
		}
		needDiscard = false
		return stats, nil
	}
	return object.StoreStats{}, lastErr
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
