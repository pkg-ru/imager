// Package httpapi реализует новый production HTTP-адаптер поверх
// application/generatev2 и domain/asset. Это изолированный adapter/runtime
// пакет: он НЕ подключает legacy handler, legacy server или legacy config.
//
// Адаптер реализует versioned URL grammar (/v1/...), GET/HEAD/OPTIONS,
// реальные HTTP-статусы с типизированным error envelope, ETag/conditional
// requests, security headers, deny-by-default CORS и not-found fallback.
package httpapi

import (
	"fmt"
	"time"

	"github.com/pkg-ru/imager/internal/observability"
)

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

	// NotFound — конфигурация not-found fallback.
	NotFound NotFoundConfig

	// Pixel — генератор прозрачного пикселя для not-found.pixel (может быть
	// nil, тогда pixel-fallback недоступен).
	Pixel PixelGenerator

	// Logger — опциональный логгер.
	Logger Logger

	// Metrics — опциональные метрики (request/cache/processor/storage).
	// Если nil, используется NopMetrics.
	Metrics observability.Metrics
}

// Logger — минимальный интерфейс логирования.
type Logger interface {
	Debugf(format string, args ...any)
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
	Errorf(format string, args ...any)
}

// nopLogger — заглушка при отсутствии логгера.
type nopLogger struct{}

func (nopLogger) Debugf(string, ...any) {}
func (nopLogger) Infof(string, ...any)  {}
func (nopLogger) Warnf(string, ...any)  {}
func (nopLogger) Errorf(string, ...any) {}

// DefaultCacheControl — значение по умолчанию для canonical assets.
const DefaultCacheControl = "public, max-age=31536000, immutable"

// DefaultReferrerPolicy — значение по умолчанию для Referrer-Policy.
const DefaultReferrerPolicy = "no-referrer"

// DefaultNotFoundCacheControl — Cache-Control по умолчанию для fallback.
const DefaultNotFoundCacheControl = "no-store"

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
	return nil
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
