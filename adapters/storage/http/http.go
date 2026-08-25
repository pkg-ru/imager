// Package http реализует storage.SourceStore поверх HTTP/HTTPS.
//
// Адаптер является source-only: он предоставляет Lookup (HEAD) и Open (GET)
// и не реализует ResultStore. Redirects запрещены: любой ответ 3xx
// трактуется как недоступность хранилища (object.ErrUnavailable).
//
// Базовый адрес задаётся в Options.BaseURL (например
// "https://addr.site/path_to_image/"). Ключ объекта безопасно
// канонизируется через remote.CanonicalKey и добавляется к базовому пути:
//
//	base: https://addr.site/path_to_image/
//	key:  foo/bar.jpg
//	url:  https://addr.site/path_to_image/foo/bar.jpg
//
// Секреты (query-параметры, токены) в BaseURL не поддерживаются: URL с
// query/fragment отклоняется при валидации.
package http

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/pkg-ru/imager/internal/adapters/storage/remote"
	"github.com/pkg-ru/imager/internal/application/ports/storage"
	"github.com/pkg-ru/imager/internal/domain/object"
)

// Options — параметры HTTP/HTTPS source-адаптера.
type Options struct {
	// BaseURL — базовый адрес исходников, например
	// "https://addr.site/path_to_image/". Обязателен.
	BaseURL string
	// SpoolDir — каталог временных spool (пусто = os.TempDir).
	SpoolDir string
	// SpoolMaxBytes — максимальный размер скачиваемого объекта
	// (0 = без лимита). Превышение → object.ErrQuota.
	SpoolMaxBytes int64
	// Pool — общий бюджет памяти процесса для spillable-буферов.
	// Если nil, буферы работают только через память без spill.
	Pool *remote.BufferPool
	// DialTimeout — таймаут установки TCP-соединения (0 = 30s).
	DialTimeout time.Duration
	// ReadTimeout — таймаут чтения ответа (0 = 60s).
	ReadTimeout time.Duration
	// MaxAttempts — максимальное число попыток запроса (0 = 3).
	MaxAttempts int
	// MaxIdleConns — максимальное число idle-соединений в пуле (0 = 100).
	MaxIdleConns int
	// MaxIdleConnsPerHost — максимальное число idle-соединений на хост
	// (0 = 10).
	MaxIdleConnsPerHost int
	// IdleConnTimeout — таймаут idle-соединений (0 = 90s).
	IdleConnTimeout time.Duration
	// Client — опциональный HTTP-клиент (для тестов). Если задан,
	// все настройки пула/таймаутов игнорируются.
	Client *http.Client
}

// Умолчания для HTTP-клиента (согласованы с S3-клиентом).
const (
	defaultDialTimeout      = 30 * time.Second
	defaultReadTimeout      = 60 * time.Second
	defaultMaxAttempts      = 3
	defaultMaxIdleConns     = 100
	defaultMaxIdleConnsHost = 10
	defaultIdleConnTimeout  = 90 * time.Second
	defaultKeepAlive        = 30 * time.Second
	defaultTLSHandshake     = 10 * time.Second
	defaultExpectContinue   = 1 * time.Second
	defaultMaxConnsPerHost  = 2048
)

