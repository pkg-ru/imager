package httpapi

import (
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v2"

	"github.com/pkg-ru/imager/internal/adapters/processor/imagemagick"
	"github.com/pkg-ru/imager/internal/adapters/processor/libvips"
	"github.com/pkg-ru/imager/internal/config"
)

// DefaultBufferMaxBytes — общий бюджет памяти процесса для spillable-буферов
// по умолчанию (500 МБ).
const DefaultBufferMaxBytes int64 = 500 * 1024 * 1024

// DefaultMaxBodyBytes — жёсткий лимит тела запроса по умолчанию (4 KiB).
// Сервис не принимает тела запросов, поэтому лимит мал (защита от slow-body
// / DoS). Настраивается через server.max-body-bytes.
const DefaultMaxBodyBytes = 4 * 1024

// RuntimeConfig — единый typed runtime-конфиг всего приложения.
//
// Собирается из YAML-файлов (setting.yaml + setting-local.yaml) через
// ParseRuntimeConfig. Содержит все настройки приложения: pipeline
// (policy/processing), HTTP-адаптер, HTTP-сервер, хранилища source/result,
// ImageMagick processor и observability.
//
// П.4: для атомарной замены конфига (copy-on-write) используется
// RuntimeConfigHolder с atomic.Pointer. Поля RuntimeConfig не мутируются
// после публикации — новый конфиг создаётся целиком и публикуется атомарно.
type RuntimeConfig struct {
	// Pipeline — typed конфигурация конвейера (policy/processing).
	Pipeline *config.Config
	// HTTP — конфигурация HTTP-адаптера.
	HTTP Config
	// Server — конфигурация HTTP-сервера (адрес и таймауты).
	Server ServerConfig

	// SourceDir — каталог исходников (используется при FS source).
	SourceDir string
	// ResultDir — каталог результатов (используется при FS result).
	ResultDir string
	// Source — конфигурация source-хранилища.
	Source RemoteStorageConfig
	// Result — конфигурация result-хранилища.
	Result RemoteStorageConfig

	// ImageMagick — конфигурация ImageMagick processor (опциональный
	// fallback для APNG).
	ImageMagick ImageMagickConfig
	// Libvips — конфигурация libvips processor (primary движок; in-process
	// через govips). Если libvips не скомпилирован (без тэка "libvips"),
	// процессор недоступен и используется ImageMagick.
	Libvips LibvipsConfig
	// OutputLimit — application-level лимит размера выхода (0 = нет).
	OutputLimit int64
	// BufferMaxBytes — общий бюджет памяти процесса для spillable-буферов
	// (0 = без лимита). По умолчанию 500 МБ.
	BufferMaxBytes int64
	// LogLevel — уровень логов (debug/info/warn/error).
	LogLevel string
}

// RuntimeConfigHolder — потокобезопасный контейнер для RuntimeConfig с
// атомарной заменой (copy-on-write). П.4: позволяет публиковать новый
// конфиг без блокировок на чтение; читатели всегда видят согласованный
// immutable конфиг.
type RuntimeConfigHolder struct {
	ref atomic.Pointer[RuntimeConfig]
}

// NewRuntimeConfigHolder создаёт holder с начальным конфигом.
func NewRuntimeConfigHolder(rc *RuntimeConfig) *RuntimeConfigHolder {
	h := &RuntimeConfigHolder{}
	h.ref.Store(rc)
	return h
}

// Load возвращает текущий конфиг (атомарно).
func (h *RuntimeConfigHolder) Load() *RuntimeConfig {
	if h == nil {
		return nil
	}
	return h.ref.Load()
}

// Store атомарно публикует новый конфиг (copy-on-write).
func (h *RuntimeConfigHolder) Store(rc *RuntimeConfig) {
	h.ref.Store(rc)
}

// ServerConfig — конфигурация HTTP-сервера.
//
// Нулевое значение таймаута означает "использовать умолчание runtime"
// (см. defaultTimeouts).
type ServerConfig struct {
	// Addr — адрес прослушивания (TCP), например ":8080".
	Addr string
	// ReadHeaderTimeout — таймаут чтения заголовков.
	ReadHeaderTimeout time.Duration
	// ReadTimeout — таймаут чтения тела запроса.
	ReadTimeout time.Duration
	// WriteTimeout — таймаут записи ответа.
	WriteTimeout time.Duration
	// IdleTimeout — таймаут idle-соединений.
	IdleTimeout time.Duration
	// ShutdownTimeout — максимальное время ожидания активных запросов.
	ShutdownTimeout time.Duration
	// MaxHeaderBytes — максимальный размер заголовков запроса.
	MaxHeaderBytes int
	// MaxBodyBytes — максимальный размер тела запроса (0 = без лимита).
	// Сервис не принимает тела, поэтому по умолчанию жёсткий лимит 4 KiB.
	MaxBodyBytes int
}

