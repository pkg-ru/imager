// Package ftp реализует storage.SourceStore и storage.ResultStore поверх
// FTP и FTPS (FTP over TLS).
//
// FTP и FTPS реализуют ResultStore через temp-upload + rename. Публикация
// проверяет доступность необходимых FTP-команд (STOR, RNFR/RNTO, DELE) и
// выполняет cleanup временного файла при любой ошибке. Для NoOverwrite
// используется best-effort проверка существования целевого файла до rename.
//
// Секреты (пароль) не передаются в URI/логи: задаются отдельными полями
// конфигурации.
package ftp

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/jlaffaye/ftp"
	"github.com/pkg-ru/imager/internal/adapters/storage/remote"
	"github.com/pkg-ru/imager/internal/application/ports/storage"
	"github.com/pkg-ru/imager/internal/domain/object"
)

// conn — узкий интерфейс FTP-соединения, используемый адаптером. Выделен,
// чтобы тесты могли подставлять in-memory реализацию без реального сервера.
type conn interface {
	Login(user, password string) error
	Quit() error
	List(path string) ([]*ftp.Entry, error)
	Retr(path string) (io.ReadCloser, error)
	Stor(path string, r io.Reader) error
	Rename(from, to string) error
	Delete(path string) error
	MakeDir(path string) error
	// Feature возвращает true, если сервер объявил поддержку команды
	// (через FEAT). Для команд базового RFC 959 (STOR, RNFR/RNTO, DELE)
	// возвращает true, даже если сервер не поддерживает FEAT.
	Feature(cmd string) bool
}

// Options — параметры FTP/FTPS-адаптера.
type Options struct {
	// Addr — адрес сервера "host:port".
	Addr string
	// User — имя пользователя.
	User string
	// Password — пароль.
	Password string
	// TLS — true для FTPS (FTP over TLS).
	TLS bool
	// TLSVerify — проверять ли TLS-сертификат для FTPS (default: true).
	TLSVerify bool
	// Root — корневой каталог (пусто = корень).
	Root string
	// SpoolDir — каталог временных spool (пусто = os.TempDir).
	SpoolDir string
	// SpoolMaxBytes — максимальный размер source spool (0 = без лимита).
	SpoolMaxBytes int64
	// Pool — общий бюджет памяти процесса для spillable-буферов.
	// Если nil, буферы работают только через память без spill.
	Pool *remote.BufferPool
	// DialTimeout — таймаут соединения.
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
	// Dialer — опциональный кастомный dialer (для тестов).
	Dialer func(ctx context.Context, addr string, tls bool) (conn, error)
}

func (o Options) validate() error {
	if o.Addr == "" {
		return fmt.Errorf("ftp: empty addr")
	}
	if o.User == "" {
		return fmt.Errorf("ftp: empty user")
	}
	if o.TLS && !o.TLSVerify {
		return fmt.Errorf("ftp: tls-verify=false is forbidden; set tls-verify: true")
	}
	return nil
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

// isConnErr отличает ошибки соединения (требуют discard и повторной попытки)
// от бизнес-ошибок; общая логика вынесена в remote.IsConnErr.
func isConnErr(err error) bool {
	return remote.IsConnErr(err)
}

func (o Options) dial(ctx context.Context) (conn, error) {
	if o.Dialer != nil {
		return o.Dialer(ctx, o.Addr, o.TLS)
	}
	timeout := o.DialTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	opts := []ftp.DialOption{ftp.DialWithTimeout(timeout), ftp.DialWithContext(ctx)}
	if o.TLS {
		// FTPS: explicit TLS (AUTH TLS) поверх управляющего соединения.
		// Верификация сертификата включена по умолчанию (TLSVerify=true).
		opts = append(opts, ftp.DialWithExplicitTLS(&tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: !o.TLSVerify,
			ServerName:         serverName(o.Addr),
		}))
	}
	serverConn, err := ftp.Dial(o.Addr, opts...)
	if err != nil {
		return nil, fmt.Errorf("ftp: dial: %w", err)
	}
	if err := serverConn.Login(o.User, o.Password); err != nil {
		_ = serverConn.Quit()
		return nil, fmt.Errorf("ftp: login: %w", err)
	}
	return &serverConnAdapter{ServerConn: serverConn}, nil
}

