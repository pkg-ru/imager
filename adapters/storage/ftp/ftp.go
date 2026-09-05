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
	"net/textproto"
	"strings"
	"sync"
	"time"

	"github.com/jlaffaye/ftp"
	"github.com/pkg-ru/imager/adapters/storage/remote"
	"github.com/pkg-ru/imager/domain/object"
	"github.com/pkg-ru/imager/ports/storage"
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
	// RemoveDir удаляет каталог (FTP-команда RMD). Используется при
	// рекурсивном пакетном удалении (DeleteByPrefix).
	RemoveDir(path string) error
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
	// ConnOptions — общие параметры соединения: DialTimeout, ReadTimeout,
	// MaxAttempts, MaxConns, MaxIdleConns, MaxIdleConnsPerHost,
	// IdleConnTimeout. Раньше эти поля дублировались в Options каждого
	// адаптера; теперь единый тип из remote.
	remote.ConnOptions
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
	// Root попадает в FTP-команды (CWD/STOR/RNFR/RNTO) как часть пути:
	// управляющие байты (включая CR/LF) запрещены — защита от CRLF-инъекции.
	for _, r := range o.Root {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("ftp: root contains control character %q", r)
		}
	}
	return nil
}

func (o Options) dial(ctx context.Context) (conn, error) {
	if o.Dialer != nil {
		return o.Dialer(ctx, o.Addr, o.TLS)
	}
	timeout := o.DialTimeoutOrDefault()
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
	case "STOR", "RNFR", "RNTO", "DELE", "MKD", "RMD":
		return true
	default:
		return false
	}
}

// RemoveDir удаляет каталог через RMD.
func (a *serverConnAdapter) RemoveDir(path string) error {
	return a.ServerConn.RemoveDir(path)
}

// baseName возвращает имя последнего компонента пути (без каталога).
func baseName(p string) string {
	if idx := strings.LastIndex(p, "/"); idx >= 0 {
		return p[idx+1:]
	}
	return p
}

// isTempName сообщает, является ли имя файла временным файлом публикации.
// Учитываются как новые имена вида ".tmp-<base>-<UnixNano>" (конкурентно
// безопасные), так и старый суффикс "<key>.tmp" для совместимости.
func isTempName(name string) bool {
	return strings.HasPrefix(name, ".tmp-") || strings.HasSuffix(name, ".tmp")
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
	s := store{Store: remote.NewStoreDirect(o, o.dial, func(c conn) error { return c.Quit() })}
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
	return s.Opts.stat(ctx, key)
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
			Pool:     s.Opts.Pool,
			Dir:      s.Opts.SpoolDir,
			MaxBytes: s.Opts.SpoolMaxBytes,
		})
		if err != nil {
			return nil, nil, remote.MapError("ftp buffer", err)
		}
		if _, err := buf.WriteFrom(rc, s.Opts.SpoolMaxBytes); err != nil {
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
	full, err := s.Opts.key(key)
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
	return r.Opts.stat(ctx, key)
}

// Open открывает перематываемый поток результата через spillable буфер.
// При ошибке соединения выполняется повторная попытка с новым соединением.
func (r *ResultStore) Open(ctx context.Context, key object.ObjectKey) (object.Artifact, error) {
	full, err := r.Opts.key(key)
	if err != nil {
		return nil, err
	}
	return r.openBuffered(ctx, full, key, "result")
}

// ReadStream открывает одноразовый поток результата напрямую из FTP без
// материализации. Соединение удерживается до закрытия Stream. При ошибке
// соединения выполняется повторная попытка с новым соединением.
func (r *ResultStore) ReadStream(ctx context.Context, key object.ObjectKey) (object.Stream, error) {
	full, err := r.Opts.key(key)
	if err != nil {
		return nil, err
	}
	return withRetry(ctx, &r.store, func(c *pooledConn) (object.Stream, error, error) {
		rc, err := c.Retr(full)
		if err != nil {
			return nil, err, remote.MapError("ftp retr", err)
		}
		// Соединение должно жить до закрытия потока: передаём владение
		// (каркас не выполнит discard после успешного возврата).
		c.KeepAlive()
		meta := object.ObjectMetadata{Key: key}
		// rc закрывается через Stream.Close; соединение освобождается
		// после закрытия потока (session.Discard вызывается в Close).
		return remote.NewStreamArtifact(rc, &ftpStreamCloser{rc: rc, session: c.Session}, meta), nil, nil
	})
}

// ftpStreamCloser закрывает поток и сбрасывает соединение пула.
type ftpStreamCloser struct {
	rc      io.Closer
	session *remote.Session[conn]
	once    sync.Once
}

func (c *ftpStreamCloser) Close() error {
	var err error
	c.once.Do(func() {
		err = c.rc.Close()
		c.session.Discard()
	})
	return err
}