// ImageMagickConfig — конфигурация ImageMagick processor.
type ImageMagickConfig struct {
	// Binary — путь к ImageMagick binary (по умолчанию "magick").
	Binary string
	// Limits — resource limits для subprocess.
	Limits imagemagick.Limits
	// Policy — настройки deny-by-default policy.xml.
	Policy imagemagick.PolicyConfig
}

// LibvipsConfig — конфигурация libvips processor (govips).
type LibvipsConfig struct {
	// Limits — resource limits обработчика libvips.
	Limits libvips.Limits
}

// RuntimeConfigFile — YAML-представление единого runtime-конфига.
//
// Поля Policy/Processing декодируются как yaml.MapSlice и пере-кодируются
// в typed config.Config (см. ParseRuntimeConfig).
type RuntimeConfigFile struct {
	// Version — версия конфигурации.
	Version string `yaml:"version"`
	// Server — конфигурация HTTP-сервера.
	Server ServerYAML `yaml:"server"`
	// HTTP — конфигурация HTTP-адаптера.
	HTTP HTTPYAML `yaml:"http"`
	// Policy — конфигурация политики (пробрасывается в config.Config).
	Policy yaml.MapSlice `yaml:"policy"`
	// Processing — конфигурация обработки.
	Processing yaml.MapSlice `yaml:"processing"`
	// Source — конфигурация source-хранилища.
	Source StorageYAML `yaml:"source"`
	// Result — конфигурация result-хранилища.
	Result StorageYAML `yaml:"result"`
	// ImageMagick — конфигурация ImageMagick processor.
	ImageMagick ImageMagickYAML `yaml:"imagemagick"`
	// Libvips — конфигурация libvips processor.
	Libvips LibvipsYAML `yaml:"libvips"`
	// Application — прикладные лимиты.
	Application ApplicationYAML `yaml:"application"`
	// Observability — логирование и метрики.
	Observability ObservabilityYAML `yaml:"observability"`
}

// ServerYAML — YAML-представление ServerConfig.
type ServerYAML struct {
	// Addr — адрес прослушивания (TCP), например ":8080".
	Addr string `yaml:"addr"`
	// ReadHeaderTimeout — таймаут чтения заголовков (duration, например "5s").
	ReadHeaderTimeout string `yaml:"read-header-timeout"`
	// ReadTimeout — таймаут чтения тела запроса.
	ReadTimeout string `yaml:"read-timeout"`
	// WriteTimeout — таймаут записи ответа.
	WriteTimeout string `yaml:"write-timeout"`
	// IdleTimeout — таймаут idle-соединений.
	IdleTimeout string `yaml:"idle-timeout"`
	// ShutdownTimeout — максимальное время ожидания активных запросов.
	ShutdownTimeout string `yaml:"shutdown-timeout"`
	// MaxHeaderBytes — максимальный размер заголовков запроса.
	MaxHeaderBytes int `yaml:"max-header-bytes"`
	// MaxBodyBytes — максимальный размер тела запроса (0 = без лимита).
	MaxBodyBytes int `yaml:"max-body-bytes"`
}