func (o Options) validate() error {
	if o.BaseURL == "" {
		return fmt.Errorf("http: empty base url")
	}
	u, err := url.Parse(o.BaseURL)
	if err != nil {
		return fmt.Errorf("http: invalid base url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("http: unsupported scheme %q (want http or https)", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("http: base url must include host")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("http: base url must not contain query or fragment")
	}
	return nil
}

// SourceStore — HTTP/HTTPS-реализация storage.SourceStore (read-only).
type SourceStore struct {
	base        *url.URL
	baseDir     string // нормализованный базовый путь с завершающим "/"
	opts        Options
	client      *http.Client
	maxAttempts int
}

// NewSourceStore создаёт HTTP/HTTPS SourceStore.
func NewSourceStore(opts Options) (*SourceStore, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	base, err := url.Parse(opts.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("http: invalid base url: %w", err)
	}
	baseDir := strings.TrimSuffix(base.Path, "/")
	if baseDir == "" {
		baseDir = "/"
	} else {
		baseDir += "/"
	}
	maxAttempts := opts.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultMaxAttempts
	}
	client := opts.Client
	if client == nil {
		dialTimeout := opts.DialTimeout
		if dialTimeout <= 0 {
			dialTimeout = defaultDialTimeout
		}
		readTimeout := opts.ReadTimeout
		if readTimeout <= 0 {
			readTimeout = defaultReadTimeout
		}
		maxIdleConns := opts.MaxIdleConns
		if maxIdleConns <= 0 {
			maxIdleConns = defaultMaxIdleConns
		}
		maxIdleConnsPerHost := opts.MaxIdleConnsPerHost
		if maxIdleConnsPerHost <= 0 {
			maxIdleConnsPerHost = defaultMaxIdleConnsHost
		}
		idleConnTimeout := opts.IdleConnTimeout
		if idleConnTimeout <= 0 {
			idleConnTimeout = defaultIdleConnTimeout
		}

		dialer := &net.Dialer{
			Timeout:   dialTimeout,
			KeepAlive: defaultKeepAlive,
			DualStack: true,
		}
		transport := &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           dialer.DialContext,
			MaxIdleConns:          maxIdleConns,
			MaxIdleConnsPerHost:   maxIdleConnsPerHost,
			MaxConnsPerHost:       defaultMaxConnsPerHost,
			IdleConnTimeout:       idleConnTimeout,
			TLSHandshakeTimeout:   defaultTLSHandshake,
			ExpectContinueTimeout: defaultExpectContinue,
			ForceAttemptHTTP2:     true,
		}
		client = &http.Client{
			Transport: transport,
			Timeout:   readTimeout,
			// Redirects запрещены: возвращаем сам ответ 3xx, чтобы
			// обработать его как недоступность.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	return &SourceStore{base: base, baseDir: baseDir, opts: opts, client: client, maxAttempts: maxAttempts}, nil
}

// do выполняет HTTP-запрос с ограниченным числом попыток (retry) при
// сетевых ошибках. Ответы с HTTP-статусом не ретраятся — они обрабатываются
// вызывающим через checkStatus.
func (s *SourceStore) do(ctx context.Context, method, u string) (*http.Response, error) {
	attempts := s.maxAttempts
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		req, err := http.NewRequestWithContext(ctx, method, u, nil)
		if err != nil {
			return nil, err
		}
		resp, err := s.client.Do(req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		// Не ретраим при отмене контекста.
		if ctx.Err() != nil {
			break
		}
		// Экспоненциальный backoff с джиттером: при синхронных сбоях
		// множество клиентов бьют в источник одновременно (thundering herd),
		// поэтому к экспоненциальной задержке добавляем случайный разброс.
		backoff := retryBackoff(i)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
	}
	return nil, lastErr
}

// retryBackoff возвращает экспоненциальную задержку с джиттером для попытки i
// (i = 0 — первая неудача): base * (1 << i) + rand.Int63n(jitter).
func retryBackoff(i int) time.Duration {
	const base = 100 * time.Millisecond
	const jitter = 100 * time.Millisecond
	exp := time.Duration(1) << min(i, 6) // ограничиваем рост, чтобы не уйти в долгий сон
	return base*exp + time.Duration(rand.Int63n(int64(jitter)))
}

// Lookup возвращает метаданные исходного объекта через HEAD.
func (s *SourceStore) Lookup(ctx context.Context, key object.ObjectKey) (object.ObjectMetadata, error) {
	u, err := s.url(key)
	if err != nil {
		return object.ObjectMetadata{}, err
	}
	resp, err := s.do(ctx, http.MethodHead, u)
	if err != nil {
		return object.ObjectMetadata{}, remote.MapError("http lookup", err)
	}
	defer resp.Body.Close()

	if err := s.checkStatus(key, resp.StatusCode); err != nil {
		return object.ObjectMetadata{}, err
	}
	return s.metadata(key, resp), nil
}

// Open открывает поток исходного объекта через GET и spillable буфер.
func (s *SourceStore) Open(ctx context.Context, key object.ObjectKey) (object.Artifact, error) {
	u, err := s.url(key)
	if err != nil {
		return nil, err
	}
	resp, err := s.do(ctx, http.MethodGet, u)
	if err != nil {
		return nil, remote.MapError("http open", err)
	}
	defer resp.Body.Close()

	if err := s.checkStatus(key, resp.StatusCode); err != nil {
		return nil, err
	}
	if s.opts.SpoolMaxBytes > 0 && resp.ContentLength > s.opts.SpoolMaxBytes {
		return nil, fmt.Errorf("http: source %q exceeds spool limit: %w", key, object.ErrQuota)
	}
	buf, err := remote.NewBuffer(remote.BufferOptions{
		Pool:     s.opts.Pool,
		Dir:      s.opts.SpoolDir,
		MaxBytes: s.opts.SpoolMaxBytes,
	})
	if err != nil {
		return nil, remote.MapError("http buffer", err)
	}
	if _, err := buf.WriteFrom(resp.Body, s.opts.SpoolMaxBytes); err != nil {
		_ = buf.Close()
		if errors.Is(err, remote.ErrBufferLimit) {
			return nil, fmt.Errorf("http: source %q exceeds spool limit: %w", key, object.ErrQuota)
		}
		return nil, remote.MapError("http buffer", err)
	}
	if _, err := buf.Seek(0, io.SeekStart); err != nil {
		_ = buf.Close()
		return nil, remote.MapError("http buffer seek", err)
	}
	meta := s.metadata(key, resp)
	meta.Size = buf.Size()
	return remote.NewBufferArtifact(buf, meta), nil
}

var _ storage.SourceStore = (*SourceStore)(nil)

// url строит полный URL объекта из базового URL и канонического ключа.
func (s *SourceStore) url(key object.ObjectKey) (string, error) {
	k, err := remote.CanonicalKey(key)
	if err != nil {
		return "", remote.Unsafe(key, err)
	}
	u, err := url.JoinPath(s.base.String(), k)
	if err != nil {
		return "", remote.MapError("http url", err)
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return "", remote.MapError("http url", err)
	}
	// Defense in depth: итоговый путь обязан оставаться внутри базового пути.
	if !strings.HasPrefix(parsed.Path, s.baseDir) {
		return "", remote.Unsafe(key, nil)
	}
	return u, nil
}

// checkStatus маппит HTTP-статус в типизированную ошибку.
func (s *SourceStore) checkStatus(key object.ObjectKey, code int) error {
	switch {
	case code >= 200 && code < 300:
		return nil
	case code == http.StatusNotFound || code == http.StatusGone:
		return remote.NotFound(key)
	case code >= 300 && code < 400:
		return fmt.Errorf("http: redirect %d: %w", code, object.ErrUnavailable)
	default:
		return fmt.Errorf("http: unexpected status %d: %w", code, object.ErrUnavailable)
	}
}

// metadata заполняет object.ObjectMetadata из заголовков ответа.
func (s *SourceStore) metadata(key object.ObjectKey, resp *http.Response) object.ObjectMetadata {
	meta := object.ObjectMetadata{Key: key}
	if resp.ContentLength >= 0 {
		meta.Size = resp.ContentLength
	}
	if v := resp.Header.Get("Last-Modified"); v != "" {
		if t, err := http.ParseTime(v); err == nil {
			meta.ModTime = t
		}
	}
	if v := resp.Header.Get("Content-Type"); v != "" {
		meta.ContentType = v
	}
	if v := resp.Header.Get("ETag"); v != "" {
		meta.ETag = v
	}
	return meta
}
