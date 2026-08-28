package composition

import (
	"fmt"
	"os"
	"time"

	"github.com/pkg-ru/dynamic"
	"gopkg.in/yaml.v2"

	"github.com/pkg-ru/imager/adapters/httpapi"
	"github.com/pkg-ru/imager/adapters/processor/libvips"
	"github.com/pkg-ru/imager/adapters/storage/remote"
	"github.com/pkg-ru/imager/app/generatev2"
	"github.com/pkg-ru/imager/config"
)

// DefaultBufferMaxBytes — общий бюджет памяти процесса для spillable-буферов
// по умолчанию (500 МБ).
const DefaultBufferMaxBytes int64 = 500 * 1024 * 1024

// RuntimeConfig — единый typed runtime-конфиг всего приложения.
//
// Собирается из YAML-файлов (setting.yaml + setting-local.yaml) через
// ParseRuntimeConfig. Содержит все настройки приложения: pipeline
// (policy/processing), HTTP-адаптер, HTTP-сервер, хранилища source/result,
// libvips processor и observability.
type RuntimeConfig struct {
	// Pipeline — typed конфигурация конвейера (policy/processing).
	Pipeline *config.Config
	// HTTP — конфигурация HTTP-адаптера.
	HTTP httpapi.Config
	// Server — конфигурация HTTP-сервера (адрес и таймауты).
	Server ServerConfig

	// Admin — конфигурация административных эндпоинтов. По умолчанию
	// выключены (enabled: false). При включении регистрируются
	// POST /admin/assets/generate и DELETE /admin/assets/delete.
	Admin httpapi.AdminConfig

	// SourceDir — каталог исходников (используется при FS source).
	SourceDir string
	// ResultDir — каталог результатов (используется при FS result).
	ResultDir string
	// Source — конфигурация source-хранилища.
	Source RemoteStorageConfig
	// Result — конфигурация result-хранилища.
	Result RemoteStorageConfig

	// Libvips — конфигурация libvips processor (единственный движок;
	// in-process через govips). Требует сборки с тэком "libvips".
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
	// result-каталог>` (без подкаталога .meta).
	MetadataDir string
	// Limits — application-level лимиты генерации ассетов (application.limits).
	// Нулевые поля = без ограничения.
	Limits generatev2.Limits
	// BufferMaxBytes — общий бюджет памяти процесса для spillable-буферов
	// (0 = без лимита). По умолчанию 500 МБ.
	BufferMaxBytes int64
	// LogLevel — уровень логов (debug/info/warn/error).
	LogLevel string
}

// ServerConfig — конфигурация HTTP-сервера.
//
// Нулевое значение таймаута означает "использовать умолчание runtime"
// (см. httpapi.defaultTimeouts).
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