// StorageYAML — YAML-представление конфигурации хранилища (source или
// result). Секреты задаются отдельными полями и не попадают в URI/логи.
type StorageYAML struct {
	// Storage — тип хранилища (fs, s3, sftp, ftp, ftps, http). Пусто = fs.
	Storage string `yaml:"storage"`
	// Path — локальный каталог для FS-хранилища.
	Path string `yaml:"path"`
	// BaseURL — базовый адрес исходников для HTTP/HTTPS source.
	BaseURL string `yaml:"base-url"`
	// Bucket — bucket для S3.
	Bucket string `yaml:"bucket"`
	// Prefix — префикс ключей для S3.
	Prefix string `yaml:"prefix"`
	// Endpoint — endpoint S3 (для S3-совместимых хранилищ; пусто = AWS).
	Endpoint string `yaml:"endpoint"`
	// Region — регион S3.
	Region string `yaml:"region"`
	// AccessKey — access key S3.
	AccessKey string `yaml:"access-key"`
	// SecretKey — secret key S3.
	SecretKey string `yaml:"secret-key"`
	// Addr — адрес "host:port" для SFTP/FTP/FTPS.
	Addr string `yaml:"addr"`
	// User — пользователь для SFTP/FTP/FTPS.
	User string `yaml:"user"`
	// Password — пароль для SFTP/FTP/FTPS.
	Password string `yaml:"password"`
	// PrivateKeyFile — путь к файлу приватного ключа для SFTP.
	PrivateKeyFile string `yaml:"private-key-file"`
	// Root — корневой каталог для SFTP/FTP/FTPS.
	Root string `yaml:"root"`
	// TLS — true для FTPS.
	TLS bool `yaml:"tls"`
	// TLSVerify — проверять ли TLS-сертификат для FTPS (default: true).
	TLSVerify *bool `yaml:"tls-verify"`
	// HostKeyFingerprint — ожидаемый SHA-256 fingerprint SFTP host key.
	// Пример: "SHA256:abcdef...". Пусто = фундаментально небезопасно
	// (см. docs/PRODUCTION.md); рекомендуется задавать всегда.
	HostKeyFingerprint string `yaml:"host-key-fingerprint"`
	// SpoolDir — каталог временных spool.
	SpoolDir string `yaml:"spool-dir"`
	// SpoolMaxBytes — максимальный размер source spool (0 = без лимита).
	SpoolMaxBytes int64 `yaml:"spool-max-bytes"`
	// DialTimeout — таймаут соединения для SFTP/FTP/FTPS, HTTP и S3 (duration).
	DialTimeout string `yaml:"dial-timeout"`
	// ReadTimeout — таймаут чтения ответа для HTTP-подобных хранилищ
	// (S3, HTTP; duration).
	ReadTimeout string `yaml:"read-timeout"`
	// MaxAttempts — максимальное число попыток запроса для HTTP-подобных
	// хранилищ (S3, HTTP).
	MaxAttempts int `yaml:"max-attempts"`
	// MaxIdleConns — максимальное число idle-соединений в пуле
	// (S3, HTTP).
	MaxIdleConns int `yaml:"max-idle-conns"`
	// MaxConns — максимальное число одновременных соединений в пуле
	// (SFTP/FTP/FTPS; 0 = 2).
	MaxConns int `yaml:"max-conns"`
	// MaxIdleConnsPerHost — максимальное число idle-соединений на хост
	// (S3, HTTP).
	MaxIdleConnsPerHost int `yaml:"max-idle-conns-per-host"`
	// IdleConnTimeout — таймаут idle-соединений (S3, HTTP; duration).
	IdleConnTimeout string `yaml:"idle-conn-timeout"`
	// MetadataTTL — TTL кэша метаданных (S3; duration; 0 = кэш отключён).
	MetadataTTL string `yaml:"metadata-ttl"`
}

// ImageMagickYAML — YAML-представление ImageMagickConfig.
type ImageMagickYAML struct {
	// Binary — путь к ImageMagick binary (по умолчанию "magick").
	Binary string `yaml:"binary"`
	// Policy — настройки deny-by-default policy.xml.
	Policy PolicyYAML `yaml:"policy"`
	// Limits — resource limits для subprocess.
	Limits LimitsYAML `yaml:"limits"`
}

// LibvipsYAML — YAML-представление libvips.Limits.
type LibvipsYAML struct {
	// Limits — resource limits обработчика libvips.
	Limits LibvipsLimitsYAML `yaml:"limits"`
}

// LibvipsLimitsYAML — YAML-представление libvips.Limits.
type LibvipsLimitsYAML struct {
	// OutputBytes — лимит размера выходных данных (байт).
	OutputBytes int64 `yaml:"output-bytes"`
	// Timeout — context deadline на одну операцию (duration).
	Timeout string `yaml:"timeout"`
	// Concurrency — максимальное число одновременно выполняемых операций.
	Concurrency int `yaml:"concurrency"`
	// Threads — число потоков libvips (vips_concurrency_set).
	Threads int `yaml:"threads"`
	// MaxCacheMem — максимум памяти кэша libvips (байт).
	MaxCacheMem int `yaml:"max-cache-mem"`
	// MaxCacheFiles — максимум файлов кэша libvips.
	MaxCacheFiles int `yaml:"max-cache-files"`
	// MaxCacheSize — максимум операций в кэше libvips.
	MaxCacheSize int `yaml:"max-cache-size"`
}

