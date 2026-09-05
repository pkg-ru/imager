// Package httpapi реализует production HTTP-адаптер поверх
// application/generatev2 и domain/asset.
//
// Адаптер реализует URL grammar, GET/HEAD/OPTIONS,
// реальные HTTP-статусы с типизированным error envelope, ETag/conditional
// requests, security headers, deny-by-default CORS и not-found fallback.
package httpapi

import (
	"fmt"
	"net/http"
	"time"

	"github.com/pkg-ru/imager/domain/asset"
	"github.com/pkg-ru/imager/observability"
	"github.com/pkg-ru/imager/ports/storage"
)

// DefaultGenerateTimeout — таймаут генерации ассета по умолчанию.
const DefaultGenerateTimeout = 30 * time.Second

// NotFoundConfig — конфигурация not-found fallback.
type NotFoundConfig struct {
	// Pixel — true, если для not-found нужно отдавать прозрачный пиксель
	// в запрошенном формате (через PixelGenerator).
	Pixel bool
	// Image — путь к статическому файлу-картинке fallback (отдаётся с 404).
	Image string
	// Page — путь к статическому HTML-файлу fallback (отдаётся с 404).
	Page string
	// Redirect — URL для 301-редиректа при not-found.
	Redirect string
}

// SourceFallbackConfig — конфигурация fallback на исходный файл при ошибке
// ассета (несуществующий пресет, неканонический URL, запрещённая политика).
//
// Если включено и исходный файл существует, вместо пикселя/ошибки отдаётся
// исходный файл с его оригинальными Content-Type/именем/форматом.
type SourceFallbackConfig struct {
	// Enabled — включать ли source fallback. Дефолт false (текущее поведение).
	Enabled bool
	// Status — HTTP-статус ответа: http.StatusOK или http.StatusNotFound.
	// 0 → дефолт http.StatusNotFound.
	Status int
	// CacheControl — значение Cache-Control для fallback-ответа. Дефолт
	// "no-store".
	CacheControl string
}

// DefaultSourceFallbackStatus — HTTP-статус source fallback по умолчанию.
const DefaultSourceFallbackStatus = http.StatusNotFound

// DefaultSourceFallbackCacheControl — Cache-Control source fallback по умолчанию.
const DefaultSourceFallbackCacheControl = "no-store"

// ServeOriginalConfig — конфигурация отдачи исходников по «простым» URL вида
// /path/name.ext (ОТДЕЛЬНАЯ фича, не относящаяся к source-fallback).
//
// Канонический URL ассета имеет форму
// /{path}/{source_name}-{source_format}/{size|preset}.{ext}; URL без дефиса
// в имени исходника (например /test/my.png) не является валидным asset URL.
// При Enabled=true такие «простые» URL трактуются как прямой путь к исходнику
// в хранилище, и исходный файл отдаётся со статусом http.StatusOK.
type ServeOriginalConfig struct {
	// Enabled — включать ли отдачу исходников по «простым» URL. Дефолт false
	// (нулевое значение): «простые» URL обрабатываются как раньше
	// (ошибка 400 "missing source format").
	Enabled bool
	// CacheControl — значение Cache-Control для ответа. Дефолт "no-store".
	CacheControl string
}

// DefaultServeOriginalCacheControl — Cache-Control serve-original по умолчанию.
const DefaultServeOriginalCacheControl = "no-store"

// AssetErrorConfig — конфигурация observability ошибок asset URL.
type AssetErrorConfig struct {
	// Enabled — включать ли учёт ошибок asset URL (счётчики, top-paths,
	// структурные логи). Дефолт true.
	Enabled bool
	// LogLevel — уровень структурного лога ошибки: debug|info|warn|error.
	// Дефолт warn.
	LogLevel string
	// TopPaths — конфигурация bounded-реестра проблемных путей.
	TopPaths TopPathsConfig
}