// LibvipsConfig — конфигурация libvips processor (govips).
type LibvipsConfig struct {
	// Limits — resource limits обработчика libvips.
	Limits libvips.Limits
	// Encoders — per-format параметры сжатия кодировщиков (WebP effort,
	// AVIF speed, PNG compression, JXL effort, JPEG progressive, PNG
	// interlace/quantization, GIF bit-depth). Нулевые поля = встроенные
	// умолчания.
	Encoders libvips.EncoderParams
	// ShrinkOnLoad — настройки shrink-on-load (предварительное уменьшение
	// при декодировании JPEG/WebP/GIF/HEIF/AVIF).
	ShrinkOnLoad libvips.ShrinkOnLoadOpts
	// WatermarkCache — настройки in-memory кэша файлов ватермарок.
	WatermarkCache libvips.WatermarkCacheOpts
	// DetectionSem — настройки detection-семафора (Фаза 4): отдельный лимит
	// конкурентности ONNX-инференса вне libvips-слотов.
	DetectionSem libvips.DetectionSemaphoreOpts
	// Color — политика ICC color management (Фаза 5a): strip (дефолт,
	// удалять профиль), transform (конвертация в sRGB перед обработкой),
	// keep (сохранить embedded-профиль в выход).
	Color libvips.ColorMode
	// OperationCache — настройки operation cache libvips (Фаза 5b).
	// Включено по умолчанию (обратная совместимость); false = нулевые
	// лимиты кэша при Startup (кэш отключён).
	OperationCache libvips.OperationCacheOpts
	// VipsMetricsInterval — интервал периодического сбора vips-метрик
	// (0 = дефолт 15s).
	VipsMetricsInterval time.Duration
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
	// OnnxRuntimeLib — путь к библиотеке libonnxruntime (dlopen). Пусто =
	// автодетекция по стандартным путям (см. onnx_cgo.go). Задаётся через
	// конфиг-файл, а не через env ONNXRUNTIME_SHARED_LIBRARY_PATH.
	OnnxRuntimeLib string
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
	// OnnxRuntimeLib — путь к библиотеке libonnxruntime (dlopen). Пусто =
	// автодетекция по стандартным путям.
	OnnxRuntimeLib dynamic.String `yaml:"onnx-runtime-lib"`
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
	// (см. docs/DEPLOYMENT.md); рекомендуется задавать всегда.
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

// LibvipsYAML — YAML-представление конфигурации libvips.
type LibvipsYAML struct {
	// Limits — resource limits обработчика libvips.
	Limits LibvipsLimitsYAML `yaml:"limits"`
	// Encoders — per-format параметры сжатия кодировщиков.
	Encoders LibvipsEncodersYAML `yaml:"encoders"`
	// ShrinkOnLoad — настройки shrink-on-load при декодировании.
	ShrinkOnLoad ShrinkOnLoadYAML `yaml:"shrink-on-load"`
	// WatermarkCache — настройки in-memory кэша файлов ватермарок.
	WatermarkCache WatermarkCacheYAML `yaml:"watermark-cache"`
	// DetectionSem — настройки detection-семафора (Фаза 4).
	DetectionSem DetectionSemYAML `yaml:"detection"`
	// Color — политика ICC color management (Фаза 5a): strip/transform/keep.
	Color ColorYAML `yaml:"color"`
	// OperationCache — настройки operation cache (Фаза 5b).
	OperationCache OperationCacheYAML `yaml:"operation-cache"`
	// MetricsInterval — интервал сбора vips-метрик (duration; 0 = дефолт 15s).
	MetricsInterval dynamic.String `yaml:"metrics-interval"`
}

// ColorYAML — YAML-представление политики color management (Фаза 5a).
type ColorYAML struct {
	// Mode — режим: strip (дефолт), transform, keep.
	Mode dynamic.String `yaml:"mode"`
}

// OperationCacheYAML — YAML-представление настроек operation cache (Фаза 5b).
type OperationCacheYAML struct {
	// Enabled — включить operation cache libvips (nil = включено по
	// умолчанию, обратная совместимость).
	Enabled dynamic.Nullable[dynamic.Bool] `yaml:"enabled"`
}

// DetectionSemYAML — YAML-представление libvips.DetectionSemaphoreOpts.
type DetectionSemYAML struct {
	// Concurrency — максимум одновременных ONNX-инференсов (0 = дефолт
	// max(1, GOMAXPROCS/2)).
	Concurrency dynamic.Int64 `yaml:"concurrency"`
	// MaxWait — бюджет ожидания detection-слота (duration; 0 = дефолт 5s).
	MaxWait dynamic.String `yaml:"max-wait"`
}

// WatermarkCacheYAML — YAML-представление libvips.WatermarkCacheOpts.
type WatermarkCacheYAML struct {
	// Enabled — включить кэш файлов ватермарок (nil = включено по умолчанию).
	Enabled dynamic.Nullable[dynamic.Bool] `yaml:"enabled"`
	// MaxFiles — максимум записей (файлов) в кэше (0 = дефолт 32).
	MaxFiles dynamic.Int64 `yaml:"max-files"`
	// MaxBytes — суммарный бюджет памяти кэша в байтах (0 = дефолт 64 MiB).
	MaxBytes dynamic.Int64 `yaml:"max-bytes"`
	// TTL — время жизни записи (duration; 0 = дефолт 5m).
	TTL dynamic.String `yaml:"ttl"`
}

// ShrinkOnLoadYAML — YAML-представление libvips.ShrinkOnLoadOpts.
type ShrinkOnLoadYAML struct {
	// Enabled — включить shrink-on-load (nil = включено по умолчанию).
	Enabled dynamic.Nullable[dynamic.Bool] `yaml:"enabled"`
}

// LibvipsEncodersYAML — YAML-представление libvips.EncoderParams.
type LibvipsEncodersYAML struct {
	// WebPReductionEffort — reduction effort WebP [0..6] (больше = лучше
	// сжатие, медленнее; 0 = умолчание 4).
	WebPReductionEffort dynamic.Int64 `yaml:"webp-reduction-effort"`
	// AVIFSpeed — speed/effort AVIF [0..9] (больше = быстрее, хуже сжатие;
	// 0 = умолчание govips).
	AVIFSpeed dynamic.Int64 `yaml:"avif-speed"`
	// PNGCompressionLevel — уровень сжатия PNG [0..9] (0 = умолчание 6).
	PNGCompressionLevel dynamic.Int64 `yaml:"png-compression-level"`
	// JXLEffort — effort JPEG XL [0..9] (больше = лучше сжатие, медленнее;
	// 0 = умолчание govips, 7).
	JXLEffort dynamic.Int64 `yaml:"jxl-effort"`
	// JPEGProgressive — прогрессивный (interlaced) JPEG. false = baseline.
	JPEGProgressive dynamic.Bool `yaml:"jpeg-progressive"`
	// PNGInterlace — чересстрочный (interlaced/Adam7) PNG. false = обычный.
	PNGInterlace dynamic.Bool `yaml:"png-interlace"`
	// PNGPalette — включить PNG-квантование (палитровый экспорт). По
	// умолчанию выключено (применяется ТОЛЬКО при явном включении).
	PNGPalette dynamic.Bool `yaml:"png-palette"`
	// PNGPaletteColors — максимальное число цветов палитры [2..256]
	// (0 = 256). Значимо при png-palette=true.
	PNGPaletteColors dynamic.Int64 `yaml:"png-palette-colors"`
	// PNGPaletteBitDepth — битность палитры [1..8] (0 = 8). Позволяет
	// сохранить палитровую битность исходника. Значимо при png-palette=true.
	PNGPaletteBitDepth dynamic.Int64 `yaml:"png-palette-bit-depth"`
	// GIFBitDepth — битность палитры GIF [1..8] (0 = умолчание govips, 8).
	GIFBitDepth dynamic.Int64 `yaml:"gif-bit-depth"`
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

// ApplicationYAML — прикладные лимиты.
type ApplicationYAML struct {
	// Limits — application-level лимиты генерации ассетов.
	Limits ApplicationLimitsYAML `yaml:"limits"`
	// BufferMaxBytes — общий бюджет памяти процесса для spillable-буферов
	// (0 = без лимита). По умолчанию 500 МБ.
	BufferMaxBytes dynamic.Int64 `yaml:"buffer-max-bytes"`
}

// ApplicationLimitsYAML — YAML-представление application-level лимитов
// генерации ассетов (application.limits).
//
// Нулевое значение поля = без ограничения. Все значения должны быть
// неотрицательными (fail-fast на старте).
type ApplicationLimitsYAML struct {
	// SourceBytes — максимальный размер исходного файла в байтах (0 = без
	// ограничения).
	SourceBytes dynamic.Int64 `yaml:"source-bytes"`
	// OutputBytes — максимальный размер выходного файла в байтах (0 = без
	// ограничения).
	OutputBytes dynamic.Int64 `yaml:"output-bytes"`
	// Pixels — максимальное число пикселей (width*height) (0 = без
	// ограничения).
	Pixels dynamic.Int64 `yaml:"pixels"`
	// Width — максимальная ширина (0 = без ограничения).
	Width dynamic.Uint32 `yaml:"width"`
	// Height — максимальная высота (0 = без ограничения).
	Height dynamic.Uint32 `yaml:"height"`
	// DPR — максимальный DPR (0 = без ограничения).
	DPR dynamic.Uint32 `yaml:"dpr"`
	// Frames — максимальное число кадров (0 = без ограничения).
	Frames dynamic.Uint32 `yaml:"frames"`
	// Duration — максимальная длительность в миллисекундах (0 = без
	// ограничения).
	Duration dynamic.Uint32 `yaml:"duration"`
	// Concurrency — максимальное число одновременно выполняемых операций
	// (0 = без ограничения). Валидируется, но НЕ подключается к HTTP-слою
	// (admission control остаётся в httpapi).
	Concurrency dynamic.Uint32 `yaml:"concurrency"`
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
// Пустой dir = дефолт `<эффективный локальный result-каталог>` (без
// подкаталога .meta).
type MetadataYAML struct {
	// Enabled — включить sidecar-кэш моделей и largest_ai_asset.
	// Тип: bool. Дефолт: true. false = поведение идентично текущему.
	Enabled dynamic.Nullable[dynamic.Bool] `yaml:"enabled"`
	// Dir — КОРЕНЬ sidecar-хранилища метаданных (НОВАЯ СЕМАНТИКА v2.1):
	// явный ЛОКАЛЬНЫЙ путь файловой системы. Метаданные всегда хранятся
	// локально по этому пути, независимо от типов source/result.
	// Тип: string. Дефолт: <эффективный локальный result-каталог>.
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
	AllowedOrigins dynamic.StringSlice `yaml:"allowed-origins"`
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
	// ServeOriginal — отдача исходников по «простым» URL вида /path/name.ext
	// (отдельная фича, не относящаяся к source-fallback).
	ServeOriginal ServeOriginalYAML `yaml:"serve-original"`
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

// ServeOriginalYAML — YAML-представление ServeOriginalConfig (отдача
// исходников по «простым» URL вида /path/name.ext).
type ServeOriginalYAML struct {
	// Enabled — включать ли отдачу исходников по «простым» URL. Дефолт false.
	Enabled dynamic.Bool `yaml:"enabled"`
	// CacheControl — Cache-Control для ответа. Дефолт "no-store".
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
		return nil, fmt.Errorf("composition: decode yaml: %w", err)
	}

	// Собираем config.Config из сырых секций.
	cfg := &config.Config{Version: raw.Version, Watermarks: raw.Watermarks}
	// Fail-fast: файлы ватермарок должны существовать на старте.
	for i, w := range raw.Watermarks {
		if w.Path.Unwrap() == "" {
			continue // пустой path отклонится в config.Validate
		}
		if _, err := os.Stat(w.Path.Unwrap()); err != nil {
			return nil, fmt.Errorf("composition: watermarks[%d] (%s): %w", i, w.Name.Unwrap(), err)
		}
	}
	if raw.Policy != nil {
		pol, err := yaml.Marshal(raw.Policy)
		if err != nil {
			return nil, fmt.Errorf("composition: re-encode policy: %w", err)
		}
		if err := yaml.Unmarshal(pol, &cfg.Policy); err != nil {
			return nil, fmt.Errorf("composition: decode policy: %w", err)
		}
	}
	if raw.Processing != nil {
		proc, err := yaml.Marshal(raw.Processing)
		if err != nil {
			return nil, fmt.Errorf("composition: re-encode processing: %w", err)
		}
		if err := yaml.Unmarshal(proc, &cfg.Processing); err != nil {
			return nil, fmt.Errorf("composition: decode processing: %w", err)
		}
	}
	cfg.Normalize() // пустая version → SupportedVersion (унифицировано с Validate)
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("composition: config: %w", err)
	}

	// HTTP-адаптер.
	allowedOrigins := make([]string, 0, len(raw.HTTP.AllowedOrigins))
	for _, o := range raw.HTTP.AllowedOrigins {
		allowedOrigins = append(allowedOrigins, o.Unwrap())
	}
	httpCfg := httpapi.Config{
		AllowedOrigins:        allowedOrigins,
		AllowCredentials:      raw.HTTP.AllowCredentials.Unwrap(),
		CacheControl:          raw.HTTP.CacheControl.Unwrap(),
		NotFoundCacheControl:  raw.HTTP.NotFoundCacheControl.Unwrap(),
		ReferrerPolicy:        raw.HTTP.ReferrerPolicy.Unwrap(),
		CSP:                   raw.HTTP.CSP.Unwrap(),
		MaxURLLen:             int(raw.HTTP.MaxURLLen.Unwrap()),
		MaxConcurrentRequests: int(raw.HTTP.MaxConcurrentRequests.Unwrap()),
		NotFound: httpapi.NotFoundConfig{
			Pixel:    raw.HTTP.NotFound.Pixel.Unwrap(),
			Image:    raw.HTTP.NotFound.Image.Unwrap(),
			Page:     raw.HTTP.NotFound.Page.Unwrap(),
			Redirect: raw.HTTP.NotFound.Redirect.Unwrap(),
		},
		SourceFallback: httpapi.SourceFallbackConfig{
			Enabled:      raw.HTTP.SourceFallback.Enabled.Unwrap(),
			Status:       int(raw.HTTP.SourceFallback.Status.Unwrap()),
			CacheControl: raw.HTTP.SourceFallback.CacheControl.Unwrap(),
		},
		ServeOriginal: httpapi.ServeOriginalConfig{
			Enabled:      raw.HTTP.ServeOriginal.Enabled.Unwrap(),
			CacheControl: raw.HTTP.ServeOriginal.CacheControl.Unwrap(),
		},
		Admin: httpapi.AdminConfig{
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
			return nil, fmt.Errorf("composition: admin.wait-timeout: %w", err)
		}
		if d < 0 {
			return nil, fmt.Errorf("composition: admin.wait-timeout: negative duration %q", raw.Admin.WaitTimeout.Unwrap())
		}
		httpCfg.Admin.WaitTimeout = d
	}
	// Asset errors observability (fail-fast на неверных значениях).
	assetErrorsEnabled := true
	if raw.Observability.AssetErrors.Enabled.Set {
		assetErrorsEnabled = raw.Observability.AssetErrors.Enabled.Value.Unwrap()
	}
	httpCfg.AssetErrors = httpapi.AssetErrorConfig{
		Enabled:  assetErrorsEnabled,
		LogLevel: raw.Observability.AssetErrors.LogLevel.Unwrap(),
		TopPaths: httpapi.TopPathsConfig{
			Enabled:    raw.Observability.AssetErrors.TopPaths.Enabled.Unwrap(),
			MaxEntries: int(raw.Observability.AssetErrors.TopPaths.MaxEntries.Unwrap()),
			ReportTop:  int(raw.Observability.AssetErrors.TopPaths.ReportTop.Unwrap()),
			KeyMode:    raw.Observability.AssetErrors.TopPaths.KeyMode.Unwrap(),
		},
	}
	if raw.HTTP.GenerateTimeout.Unwrap() != "" {
		d, err := time.ParseDuration(raw.HTTP.GenerateTimeout.Unwrap())
		if err != nil {
			return nil, fmt.Errorf("composition: http.generate-timeout: %w", err)
		}
		if d < 0 {
			return nil, fmt.Errorf("composition: http.generate-timeout: negative duration %q", raw.HTTP.GenerateTimeout.Unwrap())
		}
		httpCfg.GenerateTimeout = d
	}
	httpCfg.Normalize() // применяем умолчания (статус 404, cache-control и т.д.)
	if err := httpCfg.Validate(); err != nil {
		return nil, fmt.Errorf("composition: http: %w", err)
	}

	// Хранилища.
	source, err := raw.Source.toRemoteStorageConfig()
	if err != nil {
		return nil, fmt.Errorf("composition: source: %w", err)
	}
	result, err := raw.Result.toRemoteStorageConfig()
	if err != nil {
		return nil, fmt.Errorf("composition: result: %w", err)
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
		return nil, fmt.Errorf("composition: server: %w", err)
	}
	if server.MaxBodyBytes < 0 {
		return nil, fmt.Errorf("composition: server.max-body-bytes: negative value %d", server.MaxBodyBytes)
	}
	if server.MaxBodyBytes == 0 {
		server.MaxBodyBytes = httpapi.DefaultMaxBodyBytes
	}

	// Libvips.
	lv, err := raw.Libvips.build()
	if err != nil {
		return nil, fmt.Errorf("composition: libvips: %w", err)
	}

	// Детектор лиц/объектов (face-crop/object-crop).
	det, err := raw.Detection.build()
	if err != nil {
		return nil, fmt.Errorf("composition: detection: %w", err)
	}

	// Прикладные лимиты.
	limits, err := buildApplicationLimits(raw.Application.Limits)
	if err != nil {
		return nil, err
	}
	if raw.Application.BufferMaxBytes.Unwrap() < 0 {
		return nil, fmt.Errorf("composition: application.buffer-max-bytes: negative value %d", raw.Application.BufferMaxBytes.Unwrap())
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
	// дефолт `<эффективный локальный result-каталог>` (без подкаталога
	// .meta) — применяется на уровне DI (app.go).
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
		Libvips:         lv,
		Detection:       det,
		MetadataEnabled: metadataEnabled,
		MetadataDir:     metadataDir,
		Limits:          limits,
		BufferMaxBytes:  bufferMaxBytes,
		LogLevel:        logLevel,
	}, nil
}

// buildApplicationLimits валидирует и собирает application-level лимиты из
// YAML-представления. Все значения должны быть неотрицательными (fail-fast).
func buildApplicationLimits(raw ApplicationLimitsYAML) (generatev2.Limits, error) {
	if raw.SourceBytes.Unwrap() < 0 {
		return generatev2.Limits{}, fmt.Errorf("composition: application.limits.source-bytes: negative value %d", raw.SourceBytes.Unwrap())
	}
	if raw.OutputBytes.Unwrap() < 0 {
		return generatev2.Limits{}, fmt.Errorf("composition: application.limits.output-bytes: negative value %d", raw.OutputBytes.Unwrap())
	}
	if raw.Pixels.Unwrap() < 0 {
		return generatev2.Limits{}, fmt.Errorf("composition: application.limits.pixels: negative value %d", raw.Pixels.Unwrap())
	}
	return generatev2.Limits{
		SourceBytes: raw.SourceBytes.Unwrap(),
		OutputBytes: raw.OutputBytes.Unwrap(),
		Pixels:      raw.Pixels.Unwrap(),
		Width:       raw.Width.Unwrap(),
		Height:      raw.Height.Unwrap(),
		DPR:         raw.DPR.Unwrap(),
		Frames:      raw.Frames.Unwrap(),
		Duration:    raw.Duration.Unwrap(),
		Concurrency: raw.Concurrency.Unwrap(),
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
		Conn: remote.ConnOptions{
			DialTimeout: 30 * time.Second,
		},
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
		cfg.Conn.DialTimeout = d
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
		cfg.Conn.ReadTimeout = d
	}
	if s.IdleConnTimeout.Unwrap() != "" {
		d, err := time.ParseDuration(s.IdleConnTimeout.Unwrap())
		if err != nil {
			return RemoteStorageConfig{}, fmt.Errorf("idle-conn-timeout: %w", err)
		}
		if d < 0 {
			return RemoteStorageConfig{}, fmt.Errorf("idle-conn-timeout: negative duration %q", s.IdleConnTimeout.Unwrap())
		}
		cfg.Conn.IdleConnTimeout = d
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
	cfg.Conn.MaxAttempts = int(s.MaxAttempts.Unwrap())
	if s.MaxIdleConns.Unwrap() < 0 {
		return RemoteStorageConfig{}, fmt.Errorf("max-idle-conns: negative value %d", s.MaxIdleConns.Unwrap())
	}
	cfg.Conn.MaxIdleConns = int(s.MaxIdleConns.Unwrap())
	if s.MaxConns.Unwrap() < 0 {
		return RemoteStorageConfig{}, fmt.Errorf("max-conns: negative value %d", s.MaxConns.Unwrap())
	}
	cfg.Conn.MaxConns = int(s.MaxConns.Unwrap())
	if s.MaxIdleConnsPerHost.Unwrap() < 0 {
		return RemoteStorageConfig{}, fmt.Errorf("max-idle-conns-per-host: negative value %d", s.MaxIdleConnsPerHost.Unwrap())
	}
	cfg.Conn.MaxIdleConnsPerHost = int(s.MaxIdleConnsPerHost.Unwrap())
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
	// Per-format параметры кодировщиков: fail-fast валидация диапазонов
	// на старте (невалидное значение — ошибка конфигурации, не runtime).
	cfg.Encoders = libvips.EncoderParams{
		WebPReductionEffort: int(l.Encoders.WebPReductionEffort.Unwrap()),
		AVIFSpeed:           int(l.Encoders.AVIFSpeed.Unwrap()),
		PNGCompression:      int(l.Encoders.PNGCompressionLevel.Unwrap()),
		JXLEffort:           int(l.Encoders.JXLEffort.Unwrap()),
		JPEGProgressive:     l.Encoders.JPEGProgressive.Unwrap(),
		PNGInterlace:        l.Encoders.PNGInterlace.Unwrap(),
		PNGPalette:          l.Encoders.PNGPalette.Unwrap(),
		PNGPaletteColors:    int(l.Encoders.PNGPaletteColors.Unwrap()),
		PNGPaletteBitDepth:  int(l.Encoders.PNGPaletteBitDepth.Unwrap()),
		GIFBitDepth:         int(l.Encoders.GIFBitDepth.Unwrap()),
	}
	if v := cfg.Encoders.WebPReductionEffort; v < 0 || v > 6 {
		return LibvipsConfig{}, fmt.Errorf("encoders.webp-reduction-effort: must be in [0,6], got %d", v)
	}
	if v := cfg.Encoders.AVIFSpeed; v < 0 || v > 9 {
		return LibvipsConfig{}, fmt.Errorf("encoders.avif-speed: must be in [0,9], got %d", v)
	}
	if v := cfg.Encoders.PNGCompression; v < 0 || v > 9 {
		return LibvipsConfig{}, fmt.Errorf("encoders.png-compression-level: must be in [0,9], got %d", v)
	}
	if v := cfg.Encoders.JXLEffort; v < 0 || v > 9 {
		return LibvipsConfig{}, fmt.Errorf("encoders.jxl-effort: must be in [0,9], got %d", v)
	}
	if v := cfg.Encoders.PNGPaletteColors; v < 0 || v > 256 {
		return LibvipsConfig{}, fmt.Errorf("encoders.png-palette-colors: must be in [0,256], got %d", v)
	}
	if v := cfg.Encoders.PNGPaletteBitDepth; v < 0 || v > 8 {
		return LibvipsConfig{}, fmt.Errorf("encoders.png-palette-bit-depth: must be in [0,8], got %d", v)
	}
	if v := cfg.Encoders.GIFBitDepth; v < 0 || v > 8 {
		return LibvipsConfig{}, fmt.Errorf("encoders.gif-bit-depth: must be in [0,8], got %d", v)
	}
	// Shrink-on-load: nil (ключ не задан) = включено по умолчанию.
	if l.ShrinkOnLoad.Enabled.Set {
		cfg.ShrinkOnLoad = libvips.NewShrinkOnLoadOpts(l.ShrinkOnLoad.Enabled.Value.Unwrap(), true)
	}
	// Кэш ватермарок (Фаза 3): fail-fast валидация значений на старте.
	wc := libvips.WatermarkCacheOpts{Enabled: true}
	if l.WatermarkCache.Enabled.Set {
		wc.Enabled = l.WatermarkCache.Enabled.Value.Unwrap()
	}
	wc.MaxFiles = int(l.WatermarkCache.MaxFiles.Unwrap())
	wc.MaxBytes = l.WatermarkCache.MaxBytes.Unwrap()
	if l.WatermarkCache.TTL.Unwrap() != "" {
		d, err := time.ParseDuration(l.WatermarkCache.TTL.Unwrap())
		if err != nil {
			return LibvipsConfig{}, fmt.Errorf("watermark-cache.ttl: %w", err)
		}
		if d < 0 {
			return LibvipsConfig{}, fmt.Errorf("watermark-cache.ttl: negative duration %q", l.WatermarkCache.TTL.Unwrap())
		}
		wc.TTL = d
	}
	if err := wc.Validate(); err != nil {
		return LibvipsConfig{}, fmt.Errorf("watermark-cache: %w", err)
	}
	cfg.WatermarkCache = wc
	// Detection-семофор (Фаза 4): fail-fast валидация значений на старте.
	ds := libvips.DetectionSemaphoreOpts{
		Concurrency: int(l.DetectionSem.Concurrency.Unwrap()),
	}
	if l.DetectionSem.MaxWait.Unwrap() != "" {
		d, err := time.ParseDuration(l.DetectionSem.MaxWait.Unwrap())
		if err != nil {
			return LibvipsConfig{}, fmt.Errorf("detection.max-wait: %w", err)
		}
		if d < 0 {
			return LibvipsConfig{}, fmt.Errorf("detection.max-wait: negative duration %q", l.DetectionSem.MaxWait.Unwrap())
		}
		ds.MaxWait = d
	}
	if err := ds.Validate(); err != nil {
		return LibvipsConfig{}, fmt.Errorf("detection: %w", err)
	}
	cfg.DetectionSem = ds
	// Цветовой менеджмент (Фаза 5a): строгая политика mode (strip/transform/
	// keep). Empty = strip (дефолт, обратная совместимость); неизвестное
	// значение — fail-fast ошибка конфигурации.
	colorMode, err := libvips.ParseColorMode(l.Color.Mode.Unwrap())
	if err != nil {
		return LibvipsConfig{}, fmt.Errorf("color: %w", err)
	}
	cfg.Color = colorMode
	// Operation cache (Фаза 5b): nil (ключ не задан) = включено по умолчанию.
	if l.OperationCache.Enabled.Set {
		cfg.OperationCache = libvips.NewOperationCacheOpts(l.OperationCache.Enabled.Value.Unwrap(), true)
	}
	// Интервал сбора vips-метрик.
	if l.MetricsInterval.Unwrap() != "" {
		d, err := time.ParseDuration(l.MetricsInterval.Unwrap())
		if err != nil {
			return LibvipsConfig{}, fmt.Errorf("metrics-interval: %w", err)
		}
		if d < 0 {
			return LibvipsConfig{}, fmt.Errorf("metrics-interval: negative duration %q", l.MetricsInterval.Unwrap())
		}
		cfg.VipsMetricsInterval = d
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
		OnnxRuntimeLib:      d.OnnxRuntimeLib.Unwrap(),
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
			return fmt.Errorf("composition: %s storage: s3 bucket is required", role)
		}
		if (cfg.AccessKey == "") != (cfg.SecretKey == "") {
			return fmt.Errorf("composition: %s storage: s3 access-key and secret-key must be set together", role)
		}
	case StorageSFTP:
		if cfg.Addr == "" || cfg.User == "" {
			return fmt.Errorf("composition: %s storage: sftp addr and user are required", role)
		}
		if cfg.HostKeyFingerprint == "" {
			return fmt.Errorf("composition: %s storage: sftp host-key-fingerprint is required (SHA256:...)", role)
		}
	case StorageFTP, StorageFTPS:
		if cfg.Addr == "" {
			return fmt.Errorf("composition: %s storage: %s addr is required", role, cfg.Kind)
		}
		if cfg.Kind == StorageFTPS && !cfg.TLSVerify {
			return fmt.Errorf("composition: %s storage: ftps tls-verify=false is forbidden; set tls-verify: true", role)
		}
	case StorageHTTP:
		if cfg.BaseURL == "" {
			return fmt.Errorf("composition: %s storage: http base-url is required", role)
		}
	}
	return nil
}