// PolicyYAML — YAML-представление imagemagick.PolicyConfig.
type PolicyYAML struct {
	// Enabled — включать ли генерацию policy.xml (nil = true).
	Enabled *bool `yaml:"enabled"`
	// Dir — каталог, куда записывается policy.xml (пусто = временный).
	Dir string `yaml:"dir"`
	// MaxMemoryBytes, MaxMapBytes, MaxDiskBytes, MaxThreads, MaxTimeSeconds,
	// MaxWidth, MaxHeight, MaxPixels, MaxFrames — resource policies
	// (0 = не задавать).
	MaxMemoryBytes int64 `yaml:"max-memory-bytes"`
	MaxMapBytes    int64 `yaml:"max-map-bytes"`
	MaxDiskBytes   int64 `yaml:"max-disk-bytes"`
	MaxThreads     int   `yaml:"max-threads"`
	MaxTimeSeconds int   `yaml:"max-time-seconds"`
	MaxWidth       int64 `yaml:"max-width"`
	MaxHeight      int64 `yaml:"max-height"`
	MaxPixels      int64 `yaml:"max-pixels"`
	MaxFrames      int   `yaml:"max-frames"`
	// DisableNetwork — отключать network-capable delegates (nil = true).
	DisableNetwork *bool `yaml:"disable-network"`
	// DisabledCoders — дополнительные coders для запрета.
	DisabledCoders []string `yaml:"disabled-coders"`
	// DisabledDelegates — дополнительные delegates для запрета.
	DisabledDelegates []string `yaml:"disabled-delegates"`
}

// LimitsYAML describes the YAML representation of imagemagick.Limits.
type LimitsYAML struct {
	// MemoryBytes — лимит памяти в байтах.
	MemoryBytes int64 `yaml:"memory-bytes"`
	// MapBytes — лимит виртуальной памяти в байтах.
	MapBytes int64 `yaml:"map-bytes"`
	// DiskBytes — лимит дискового кэша в байтах.
	DiskBytes int64 `yaml:"disk-bytes"`
	// Threads — лимит потоков.
	Threads int `yaml:"threads"`
	// TimeSeconds — лимит времени CPU в секундах.
	TimeSeconds int `yaml:"time-seconds"`
	// Width — лимит ширины в пикселях (0 = не ограничено).
	Width int64 `yaml:"width"`
	// Height — лимит высоты в пикселях (0 = не ограничено).
	Height int64 `yaml:"height"`
	// Pixels — лимит площади (width*height) в пикселях.
	Pixels int64 `yaml:"pixels"`
	// Frames — лимит числа кадров.
	Frames int `yaml:"frames"`
	// OutputBytes — application-level лимит размера выхода в байтах.
	OutputBytes int64 `yaml:"output-bytes"`
	// Timeout — application-level context deadline для subprocess (duration).
	Timeout string `yaml:"timeout"`
	// Concurrency — максимальное число одновременно работающих subprocess
	// (0 = без ограничения).
	Concurrency int `yaml:"concurrency"`
	// WebPMethod — метод сжатия WebP (0-6; 0 = умолчание ImageMagick).
	WebPMethod int `yaml:"webp-method"`
	// PNGCompressionLevel — уровень сжатия PNG (0-9; 0 = умолчание).
	PNGCompressionLevel int `yaml:"png-compression-level"`
}

// ApplicationYAML — прикладные лимиты.
type ApplicationYAML struct {
	// OutputLimit — максимальный размер выходного файла (0 = без лимита).
	OutputLimit int64 `yaml:"output-limit"`
	// BufferMaxBytes — общий бюджет памяти процесса для spillable-буферов
	// (0 = без лимита). По умолчанию 500 МБ.
	BufferMaxBytes int64 `yaml:"buffer-max-bytes"`
}

// ObservabilityYAML — логирование и метрики.
type ObservabilityYAML struct {
	// LogLevel — уровень логов: debug, info, warn, error (по умолчанию info).
	LogLevel string `yaml:"log-level"`
}

// HTTPYAML — YAML-представление httpapi.Config.
type HTTPYAML struct {
	// AllowedOrigins — CORS allowlist.
	AllowedOrigins []string `yaml:"allowed-origins"`
	// AllowCredentials — разрешить credentials.
	AllowCredentials bool `yaml:"allow-credentials"`
	// CacheControl — Cache-Control для canonical assets.
	CacheControl string `yaml:"cache-control"`
	// NotFoundCacheControl — Cache-Control для fallback.
	NotFoundCacheControl string `yaml:"not-found-cache-control"`
	// ReferrerPolicy — Referrer-Policy.
	ReferrerPolicy string `yaml:"referrer-policy"`
	// CSP — Content-Security-Policy.
	CSP string `yaml:"csp"`
	// MaxURLLen — максимальная длина URL.
	MaxURLLen int `yaml:"max-url-len"`
	// GenerateTimeout — таймаут генерации ассета (duration, например "30s").
	GenerateTimeout string `yaml:"generate-timeout"`
	// NotFound — not-found fallback.
	NotFound NotFoundYAML `yaml:"not-found"`
	// MaxConcurrentRequests — максимальное число одновременно обрабатываемых
	// HTTP-запросов (0 = без ограничения).
	MaxConcurrentRequests int `yaml:"max-concurrent-requests"`
}