// TopPathsConfig — конфигурация реестра проблемных путей (top-paths).
type TopPathsConfig struct {
	// Enabled — включать ли учёт top-paths. Дефолт false.
	Enabled bool
	// MaxEntries — максимальное число отслеживаемых путей (LRU). Дефолт 1024.
	MaxEntries int
	// ReportTop — число путей в отчёте (Top(n)). Дефолт 20.
	ReportTop int
	// KeyMode — режим ключа: "source" (путь исходника) или "hash"
	// (sha256 первые 16 байт hex). Дефолт "source".
	KeyMode string
}

// AdminConfig — конфигурация административных эндпоинтов.
//
// По умолчанию admin-эндпоинты ВЫКЛЮЧЕНЫ (enabled: false). При включении
// обязателен непустой bearer-токен, иначе старт завершается ошибкой
// (fail-fast) — endpoindы не могут работать с пустой авторизацией.
type AdminConfig struct {
	// Enabled — включать ли admin-эндпоинты (POST /admin/assets/generate,
	// DELETE /admin/assets/delete). Дефолт false.
	Enabled bool
	// Token — bearer-токен для авторизации через
	// Authorization: Bearer <token> (crypto/subtle.ConstantTimeCompare).
	// Обязателен при Enabled=true.
	Token string
	// Workers — число параллельных фоновых генераций. Дефолт 2.
	Workers int
	// QueueSize — ёмкость очереди задач. Переполнение → 503. Дефолт 64.
	QueueSize int
	// WaitTimeout — таймаут режима wait=true (ожидания завершения всех
	// ассетов до ответа). Дефолт 300s.
	WaitTimeout time.Duration
}

// DefaultAdminWorkers — число воркеров admin-очереди по умолчанию.
const DefaultAdminWorkers = 2

// DefaultAdminQueueSize — ёмкость admin-очереди по умолчанию.
const DefaultAdminQueueSize = 64

// DefaultAdminWaitTimeout — таймаут режима wait=true по умолчанию.
const DefaultAdminWaitTimeout = 300 * time.Second

// PolicyRecorder — узкий интерфейс сборщика наблюдений learning-mode.
// Реализуется app/learning.Recorder (и learning.Service). nil = выключено.
type PolicyRecorder interface {
	// Observe регистрирует наблюдение (неблокирующе). req == nil
	// игнорируется.
	Observe(req *asset.Request)
}

// Config — typed runtime конфигурация HTTP-адаптера.
type Config struct {
	// AllowedOrigins — CORS allowlist (deny-by-default). Пустой список
	// означает "никакие origin не разрешены". Wildcard "*" с credentials
	// запрещён.
	AllowedOrigins []string
	// AllowCredentials — разрешить Access-Control-Allow-Credentials.
	AllowCredentials bool

	// CacheControl — значение Cache-Control для canonical generated assets
	// (immutable). Пустое значение — заголовок не выставляется.
	CacheControl string
	// NotFoundCacheControl — Cache-Control для fallback-ответов.
	NotFoundCacheControl string

	// ReferrerPolicy — значение заголовка Referrer-Policy.
	ReferrerPolicy string
	// CSP — значение заголовка Content-Security-Policy (для fallback).
	CSP string

	// MaxURLLen — максимальная длина asset URL (0 → asset.MaxURLLen).
	MaxURLLen int

	// GenerateTimeout — таймаут генерации ассета (0 → DefaultGenerateTimeout).
	// Применяется как context deadline для Generate; превышение маппится в 504.
	GenerateTimeout time.Duration

	// NotFound — конфигурация not-found fallback.
	NotFound NotFoundConfig

	// SourceFallback — конфигурация fallback на исходный файл при ошибке
	// ассета (несуществующий пресет, неканонический URL, запрещённая
	// политика). Выключено по умолчанию.
	SourceFallback SourceFallbackConfig

	// ServeOriginal — конфигурация отдачи исходников по «простым» URL вида
	// /path/name.ext (отдельная фича, не относящаяся к source-fallback).
	// Выключено по умолчанию.
	ServeOriginal ServeOriginalConfig

	// Sources — хранилище исходников для source fallback. nil = фича
	// недоступна (fallback не выполняется).
	Sources storage.SourceStore

	// Pixel — генератор прозрачного пикселя для not-found.pixel (может быть
	// nil, тогда pixel-fallback недоступен).
	Pixel PixelGenerator

	// Logger — опциональный логгер.
	Logger Logger

	// Metrics — опциональные метрики (request/cache/processor/storage).
	// Если nil, используется NopMetrics.
	Metrics observability.Metrics

	// AssetErrors — конфигурация observability ошибок asset URL
	// (счётчики, top-paths, структурные логи).
	AssetErrors AssetErrorConfig

	// Admin — конфигурация административных эндпоинтов. По умолчанию
	// выключены; при включении регистрируются POST /admin/assets/generate и
	// DELETE /admin/assets/delete.
	Admin AdminConfig

	// MaxConcurrentRequests — максимальное число одновременно обрабатываемых
	// HTTP-запросов (admission control). 0 = без ограничения. При превышении
	// лимита возвращается HTTP 503 + Retry-After: 1.
	MaxConcurrentRequests int

	// PolicyRecorder — сборщик наблюдений learning-mode (nil = выключено).
	// Вызывается после успешного asset.Parse (best-effort, nil-safe).
	PolicyRecorder PolicyRecorder
}

