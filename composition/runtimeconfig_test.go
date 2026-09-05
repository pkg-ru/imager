package composition

import (
	"strings"
	"testing"
	"time"

	"gitverse.ru/pkg-ru/imager/adapters/httpapi"
)

// TestParseRuntimeConfigDetection проверяет декодирование секции detection
// и применение значений по умолчанию.
func TestParseRuntimeConfigDetection(t *testing.T) {
	rc, err := ParseRuntimeConfig([]byte(`
version: "1"
policy: {}
detection:
  face-model: /models/face.onnx
  object-model: /models/obj.onnx
  onnx-runtime-lib: /usr/lib/libonnxruntime.so.1.29.0
  confidence-threshold: 0.6
  max-objects: 10
  margin: 0.2
`))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	if rc.Detection.FaceModel != "/models/face.onnx" {
		t.Errorf("FaceModel = %q, want /models/face.onnx", rc.Detection.FaceModel)
	}
	if rc.Detection.ObjectModel != "/models/obj.onnx" {
		t.Errorf("ObjectModel = %q, want /models/obj.onnx", rc.Detection.ObjectModel)
	}
	if rc.Detection.OnnxRuntimeLib != "/usr/lib/libonnxruntime.so.1.29.0" {
		t.Errorf("OnnxRuntimeLib = %q, want /usr/lib/libonnxruntime.so.1.29.0", rc.Detection.OnnxRuntimeLib)
	}
	if rc.Detection.ConfidenceThreshold != 0.6 {
		t.Errorf("ConfidenceThreshold = %v, want 0.6", rc.Detection.ConfidenceThreshold)
	}
	if rc.Detection.MaxObjects != 10 {
		t.Errorf("MaxObjects = %d, want 10", rc.Detection.MaxObjects)
	}
	if rc.Detection.Margin != 0.2 {
		t.Errorf("Margin = %v, want 0.2", rc.Detection.Margin)
	}
}

// TestParseRuntimeConfigDetectionDefaults проверяет дефолты секции detection
// при полностью пустой секции (модели не заданы).
func TestParseRuntimeConfigDetectionDefaults(t *testing.T) {
	rc, err := ParseRuntimeConfig([]byte(`
version: "1"
policy: {}
`))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	if rc.Detection.FaceModel != "" || rc.Detection.ObjectModel != "" {
		t.Errorf("models = %q/%q, want empty", rc.Detection.FaceModel, rc.Detection.ObjectModel)
	}
	if rc.Detection.OnnxRuntimeLib != "" {
		t.Errorf("OnnxRuntimeLib = %q, want empty (autodetect)", rc.Detection.OnnxRuntimeLib)
	}
	if rc.Detection.ConfidenceThreshold != 0.5 {
		t.Errorf("ConfidenceThreshold = %v, want default 0.5", rc.Detection.ConfidenceThreshold)
	}
	if rc.Detection.MaxObjects != 5 {
		t.Errorf("MaxObjects = %d, want default 5", rc.Detection.MaxObjects)
	}
	if rc.Detection.Margin != 0.1 {
		t.Errorf("Margin = %v, want default 0.1", rc.Detection.Margin)
	}
}