// NotFoundYAML — YAML-представление NotFoundConfig.
type NotFoundYAML struct {
	Pixel    bool   `yaml:"pixel"`
	Image    string `yaml:"image"`
	Page     string `yaml:"page"`
	Redirect string `yaml:"redirect"`
}

// ParseRuntimeConfig десериализует merged YAML-данные в единый typed
// RuntimeConfig. Применяет strict-декодирование (неизвестные поля
// отклоняются) и fail-fast валидацию.
func ParseRuntimeConfig(data []byte) (*RuntimeConfig, error) {
	var raw RuntimeConfigFile
	if err := yaml.UnmarshalStrict(data, &raw); err != nil {
		return nil, fmt.Errorf("httpapi: decode yaml: %w", err)
	}

	// Собираем config.Config из сырых секций.
	cfg := &config.Config{Version: raw.Version}
	if raw.Policy != nil {
		pol, err := yaml.Marshal(raw.Policy)
		if err != nil {
			return nil, fmt.Errorf("httpapi: re-encode policy: %w", err)
		}
		if err := yaml.Unmarshal(pol, &cfg.Policy); err != nil {
			return nil, fmt.Errorf("httpapi: decode policy: %w", err)
		}
	}
	if raw.Processing != nil {
		proc, err := yaml.Marshal(raw.Processing)
		if err != nil {
			return nil, fmt.Errorf("httpapi: re-encode processing: %w", err)
		}
		if err := yaml.Unmarshal(proc, &cfg.Processing); err != nil {
			return nil, fmt.Errorf("httpapi: decode processing: %w", err)
		}
	}
	cfg.Normalize() // пустая version → SupportedVersion (унифицировано с Validate)
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("httpapi: config: %w", err)
	}

	// HTTP-адаптер.
	httpCfg := Config{
		AllowedOrigins:        raw.HTTP.AllowedOrigins,
		AllowCredentials:      raw.HTTP.AllowCredentials,
		CacheControl:          raw.HTTP.CacheControl,
		NotFoundCacheControl:  raw.HTTP.NotFoundCacheControl,
		ReferrerPolicy:        raw.HTTP.ReferrerPolicy,
		CSP:                   raw.HTTP.CSP,
		MaxURLLen:             raw.HTTP.MaxURLLen,
		MaxConcurrentRequests: raw.HTTP.MaxConcurrentRequests,
		NotFound: NotFoundConfig{
			Pixel:    raw.HTTP.NotFound.Pixel,
			Image:    raw.HTTP.NotFound.Image,
			Page:     raw.HTTP.NotFound.Page,
			Redirect: raw.HTTP.NotFound.Redirect,
		},
	}
	if raw.HTTP.GenerateTimeout != "" {
		d, err := time.ParseDuration(raw.HTTP.GenerateTimeout)
		if err != nil {
			return nil, fmt.Errorf("httpapi: http.generate-timeout: %w", err)
		}
		if d < 0 {
			return nil, fmt.Errorf("httpapi: http.generate-timeout: negative duration %q", raw.HTTP.GenerateTimeout)
		}
		httpCfg.GenerateTimeout = d
	}
	if err := httpCfg.Validate(); err != nil {
		return nil, fmt.Errorf("httpapi: http: %w", err)
	}

	// Хранилища.
	source, err := raw.Source.toRemoteStorageConfig()
	if err != nil {
		return nil, fmt.Errorf("httpapi: source: %w", err)
	}
	result, err := raw.Result.toRemoteStorageConfig()
	if err != nil {
		return nil, fmt.Errorf("httpapi: result: %w", err)
	}
	if err := validateStorageConfig(source, "source"); err != nil {
		return nil, err
	}
	if err := validateStorageConfig(result, "result"); err != nil {
		return nil, err
	}

	// FS-каталоги.
	sourceDir := raw.Source.Path
	if sourceDir == "" {
		sourceDir = "./data/source"
	}
	resultDir := raw.Result.Path
	if resultDir == "" {
		resultDir = "./data/result"
	}

	// HTTP-сервер.
	server, err := raw.Server.build()
	if err != nil {
		return nil, fmt.Errorf("httpapi: server: %w", err)
	}
	if server.MaxBodyBytes < 0 {
		return nil, fmt.Errorf("httpapi: server.max-body-bytes: negative value %d", server.MaxBodyBytes)
	}
	if server.MaxBodyBytes == 0 {
		server.MaxBodyBytes = DefaultMaxBodyBytes
	}

	// ImageMagick.
	img, err := raw.ImageMagick.build()
	if err != nil {
		return nil, fmt.Errorf("httpapi: imagemagick: %w", err)
	}

	// Libvips.
	lv, err := raw.Libvips.build()
	if err != nil {
		return nil, fmt.Errorf("httpapi: libvips: %w", err)
	}

	// Прикладные лимиты.
	if raw.Application.OutputLimit < 0 {
		return nil, fmt.Errorf("httpapi: application.output-limit: negative value %d", raw.Application.OutputLimit)
	}
	if raw.Application.BufferMaxBytes < 0 {
		return nil, fmt.Errorf("httpapi: application.buffer-max-bytes: negative value %d", raw.Application.BufferMaxBytes)
	}
	bufferMaxBytes := raw.Application.BufferMaxBytes
	if bufferMaxBytes == 0 {
		bufferMaxBytes = DefaultBufferMaxBytes
	}

	logLevel := raw.Observability.LogLevel
	if logLevel == "" {
		logLevel = "info"
	}

	return &RuntimeConfig{
		Pipeline:       cfg,
		HTTP:           httpCfg,
		Server:         server,
		SourceDir:      sourceDir,
		ResultDir:      resultDir,
		Source:         source,
		Result:         result,
		ImageMagick:    img,
		Libvips:        lv,
		OutputLimit:    raw.Application.OutputLimit,
		BufferMaxBytes: bufferMaxBytes,
		LogLevel:       logLevel,
	}, nil
}