// serverName извлекает hostname из "host:port" для TLS ServerName.
func serverName(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

// serverConnAdapter адаптирует *ftp.ServerConn к интерфейсу conn.
type serverConnAdapter struct {
	*ftp.ServerConn
}

// Retr возвращает поток данных как io.ReadCloser.
func (a *serverConnAdapter) Retr(path string) (io.ReadCloser, error) {
	return a.ServerConn.Retr(path)
}

// Feature сообщает о поддержке команды. Библиотека jlaffaye/ftp не
// предоставляет публичного доступа к результатам FEAT, поэтому для команд
// базового RFC 959 (STOR, RNFR/RNTO, DELE, MKD), которые требуются для
// публикации, возвращается true. Расширенные команды (например, MFMT)
// считаются неподдерживаемыми.
func (a *serverConnAdapter) Feature(cmd string) bool {
	switch strings.ToUpper(cmd) {
	case "STOR", "RNFR", "RNTO", "DELE", "MKD":
		return true
	default:
		return false
	}
}

func (o Options) key(key object.ObjectKey) (string, error) {
	k, err := remote.CanonicalKey(key)
	if err != nil {
		return "", remote.Unsafe(key, err)
	}
	if o.Root == "" {
		return k, nil
	}
	return strings.TrimSuffix(o.Root, "/") + "/" + k, nil
}

// stat возвращает метаданные объекта. При ошибке соединения выполняется
// повторная попытка с новым соединением (до MaxAttempts).
func (o Options) stat(ctx context.Context, key object.ObjectKey) (object.ObjectMetadata, error) {
	full, err := o.key(key)
	if err != nil {
		return object.ObjectMetadata{}, err
	}
	s := store{opts: o}
	return withRetry(ctx, &s, func(c *pooledConn) (object.ObjectMetadata, error, error) {
		entries, err := c.List(full)
		if err != nil {
			return object.ObjectMetadata{}, err, remote.MapError("ftp list", err)
		}
		if len(entries) == 0 {
			return object.ObjectMetadata{}, nil, remote.NotFound(key)
		}
		e := entries[0]
		if e.Type == ftp.EntryTypeFolder {
			return object.ObjectMetadata{}, nil, remote.NotFound(key)
		}
		return object.ObjectMetadata{
			Key:     key,
			Size:    int64(e.Size),
			ModTime: e.Time,
		}, nil, nil
	})
}

// SourceStore — FTP/FTPS-реализация storage.SourceStore (read-only).
type SourceStore struct {
	store
}

// NewSourceStore создаёт FTP/FTPS SourceStore.
func NewSourceStore(opts Options) (*SourceStore, error) {
	s, err := newStore(opts)
	if err != nil {
		return nil, err
	}
	return &SourceStore{store: s}, nil
}

// Lookup возвращает метаданные исходного объекта.
func (s *SourceStore) Lookup(ctx context.Context, key object.ObjectKey) (object.ObjectMetadata, error) {
	return s.opts.stat(ctx, key)
}

// openBuffered открывает объект по полному пути full и материализует его в
// spillable буфер. role используется только в тексте ошибки quota
// ("source"/"result").
func (s *store) openBuffered(ctx context.Context, full string, key object.ObjectKey, role string) (object.Artifact, error) {
	return withRetry(ctx, s, func(c *pooledConn) (object.Artifact, error, error) {
		rc, err := c.Retr(full)
		if err != nil {
			return nil, err, remote.MapError("ftp retr", err)
		}
		defer rc.Close()

		buf, err := remote.NewBuffer(remote.BufferOptions{
			Pool:     s.opts.Pool,
			Dir:      s.opts.SpoolDir,
			MaxBytes: s.opts.SpoolMaxBytes,
		})
		if err != nil {
			return nil, nil, remote.MapError("ftp buffer", err)
		}
		if _, err := buf.WriteFrom(rc, s.opts.SpoolMaxBytes); err != nil {
			_ = buf.Close()
			if errors.Is(err, remote.ErrBufferLimit) {
				return nil, nil, fmt.Errorf("ftp: %s %q exceeds spool limit: %w", role, key, object.ErrQuota)
			}
			return nil, nil, remote.MapError("ftp buffer", err)
		}
		if _, err := buf.Seek(0, io.SeekStart); err != nil {
			_ = buf.Close()
			return nil, nil, remote.MapError("ftp buffer seek", err)
		}
		meta := object.ObjectMetadata{Key: key, Size: buf.Size()}
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

// ResultStore — FTP/FTPS-реализация storage.ResultStore (temp-upload + rename).
type ResultStore struct {
	store
}

// NewResultStore создаёт FTP/FTPS ResultStore. Публикация выполняется через
// temp-upload + rename и требует поддержки сервером команд STOR, RNFR/RNTO и
// DELE (базовый RFC 959). Для plain FTP и FTPS используется одинаковый путь.
func NewResultStore(opts Options) (*ResultStore, error) {
	s, err := newStore(opts)
	if err != nil {
		return nil, err
	}
	return &ResultStore{store: s}, nil
}

// Lookup возвращает метаданные результата.
func (r *ResultStore) Lookup(ctx context.Context, key object.ObjectKey) (object.ObjectMetadata, error) {
	return r.opts.stat(ctx, key)
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

// ReadStream открывает одноразовый поток результата напрямую из FTP без
// материализации. Соединение удерживается до закрытия Stream. При ошибке
// соединения выполняется повторная попытка с новым соединением.
func (r *ResultStore) ReadStream(ctx context.Context, key object.ObjectKey) (object.Stream, error) {
	full, err := r.opts.key(key)
	if err != nil {
		return nil, err
	}
	return withRetry(ctx, &r.store, func(c *pooledConn) (object.Stream, error, error) {
		rc, err := c.Retr(full)
		if err != nil {
			return nil, err, remote.MapError("ftp retr", err)
		}
		meta := object.ObjectMetadata{Key: key}
		// rc закрывается через Stream.Close; соединение возвращается в пул
		// после закрытия потока (c.discard вызывается в Close).
		return remote.NewStreamArtifact(rc, &ftpStreamCloser{rc: rc, conn: c}, meta), nil, nil
	})
}

// ftpStreamCloser закрывает поток и сбрасывает соединение пула.
type ftpStreamCloser struct {
	rc   io.Closer
	conn *pooledConn
	once bool
}

func (c *ftpStreamCloser) Close() error {
	if c.once {
		return nil
	}
	c.once = true
	err := c.rc.Close()
	c.conn.discard()
	return err
}

// Publish полностью завершает upload до возврата: temp-upload + rename.
// Перед загрузкой проверяет поддержку необходимых FTP-команд (STOR,
// RNFR/RNTO, DELE) и выполняет cleanup временного файла при любой ошибке.
// При ошибке соединения выполняется повторная попытка с новым соединением.
func (r *ResultStore) Publish(ctx context.Context, key object.ObjectKey, src io.Reader, opts object.PublishOptions) error {
	full, err := r.opts.key(key)
	if err != nil {
		return err
	}
	_, err = withRetry(ctx, &r.store, func(c *pooledConn) (struct{}, error, error) {
		// Проверка capability: публикация требует STOR, RNFR/RNTO и DELE.
		for _, cmd := range []string{"STOR", "RNFR", "RNTO", "DELE"} {
			if !c.Feature(cmd) {
				return struct{}{}, nil, fmt.Errorf("ftp: server does not support required command %s: %w", cmd, object.ErrUnavailable)
			}
		}

		dir := full
		if idx := strings.LastIndex(full, "/"); idx >= 0 {
			dir = full[:idx]
		}
		if dir != "" {
			if err := c.MakeDir(dir); err != nil {
				// Каталог может уже существовать — игнорируем ошибку создания.
				_ = err
			}
		}

		tmpName := full + ".tmp"
		if err := c.Stor(tmpName, src); err != nil {
			return struct{}{}, err, remote.MapError("ftp stor temp", err)
		}

		if opts.NoOverwrite {
			// FTP не предоставляет атомарного no-overwrite rename; проверяем
			// существование целевого файла до rename (best-effort).
			entries, err := c.List(full)
			if err == nil && len(entries) > 0 {
				_ = c.Delete(tmpName)
				return struct{}{}, nil, remote.Conflict(key)
			}
		}

		if err := c.Rename(tmpName, full); err != nil {
			_ = c.Delete(tmpName)
			return struct{}{}, err, remote.MapError("ftp rename", err)
		}
		return struct{}{}, nil, nil
	})
	return err
}

// Delete удаляет объект. Идемпотентно. При ошибке соединения выполняется
// повторная попытка с новым соединением.
func (r *ResultStore) Delete(ctx context.Context, key object.ObjectKey) error {
	full, err := r.opts.key(key)
	if err != nil {
		return err
	}
	_, err = withRetry(ctx, &r.store, func(c *pooledConn) (struct{}, error, error) {
		err := c.Delete(full)
		if err != nil {
			return struct{}{}, err, remote.MapError("ftp delete", err)
		}
		return struct{}{}, nil, nil
	})
	return err
}

// Stats возвращает агрегированную статистику по корню (рекурсивно).
// При ошибке соединения выполняется повторная попытка с новым соединением.
func (r *ResultStore) Stats(ctx context.Context) (object.StoreStats, error) {
	root := r.opts.Root
	if root == "" {
		root = "/"
	}
	return withRetry(ctx, &r.store, func(c *pooledConn) (object.StoreStats, error, error) {
		var stats object.StoreStats
		if err := r.walk(c.conn, root, &stats); err != nil {
			return object.StoreStats{}, err, remote.MapError("ftp walk", err)
		}
		return stats, nil, nil
	})
}

func (r *ResultStore) walk(c conn, dir string, stats *object.StoreStats) error {
	entries, err := c.List(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name
		if strings.HasPrefix(name, ".tmp") {
			continue
		}
		full := strings.TrimSuffix(dir, "/") + "/" + name
		if e.Type == ftp.EntryTypeFolder {
			if err := r.walk(c, full, stats); err != nil {
				return err
			}
			continue
		}
		stats.Objects++
		stats.TotalBytes += int64(e.Size)
	}
	return nil
}

var _ storage.ResultStore = (*ResultStore)(nil)
