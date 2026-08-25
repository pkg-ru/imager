package httpapi

import (
	"fmt"
	"os"
	"time"

	"github.com/pkg-ru/dynamic"
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
type RuntimeConfig struct {
	// Pipeline — typed конфигурация конвейера (policy/processing).
	Pipeline *config.Config
	// HTTP — конфигурация HTTP-адаптера.
	HTTP Config
	// Server — конфигурация HTTP-сервера (адрес и таймауты).
	Server ServerConfig

	// Admin — конфигурация административных эндпоинтов. По умолчанию
	// выключены (enabled: false). При включении регистрируются
	// POST /admin/assets/generate и DELETE /admin/assets/delete.
	Admin AdminConfig

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
	// Detection — конфигурация детектора лиц/объектов (face-crop/object-crop).
	// Пустые пути к моделям = face-crop/object-crop отключены (запрос с
	// такими операциями вернёт понятную ошибку).
	Detection DetectionConfig
	// MetadataEnabled — включить sidecar-кэш моделей и largest_ai_asset
	// Дефолт: true. false = поведение
	// идентично текущему (кэш моделей отключён).
	MetadataEnabled bool
	// MetadataDir — КОРЕНЬ sidecar-хранилища metаданных (metadata.dir):
	// явный ЛОКАЛЬНЫЙ путь файловой системы, НЕЗАВИСИМЫЙ от хранилищ
	// source/result (fs/S3/SFTP/FTP/HTTP). Метаданные ВСЕГДА хранятся
	// локально по этому пути. Пусто = дефолт `<эффективный локальный
	// result-каталог>/.meta`.
	MetadataDir string
	// OutputLimit — application-level лимит размера выхода (0 = нет).
	OutputLimit int64
	// BufferMaxBytes — общий бюджет памяти процесса для spillable-буферов
	// (0 = без лимита). По умолчанию 500 МБ.
	BufferMaxBytes int64
	// LogLevel — уровень логов (debug/info/warn/error).
	LogLevel string
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

// DetectionConfig — конфигурация детектора лиц/объектов для операций
// face-crop ("fc") и object-crop ("oc") на libvips.
//
// Пустой путь модели (FaceModel/ObjectModel) = соответствующий детектор
// отключён: запрос с такой операцией вернёт понятную ошибку от процессора.
// Секция не имеет флага enabled — «включение» задаётся непустыми путями.
type DetectionConfig struct {
	// FaceModel — путь к ONNX-модели YuNet для детекции лиц.
	// Пусто = face-crop недоступен.
	FaceModel string
	// ObjectModel — путь к ONNX-модели (SSD/YOLO-подобной) для детекции
	// объектов. Пусто = object-crop недоступен.
	ObjectModel string
	// ConfidenceThreshold — порог уверенности в интервале [0,1]. Боксы
	// с Confidence ниже порога отбрасываются (до NMS). Дефолт: 0.5.
	ConfidenceThreshold float64
	// MaxObjects — максимальное число объектов после NMS (первые N самых
	// уверенных). Должен быть > 0. Дефолт: 5.
	MaxObjects int
	// Margin — отступ к найденной области как доля от её размера в
	// интервале [0,1]. Применяется равномерно по осям (половина с каждой
	// стороны). 0 = кроп строго по bounding box. Дефолт: 0.1 (10%).
	Margin float64
}

// DetectionYAML — YAML-представление DetectionConfig.
//
// Пороговые значения — nullable, чтобы отличать «не задано» (Set=false →
// дефолт) от явного значения (включая 0), которое валидируется.
type DetectionYAML struct {
	// FaceModel — путь к ONNX-модели YuNet для детекции лиц.
	FaceModel dynamic.String `yaml:"face-model"`
	// ObjectModel — путь к ONNX-модели (SSD/YOLO-подобной) для детекции
	// объектов.
	ObjectModel dynamic.String `yaml:"object-model"`
	// ConfidenceThreshold — порог уверенности в интервале [0,1] (nil = 0.5).
	ConfidenceThreshold dynamic.Nullable[dynamic.Float64] `yaml:"confidence-threshold"`
	// MaxObjects — максимальное число объектов после NMS (первые N самых
	// уверенных, должет быть > 0; nil = 5).
	MaxObjects dynamic.Nullable[dynamic.Int64] `yaml:"max-objects"`
	// Margin — отступ к найденной области как доля от её размера в
	// интервале [0,1] (nil = 0.1).
	Margin dynamic.Nullable[dynamic.Float64] `yaml:"margin"`
}

// RuntimeConfigFile — YAML-представление единого runtime-конфига.
//
// Поля Policy/Processing декодируются как yaml.MapSlice и пере-кодируются
// в typed config.Config (см. ParseRuntimeConfig).
type RuntimeConfigFile struct {
	// Version — версия конфигурации.
	Version dynamic.String `yaml:"version"`
	// Watermarks — именованные декларации ватермарок (пробрасываются в
	// config.Config; ссылки из пресетов/path-policies разрешаются при
	// компиляции).
	Watermarks []config.WatermarkConfig `yaml:"watermarks"`
	// Server — конфигурация HTTP-сервера.
	Server ServerYAML `yaml:"server"`
	// Admin — конфигурация административных эндпоинтов.
	Admin AdminYAML `yaml:"admin"`
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
	// Detection — конфигурация детектора лиц/объектов (face-crop/object-crop).
	Detection DetectionYAML `yaml:"detection"`
	// Application — прикладные лимиты.
	Application ApplicationYAML `yaml:"application"`
	// Observability — логирование и метрики.
	Observability ObservabilityYAML `yaml:"observability"`
	// Metadata — sidecar-кэш моделей и largest_ai_asset.
	Metadata MetadataYAML `yaml:"metadata"`
}

// ServerYAML — YAML-представление ServerConfig.
type ServerYAML struct {
	// Addr — адрес прослушивания (TCP), например ":8080".
	Addr dynamic.String `yaml:"addr"`
	// ReadHeaderTimeout — таймаут чтения заголовков (duration, например "5s").
	ReadHeaderTimeout dynamic.String `yaml:"read-header-timeout"`
	// ReadTimeout — таймаут чтения тела запроса.
	ReadTimeout dynamic.String `yaml:"read-timeout"`
	// WriteTimeout — таймаут записи ответа.
	WriteTimeout dynamic.String `yaml:"write-timeout"`
	// IdleTimeout — таймаут idle-соединений.
	IdleTimeout dynamic.String `yaml:"idle-timeout"`
	// ShutdownTimeout — максимальное время ожидания активных запросов.
	ShutdownTimeout dynamic.String `yaml:"shutdown-timeout"`
	// MaxHeaderBytes — максимальный размер заголовков запроса.
	MaxHeaderBytes dynamic.Int64 `yaml:"max-header-bytes"`
	// MaxBodyBytes — максимальный размер тела запроса (0 = без лимита).
	MaxBodyBytes dynamic.Int64 `yaml:"max-body-bytes"`
}

// StorageYAML — YAML-представление конфигурации хранилища (source или
// result). Секреты задаются отдельными полями и не попадают в URI/логи.
type StorageYAML struct {
	// Storage — тип хранилища (fs, s3, sftp, ftp, ftps, http). Пусто = fs.
	Storage dynamic.String `yaml:"storage"`
	// Path — локальный каталог для FS-хранилища.
	Path dynamic.String `yaml:"path"`
	// BaseURL — базовый адрес исходников для HTTP/HTTPS source.
	BaseURL dynamic.String `yaml:"base-url"`
	// Bucket — bucket для S3.
	Bucket dynamic.String `yaml:"bucket"`
	// Prefix — префикс ключей для S3.
	Prefix dynamic.String `yaml:"prefix"`
	// Endpoint — endpoint S3 (для S3-совместимых хранилищ; пусто = AWS).
	Endpoint dynamic.String `yaml:"endpoint"`
	// Region — регион S3.
	Region dynamic.String `yaml:"region"`
	// AccessKey — access key S3.
	AccessKey dynamic.String `yaml:"access-key"`
	// SecretKey — secret key S3.
	SecretKey dynamic.String `yaml:"secret-key"`
	// Addr — адрес "host:port" для SFTP/FTP/FTPS.
	Addr dynamic.String `yaml:"addr"`
	// User — пользователь для SFTP/FTP/FTPS.
	User dynamic.String `yaml:"user"`
	// Password — пароль для SFTP/FTP/FTPS.
	Password dynamic.String `yaml:"password"`
	// PrivateKeyFile — путь к файлу приватного ключа для SFTP.
	PrivateKeyFile dynamic.String `yaml:"private-key-file"`
	// Root — корневой каталог для SFTP/FTP/FTPS.
	Root dynamic.String `yaml:"root"`
	// TLS — true для FTPS.
	TLS dynamic.Bool `yaml:"tls"`
	// TLSVerify — проверять ли TLS-сертификат для FTPS (default: true).
	TLSVerify dynamic.Nullable[dynamic.Bool] `yaml:"tls-verify"`
	// HostKeyFingerprint — ожидаемый SHA-256 fingerprint SFTP host key.
	// Пример: "SHA256:abcdef...". Пусто = фундаментально небезопасно
	// (см. docs/PRODUCTION.md); рекомендуется задавать всегда.
	HostKeyFingerprint dynamic.String `yaml:"host-key-fingerprint"`
	// SpoolDir — каталог временных spool.
	SpoolDir dynamic.String `yaml:"spool-dir"`
	// SpoolMaxBytes — максимальный размер source spool (0 = без лимита).
	SpoolMaxBytes dynamic.Int64 `yaml:"spool-max-bytes"`
	// DialTimeout — таймаут соединения для SFTP/FTP/FTPS, HTTP и S3 (duration).
	DialTimeout dynamic.String `yaml:"dial-timeout"`
	// ReadTimeout — таймаут чтения ответа для HTTP-подобных хранилищ
	// (S3, HTTP; duration).
	ReadTimeout dynamic.String `yaml:"read-timeout"`
	// MaxAttempts — максимальное число попыток запроса для HTTP-подобных
	// хранилищ (S3, HTTP).
	MaxAttempts dynamic.Int64 `yaml:"max-attempts"`
	// MaxIdleConns — максимальное число idle-соединений в пуле
	// (S3, HTTP).
	MaxIdleConns dynamic.Int64 `yaml:"max-idle-conns"`
	// MaxConns — максимальное число одновременных соединений в пуле
	// (SFTP/FTP/FTPS; 0 = 2).
	MaxConns dynamic.Int64 `yaml:"max-conns"`
	// MaxIdleConnsPerHost — максимальное число idle-соединений на хост
	// (S3, HTTP).
	MaxIdleConnsPerHost dynamic.Int64 `yaml:"max-idle-conns-per-host"`
	// IdleConnTimeout — таймаут idle-соединений (S3, HTTP; duration).
	IdleConnTimeout dynamic.String `yaml:"idle-conn-timeout"`
	// MetadataTTL — TTL кэша метаданных (S3; duration; 0 = кэш отключён).
	MetadataTTL dynamic.String `yaml:"metadata-ttl"`
}

// ImageMagickYAML — YAML-представление ImageMagickConfig.
type ImageMagickYAML struct {
	// Binary — путь к ImageMagick binary (по умолчанию "magick").
	Binary dynamic.String `yaml:"binary"`
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
	OutputBytes dynamic.Int64 `yaml:"output-bytes"`
	// Timeout — context deadline на одну операцию (duration).
	Timeout dynamic.String `yaml:"timeout"`
	// Concurrency — максимальное число одновременно выполняемых операций.
	Concurrency dynamic.Int64 `yaml:"concurrency"`
	// Threads — число потоков libvips (vips_concurrency_set).
	Threads dynamic.Int64 `yaml:"threads"`
	// MaxCacheMem — максимум памяти кэша libvips (байт).
	MaxCacheMem dynamic.Int64 `yaml:"max-cache-mem"`
	// MaxCacheFiles — максимум файлов кэша libvips.
	MaxCacheFiles dynamic.Int64 `yaml:"max-cache-files"`
	// MaxCacheSize — максимум операций в кэше libvips.
	MaxCacheSize dynamic.Int64 `yaml:"max-cache-size"`
}

// PolicyYAML — YAML-представление imagemagick.PolicyConfig.
type PolicyYAML struct {
	// Enabled — включать ли генерацию policy.xml (nil = true).
	Enabled dynamic.Nullable[dynamic.Bool] `yaml:"enabled"`
	// Dir — каталог, куда записывается policy.xml (пусто = временный).
	Dir dynamic.String `yaml:"dir"`
	// MaxMemoryBytes, MaxMapBytes, MaxDiskBytes, MaxThreads, MaxTimeSeconds,
	// MaxWidth, MaxHeight, MaxPixels, MaxFrames — resource policies
	// (0 = не задавать).
	MaxMemoryBytes dynamic.Int64 `yaml:"max-memory-bytes"`
	MaxMapBytes    dynamic.Int64 `yaml:"max-map-bytes"`
	MaxDiskBytes   dynamic.Int64 `yaml:"max-disk-bytes"`
	MaxThreads     dynamic.Int64 `yaml:"max-threads"`
	MaxTimeSeconds dynamic.Int64 `yaml:"max-time-seconds"`
	MaxWidth       dynamic.Int64 `yaml:"max-width"`
	MaxHeight      dynamic.Int64 `yaml:"max-height"`
	MaxPixels      dynamic.Int64 `yaml:"max-pixels"`
	MaxFrames      dynamic.Int64 `yaml:"max-frames"`
	// DisableNetwork — отключать network-capable delegates (nil = true).
	DisableNetwork dynamic.Nullable[dynamic.Bool] `yaml:"disable-network"`
	// DisabledCoders — дополнительные coders для запрета.
	DisabledCoders []dynamic.String `yaml:"disabled-coders"`
	// DisabledDelegates — дополнительные delegates для запрета.
	DisabledDelegates []dynamic.String `yaml:"disabled-delegates"`
}

// LimitsYAML describes the YAML representation of imagemagick.Limits.
type LimitsYAML struct {
	// MemoryBytes — лимит памяти в байтах.
	MemoryBytes dynamic.Int64 `yaml:"memory-bytes"`
	// MapBytes — лимит виртуальной памяти в байтах.
	MapBytes dynamic.Int64 `yaml:"map-bytes"`
	// DiskBytes — лимит дискового кэша в байтах.
	DiskBytes dynamic.Int64 `yaml:"disk-bytes"`
	// Threads — лимит потоков.
	Threads dynamic.Int64 `yaml:"threads"`
	// TimeSeconds — лимит времени CPU в секундах.
	TimeSeconds dynamic.Int64 `yaml:"time-seconds"`
	// Width — лимит ширины в пикселях (0 = не ограничено).
	Width dynamic.Int64 `yaml:"width"`
	// Height — лимит высоты в пикселях (0 = не ограничено).
	Height dynamic.Int64 `yaml:"height"`
	// Pixels — лимит площади (width*height) в пикселях.
	Pixels dynamic.Int64 `yaml:"pixels"`
	// Frames — лимит числа кадров.
	Frames dynamic.Int64 `yaml:"frames"`
	// OutputBytes — application-level лимит размера выхода в байтах.
	OutputBytes dynamic.Int64 `yaml:"output-bytes"`
	// Timeout — application-level context deadline для subprocess (duration).
	Timeout dynamic.String `yaml:"timeout"`
	// Concurrency — максимальное число одновременно работающих subprocess
	// (0 = без ограничения).
	Concurrency dynamic.Int64 `yaml:"concurrency"`
	// WebPMethod — метод сжатия WebP (0-6; 0 = умолчание ImageMagick).
	WebPMethod dynamic.Int64 `yaml:"webp-method"`
	// PNGCompressionLevel — уровень сжатия PNG (0-9; 0 = умолчание).
	PNGCompressionLevel dynamic.Int64 `yaml:"png-compression-level"`
}

// ApplicationYAML — прикладные лимиты.
type ApplicationYAML struct {
	// OutputLimit — максимальный размер выходного файла (0 = без лимита).
	OutputLimit dynamic.Int64 `yaml:"output-limit"`
	// BufferMaxBytes — общий бюджет памяти процесса для spillable-буферов
	// (0 = без лимита). По умолчанию 500 МБ.
	BufferMaxBytes dynamic.Int64 `yaml:"buffer-max-bytes"`
}

// ObservabilityYAML — логирование и метрики.
type ObservabilityYAML struct {
	// LogLevel — уровень логов: debug, info, warn, error (по умолчанию info).
	LogLevel dynamic.String `yaml:"log-level"`
	// AssetErrors — учёт ошибок asset URL (счётчики, top-paths, логи).
	AssetErrors AssetErrorsYAML `yaml:"asset-errors"`
}

// AssetErrorsYAML — YAML-представление AssetErrorConfig.
type AssetErrorsYAML struct {
	// Enabled — включать ли учёт ошибок asset URL. Дефолт true.
	Enabled dynamic.Nullable[dynamic.Bool] `yaml:"enabled"`
	// LogLevel — уровень структурного лога ошибки. Дефолт warn.
	LogLevel dynamic.String `yaml:"log-level"`
	// TopPaths — bounded-реестр проблемных путей.
	TopPaths TopPathsYAML `yaml:"top-paths"`
}

// TopPathsYAML — YAML-представление TopPathsConfig.
type TopPathsYAML struct {
	// Enabled — включать ли учёт top-paths. Дефолт false.
	Enabled dynamic.Bool `yaml:"enabled"`
	// MaxEntries — максимальное число отслеживаемых путей (LRU). Дефолт 1024.
	MaxEntries dynamic.Int64 `yaml:"max-entries"`
	// ReportTop — число путей в отчёте. Дефолт 20.
	ReportTop dynamic.Int64 `yaml:"report-top"`
	// KeyMode — режим ключа: source | hash. Дефолт source.
	KeyMode dynamic.String `yaml:"key-mode"`
}

// MetadataYAML — конфигурация sidecar-кэша моделей и largest_ai_asset
//
// Дир расположения НАСТРАИВАЕТСЯ отдельным ключом metadata.dir — явный
// локальный путь файловой системы, НЕЗАВИСИМЫЙ от хранилищ source/result.
// Пустой dir = дефолт `<эффективный локальный result-каталог>/.meta`
// (обратно совместимо).
type MetadataYAML struct {
	// Enabled — включить sidecar-кэш моделей и largest_ai_asset.
	// Тип: bool. Дефолт: true. false = поведение идентично текущему.
	Enabled dynamic.Nullable[dynamic.Bool] `yaml:"enabled"`
	// Dir — КОРЕНЬ sidecar-хранилища метаданных (НОВАЯ СЕМАНТИКА v2.1):
	// явный ЛОКАЛЬНЫЙ путь файловой системы. Метаданные всегда хранятся
	// локально по этому пути, независимо от типов source/result.
	// Тип: string. Дефолт: <эффективный локальный result-каталог>/.meta.
	Dir dynamic.String `yaml:"dir"`
}

// AdminYAML — YAML-представление AdminConfig.
//
// Выключено по умолчанию (enabled: false). При enabled: true ТРЕБУЕТСЯ
// непустой token (иначе — fail-fast ошибка старта). workers ≥ 1 (дефолт 2),
// queue-size ≥ 1 (дефолт 64), wait-timeout > 0 (дефолт "300s").
type AdminYAML struct {
	// Enabled — включать ли admin-эндпоинты. Дефолт false.
	Enabled dynamic.Bool `yaml:"enabled"`
	// Token — bearer-токен (Authorization: Bearer <token>). Обязателен при
	// enabled: true.
	Token dynamic.String `yaml:"token"`
	// Workers — число параллельных фоновых генераций. Дефолт 2.
	Workers dynamic.Int64 `yaml:"workers"`
	// QueueSize — ёмкость очереди задач; переполнение → 503. Дефолт 64.
	QueueSize dynamic.Int64 `yaml:"queue-size"`
	// WaitTimeout — таймаут режима wait=true (duration). Дефолт "300s".
	WaitTimeout dynamic.String `yaml:"wait-timeout"`
}

// HTTPYAML — YAML-представление httpapi.Config.
type HTTPYAML struct {
	// AllowedOrigins — CORS allowlist.
	AllowedOrigins []dynamic.String `yaml:"allowed-origins"`
	// AllowCredentials — разрешить credentials.
	AllowCredentials dynamic.Bool `yaml:"allow-credentials"`
	// CacheControl — Cache-Control для canonical assets.
	CacheControl dynamic.String `yaml:"cache-control"`
	// NotFoundCacheControl — Cache-Control для fallback.
	NotFoundCacheControl dynamic.String `yaml:"not-found-cache-control"`
	// ReferrerPolicy — Referrer-Policy.
	ReferrerPolicy dynamic.String `yaml:"referrer-policy"`
	// CSP — Content-Security-Policy.
	CSP dynamic.String `yaml:"csp"`
	// MaxURLLen — максимальная длина URL.
	MaxURLLen dynamic.Int64 `yaml:"max-url-len"`
	// GenerateTimeout — таймаут генерации ассета (duration, например "30s").
	GenerateTimeout dynamic.String `yaml:"generate-timeout"`
	// NotFound — not-found fallback.
	NotFound NotFoundYAML `yaml:"not-found"`
	// SourceFallback — fallback на исходный файл при ошибке ассета.
	SourceFallback SourceFallbackYAML `yaml:"source-fallback"`
	// MaxConcurrentRequests — максимальное число одновременно обрабатываемых
	// HTTP-запросов (0 = без ограничения).
	MaxConcurrentRequests dynamic.Int64 `yaml:"max-concurrent-requests"`
}

// SourceFallbackYAML — YAML-представление SourceFallbackConfig.
type SourceFallbackYAML struct {
	// Enabled — включать ли source fallback. Дефолт false.
	Enabled dynamic.Bool `yaml:"enabled"`
	// Status — HTTP-статус ответа: 200 или 404 (0 → 404).
	Status dynamic.Int64 `yaml:"status"`
	// CacheControl — Cache-Control для fallback-ответа. Дефолт "no-store".
	CacheControl dynamic.String `yaml:"cache-control"`
}

// NotFoundYAML — YAML-представление NotFoundConfig.
type NotFoundYAML struct {
	Pixel    dynamic.Bool   `yaml:"pixel"`
	Image    dynamic.String `yaml:"image"`
	Page     dynamic.String `yaml:"page"`
	Redirect dynamic.String `yaml:"redirect"`
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
	cfg := &config.Config{Version: raw.Version, Watermarks: raw.Watermarks}
	// Fail-fast: файлы ватермарок должны существовать на старте.
	for i, w := range raw.Watermarks {
		if w.Path.Unwrap() == "" {
			continue // пустой path отклонится в config.Validate
		}
		if _, err := os.Stat(w.Path.Unwrap()); err != nil {
			return nil, fmt.Errorf("httpapi: watermarks[%d] (%s): %w", i, w.Name.Unwrap(), err)
		}
	}
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
	allowedOrigins := make([]string, 0, len(raw.HTTP.AllowedOrigins))
	for _, o := range raw.HTTP.AllowedOrigins {
		allowedOrigins = append(allowedOrigins, o.Unwrap())
	}
	httpCfg := Config{
		AllowedOrigins:        allowedOrigins,
		AllowCredentials:      raw.HTTP.AllowCredentials.Unwrap(),
		CacheControl:          raw.HTTP.CacheControl.Unwrap(),
		NotFoundCacheControl:  raw.HTTP.NotFoundCacheControl.Unwrap(),
		ReferrerPolicy:        raw.HTTP.ReferrerPolicy.Unwrap(),
		CSP:                   raw.HTTP.CSP.Unwrap(),
		MaxURLLen:             int(raw.HTTP.MaxURLLen.Unwrap()),
		MaxConcurrentRequests: int(raw.HTTP.MaxConcurrentRequests.Unwrap()),
		NotFound: NotFoundConfig{
			Pixel:    raw.HTTP.NotFound.Pixel.Unwrap(),
			Image:    raw.HTTP.NotFound.Image.Unwrap(),
			Page:     raw.HTTP.NotFound.Page.Unwrap(),
			Redirect: raw.HTTP.NotFound.Redirect.Unwrap(),
		},
		SourceFallback: SourceFallbackConfig{
			Enabled:      raw.HTTP.SourceFallback.Enabled.Unwrap(),
			Status:       int(raw.HTTP.SourceFallback.Status.Unwrap()),
			CacheControl: raw.HTTP.SourceFallback.CacheControl.Unwrap(),
		},
		Admin: AdminConfig{
			Enabled:   raw.Admin.Enabled.Unwrap(),
			Token:     raw.Admin.Token.Unwrap(),
			Workers:   int(raw.Admin.Workers.Unwrap()),
			QueueSize: int(raw.Admin.QueueSize.Unwrap()),
		},
	}
	// Admin wait-timeout (duration).
	if raw.Admin.WaitTimeout.Unwrap() != "" {
		d, err := time.ParseDuration(raw.Admin.WaitTimeout.Unwrap())
		if err != nil {
			return nil, fmt.Errorf("httpapi: admin.wait-timeout: %w", err)
		}
		if d < 0 {
			return nil, fmt.Errorf("httpapi: admin.wait-timeout: negative duration %q", raw.Admin.WaitTimeout.Unwrap())
		}
		httpCfg.Admin.WaitTimeout = d
	}
	// Asset errors observability (fail-fast на неверных значениях).
	assetErrorsEnabled := true
	if raw.Observability.AssetErrors.Enabled.Set {
		assetErrorsEnabled = raw.Observability.AssetErrors.Enabled.Value.Unwrap()
	}
	httpCfg.AssetErrors = AssetErrorConfig{
		Enabled:  assetErrorsEnabled,
		LogLevel: raw.Observability.AssetErrors.LogLevel.Unwrap(),
		TopPaths: TopPathsConfig{
			Enabled:    raw.Observability.AssetErrors.TopPaths.Enabled.Unwrap(),
			MaxEntries: int(raw.Observability.AssetErrors.TopPaths.MaxEntries.Unwrap()),
			ReportTop:  int(raw.Observability.AssetErrors.TopPaths.ReportTop.Unwrap()),
			KeyMode:    raw.Observability.AssetErrors.TopPaths.KeyMode.Unwrap(),
		},
	}
	if raw.HTTP.GenerateTimeout.Unwrap() != "" {
		d, err := time.ParseDuration(raw.HTTP.GenerateTimeout.Unwrap())
		if err != nil {
			return nil, fmt.Errorf("httpapi: http.generate-timeout: %w", err)
		}
		if d < 0 {
			return nil, fmt.Errorf("httpapi: http.generate-timeout: negative duration %q", raw.HTTP.GenerateTimeout.Unwrap())
		}
		httpCfg.GenerateTimeout = d
	}
	httpCfg.normalize() // применяем умолчания (статус 404, cache-control и т.д.)
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
	sourceDir := raw.Source.Path.Unwrap()
	if sourceDir == "" {
		sourceDir = "./data/source"
	}
	resultDir := raw.Result.Path.Unwrap()
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

	// Детектор лиц/объектов (face-crop/object-crop).
	det, err := raw.Detection.build()
	if err != nil {
		return nil, fmt.Errorf("httpapi: detection: %w", err)
	}

	// Прикладные лимиты.
	if raw.Application.OutputLimit.Unwrap() < 0 {
		return nil, fmt.Errorf("httpapi: application.output-limit: negative value %d", raw.Application.OutputLimit.Unwrap())
	}
	if raw.Application.BufferMaxBytes.Unwrap() < 0 {
		return nil, fmt.Errorf("httpapi: application.buffer-max-bytes: negative value %d", raw.Application.BufferMaxBytes.Unwrap())
	}
	bufferMaxBytes := raw.Application.BufferMaxBytes.Unwrap()
	if bufferMaxBytes == 0 {
		bufferMaxBytes = DefaultBufferMaxBytes
	}

	logLevel := raw.Observability.LogLevel.Unwrap()
	if logLevel == "" {
		logLevel = "info"
	}

	// Metadata: sidecar-кэш моделей и largest_ai_asset.
	// Дефолт enabled = true. metadata.dir — ЯВНЫЙ локальный корень
	// sidecar-хранилища (НЕЗАВИСИМ от хранилищ source/result); пусто =
	// дефолт `<эффективный локальный result-каталог>/.meta` (обратно
	// совместимо) — применяется на уровне DI (app.go).
	metadataEnabled := true
	if raw.Metadata.Enabled.Set {
		metadataEnabled = raw.Metadata.Enabled.Value.Unwrap()
	}
	metadataDir := raw.Metadata.Dir.Unwrap()

	return &RuntimeConfig{
		Pipeline:        cfg,
		HTTP:            httpCfg,
		Server:          server,
		Admin:           httpCfg.Admin,
		SourceDir:       sourceDir,
		ResultDir:       resultDir,
		Source:          source,
		Result:          result,
		ImageMagick:     img,
		Libvips:         lv,
		Detection:       det,
		MetadataEnabled: metadataEnabled,
		MetadataDir:     metadataDir,
		OutputLimit:     raw.Application.OutputLimit.Unwrap(),
		BufferMaxBytes:  bufferMaxBytes,
		LogLevel:        logLevel,
	}, nil
}

// toRemoteStorageConfig конвертирует YAML-конфигурацию в RemoteStorageConfig.
func (s StorageYAML) toRemoteStorageConfig() (RemoteStorageConfig, error) {
	if s.SpoolMaxBytes.Unwrap() < 0 {
		return RemoteStorageConfig{}, fmt.Errorf("spool-max-bytes: negative value %d", s.SpoolMaxBytes.Unwrap())
	}
	// Секреты S3 могут задаваться через переменные окружения
	// (IMAGER_S3_ACCESS_KEY / IMAGER_S3_SECRET_KEY), чтобы не хранить их
	// открытым текстом в конфиге. Значение из YAML имеет приоритет.
	accessKey := s.AccessKey.Unwrap()
	if accessKey == "" {
		accessKey = os.Getenv("IMAGER_S3_ACCESS_KEY")
	}
	secretKey := s.SecretKey.Unwrap()
	if secretKey == "" {
		secretKey = os.Getenv("IMAGER_S3_SECRET_KEY")
	}
	cfg := RemoteStorageConfig{
		Kind:               StorageKind(s.Storage.Unwrap()),
		Path:               s.Path.Unwrap(),
		BaseURL:            s.BaseURL.Unwrap(),
		Bucket:             s.Bucket.Unwrap(),
		Prefix:             s.Prefix.Unwrap(),
		Endpoint:           s.Endpoint.Unwrap(),
		Region:             s.Region.Unwrap(),
		AccessKey:          accessKey,
		SecretKey:          secretKey,
		Addr:               s.Addr.Unwrap(),
		User:               s.User.Unwrap(),
		Password:           s.Password.Unwrap(),
		Root:               s.Root.Unwrap(),
		TLS:                s.TLS.Unwrap(),
		TLSVerify:          true,
		HostKeyFingerprint: s.HostKeyFingerprint.Unwrap(),
		SpoolDir:           s.SpoolDir.Unwrap(),
		SpoolMaxBytes:      s.SpoolMaxBytes.Unwrap(),
		DialTimeout:        30 * time.Second,
	}
	if s.TLSVerify.Set {
		cfg.TLSVerify = s.TLSVerify.Value.Unwrap()
	}
	if s.DialTimeout.Unwrap() != "" {
		d, err := time.ParseDuration(s.DialTimeout.Unwrap())
		if err != nil {
			return RemoteStorageConfig{}, fmt.Errorf("dial-timeout: %w", err)
		}
		if d < 0 {
			return RemoteStorageConfig{}, fmt.Errorf("dial-timeout: negative duration %q", s.DialTimeout.Unwrap())
		}
		cfg.DialTimeout = d
	}
	// Общие настройки HTTP-подобных хранилищ (S3, HTTP): таймауты, retry,
	// пул соединений, кэш метаданных. Для SFTP/FTP/FTPS применяется только
	// dial-timeout (см. выше).
	if s.ReadTimeout.Unwrap() != "" {
		d, err := time.ParseDuration(s.ReadTimeout.Unwrap())
		if err != nil {
			return RemoteStorageConfig{}, fmt.Errorf("read-timeout: %w", err)
		}
		if d < 0 {
			return RemoteStorageConfig{}, fmt.Errorf("read-timeout: negative duration %q", s.ReadTimeout.Unwrap())
		}
		cfg.ReadTimeout = d
	}
	if s.IdleConnTimeout.Unwrap() != "" {
		d, err := time.ParseDuration(s.IdleConnTimeout.Unwrap())
		if err != nil {
			return RemoteStorageConfig{}, fmt.Errorf("idle-conn-timeout: %w", err)
		}
		if d < 0 {
			return RemoteStorageConfig{}, fmt.Errorf("idle-conn-timeout: negative duration %q", s.IdleConnTimeout.Unwrap())
		}
		cfg.IdleConnTimeout = d
	}
	if s.MetadataTTL.Unwrap() != "" {
		d, err := time.ParseDuration(s.MetadataTTL.Unwrap())
		if err != nil {
			return RemoteStorageConfig{}, fmt.Errorf("metadata-ttl: %w", err)
		}
		if d < 0 {
			return RemoteStorageConfig{}, fmt.Errorf("metadata-ttl: negative duration %q", s.MetadataTTL.Unwrap())
		}
		cfg.MetadataTTL = d
	}
	if s.MaxAttempts.Unwrap() < 0 {
		return RemoteStorageConfig{}, fmt.Errorf("max-attempts: negative value %d", s.MaxAttempts.Unwrap())
	}
	cfg.MaxAttempts = int(s.MaxAttempts.Unwrap())
	if s.MaxIdleConns.Unwrap() < 0 {
		return RemoteStorageConfig{}, fmt.Errorf("max-idle-conns: negative value %d", s.MaxIdleConns.Unwrap())
	}
	cfg.MaxIdleConns = int(s.MaxIdleConns.Unwrap())
	if s.MaxConns.Unwrap() < 0 {
		return RemoteStorageConfig{}, fmt.Errorf("max-conns: negative value %d", s.MaxConns.Unwrap())
	}
	cfg.MaxConns = int(s.MaxConns.Unwrap())
	if s.MaxIdleConnsPerHost.Unwrap() < 0 {
		return RemoteStorageConfig{}, fmt.Errorf("max-idle-conns-per-host: negative value %d", s.MaxIdleConnsPerHost.Unwrap())
	}
	cfg.MaxIdleConnsPerHost = int(s.MaxIdleConnsPerHost.Unwrap())
	if s.PrivateKeyFile.Unwrap() != "" {
		data, err := os.ReadFile(s.PrivateKeyFile.Unwrap())
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
		Addr:           s.Addr.Unwrap(),
		MaxHeaderBytes: int(s.MaxHeaderBytes.Unwrap()),
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
		{"read-header-timeout", s.ReadHeaderTimeout.Unwrap()},
		{"read-timeout", s.ReadTimeout.Unwrap()},
		{"write-timeout", s.WriteTimeout.Unwrap()},
		{"idle-timeout", s.IdleTimeout.Unwrap()},
		{"shutdown-timeout", s.ShutdownTimeout.Unwrap()},
	} {
		if err := parse(p.name, p.val); err != nil {
			return ServerConfig{}, err
		}
	}
	return cfg, nil
}

// build конвертирует YAML-конфигурацию ImageMagick в ImageMagickConfig.
func (i ImageMagickYAML) build() (ImageMagickConfig, error) {
	disabledCoders := make([]string, 0, len(i.Policy.DisabledCoders))
	for _, c := range i.Policy.DisabledCoders {
		disabledCoders = append(disabledCoders, c.Unwrap())
	}
	disabledDelegates := make([]string, 0, len(i.Policy.DisabledDelegates))
	for _, d := range i.Policy.DisabledDelegates {
		disabledDelegates = append(disabledDelegates, d.Unwrap())
	}
	cfg := ImageMagickConfig{
		Binary: i.Binary.Unwrap(),
		Limits: imagemagick.Limits{
			MemoryBytes:         i.Limits.MemoryBytes.Unwrap(),
			MapBytes:            i.Limits.MapBytes.Unwrap(),
			DiskBytes:           i.Limits.DiskBytes.Unwrap(),
			Threads:             int(i.Limits.Threads.Unwrap()),
			TimeSeconds:         int(i.Limits.TimeSeconds.Unwrap()),
			Width:               i.Limits.Width.Unwrap(),
			Height:              i.Limits.Height.Unwrap(),
			Pixels:              i.Limits.Pixels.Unwrap(),
			Frames:              int(i.Limits.Frames.Unwrap()),
			OutputBytes:         i.Limits.OutputBytes.Unwrap(),
			Concurrency:         int(i.Limits.Concurrency.Unwrap()),
			WebPMethod:          int(i.Limits.WebPMethod.Unwrap()),
			PNGCompressionLevel: int(i.Limits.PNGCompressionLevel.Unwrap()),
		},
		Policy: imagemagick.PolicyConfig{
			Enabled:           true,
			DisableNetwork:    true,
			Dir:               i.Policy.Dir.Unwrap(),
			MaxMemoryBytes:    i.Policy.MaxMemoryBytes.Unwrap(),
			MaxMapBytes:       i.Policy.MaxMapBytes.Unwrap(),
			MaxDiskBytes:      i.Policy.MaxDiskBytes.Unwrap(),
			MaxThreads:        int(i.Policy.MaxThreads.Unwrap()),
			MaxTimeSeconds:    int(i.Policy.MaxTimeSeconds.Unwrap()),
			MaxWidth:          i.Policy.MaxWidth.Unwrap(),
			MaxHeight:         i.Policy.MaxHeight.Unwrap(),
			MaxPixels:         i.Policy.MaxPixels.Unwrap(),
			MaxFrames:         int(i.Policy.MaxFrames.Unwrap()),
			DisabledCoders:    disabledCoders,
			DisabledDelegates: disabledDelegates,
		},
	}
	if cfg.Binary == "" {
		cfg.Binary = "magick"
	}
	if i.Policy.Enabled.Set {
		cfg.Policy.Enabled = i.Policy.Enabled.Value.Unwrap()
	}
	if i.Policy.DisableNetwork.Set {
		cfg.Policy.DisableNetwork = i.Policy.DisableNetwork.Value.Unwrap()
	}
	if i.Limits.Timeout.Unwrap() != "" {
		d, err := time.ParseDuration(i.Limits.Timeout.Unwrap())
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
			OutputBytes:   l.Limits.OutputBytes.Unwrap(),
			Concurrency:   int(l.Limits.Concurrency.Unwrap()),
			Threads:       int(l.Limits.Threads.Unwrap()),
			MaxCacheMem:   int(l.Limits.MaxCacheMem.Unwrap()),
			MaxCacheFiles: int(l.Limits.MaxCacheFiles.Unwrap()),
			MaxCacheSize:  int(l.Limits.MaxCacheSize.Unwrap()),
		},
	}
	if l.Limits.Timeout.Unwrap() != "" {
		d, err := time.ParseDuration(l.Limits.Timeout.Unwrap())
		if err != nil {
			return LibvipsConfig{}, fmt.Errorf("limits.timeout: %w", err)
		}
		if d < 0 {
			return LibvipsConfig{}, fmt.Errorf("limits.timeout: negative duration %q", l.Limits.Timeout.Unwrap())
		}
		cfg.Limits.Timeout = d
	}
	return cfg, nil
}

// build конвертирует YAML-конфигурацию детектора в DetectionConfig с
// валидацией (fail-fast). Значения по умолчанию: confidence-threshold = 0.5,
// max-objects = 5, margin = 0.1. Пустые пути к моделям допустимы (детектор
// просто отключён).
func (d DetectionYAML) build() (DetectionConfig, error) {
	cfg := DetectionConfig{
		FaceModel:           d.FaceModel.Unwrap(),
		ObjectModel:         d.ObjectModel.Unwrap(),
		ConfidenceThreshold: 0.5,
		MaxObjects:          5,
		Margin:              0.1,
	}
	// Set = false (ключ не задан) → дефолт. Явное значение (включая 0)
	// валидируется.
	if d.ConfidenceThreshold.Set {
		cfg.ConfidenceThreshold = d.ConfidenceThreshold.Value.Unwrap()
	}
	if d.MaxObjects.Set {
		cfg.MaxObjects = int(d.MaxObjects.Value.Unwrap())
	}
	if d.Margin.Set {
		cfg.Margin = d.Margin.Value.Unwrap()
	}
	if cfg.ConfidenceThreshold < 0 || cfg.ConfidenceThreshold > 1 {
		return DetectionConfig{}, fmt.Errorf("confidence-threshold: must be in [0,1], got %v", cfg.ConfidenceThreshold)
	}
	if cfg.MaxObjects <= 0 {
		return DetectionConfig{}, fmt.Errorf("max-objects: must be > 0, got %d", cfg.MaxObjects)
	}
	if cfg.Margin < 0 {
		return DetectionConfig{}, fmt.Errorf("margin: must be >= 0, got %v", cfg.Margin)
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