// toRemoteStorageConfig конвертирует YAML-конфигурацию в RemoteStorageConfig.
func (s StorageYAML) toRemoteStorageConfig() (RemoteStorageConfig, error) {
	if s.SpoolMaxBytes < 0 {
		return RemoteStorageConfig{}, fmt.Errorf("spool-max-bytes: negative value %d", s.SpoolMaxBytes)
	}
	// Секреты S3 могут задаваться через переменные окружения
	// (IMAGER_S3_ACCESS_KEY / IMAGER_S3_SECRET_KEY), чтобы не хранить их
	// открытым текстом в конфиге. Значение из YAML имеет приоритет.
	accessKey := s.AccessKey
	if accessKey == "" {
		accessKey = os.Getenv("IMAGER_S3_ACCESS_KEY")
	}
	secretKey := s.SecretKey
	if secretKey == "" {
		secretKey = os.Getenv("IMAGER_S3_SECRET_KEY")
	}
	cfg := RemoteStorageConfig{
		Kind:               StorageKind(s.Storage),
		Path:               s.Path,
		BaseURL:            s.BaseURL,
		Bucket:             s.Bucket,
		Prefix:             s.Prefix,
		Endpoint:           s.Endpoint,
		Region:             s.Region,
		AccessKey:          accessKey,
		SecretKey:          secretKey,
		Addr:               s.Addr,
		User:               s.User,
		Password:           s.Password,
		Root:               s.Root,
		TLS:                s.TLS,
		TLSVerify:          true,
		HostKeyFingerprint: s.HostKeyFingerprint,
		SpoolDir:           s.SpoolDir,
		SpoolMaxBytes:      s.SpoolMaxBytes,
		DialTimeout:        30 * time.Second,
	}
	if s.TLSVerify != nil {
		cfg.TLSVerify = *s.TLSVerify
	}
	if s.DialTimeout != "" {
		d, err := time.ParseDuration(s.DialTimeout)
		if err != nil {
			return RemoteStorageConfig{}, fmt.Errorf("dial-timeout: %w", err)
		}
		if d < 0 {
			return RemoteStorageConfig{}, fmt.Errorf("dial-timeout: negative duration %q", s.DialTimeout)
		}
		cfg.DialTimeout = d
	}
	// Общие настройки HTTP-подобных хранилищ (S3, HTTP): таймауты, retry,
	// пул соединений, кэш метаданных. Для SFTP/FTP/FTPS применяется только
	// dial-timeout (см. выше).
	if s.ReadTimeout != "" {
		d, err := time.ParseDuration(s.ReadTimeout)
		if err != nil {
			return RemoteStorageConfig{}, fmt.Errorf("read-timeout: %w", err)
		}
		if d < 0 {
			return RemoteStorageConfig{}, fmt.Errorf("read-timeout: negative duration %q", s.ReadTimeout)
		}
		cfg.ReadTimeout = d
	}
	if s.IdleConnTimeout != "" {
		d, err := time.ParseDuration(s.IdleConnTimeout)
		if err != nil {
			return RemoteStorageConfig{}, fmt.Errorf("idle-conn-timeout: %w", err)
		}
		if d < 0 {
			return RemoteStorageConfig{}, fmt.Errorf("idle-conn-timeout: negative duration %q", s.IdleConnTimeout)
		}
		cfg.IdleConnTimeout = d
	}
	if s.MetadataTTL != "" {
		d, err := time.ParseDuration(s.MetadataTTL)
		if err != nil {
			return RemoteStorageConfig{}, fmt.Errorf("metadata-ttl: %w", err)
		}
		if d < 0 {
			return RemoteStorageConfig{}, fmt.Errorf("metadata-ttl: negative duration %q", s.MetadataTTL)
		}
		cfg.MetadataTTL = d
	}
	if s.MaxAttempts < 0 {
		return RemoteStorageConfig{}, fmt.Errorf("max-attempts: negative value %d", s.MaxAttempts)
	}
	cfg.MaxAttempts = s.MaxAttempts
	if s.MaxIdleConns < 0 {
		return RemoteStorageConfig{}, fmt.Errorf("max-idle-conns: negative value %d", s.MaxIdleConns)
	}
	cfg.MaxIdleConns = s.MaxIdleConns
	if s.MaxConns < 0 {
		return RemoteStorageConfig{}, fmt.Errorf("max-conns: negative value %d", s.MaxConns)
	}
	cfg.MaxConns = s.MaxConns
	if s.MaxIdleConnsPerHost < 0 {
		return RemoteStorageConfig{}, fmt.Errorf("max-idle-conns-per-host: negative value %d", s.MaxIdleConnsPerHost)
	}
	cfg.MaxIdleConnsPerHost = s.MaxIdleConnsPerHost
	if s.PrivateKeyFile != "" {
		data, err := os.ReadFile(s.PrivateKeyFile)
		if err != nil {
			return RemoteStorageConfig{}, fmt.Errorf("private-key-file: %w", err)
		}
		cfg.PrivateKey = data
	}
	return cfg, nil
}