// Logger — единый интерфейс логирования из observability.
type Logger = observability.Logger

// DefaultCacheControl — значение по умолчанию для canonical assets.
const DefaultCacheControl = "public, max-age=31536000, immutable"

// DefaultReferrerPolicy — значение по умолчанию для Referrer-Policy.
const DefaultReferrerPolicy = "no-referrer"

// DefaultNotFoundCacheControl — Cache-Control по умолчанию для fallback.
const DefaultNotFoundCacheControl = "no-store"

// DefaultMaxBodyBytes — жёсткий лимит тела запроса по умолчанию (4 KiB).
// Сервис не принимает тела запросов, поэтому лимит мал (защита от slow-body
// / DoS). Настраивается через server.max-body-bytes.
const DefaultMaxBodyBytes = 4 * 1024

// Validate проверяет корректность конфигурации.
func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("httpapi: config is nil")
	}
	for _, o := range c.AllowedOrigins {
		if o == "*" && c.AllowCredentials {
			return fmt.Errorf("httpapi: wildcard origin with credentials is forbidden")
		}
	}
	if c.MaxURLLen < 0 {
		return fmt.Errorf("httpapi: max url len must be non-negative")
	}
	// Source fallback: статус должен быть 0 (→404), 200 или 404.
	switch c.SourceFallback.Status {
	case 0, http.StatusOK, http.StatusNotFound:
	default:
		return fmt.Errorf("httpapi: source-fallback.status must be 200 or 404, got %d", c.SourceFallback.Status)
	}
	// Asset errors observability.
	if c.AssetErrors.Enabled {
		switch c.AssetErrors.LogLevel {
		case "", "debug", "info", "warn", "error":
		default:
			return fmt.Errorf("httpapi: asset-errors.log-level must be one of debug|info|warn|error, got %q", c.AssetErrors.LogLevel)
		}
		if c.AssetErrors.TopPaths.Enabled {
			switch c.AssetErrors.TopPaths.KeyMode {
			case "", "source", "hash":
			default:
				return fmt.Errorf("httpapi: asset-errors.top-paths.key-mode must be source or hash, got %q", c.AssetErrors.TopPaths.KeyMode)
			}
			if c.AssetErrors.TopPaths.MaxEntries < 0 {
				return fmt.Errorf("httpapi: asset-errors.top-paths.max-entries must be non-negative, got %d", c.AssetErrors.TopPaths.MaxEntries)
			}
			if c.AssetErrors.TopPaths.ReportTop < 0 {
				return fmt.Errorf("httpapi: asset-errors.top-paths.report-top must be non-negative, got %d", c.AssetErrors.TopPaths.ReportTop)
			}
		}
	}
	// Admin: при включении обязателен непустой токен (fail-fast). Значения
	// workers/queue-size/wait-timeout не могут быть отрицательными (0 = дефолт,
	// применяется в normalize).
	if c.Admin.Enabled && c.Admin.Token == "" {
		return fmt.Errorf("httpapi: admin.enabled requires non-empty admin.token")
	}
	if c.Admin.Workers < 0 {
		return fmt.Errorf("httpapi: admin.workers must be non-negative, got %d", c.Admin.Workers)
	}
	if c.Admin.QueueSize < 0 {
		return fmt.Errorf("httpapi: admin.queue-size must be non-negative, got %d", c.Admin.QueueSize)
	}
	if c.Admin.WaitTimeout < 0 {
		return fmt.Errorf("httpapi: admin.wait-timeout must be non-negative, got %v", c.Admin.WaitTimeout)
	}
	return nil
}

