package composition

import (
	"strings"
	"testing"
	"time"

	"github.com/pkg-ru/imager/adapters/httpapi"
)

// TestParseRuntimeConfigDetection проверяет декодирование секции detection
// и применение значений по умолчанию.
func TestParseRuntimeConfigDetection(t *testing.T) {
	rc, err := ParseRuntimeConfig([]byte(`
version: "1"
policy:
  global:
    authorization: unsafe
detection:
  face-model: /models/face.onnx
  object-model: /models/obj.onnx
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
policy:
  global:
    authorization: unsafe
`))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	if rc.Detection.FaceModel != "" || rc.Detection.ObjectModel != "" {
		t.Errorf("models = %q/%q, want empty", rc.Detection.FaceModel, rc.Detection.ObjectModel)
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
policy:
  global:
    authorization: unsafe
detection:
  confidence-threshold: 1.5
`,
		},
		{
			name: "threshold<0",
			yaml: `
version: "1"
policy:
  global:
    authorization: unsafe
detection:
  confidence-threshold: -0.1
`,
		},
		{
			name: "max-objects=0",
			yaml: `
version: "1"
policy:
  global:
    authorization: unsafe
detection:
  max-objects: 0
`,
		},
		{
			name: "max-objects negative",
			yaml: `
version: "1"
policy:
  global:
    authorization: unsafe
detection:
  max-objects: -2
`,
		},
		{
			name: "margin negative",
			yaml: `
version: "1"
policy:
  global:
    authorization: unsafe
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
policy:
  global:
    authorization: unsafe
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
policy:
  global:
    authorization: unsafe
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
policy:
  global:
    authorization: unsafe
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
policy:
  global:
    authorization: unsafe
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
		"policy:\n" +
		"  global:\n" +
		"    authorization: unsafe\n" +
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
policy:
  global:
    authorization: unsafe
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
policy:
  global:
    authorization: unsafe
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
policy:
  global:
    authorization: unsafe
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
policy:
  global:
    authorization: unsafe
http:
  source-fallback:
    status: 500
`,
		},
		{
			name: "status=201",
			yaml: `
version: "1"
policy:
  global:
    authorization: unsafe
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
policy:
  global:
    authorization: unsafe
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
policy:
  global:
    authorization: unsafe
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
policy:
  global:
    authorization: unsafe
observability:
  asset-errors:
    log-level: verbose
`,
		},
		{
			name: "bad key-mode",
			yaml: `
version: "1"
policy:
  global:
    authorization: unsafe
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
policy:
  global:
    authorization: unsafe
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
policy:
  global:
    authorization: unsafe
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
policy:
  global:
    authorization: unsafe
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
policy:
  global:
    authorization: unsafe
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
policy:
  global:
    authorization: unsafe
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
policy:
  global:
    authorization: unsafe
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
policy:
  global:
    authorization: unsafe
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
policy:
  global:
    authorization: unsafe
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
policy:
  global:
    authorization: unsafe
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

// TestParseRuntimeConfigLibvipsEncoders проверяет декодирование секции
// libvips.encoders (per-format параметры кодировщиков).
func TestParseRuntimeConfigLibvipsEncoders(t *testing.T) {
	rc, err := ParseRuntimeConfig([]byte(strings.ReplaceAll(`
version: "1"
policy:
	global:
		authorization: unsafe
libvips:
	encoders:
		webp-reduction-effort: 2
		avif-speed: 8
		png-compression-level: 9
		jxl-effort: 3
		jpeg-progressive: true
		png-interlace: true
		png-palette: true
		png-palette-colors: 128
		png-palette-bit-depth: 4
		gif-bit-depth: 4
`, "\t", "  ")))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	e := rc.Libvips.Encoders
	if e.WebPReductionEffort != 2 {
		t.Errorf("WebPReductionEffort = %d, want 2", e.WebPReductionEffort)
	}
	if e.AVIFSpeed != 8 {
		t.Errorf("AVIFSpeed = %d, want 8", e.AVIFSpeed)
	}
	if e.PNGCompression != 9 {
		t.Errorf("PNGCompression = %d, want 9", e.PNGCompression)
	}
	if e.JXLEffort != 3 {
		t.Errorf("JXLEffort = %d, want 3", e.JXLEffort)
	}
	if !e.JPEGProgressive {
		t.Error("JPEGProgressive must be true")
	}
	if !e.PNGInterlace {
		t.Error("PNGInterlace must be true")
	}
	if !e.PNGPalette {
		t.Error("PNGPalette must be true")
	}
	if e.PNGPaletteColors != 128 {
		t.Errorf("PNGPaletteColors = %d, want 128", e.PNGPaletteColors)
	}
	if e.PNGPaletteBitDepth != 4 {
		t.Errorf("PNGPaletteBitDepth = %d, want 4", e.PNGPaletteBitDepth)
	}
	if e.GIFBitDepth != 4 {
		t.Errorf("GIFBitDepth = %d, want 4", e.GIFBitDepth)
	}
}

// TestParseRuntimeConfigLibvipsEncodersDefaults проверяет дефолты секции
// libvips.encoders при её отсутствии.
func TestParseRuntimeConfigLibvipsEncodersDefaults(t *testing.T) {
	rc, err := ParseRuntimeConfig([]byte(strings.ReplaceAll(`
version: "1"
policy:
	global:
		authorization: unsafe
`, "\t", "  ")))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	e := rc.Libvips.Encoders
	if e.WebPReductionEffort != 0 || e.AVIFSpeed != 0 || e.PNGCompression != 0 ||
		e.JXLEffort != 0 || e.JPEGProgressive || e.PNGInterlace || e.PNGPalette ||
		e.PNGPaletteColors != 0 || e.PNGPaletteBitDepth != 0 || e.GIFBitDepth != 0 {
		t.Errorf("encoders = %+v, want zero values (= встроенные умолчания движка)", e)
	}
}

// TestParseRuntimeConfigLibvipsShrinkOnLoad — секция libvips.shrink-on-load:
// явное значение, отсутствие (дефолт = включено) и неизвестный ключ
// (strict-декодирование).
func TestParseRuntimeConfigLibvipsShrinkOnLoad(t *testing.T) {
	t.Run("explicit false", func(t *testing.T) {
		rc, err := ParseRuntimeConfig([]byte(strings.ReplaceAll(`
version: "1"
policy:
	global:
		authorization: unsafe
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
policy:
	global:
		authorization: unsafe
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
policy:
	global:
		authorization: unsafe
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
policy:
  global:
    authorization: unsafe
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
// диапазонов: невалидное значение — ошибка старта, не runtime.
func TestParseRuntimeConfigLibvipsEncodersInvalid(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{
			name: "webp effort > 6",
			yaml: `
version: "1"
policy:
		global:
			 authorization: unsafe
libvips:
		encoders:
			 webp-reduction-effort: 7
`,
		},
		{
			name: "webp effort negative",
			yaml: `
version: "1"
policy:
		global:
			 authorization: unsafe
libvips:
		encoders:
			 webp-reduction-effort: -1
`,
		},
		{
			name: "avif speed > 9",
			yaml: `
version: "1"
policy:
		global:
			 authorization: unsafe
libvips:
		encoders:
			 avif-speed: 10
`,
		},
		{
			name: "avif speed negative",
			yaml: `
version: "1"
policy:
		global:
			 authorization: unsafe
libvips:
		encoders:
			 avif-speed: -3
`,
		},
		{
			name: "png compression > 9",
			yaml: `
version: "1"
policy:
		global:
			 authorization: unsafe
libvips:
		encoders:
			 png-compression-level: 10
`,
		},
		{
			name: "png compression negative",
			yaml: `
version: "1"
policy:
		global:
			 authorization: unsafe
libvips:
		encoders:
			 png-compression-level: -1
`,
		},
		{
			name: "jxl effort > 9",
			yaml: `
version: "1"
policy:
		global:
			 authorization: unsafe
libvips:
		encoders:
			 jxl-effort: 10
`,
		},
		{
			name: "jxl effort negative",
			yaml: `
version: "1"
policy:
		global:
			 authorization: unsafe
libvips:
		encoders:
			 jxl-effort: -1
`,
		},
		{
			name: "png palette colors > 256",
			yaml: `
version: "1"
policy:
		global:
			 authorization: unsafe
libvips:
		encoders:
			 png-palette-colors: 257
`,
		},
		{
			name: "png palette colors negative",
			yaml: `
version: "1"
policy:
		global:
			 authorization: unsafe
libvips:
		encoders:
			 png-palette-colors: -4
`,
		},
		{
			name: "png palette bit depth > 8",
			yaml: `
version: "1"
policy:
		global:
			 authorization: unsafe
libvips:
		encoders:
			 png-palette-bit-depth: 16
`,
		},
		{
			name: "png palette bit depth negative",
			yaml: `
version: "1"
policy:
		global:
			 authorization: unsafe
libvips:
		encoders:
			 png-palette-bit-depth: -2
`,
		},
		{
			name: "gif bit depth > 8",
			yaml: `
version: "1"
policy:
		global:
			 authorization: unsafe
libvips:
		encoders:
			 gif-bit-depth: 9
`,
		},
		{
			name: "gif bit depth negative",
			yaml: `
version: "1"
policy:
		global:
			 authorization: unsafe
libvips:
		encoders:
			 gif-bit-depth: -1
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
// неизвестный ключ в libvips.encoders — ошибка старта.
func TestParseRuntimeConfigLibvipsEncodersUnknownField(t *testing.T) {
	yaml := `
version: "1"
policy:
  global:
    authorization: unsafe
libvips:
  encoders:
    jpeg-quality: 90
`
	if _, err := ParseRuntimeConfig([]byte(yaml)); err == nil {
		t.Fatal("expected error for unknown libvips.encoders.jpeg-quality field")
	}
}

// TestParseRuntimeConfigLibvipsDetectionSem проверяет декодирование секции
// libvips.detection (detection-семофор, Фаза 4) и libvips.metrics-interval.
func TestParseRuntimeConfigLibvipsDetectionSem(t *testing.T) {
	rc, err := ParseRuntimeConfig([]byte(`
version: "1"
policy:
   global:
     authorization: unsafe
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
policy:
   global:
     authorization: unsafe
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
policy:
   global:
     authorization: unsafe
libvips:
   detection:
     concurrency: -1
`,
		},
		{
			name: "negative max-wait",
			yaml: `
version: "1"
policy:
   global:
     authorization: unsafe
libvips:
   detection:
     max-wait: "-5s"
`,
		},
		{
			name: "bad max-wait duration",
			yaml: `
version: "1"
policy:
   global:
     authorization: unsafe
libvips:
   detection:
     max-wait: "soon"
`,
		},
		{
			name: "negative metrics-interval",
			yaml: `
version: "1"
policy:
   global:
     authorization: unsafe
libvips:
   metrics-interval: "-15s"
`,
		},
		{
			name: "unknown detection key",
			yaml: `
version: "1"
policy:
   global:
     authorization: unsafe
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
// management (Фаза 5a): явные режимы, дефолт (strip) и fail-fast на
// неизвестном значении.
func TestParseRuntimeConfigLibvipsColor(t *testing.T) {
	// transform.
	rc, err := ParseRuntimeConfig([]byte(`
version: "1"
policy:
   global:
     authorization: unsafe
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
policy:
  global:
    authorization: unsafe
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
policy:
  global:
    authorization: unsafe
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
policy:
  global:
    authorization: unsafe
libvips:
  color:
    mode: icm
`))
	if err == nil {
		t.Fatal("expected error for unknown color.mode")
	}
}

// TestParseRuntimeConfigLibvipsOperationCache — декодирование operation cache
// (Фаза 5b): явное false/true и дефолт (включено).
func TestParseRuntimeConfigLibvipsOperationCache(t *testing.T) {
	// Явное false → отключено.
	rc, err := ParseRuntimeConfig([]byte(`
version: "1"
policy:
  global:
    authorization: unsafe
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
policy:
  global:
    authorization: unsafe
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

	// Дефолт: пустая секция → включено (обратная совместимость).
	rc, err = ParseRuntimeConfig([]byte(`
version: "1"
policy:
  global:
    authorization: unsafe
libvips: {}
`))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig(default): %v", err)
	}
	if !rc.Libvips.OperationCache.Enabled() {
		t.Error("absent operation-cache must default to enabled")
	}
}