// build конвертирует YAML-конфигурацию сервера в ServerConfig.
// Пустые таймауты оставляются нулевыми — runtime применит умолчания.
func (s ServerYAML) build() (ServerConfig, error) {
	cfg := ServerConfig{
		Addr:           s.Addr,
		MaxHeaderBytes: s.MaxHeaderBytes,
	}
	parse := func(name, val string) error {
		if val == "" {
			return nil
		}
		d, err := time.ParseDuration(val)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if d < 0 {
			return fmt.Errorf("%s: negative duration %q", name, val)
		}
		switch name {
		case "read-header-timeout":
			cfg.ReadHeaderTimeout = d
		case "read-timeout":
			cfg.ReadTimeout = d
		case "write-timeout":
			cfg.WriteTimeout = d
		case "idle-timeout":
			cfg.IdleTimeout = d
		case "shutdown-timeout":
			cfg.ShutdownTimeout = d
		}
		return nil
	}
	for _, p := range []struct{ name, val string }{
		{"read-header-timeout", s.ReadHeaderTimeout},
		{"read-timeout", s.ReadTimeout},
		{"write-timeout", s.WriteTimeout},
		{"idle-timeout", s.IdleTimeout},
		{"shutdown-timeout", s.ShutdownTimeout},
	} {
		if err := parse(p.name, p.val); err != nil {
			return ServerConfig{}, err
		}
	}
	return cfg, nil
}