// TestParseRuntimeConfigDetectionInvalid проверяет fail-fast валидацию
// некорректных значений секции detection.
func TestParseRuntimeConfigDetectionInvalid(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{
			name: "threshold>1",
			yaml: `
version: "1"
policy: {}
detection:
  confidence-threshold: 1.5
`,
		},
		{
			name: "threshold<0",
			yaml: `
version: "1"
policy: {}
detection:
  confidence-threshold: -0.1
`,
		},
		{
			name: "max-objects=0",
			yaml: `
version: "1"
policy: {}
detection:
  max-objects: 0
`,
		},
		{
			name: "max-objects negative",
			yaml: `
version: "1"
policy: {}
detection:
  max-objects: -2
`,
		},
		{
			name: "margin negative",
			yaml: `
version: "1"
policy: {}
detection:
  margin: -0.1
`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseRuntimeConfig([]byte(tc.yaml)); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

// TestParseRuntimeConfigDetectionUnknownField проверяет strict-декодирование:
// неизвестный ключ в секции detection — ошибка старта.
func TestParseRuntimeConfigDetectionUnknownField(t *testing.T) {
	yaml := `
version: "1"
policy: {}
detection:
  enabled: true
`
	if _, err := ParseRuntimeConfig([]byte(yaml)); err == nil {
		t.Fatal("expected error for unknown detection.enabled field")
	}
}

// TestParseRuntimeConfigMetadataDefault проверяет дефолт metadata.enabled=true
// при отсутствии секции metadata.
func TestParseRuntimeConfigMetadataDefault(t *testing.T) {
	rc, err := ParseRuntimeConfig([]byte(`
version: "1"
policy: {}
`))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	if !rc.MetadataEnabled {
		t.Errorf("MetadataEnabled = false, want default true")
	}
}

// TestParseRuntimeConfigMetadataEnabledFalse проверяет явное
// metadata.enabled: false.
func TestParseRuntimeConfigMetadataEnabledFalse(t *testing.T) {
	rc, err := ParseRuntimeConfig([]byte(`
version: "1"
policy: {}
metadata:
  enabled: false
`))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	if rc.MetadataEnabled {
		t.Errorf("MetadataEnabled = true, want false")
	}
}

// TestParseRuntimeConfigMetadataDirAccepted проверяет, что metadata.dir задаёт
// явный локальный корень sidecar-хранилища, независимый от хранилищ
// source/result. Пусто = дефолт (пустая строка → деривация в DI).
func TestParseRuntimeConfigMetadataDir(t *testing.T) {
	rc, err := ParseRuntimeConfig([]byte(`
version: "1"
policy: {}
metadata:
  dir: /custom/meta
`))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig with metadata.dir: %v", err)
	}
	if rc.MetadataDir != "/custom/meta" {
		t.Errorf("MetadataDir = %q, want /custom/meta", rc.MetadataDir)
	}
	if !rc.MetadataEnabled {
		t.Errorf("MetadataEnabled = false, want default true")
	}
}

// TestParseRuntimeConfigMetadataDirDefault проверяет, что при отсутствии
// metadata.dir поле остаётся пустым (дефолт <resultRoot> применяется
// на уровне DI — app.go).
func TestParseRuntimeConfigMetadataDirDefault(t *testing.T) {
	rc, err := ParseRuntimeConfig([]byte("version: \"1\"\n" +
		"policy: {}\n" +
		"metadata:\n" +
		"  enabled: true\n"))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	if rc.MetadataDir != "" {
		t.Errorf("MetadataDir = %q, want empty (default)", rc.MetadataDir)
	}
}

// TestParseRuntimeConfigMetadataUnknownField проверяет strict-декодирование:
// неизвестный ключ в секции metadata — ошибка старта.
func TestParseRuntimeConfigMetadataUnknownField(t *testing.T) {
	yaml := `
version: "1"
policy: {}
metadata:
  ttl: 60
`
	if _, err := ParseRuntimeConfig([]byte(yaml)); err == nil {
		t.Fatal("expected error for unknown metadata.ttl field")
	}
}

// TestParseRuntimeConfigSourceFallback проверяет декодирование секции
// http.source-fallback и применение значений по умолчанию.
func TestParseRuntimeConfigSourceFallback(t *testing.T) {
	rc, err := ParseRuntimeConfig([]byte(`
version: "1"
policy: {}
http:
  source-fallback:
    enabled: true
    status: 200
    cache-control: public, max-age=60
`))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	if !rc.HTTP.SourceFallback.Enabled {
		t.Errorf("SourceFallback.Enabled = false, want true")
	}
	if rc.HTTP.SourceFallback.Status != 200 {
		t.Errorf("SourceFallback.Status = %d, want 200", rc.HTTP.SourceFallback.Status)
	}
	if rc.HTTP.SourceFallback.CacheControl != "public, max-age=60" {
		t.Errorf("SourceFallback.CacheControl = %q, want public, max-age=60", rc.HTTP.SourceFallback.CacheControl)
	}
}

// TestParseRuntimeConfigSourceFallbackDefaults проверяет дефолты source-fallback
// при пустой секции: выключен, статус 404, cache-control "no-store".
func TestParseRuntimeConfigSourceFallbackDefaults(t *testing.T) {
	rc, err := ParseRuntimeConfig([]byte(`
version: "1"
policy: {}
`))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	if rc.HTTP.SourceFallback.Enabled {
		t.Errorf("SourceFallback.Enabled = true, want default false")
	}
	if rc.HTTP.SourceFallback.Status != 404 {
		t.Errorf("SourceFallback.Status = %d, want default 404", rc.HTTP.SourceFallback.Status)
	}
	if rc.HTTP.SourceFallback.CacheControl != "no-store" {
		t.Errorf("SourceFallback.CacheControl = %q, want default no-store", rc.HTTP.SourceFallback.CacheControl)
	}
}

// TestParseRuntimeConfigServeOriginal проверяет декодирование отдельной
// секции http.serve-original из YAML (enabled true/false, cache-control).
func TestParseRuntimeConfigServeOriginal(t *testing.T) {
	rc, err := ParseRuntimeConfig([]byte(`
version: "1"
policy: {}
http:
  serve-original:
    enabled: true
    cache-control: "public, max-age=60"
`))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	if !rc.HTTP.ServeOriginal.Enabled {
		t.Errorf("ServeOriginal.Enabled = false, want true")
	}
	if got := rc.HTTP.ServeOriginal.CacheControl; got != "public, max-age=60" {
		t.Errorf("ServeOriginal.CacheControl = %q, want %q", got, "public, max-age=60")
	}

	rc, err = ParseRuntimeConfig([]byte(`
version: "1"
policy: {}
http:
  serve-original:
    enabled: false
`))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	if rc.HTTP.ServeOriginal.Enabled {
		t.Errorf("ServeOriginal.Enabled = true, want false")
	}
}

// TestParseRuntimeConfigServeOriginalDefault проверяет дефолты секции
// http.serve-original при её отсутствии в YAML: enabled=false,
// cache-control="no-store" (после Normalize).
func TestParseRuntimeConfigServeOriginalDefault(t *testing.T) {
	rc, err := ParseRuntimeConfig([]byte(`
version: "1"
policy: {}
http:
  source-fallback:
    enabled: true
`))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	if rc.HTTP.ServeOriginal.Enabled {
		t.Errorf("ServeOriginal.Enabled = true, want default false")
	}
	if got := rc.HTTP.ServeOriginal.CacheControl; got != "no-store" {
		t.Errorf("ServeOriginal.CacheControl = %q, want default no-store", got)
	}
}

// TestParseRuntimeConfigSourceFallbackInvalid проверяет fail-fast валидацию
// некорректного статуса source-fallback.
func TestParseRuntimeConfigSourceFallbackInvalid(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{
			name: "status=500",
			yaml: `
version: "1"
policy: {}
http:
  source-fallback:
    status: 500
`,
		},
		{
			name: "status=201",
			yaml: `
version: "1"
policy: {}
http:
  source-fallback:
    status: 201
`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseRuntimeConfig([]byte(tc.yaml)); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

// TestParseRuntimeConfigAssetErrors проверяет декодирование секции
// observability.asset-errors.
func TestParseRuntimeConfigAssetErrors(t *testing.T) {
	rc, err := ParseRuntimeConfig([]byte(`
version: "1"
policy: {}
observability:
  asset-errors:
    enabled: true
    log-level: error
    top-paths:
      enabled: true
      max-entries: 512
      report-top: 10
      key-mode: hash
`))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	if !rc.HTTP.AssetErrors.Enabled {
		t.Errorf("AssetErrors.Enabled = false, want true")
	}
	if rc.HTTP.AssetErrors.LogLevel != "error" {
		t.Errorf("AssetErrors.LogLevel = %q, want error", rc.HTTP.AssetErrors.LogLevel)
	}
	tp := rc.HTTP.AssetErrors.TopPaths
	if !tp.Enabled {
		t.Errorf("TopPaths.Enabled = false, want true")
	}
	if tp.MaxEntries != 512 {
		t.Errorf("TopPaths.MaxEntries = %d, want 512", tp.MaxEntries)
	}
	if tp.ReportTop != 10 {
		t.Errorf("TopPaths.ReportTop = %d, want 10", tp.ReportTop)
	}
	if tp.KeyMode != "hash" {
		t.Errorf("TopPaths.KeyMode = %q, want hash", tp.KeyMode)
	}
}

// TestParseRuntimeConfigAssetErrorsDefaults проверяет дефолты asset-errors:
// enabled=true, log-level=warn, top-paths выключен.
func TestParseRuntimeConfigAssetErrorsDefaults(t *testing.T) {
	rc, err := ParseRuntimeConfig([]byte(`
version: "1"
policy: {}
`))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	if !rc.HTTP.AssetErrors.Enabled {
		t.Errorf("AssetErrors.Enabled = false, want default true")
	}
	if rc.HTTP.AssetErrors.LogLevel != "warn" {
		t.Errorf("AssetErrors.LogLevel = %q, want default warn", rc.HTTP.AssetErrors.LogLevel)
	}
	if rc.HTTP.AssetErrors.TopPaths.Enabled {
		t.Errorf("TopPaths.Enabled = true, want default false")
	}
}

// TestParseRuntimeConfigAssetErrorsInvalid проверяет fail-fast валидацию
// некорректных значений asset-errors.
func TestParseRuntimeConfigAssetErrorsInvalid(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{
			name: "bad log-level",
			yaml: `
version: "1"
policy: {}
observability:
  asset-errors:
    log-level: verbose
`,
		},
		{
			name: "bad key-mode",
			yaml: `
version: "1"
policy: {}
observability:
  asset-errors:
    top-paths:
      enabled: true
      key-mode: url
`,
		},
		{
			name: "negative max-entries",
			yaml: `
version: "1"
policy: {}
observability:
  asset-errors:
    top-paths:
      enabled: true
      max-entries: -1
`,
		},
		{
			name: "negative report-top",
			yaml: `
version: "1"
policy: {}
observability:
  asset-errors:
    top-paths:
      enabled: true
      report-top: -5
`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseRuntimeConfig([]byte(tc.yaml)); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

// TestParseRuntimeConfigAdmin проверяет декодирование секции admin и
// применение значений по умолчанию.
func TestParseRuntimeConfigAdmin(t *testing.T) {
	rc, err := ParseRuntimeConfig([]byte(strings.ReplaceAll(`
version: "1"
policy: {}
admin:
  enabled: true
  token: "secret"
  workers: 4
  queue-size: 128
  wait-timeout: "120s"
`, "\t", "  ")))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	if !rc.Admin.Enabled {
		t.Error("Admin.Enabled = false, want true")
	}
	if rc.Admin.Token != "secret" {
		t.Errorf("Admin.Token = %q, want secret", rc.Admin.Token)
	}
	if rc.Admin.Workers != 4 {
		t.Errorf("Admin.Workers = %d, want 4", rc.Admin.Workers)
	}
	if rc.Admin.QueueSize != 128 {
		t.Errorf("Admin.QueueSize = %d, want 128", rc.Admin.QueueSize)
	}
	if rc.Admin.WaitTimeout != 120*time.Second {
		t.Errorf("Admin.WaitTimeout = %v, want 120s", rc.Admin.WaitTimeout)
	}
}

// TestParseRuntimeConfigAdminDefaults проверяет дефолты admin при пустой
// секции (выключен, workers=2, queue-size=64, wait-timeout=300s).
func TestParseRuntimeConfigAdminDefaults(t *testing.T) {
	rc, err := ParseRuntimeConfig([]byte(`
version: "1"
policy: {}
`))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	if rc.Admin.Enabled {
		t.Error("Admin.Enabled = true, want false (disabled by default)")
	}
	if rc.Admin.Workers != httpapi.DefaultAdminWorkers {
		t.Errorf("Admin.Workers = %d, want default %d", rc.Admin.Workers, httpapi.DefaultAdminWorkers)
	}
	if rc.Admin.QueueSize != httpapi.DefaultAdminQueueSize {
		t.Errorf("Admin.QueueSize = %d, want default %d", rc.Admin.QueueSize, httpapi.DefaultAdminQueueSize)
	}
	if rc.Admin.WaitTimeout != httpapi.DefaultAdminWaitTimeout {
		t.Errorf("Admin.WaitTimeout = %v, want default %v", rc.Admin.WaitTimeout, httpapi.DefaultAdminWaitTimeout)
	}
}

// TestParseRuntimeConfigAdminFailFastEmptyToken — enabled=true при пустом
// token → fail-fast ошибка старта.
func TestParseRuntimeConfigAdminFailFastEmptyToken(t *testing.T) {
	_, err := ParseRuntimeConfig([]byte(`
version: "1"
policy: {}
admin:
  enabled: true
`))
	if err == nil {
		t.Fatal("expected error for admin.enabled=true with empty token")
	}
}

// TestParseRuntimeConfigAdminInvalid проверяет fail-fast валидацию
// некорректных значений секции admin.
func TestParseRuntimeConfigAdminInvalid(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{
			name: "negative workers",
			yaml: strings.ReplaceAll(`
version: "1"
policy: {}
admin:
  enabled: true
  token: "x"
  workers: -1
`, "\t", "  "),
		},
		{
			name: "negative queue-size",
			yaml: strings.ReplaceAll(`
version: "1"
policy: {}
admin:
  enabled: true
  token: "x"
  queue-size: -5
`, "\t", "  "),
		},
		{
			name: "negative wait-timeout",
			yaml: strings.ReplaceAll(`
version: "1"
policy: {}
admin:
  enabled: true
  token: "x"
  wait-timeout: "-10s"
`, "\t", "  "),
		},
		{
			name: "invalid wait-timeout",
			yaml: strings.ReplaceAll(`
version: "1"
policy: {}
admin:
  enabled: true
  token: "x"
  wait-timeout: "bogus"
`, "\t", "  "),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseRuntimeConfig([]byte(tc.yaml)); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

// TestParseRuntimeConfigLibvipsEncoders проверяет декодирование ЕДИНОЙ
// top-level секции encoders в libvips.EncodersConfig.
func TestParseRuntimeConfigLibvipsEncoders(t *testing.T) {
	rc, err := ParseRuntimeConfig([]byte(strings.ReplaceAll(`
version: "1"
policy: {}
encoders:
	default-quality: 85
	jpeg:
		progressive: true
	webp:
		reduction-effort: 2
	avif:
		speed: 8
	jxl:
		effort: 3
	png:
		compression-level: 9
		interlace: true
		palette: true
		palette-colors: 128
		palette-bit-depth: 4
	apng:
		compression-level: 7
	gif:
		effort: 5
		bit-depth: 4
`, "\t", "  ")))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	// Default-quality из новой секции.
	if rc.Encoders.DefaultQuality != 85 {
		t.Errorf("Encoders.DefaultQuality = %d, want 85", rc.Encoders.DefaultQuality)
	}
	// Проброс в libvips.EncodersConfig: глобальные параметры из YAML.
	e := rc.Libvips.EncodersConfig
	if got := ptrOr(e.Formats["webp"].ReductionEffort, -1); got != 2 {
		t.Errorf("webp.reduction-effort = %d, want 2", got)
	}
	if got := ptrOr(e.Formats["avif"].Speed, -1); got != 8 {
		t.Errorf("avif.speed = %d, want 8", got)
	}
	if got := ptrOr(e.Formats["png"].CompressionLevel, -1); got != 9 {
		t.Errorf("png.compression-level = %d, want 9", got)
	}
	if got := ptrOr(e.Formats["jxl"].Effort, -1); got != 3 {
		t.Errorf("jxl.effort = %d, want 3", got)
	}
	if e.Formats["jpeg"].Progressive == nil || !*e.Formats["jpeg"].Progressive {
		t.Error("jpeg.progressive must be true")
	}
	if e.Formats["png"].Interlace == nil || !*e.Formats["png"].Interlace {
		t.Error("png.interlace must be true")
	}
	if e.Formats["png"].Palette == nil || !*e.Formats["png"].Palette {
		t.Error("png.palette must be true")
	}
	if got := ptrOr(e.Formats["png"].PaletteColors, -1); got != 128 {
		t.Errorf("png.palette-colors = %d, want 128", got)
	}
	if got := ptrOr(e.Formats["png"].PaletteBitDepth, -1); got != 4 {
		t.Errorf("png.palette-bit-depth = %d, want 4", got)
	}
	if got := ptrOr(e.Formats["gif"].BitDepth, -1); got != 4 {
		t.Errorf("gif.bit-depth = %d, want 4", got)
	}
	if got := ptrOr(e.Formats["gif"].Effort, -1); got != 5 {
		t.Errorf("gif.effort = %d, want 5", got)
	}
	if got := ptrOr(e.Formats["apng"].CompressionLevel, -1); got != 7 {
		t.Errorf("apng.compression-level = %d, want 7", got)
	}
}

// TestParseRuntimeConfigLibvipsEncodersDefaults проверяет дефолты ЕДИНОЙ
// секции encoders при её отсутствии: EncodersConfig.DefaultQuality = 80,
// группы форматов присутствуют с nil-параметрами (дефолты применяются на
// этапе разрешения через domain/encoding).
func TestParseRuntimeConfigLibvipsEncodersDefaults(t *testing.T) {
	rc, err := ParseRuntimeConfig([]byte(strings.ReplaceAll(`
version: "1"
policy: {}
`, "\t", "  ")))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	// Default-quality: отсутствие в YAML = дефолт кода 80.
	if rc.Encoders.DefaultQuality != 80 {
		t.Errorf("Encoders.DefaultQuality = %d, want 80", rc.Encoders.DefaultQuality)
	}
	// Без заданных параметров группы форматов содержат только nil — дефолты
	// применяются на этапе разрешения (domain/encoding.Resolve + автомаппинг).
	e := rc.Libvips.EncodersConfig
	if e.DefaultQuality != 80 {
		t.Errorf("EncodersConfig.DefaultQuality = %d, want 80", e.DefaultQuality)
	}
	for _, f := range []string{"jpeg", "webp", "avif", "heif", "jxl", "png", "apng", "gif"} {
		fc, ok := e.Formats[f]
		if !ok {
			t.Errorf("EncodersConfig.Formats[%q] missing", f)
			continue
		}
		if fc.Quality != nil || fc.Progressive != nil || fc.ReductionEffort != nil ||
			fc.Lossless != nil || fc.NearLossless != nil || fc.Speed != nil ||
			fc.Effort != nil || fc.CompressionLevel != nil || fc.Interlace != nil ||
			fc.Palette != nil || fc.PaletteColors != nil || fc.PaletteBitDepth != nil ||
			fc.Dither != nil || fc.BitDepth != nil {
			t.Errorf("EncodersConfig.Formats[%q] = %+v, want all nil (unset) with empty yaml", f, fc)
		}
	}
	// Все группы форматов присутствуют в формате EncodersConfig.
	for _, f := range []string{"jpeg", "webp", "avif", "heif", "jxl", "png", "apng", "gif"} {
		if _, ok := rc.Encoders.Formats[f]; !ok {
			t.Errorf("Encoders.Formats[%q] missing", f)
		}
	}
}

// TestParseRuntimeConfigLibvipsShrinkOnLoad — секция libvips.shrink-on-load:
// явное значение, отсутствие (дефолт = включено) и неизвестный ключ
// (strict-декодирование).
func TestParseRuntimeConfigLibvipsShrinkOnLoad(t *testing.T) {
	t.Run("explicit false", func(t *testing.T) {
		rc, err := ParseRuntimeConfig([]byte(strings.ReplaceAll(`
version: "1"
policy: {}
libvips:
	shrink-on-load:
		enabled: false
`, "\t", "  ")))
		if err != nil {
			t.Fatalf("ParseRuntimeConfig: %v", err)
		}
		if rc.Libvips.ShrinkOnLoad.Enabled() {
			t.Error("shrink-on-load.enabled=false must disable shrink-on-load")
		}
	})
	t.Run("explicit true", func(t *testing.T) {
		rc, err := ParseRuntimeConfig([]byte(strings.ReplaceAll(`
version: "1"
policy: {}
libvips:
	shrink-on-load:
		enabled: true
`, "\t", "  ")))
		if err != nil {
			t.Fatalf("ParseRuntimeConfig: %v", err)
		}
		if !rc.Libvips.ShrinkOnLoad.Enabled() {
			t.Error("shrink-on-load.enabled=true must keep shrink-on-load enabled")
		}
	})
	t.Run("absent defaults to enabled", func(t *testing.T) {
		rc, err := ParseRuntimeConfig([]byte(strings.ReplaceAll(`
version: "1"
policy: {}
`, "\t", "  ")))
		if err != nil {
			t.Fatalf("ParseRuntimeConfig: %v", err)
		}
		if !rc.Libvips.ShrinkOnLoad.Enabled() {
			t.Error("absent shrink-on-load must default to enabled")
		}
	})
	t.Run("unknown field rejected", func(t *testing.T) {
		yaml := strings.ReplaceAll(`
version: "1"
policy: {}
libvips:
  shrink-on-load:
    jpeg-quality: 80
`, "\t", "  ")
		if _, err := ParseRuntimeConfig([]byte(yaml)); err == nil {
			t.Fatal("expected error for unknown libvips.shrink-on-load.jpeg-quality field")
		}
	})
}

// TestParseRuntimeConfigLibvipsEncodersInvalid — fail-fast валидация
// диапазонов ЕДИНОЙ секции encoders по реестру domain/encoding: невалидное
// значение — ошибка старта, не runtime.
func TestParseRuntimeConfigLibvipsEncodersInvalid(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{
			name: "default-quality > 100",
			yaml: `
version: "1"
policy: {}
encoders:
	default-quality: 101
`,
		},
		{
			name: "default-quality negative",
			yaml: `
version: "1"
policy: {}
encoders:
	default-quality: -5
`,
		},
		{
			name: "jpeg quality > 100",
			yaml: `
version: "1"
policy: {}
encoders:
	jpeg:
		quality: 101
`,
		},
		{
			name: "jpeg quality negative",
			yaml: `
version: "1"
policy: {}
encoders:
	jpeg:
		quality: -1
`,
		},
		{
			name: "webp reduction-effort > 6",
			yaml: `
version: "1"
policy: {}
encoders:
	webp:
		reduction-effort: 7
`,
		},
		{
			name: "webp reduction-effort negative",
			yaml: `
version: "1"
policy: {}
encoders:
	webp:
		reduction-effort: -1
`,
		},
		{
			name: "avif speed > 9",
			yaml: `
version: "1"
policy: {}
encoders:
	avif:
		speed: 10
`,
		},
		{
			name: "avif speed negative",
			yaml: `
version: "1"
policy: {}
encoders:
	avif:
		speed: -3
`,
		},
		{
			name: "png compression-level > 9",
			yaml: `
version: "1"
policy: {}
encoders:
	png:
		compression-level: 10
`,
		},
		{
			name: "png compression-level negative",
			yaml: `
version: "1"
policy: {}
encoders:
	png:
		compression-level: -1
`,
		},
		{
			name: "jxl effort > 9",
			yaml: `
version: "1"
policy: {}
encoders:
	jxl:
		effort: 10
`,
		},
		{
			name: "jxl effort < 3",
			yaml: `
version: "1"
policy: {}
encoders:
	jxl:
		effort: 2
`,
		},
		{
			name: "png palette-colors > 256",
			yaml: `
version: "1"
policy: {}
encoders:
	png:
		palette-colors: 257
`,
		},
		{
			name: "png palette-colors negative",
			yaml: `
version: "1"
policy: {}
encoders:
	png:
		palette-colors: -4
`,
		},
		{
			name: "png palette-bit-depth > 8",
			yaml: `
version: "1"
policy: {}
encoders:
	png:
		palette-bit-depth: 16
`,
		},
		{
			name: "png palette-bit-depth negative",
			yaml: `
version: "1"
policy: {}
encoders:
	png:
		palette-bit-depth: -2
`,
		},
		{
			name: "png dither > 1",
			yaml: `
version: "1"
policy: {}
encoders:
	png:
		dither: 1.5
`,
		},
		{
			name: "png dither negative",
			yaml: `
version: "1"
policy: {}
encoders:
	png:
		dither: -0.1
`,
		},
		{
			name: "gif effort < 1",
			yaml: `
version: "1"
policy: {}
encoders:
	gif:
		effort: 0
`,
		},
		{
			name: "gif effort > 10",
			yaml: `
version: "1"
policy: {}
encoders:
	gif:
		effort: 11
`,
		},
		{
			name: "gif bit-depth > 8",
			yaml: `
version: "1"
policy: {}
encoders:
	gif:
		bit-depth: 9
`,
		},
		{
			name: "gif bit-depth negative",
			yaml: `
version: "1"
policy: {}
encoders:
	gif:
		bit-depth: -1
`,
		},
		{
			name: "apng compression-level > 9",
			yaml: `
version: "1"
policy: {}
encoders:
	apng:
		compression-level: 10
`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseRuntimeConfig([]byte(tc.yaml)); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

// TestParseRuntimeConfigLibvipsEncodersUnknownField — strict-декодирование:
// неизвестный ключ в top-level секции encoders — ошибка старта.
func TestParseRuntimeConfigLibvipsEncodersUnknownField(t *testing.T) {
	yaml := `
version: "1"
policy: {}
encoders:
  bogus-format:
    quality: 90
`
	if _, err := ParseRuntimeConfig([]byte(yaml)); err == nil {
		t.Fatal("expected error for unknown encoders.bogus-format field")
	}
}

// TestParseRuntimeConfigLibvipsDetectionSem проверяет декодирование секции
// libvips.detection (detection-семофор) и libvips.metrics-interval.
func TestParseRuntimeConfigLibvipsDetectionSem(t *testing.T) {
	rc, err := ParseRuntimeConfig([]byte(`
version: "1"
policy: {}
libvips:
   detection:
     concurrency: 3
     max-wait: "2s"
   metrics-interval: "30s"
`))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	if rc.Libvips.DetectionSem.Concurrency != 3 {
		t.Errorf("detection.concurrency = %d, want 3", rc.Libvips.DetectionSem.Concurrency)
	}
	if rc.Libvips.DetectionSem.MaxWait != 2*time.Second {
		t.Errorf("detection.max-wait = %s, want 2s", rc.Libvips.DetectionSem.MaxWait)
	}
	if rc.Libvips.VipsMetricsInterval != 30*time.Second {
		t.Errorf("metrics-interval = %s, want 30s", rc.Libvips.VipsMetricsInterval)
	}
}

// TestParseRuntimeConfigLibvipsDetectionSemDefaults — при пустой секции
// значения нулевые (дефолты применяются в libvips.New через Normalized).
func TestParseRuntimeConfigLibvipsDetectionSemDefaults(t *testing.T) {
	rc, err := ParseRuntimeConfig([]byte(`
version: "1"
policy: {}
libvips: {}
`))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	if rc.Libvips.DetectionSem.Concurrency != 0 || rc.Libvips.DetectionSem.MaxWait != 0 {
		t.Errorf("expected zero defaults, got %+v", rc.Libvips.DetectionSem)
	}
	if rc.Libvips.VipsMetricsInterval != 0 {
		t.Errorf("metrics-interval default = %s, want 0 (runtime default)", rc.Libvips.VipsMetricsInterval)
	}
}

// TestParseRuntimeConfigLibvipsDetectionSemInvalid — fail-fast валидация:
// отрицательная конкурентность, отрицательный max-wait/interval — ошибка старта.
func TestParseRuntimeConfigLibvipsDetectionSemInvalid(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{
			name: "negative concurrency",
			yaml: `
version: "1"
policy: {}
libvips:
   detection:
     concurrency: -1
`,
		},
		{
			name: "negative max-wait",
			yaml: `
version: "1"
policy: {}
libvips:
   detection:
     max-wait: "-5s"
`,
		},
		{
			name: "bad max-wait duration",
			yaml: `
version: "1"
policy: {}
libvips:
   detection:
     max-wait: "soon"
`,
		},
		{
			name: "negative metrics-interval",
			yaml: `
version: "1"
policy: {}
libvips:
   metrics-interval: "-15s"
`,
		},
		{
			name: "unknown detection key",
			yaml: `
version: "1"
policy: {}
libvips:
   detection:
     enabled: true
`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseRuntimeConfig([]byte(tc.yaml)); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

// TestParseRuntimeConfigLibvipsColor — декодирование политики color
// management: явные режимы, дефолт (strip) и fail-fast на
// неизвестном значении.
func TestParseRuntimeConfigLibvipsColor(t *testing.T) {
	// transform.
	rc, err := ParseRuntimeConfig([]byte(`
version: "1"
policy: {}
libvips:
  color:
    mode: transform
`))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	if rc.Libvips.Color != "transform" {
		t.Errorf("color.mode = %q, want transform", rc.Libvips.Color)
	}

	// keep.
	rc, err = ParseRuntimeConfig([]byte(`
version: "1"
policy: {}
libvips:
  color:
    mode: keep
`))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig(keep): %v", err)
	}
	if rc.Libvips.Color != "keep" {
		t.Errorf("color.mode = %q, want keep", rc.Libvips.Color)
	}

	// Дефолт: пустая секция → strip.
	rc, err = ParseRuntimeConfig([]byte(`
version: "1"
policy: {}
libvips: {}
`))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig(default): %v", err)
	}
	if rc.Libvips.Color != "strip" {
		t.Errorf("color.mode default = %q, want strip", rc.Libvips.Color)
	}
}

// TestParseRuntimeConfigLibvipsColorInvalid — fail-fast: неизвестный режим
// color.mode — ошибка старта.
func TestParseRuntimeConfigLibvipsColorInvalid(t *testing.T) {
	_, err := ParseRuntimeConfig([]byte(`
version: "1"
policy: {}
libvips:
  color:
    mode: icm
`))
	if err == nil {
		t.Fatal("expected error for unknown color.mode")
	}
}

// TestParseRuntimeConfigLibvipsOperationCache — декодирование operation cache:
// явное false/true и дефолт (включено).
func TestParseRuntimeConfigLibvipsOperationCache(t *testing.T) {
	// Явное false → отключено.
	rc, err := ParseRuntimeConfig([]byte(`
version: "1"
policy: {}
libvips:
  operation-cache:
    enabled: false
`))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	if rc.Libvips.OperationCache.Enabled() {
		t.Error("operation-cache.enabled=false must disable operation cache")
	}

	// Явное true → включено.
	rc, err = ParseRuntimeConfig([]byte(`
version: "1"
policy: {}
libvips:
  operation-cache:
    enabled: true
`))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig(true): %v", err)
	}
	if !rc.Libvips.OperationCache.Enabled() {
		t.Error("operation-cache.enabled=true must keep operation cache enabled")
	}

	// Дефолт: пустая секция → включено.
	rc, err = ParseRuntimeConfig([]byte(`
version: "1"
policy: {}
libvips: {}
`))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig(default): %v", err)
	}
	if !rc.Libvips.OperationCache.Enabled() {
		t.Error("absent operation-cache must default to enabled")
	}
}

// ptrOr — значение указателя или дефолт (хелпер тестов для
// libvips.FormatEncodersConfig).
func ptrOr(v *int, def int) int {
	if v == nil {
		return def
	}
	return *v
}