// Normalize применяет умолчания к конфигурации. Публичная обёртка над
// normalize для composition root (загрузка YAML-конфигурации).
func (c *Config) Normalize() {
	c.normalize()
}

// normalize применяет умолчания.
func (c *Config) normalize() {
	if c.CacheControl == "" {
		c.CacheControl = DefaultCacheControl
	}
	if c.ReferrerPolicy == "" {
		c.ReferrerPolicy = DefaultReferrerPolicy
	}
	if c.NotFoundCacheControl == "" {
		c.NotFoundCacheControl = DefaultNotFoundCacheControl
	}
	if c.MaxURLLen == 0 {
		c.MaxURLLen = 1024
	}
	if c.GenerateTimeout <= 0 {
		c.GenerateTimeout = DefaultGenerateTimeout
	}
	// Source fallback умолчания.
	if c.SourceFallback.Status == 0 {
		c.SourceFallback.Status = DefaultSourceFallbackStatus
	}
	if c.SourceFallback.CacheControl == "" {
		c.SourceFallback.CacheControl = DefaultSourceFallbackCacheControl
	}
	// Serve original умолчания.
	if c.ServeOriginal.CacheControl == "" {
		c.ServeOriginal.CacheControl = DefaultServeOriginalCacheControl
	}
	// Asset errors умолчания.
	if c.AssetErrors.LogLevel == "" {
		c.AssetErrors.LogLevel = "warn"
	}
	if c.AssetErrors.TopPaths.MaxEntries == 0 {
		c.AssetErrors.TopPaths.MaxEntries = 1024
	}
	if c.AssetErrors.TopPaths.ReportTop == 0 {
		c.AssetErrors.TopPaths.ReportTop = 20
	}
	if c.AssetErrors.TopPaths.KeyMode == "" {
		c.AssetErrors.TopPaths.KeyMode = "source"
	}
	// Admin умолчания.
	if c.Admin.Workers == 0 {
		c.Admin.Workers = DefaultAdminWorkers
	}
	if c.Admin.QueueSize == 0 {
		c.Admin.QueueSize = DefaultAdminQueueSize
	}
	if c.Admin.WaitTimeout <= 0 {
		c.Admin.WaitTimeout = DefaultAdminWaitTimeout
	}
}

// defaultTimeouts — значения таймаутов по умолчанию (используются runtime).
var defaultTimeouts = struct {
	ReadHeader time.Duration
	Read       time.Duration
	Write      time.Duration
	Idle       time.Duration
	Shutdown   time.Duration
}{
	ReadHeader: 5 * time.Second,
	Read:       15 * time.Second,
	Write:      30 * time.Second,
	Idle:       60 * time.Second,
	Shutdown:   15 * time.Second,
}