// build конвертирует YAML-конфигурацию ImageMagick в ImageMagickConfig.
func (i ImageMagickYAML) build() (ImageMagickConfig, error) {
	cfg := ImageMagickConfig{
		Binary: i.Binary,
		Limits: imagemagick.Limits{
			MemoryBytes:         i.Limits.MemoryBytes,
			MapBytes:            i.Limits.MapBytes,
			DiskBytes:           i.Limits.DiskBytes,
			Threads:             i.Limits.Threads,
			TimeSeconds:         i.Limits.TimeSeconds,
			Width:               i.Limits.Width,
			Height:              i.Limits.Height,
			Pixels:              i.Limits.Pixels,
			Frames:              i.Limits.Frames,
			OutputBytes:         i.Limits.OutputBytes,
			Concurrency:         i.Limits.Concurrency,
			WebPMethod:          i.Limits.WebPMethod,
			PNGCompressionLevel: i.Limits.PNGCompressionLevel,
		},
		Policy: imagemagick.PolicyConfig{
			Enabled:           true,
			DisableNetwork:    true,
			Dir:               i.Policy.Dir,
			MaxMemoryBytes:    i.Policy.MaxMemoryBytes,
			MaxMapBytes:       i.Policy.MaxMapBytes,
			MaxDiskBytes:      i.Policy.MaxDiskBytes,
			MaxThreads:        i.Policy.MaxThreads,
			MaxTimeSeconds:    i.Policy.MaxTimeSeconds,
			MaxWidth:          i.Policy.MaxWidth,
			MaxHeight:         i.Policy.MaxHeight,
			MaxPixels:         i.Policy.MaxPixels,
			MaxFrames:         i.Policy.MaxFrames,
			DisabledCoders:    i.Policy.DisabledCoders,
			DisabledDelegates: i.Policy.DisabledDelegates,
		},
	}
	if cfg.Binary == "" {
		cfg.Binary = "magick"
	}
	if i.Policy.Enabled != nil {
		cfg.Policy.Enabled = *i.Policy.Enabled
	}
	if i.Policy.DisableNetwork != nil {
		cfg.Policy.DisableNetwork = *i.Policy.DisableNetwork
	}
	if i.Limits.Timeout != "" {
		d, err := time.ParseDuration(i.Limits.Timeout)
		if err != nil {
			return ImageMagickConfig{}, fmt.Errorf("limits.timeout: %w", err)
		}
		cfg.Limits.Timeout = d
	}
	return cfg, nil
}

// build конвертирует YAML-конфигурацию libvips в LibvipsConfig.
func (l LibvipsYAML) build() (LibvipsConfig, error) {
	cfg := LibvipsConfig{
		Limits: libvips.Limits{
			OutputBytes:   l.Limits.OutputBytes,
			Concurrency:   l.Limits.Concurrency,
			Threads:       l.Limits.Threads,
			MaxCacheMem:   l.Limits.MaxCacheMem,
			MaxCacheFiles: l.Limits.MaxCacheFiles,
			MaxCacheSize:  l.Limits.MaxCacheSize,
		},
	}
	if l.Limits.Timeout != "" {
		d, err := time.ParseDuration(l.Limits.Timeout)
		if err != nil {
			return LibvipsConfig{}, fmt.Errorf("limits.timeout: %w", err)
		}
		if d < 0 {
			return LibvipsConfig{}, fmt.Errorf("limits.timeout: negative duration %q", l.Limits.Timeout)
		}
		cfg.Limits.Timeout = d
	}
	return cfg, nil
}

// validateStorageConfig проверяет обязательные поля хранилища в зависимости
// от типа. FS и пустой Kind валидации не требуют.
func validateStorageConfig(cfg RemoteStorageConfig, role string) error {
	if cfg.Kind == "" || cfg.Kind == StorageFS {
		return nil
	}
	switch cfg.Kind {
	case StorageS3:
		if cfg.Bucket == "" {
			return fmt.Errorf("httpapi: %s storage: s3 bucket is required", role)
		}
		if (cfg.AccessKey == "") != (cfg.SecretKey == "") {
			return fmt.Errorf("httpapi: %s storage: s3 access-key and secret-key must be set together", role)
		}
	case StorageSFTP:
		if cfg.Addr == "" || cfg.User == "" {
			return fmt.Errorf("httpapi: %s storage: sftp addr and user are required", role)
		}
		if cfg.HostKeyFingerprint == "" {
			return fmt.Errorf("httpapi: %s storage: sftp host-key-fingerprint is required (SHA256:...)", role)
		}
	case StorageFTP, StorageFTPS:
		if cfg.Addr == "" {
			return fmt.Errorf("httpapi: %s storage: %s addr is required", role, cfg.Kind)
		}
		if cfg.Kind == StorageFTPS && !cfg.TLSVerify {
			return fmt.Errorf("httpapi: %s storage: ftps tls-verify=false is forbidden; set tls-verify: true", role)
		}
	case StorageHTTP:
		if cfg.BaseURL == "" {
			return fmt.Errorf("httpapi: %s storage: http base-url is required", role)
		}
	}
	return nil
}