// Publish полностью завершает upload до возврата: temp-upload + rename.
// Перед загрузкой проверяет поддержку необходимых FTP-команд (STOR,
// RNFR/RNTO, DELE) и выполняет cleanup временного файла при любой ошибке.
// При ошибке соединения выполняется повторная попытка с новым соединением.
func (r *ResultStore) Publish(ctx context.Context, key object.ObjectKey, src io.Reader, opts object.PublishOptions) error {
	full, err := r.Opts.key(key)
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

		// Уникальное имя temp-файла: не попадает в List/Stats (фильтр по
		// префиксу ".tmp") и не конфликтует при конкурентных публикациях
		// одного ключа. Каталог — тот же, что и у целевого файла, для
		// атомарного rename.
		tmpName := dir + "/.tmp-" + baseName(full) + "-" + fmt.Sprint(time.Now().UnixNano())
		if dir == "" {
			tmpName = ".tmp-" + baseName(full) + "-" + fmt.Sprint(time.Now().UnixNano())
		}
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

// isFTPNotFound сообщает, является ли ошибка FTP признаком отсутствия файла
// (550 File unavailable). Библиотека jlaffaye/ftp возвращает
// *textproto.Error с полем Code.
func isFTPNotFound(err error) bool {
	if err == nil {
		return false
	}
	var tpErr *textproto.Error
	if errors.As(err, &tpErr) {
		return tpErr.Code == ftp.StatusFileUnavailable
	}
	return false
}

// Delete удаляет объект. Идемпотентно: 550 (no such file) маппится в
// object.ErrNotFound и не считается ошибкой соединения. При ошибке
// соединения выполняется повторная попытка с новым соединением.
func (r *ResultStore) Delete(ctx context.Context, key object.ObjectKey) error {
	full, err := r.Opts.key(key)
	if err != nil {
		return err
	}
	_, err = withRetry(ctx, &r.store, func(c *pooledConn) (struct{}, error, error) {
		err := c.Delete(full)
		if err != nil {
			if isFTPNotFound(err) {
				// Идемпотентность: объекта уже нет — это не ошибка.
				return struct{}{}, nil, nil
			}
			return struct{}{}, err, remote.MapError("ftp delete", err)
		}
		return struct{}{}, nil, nil
	})
	return err
}

// Stats возвращает агрегированную статистику по корню (рекурсивно).
// При ошибке соединения выполняется повторная попытка с новым соединением.
func (r *ResultStore) Stats(ctx context.Context) (object.StoreStats, error) {
	root := r.Opts.Root
	if root == "" {
		root = "/"
	}
	return withRetry(ctx, &r.store, func(c *pooledConn) (object.StoreStats, error, error) {
		var stats object.StoreStats
		if err := r.walk(c.Value, root, &stats); err != nil {
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
		if isTempName(name) {
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

// DeleteByPrefix рекурсивно удаляет все ключи, начинающиеся с prefix (с
// границей '/'), одним каталогом/пакетом. Реализует опциональный
// storage.PrefixDeleter (используется admin DELETE по исходнику).
//
// Обход использует ту же логику, что и walk (Stats): файлы удаляются через
// DELE, подкаталоги — через RMD (после опустошения). Временные файлы
// публикации (.tmp-* и старый суффикс .tmp) пропускаются. Идемпотентно:
// если каталога нет — возвращает (0, nil). Возвращает число удалённых
// файлов.
func (r *ResultStore) DeleteByPrefix(ctx context.Context, prefix object.ObjectKey) (int64, error) {
	full, err := r.Opts.key(prefix)
	if err != nil {
		return 0, err
	}
	return withRetry(ctx, &r.store, func(c *pooledConn) (int64, error, error) {
		n, err := r.deleteByPrefixWalk(c.Value, full)
		if err != nil {
			if isFTPNotFound(err) {
				// Каталог уже удалён — идемпотентно.
				return 0, nil, nil
			}
			return 0, err, remote.MapError("ftp delete by prefix", err)
		}
		return n, nil, nil
	})
}

// deleteByPrefixWalk рекурсивно удаляет содержимое dir: DELE для файлов,
// RMD для подкаталогов. Временные файлы пропускаются. Разделитель каталога
// нормализуется (без завершающего '/' для List/RMD).
func (r *ResultStore) deleteByPrefixWalk(c conn, dir string) (int64, error) {
	return r.deleteDir(c, strings.TrimSuffix(dir, "/"))
}

// deleteDir рекурсивно удаляет каталог dir: файлы через DELE, подкаталоги
// через рекурсию и RMD. Пропускает temp-файлы.
func (r *ResultStore) deleteDir(c conn, dir string) (int64, error) {
	entries, err := c.List(dir)
	if err != nil {
		return 0, err
	}
	var removed int64
	for _, e := range entries {
		name := e.Name
		if isTempName(name) {
			continue
		}
		sub := strings.TrimSuffix(dir, "/") + "/" + name
		if e.Type == ftp.EntryTypeFolder {
			n, err := r.deleteDir(c, sub)
			if err != nil {
				return removed, err
			}
			removed += n
			if err := c.RemoveDir(sub); err != nil && !isFTPNotFound(err) {
				return removed, err
			}
			continue
		}
		if err := c.Delete(sub); err != nil && !isFTPNotFound(err) {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

var _ storage.ResultStore = (*ResultStore)(nil)
var _ storage.PrefixDeleter = (*ResultStore)(nil)
