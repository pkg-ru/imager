package composition

import (
	"path/filepath"
	"testing"
	"time"

	"gitverse.ru/pkg-ru/imager/domain/asset"
	"gitverse.ru/pkg-ru/imager/domain/processing"
)

// TestLoadConfigDirBaseOnly проверяет, что загрузка одного обязательного
// server.yaml работает, а отсутствие local-файла — нормальная ситуация.
func TestLoadConfigDirBaseOnly(t *testing.T) {
	dir := t.TempDir()
	base := `
version: "1"
server:
  addr: ":9090"
http:
  cache-control: "public, max-age=2592000"
policy: {}
encoders:
  default-quality: 80
source:
  storage: fs
  path: /var/www/site.ru/images
result:
  storage: fs
  path: /var/cache/imager
`
	writeConfig(t, filepath.Join(dir, BaseConfigFile), base)

	rc, err := LoadConfigDir(dir)
	if err != nil {
		t.Fatalf("LoadConfigDir: %v", err)
	}
	if rc.Server.Addr != ":9090" {
		t.Errorf("server.addr = %q, want :9090", rc.Server.Addr)
	}
	if rc.SourceDir != "/var/www/site.ru/images" {
		t.Errorf("SourceDir = %q, want /var/www/site.ru/images", rc.SourceDir)
	}
	if rc.ResultDir != "/var/cache/imager" {
		t.Errorf("ResultDir = %q, want /var/cache/imager", rc.ResultDir)
	}
	if rc.Limits.OutputBytes != 0 {
		t.Errorf("Limits.OutputBytes = %d, want 0", rc.Limits.OutputBytes)
	}
	if rc.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want default info", rc.LogLevel)
	}
}

// TestLoadConfigDirDeepMerge проверяет глубокий merge local-конфига:
// вложенные maps мержатся, скаляры заменяются, списки заменяются целиком.
func TestLoadConfigDirDeepMerge(t *testing.T) {
	dir := t.TempDir()
	base := `
version: "1"
server:
  addr: ":8080"
  read-timeout: "5s"
source:
  storage: s3
  bucket: base-bucket
  prefix: base/prefix
  spool-max-bytes: 1024
http:
  allowed-origins:
    - https://base.example.com
  cache-control: "public, max-age=31536000"
observability:
  log-level: info
`
	local := `
server:
  addr: ":9090"
source:
  prefix: local/prefix
http:
  allowed-origins:
    - https://local.example.com
`
	writeConfig(t, filepath.Join(dir, BaseConfigFile), base)
	writeConfig(t, filepath.Join(dir, LocalConfigFile), local)

	rc, err := LoadConfigDir(dir)
	if err != nil {
		t.Fatalf("LoadConfigDir: %v", err)
	}

	// Скаляр переопределён local-конфигом.
	if rc.Server.Addr != ":9090" {
		t.Errorf("Server.Addr = %q, want :9090 (overridden)", rc.Server.Addr)
	}
	// Отсутствующий в local ключ сохранён из base.
	if rc.Server.ReadTimeout.String() != "5s" {
		t.Errorf("Server.ReadTimeout = %v, want 5s (kept from base)", rc.Server.ReadTimeout)
	}
	// Вложенный map source: prefix переопределён, bucket из base сохранён.
	if rc.Source.Bucket != "base-bucket" {
		t.Errorf("Source.Bucket = %q, want base-bucket (kept)", rc.Source.Bucket)
	}
	if rc.Source.Prefix != "local/prefix" {
		t.Errorf("Source.Prefix = %q, want local/prefix (overridden)", rc.Source.Prefix)
	}
	// Список (allowed-origins) заменён целиком.
	if len(rc.HTTP.AllowedOrigins) != 1 || rc.HTTP.AllowedOrigins[0] != "https://local.example.com" {
		t.Errorf("AllowedOrigins = %v, want exactly [https://local.example.com] (replaced)", rc.HTTP.AllowedOrigins)
	}
	// Значение по умолчанию для отсутствующего раздела.
	if rc.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want info", rc.LogLevel)
	}
}

// TestLoadConfigDirMissingBaseFailFast проверяет, что отсутствие
// обязательного server.yaml — ошибка (fail-fast).
func TestLoadConfigDirMissingBaseFail(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadConfigDir(dir); err == nil {
		t.Fatal("expected error when server.yaml is missing")
	}
}

// TestLoadConfigDirBrokenLocalIsError проверяет, что невалидный
// server-local.yaml — ошибка (не игнорируется).
func TestLoadConfigDirBrokenLocalIsError(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, filepath.Join(dir, BaseConfigFile), `version: "1"`)
	writeConfig(t, filepath.Join(dir, LocalConfigFile), "not: [valid")
	if _, err := LoadConfigDir(dir); err == nil {
		t.Fatal("expected error for invalid local config")
	}
}

// TestLoadConfigDirUnknownFieldFail проверяет strict-декодирование:
// неизвестное поле в любом слое — ошибка.
func TestLoadConfigDirUnknownFieldFail(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, filepath.Join(dir, BaseConfigFile), `
version: "1"
unknown-section:
  foo: bar
`)
	if _, err := LoadConfigDir(dir); err == nil {
		t.Fatal("expected error for unknown field in base config")
	}

	dir2 := t.TempDir()
	writeConfig(t, filepath.Join(dir2, BaseConfigFile), `version: "1"`)
	writeConfig(t, filepath.Join(dir2, LocalConfigFile), "bogus: 42")
	if _, err := LoadConfigDir(dir2); err == nil {
		t.Fatal("expected error for unknown field in local config")
	}
}

// TestParseRuntimeConfigStorageDefaults проверяет умолчания и валидацию
// хранилищ при typed parse.
func TestParseRuntimeConfigStorageDefaults(t *testing.T) {
	rc, err := ParseRuntimeConfig([]byte(`
version: "1"
source:
  storage: s3
  bucket: my-bucket
  dial-timeout: 10s
result:
  storage: sftp
  addr: host:22
  user: bob
  host-key-fingerprint: "SHA256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
`))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	if rc.Source.Kind != StorageS3 {
		t.Errorf("Source.Kind = %q, want s3", rc.Source.Kind)
	}
	if rc.Source.Conn.DialTimeout.String() != "10s" {
		t.Errorf("Source.DialTimeout = %v, want 10s", rc.Source.Conn.DialTimeout)
	}
	// Умолчания для пути (когда storage: fs или fs не указан).
	if rc.SourceDir != "./data/source" {
		t.Errorf("SourceDir = %q, want default ./data/source", rc.SourceDir)
	}
	if rc.Result.Kind != StorageSFTP {
		t.Errorf("Result.Kind = %q, want sftp", rc.Result.Kind)
	}
}

// TestParseRuntimeConfigStorageHTTPOptions проверяет парсинг общих настроек
// HTTP-подобных хранилищ (S3, HTTP) без s3-префикса: read-timeout,
// max-attempts, max-idle-conns, max-idle-conns-per-host, idle-conn-timeout,
// metadata-ttl. dial-timeout применяется ко всем типам.
func TestParseRuntimeConfigStorageHTTPOptions(t *testing.T) {
	rc, err := ParseRuntimeConfig([]byte(`
version: "1"
source:
  storage: s3
  bucket: my-bucket
  dial-timeout: 15s
  read-timeout: 45s
  max-attempts: 5
  max-idle-conns: 200
  max-idle-conns-per-host: 20
  idle-conn-timeout: 120s
  metadata-ttl: 60s
`))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	if rc.Source.Conn.DialTimeout.String() != "15s" {
		t.Errorf("DialTimeout = %v, want 15s", rc.Source.Conn.DialTimeout)
	}
	if rc.Source.Conn.ReadTimeout.String() != "45s" {
		t.Errorf("ReadTimeout = %v, want 45s", rc.Source.Conn.ReadTimeout)
	}
	if rc.Source.Conn.MaxAttempts != 5 {
		t.Errorf("MaxAttempts = %d, want 5", rc.Source.Conn.MaxAttempts)
	}
	if rc.Source.Conn.MaxIdleConns != 200 {
		t.Errorf("MaxIdleConns = %d, want 200", rc.Source.Conn.MaxIdleConns)
	}
	if rc.Source.Conn.MaxIdleConnsPerHost != 20 {
		t.Errorf("MaxIdleConnsPerHost = %d, want 20", rc.Source.Conn.MaxIdleConnsPerHost)
	}
	if rc.Source.Conn.IdleConnTimeout != 120*time.Second {
		t.Errorf("IdleConnTimeout = %v, want 120s", rc.Source.Conn.IdleConnTimeout)
	}
	if rc.Source.MetadataTTL != 60*time.Second {
		t.Errorf("MetadataTTL = %v, want 60s", rc.Source.MetadataTTL)
	}
}

// TestParseRuntimeConfigFailFast проверяет fail-fast валидации:
// невалидная версия и отсутствие обязательных полей хранилищ.
func TestParseRuntimeConfigFailFast(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{"unsupported version", `version: "999"`},
		{"s3 without bucket", "source:\n  storage: s3\n"},
		{"sftp missing user", "result:\n  storage: sftp\n  addr: x:22\n"},
		{"http missing base url", "source:\n  storage: http\n"},
		{"bad duration", `server:` + "\n  read-timeout: \"bogus\"\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseRuntimeConfig([]byte(tc.yaml)); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

// TestLoadConfigDirFSWithPath проверяет, что путь из YAML (source.path /
// result.path) попадает в SourceDir/ResultDir и используется при FS-сборке.
func TestLoadConfigDirFSWithPath(t *testing.T) {
	dir := t.TempDir()
	srcDir := t.TempDir()
	resDir := t.TempDir()
	writeConfig(t, filepath.Join(dir, BaseConfigFile), `
version: "1"
source:
  storage: fs
  path: `+srcDir+`
result:
  storage: fs
  path: `+resDir+`
`)

	rc, err := LoadConfigDir(dir)
	if err != nil {
		t.Fatalf("LoadConfigDir: %v", err)
	}
	if rc.SourceDir != srcDir {
		t.Errorf("SourceDir = %q, want %q", rc.SourceDir, srcDir)
	}
	if rc.ResultDir != resDir {
		t.Errorf("ResultDir = %q, want %q", rc.ResultDir, resDir)
	}
}

// TestParseRuntimeConfigPathPolicies проверяет декодирование path-policies
// из YAML через ParseRuntimeConfig и компиляцию в config.Config.Compile().
func TestParseRuntimeConfigPathPolicies(t *testing.T) {
	rc, err := ParseRuntimeConfig([]byte(`
version: "1"
policy:
   presets:
     thumb:
       crop: center
       width: 120
       height: 80
       output-formats: [webp]
   path-policies:
     "/":
       presets: ["thumb"]
     "/users":
       presets: ["thumb"]
     "basket/products":
       presets: ["thumb"]
     "/basket/users/":
       presets: ["thumb"]
application:
   limits:
     source-bytes: 10485760
     output-bytes: 10485760
`))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	if len(rc.Pipeline.Policy.PathPolicies) != 4 {
		t.Fatalf("PathPolicies = %d, want 4", len(rc.Pipeline.Policy.PathPolicies))
	}
	// Лимиты из application.limits декодируются из YAML.
	if rc.Limits.SourceBytes != 10485760 {
		t.Errorf("Limits.SourceBytes = %d, want 10485760", rc.Limits.SourceBytes)
	}
	if rc.Limits.OutputBytes != 10485760 {
		t.Errorf("Limits.OutputBytes = %d, want 10485760", rc.Limits.OutputBytes)
	}
	// Нормализация имён при компиляции.
	compiled, err := rc.Pipeline.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	names := compiled.Policy.PathNames()
	want := []string{"/", "/basket/products", "/basket/users", "/users"}
	if len(names) != len(want) {
		t.Fatalf("PathNames = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("PathNames[%d] = %q, want %q", i, names[i], want[i])
		}
	}
}

// TestParseRuntimeConfigPathPolicyCropModes проверяет декодирование форм
// поля crop в пресетах: строка-режим (center/smart/face/object) — и их
// компиляцию в доменные transform-коды. В новой архитектуре crop задаётся
// в пресетах/customs, а не в path-policies.
func TestParseRuntimeConfigPathPolicyCropModes(t *testing.T) {
	rc, err := ParseRuntimeConfig([]byte(`
version: "1"
policy:
   presets:
     center:
       crop: center
       width: 120
       height: 80
       output-formats: [webp]
     smart:
       crop: smart
       width: 120
       height: 80
       output-formats: [webp]
     face:
       crop: face
       width: 120
       height: 80
       output-formats: [webp]
     object:
       crop: object
       width: 120
       height: 80
       output-formats: [webp]
     resize:
       width: 120
       height: 80
       output-formats: [webp]
   path-policies:
     "/":
       presets: ["center", "smart", "face", "object", "resize"]
`))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	compiled, err := rc.Pipeline.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	cases := []struct {
		name      string
		transform asset.Transform
	}{
		// crop: center → c.
		{"center", asset.TransformCrop},
		// crop: smart → sc.
		{"smart", asset.TransformSmartCrop},
		// crop: face → fc.
		{"face", asset.TransformFaceCrop},
		// crop: object → oc.
		{"object", asset.TransformObjectCrop},
		// crop: "" → resize.
		{"resize", ""},
	}
	for _, tc := range cases {
		p, ok := compiled.Presets.Get(tc.name)
		if !ok {
			t.Fatalf("preset %q not found", tc.name)
		}
		if got := p.Transform(); got != tc.transform {
			t.Errorf("%s: Transform() = %q, want %q", tc.name, got, tc.transform)
		}
	}
}

// TestParseRuntimeConfigPathPolicyCropInvalid проверяет, что неизвестный
// crop-режим в пресете отклоняется при загрузке конфигурации.
func TestParseRuntimeConfigPathPolicyCropInvalid(t *testing.T) {
	_, err := ParseRuntimeConfig([]byte(`
version: "1"
policy:
   presets:
     thumb:
       crop: bogus
       width: 120
       height: 80
       output-formats: [webp]
`))
	if err == nil {
		t.Fatal("expected error for invalid crop mode")
	}
}

// TestParseRuntimeConfigOrientationKeys проверяет, что новые ключи
// ориентации (processing.default-* и preset auto-orient/rotate/flip)
// принимаются строгим YAML-парсером и попадают в скомпилированную политику.
func TestParseRuntimeConfigOrientationKeys(t *testing.T) {
	rc, err := ParseRuntimeConfig([]byte(`
version: "1"
policy:
   presets:
     thumb:
       crop: center
       width: 120
       height: 80
       output-formats: [webp]
       auto-orient: false
       rotate: "90"
       flip: horizontal
processing:
   default-auto-orient: true
   default-rotate: "270"
   default-flip: vertical
`))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	compiled, err := rc.Pipeline.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	// Глобальный дефолт.
	def := compiled.DefaultOrientation
	if def == nil {
		t.Fatal("expected DefaultOrientation")
	}
	if !def.AutoOrient || def.Rotate != processing.Rotation270 || def.Flip != processing.FlipVertical {
		t.Errorf("DefaultOrientation = %v, want auto-orient + rotate 270 + flip vertical", def)
	}
	// Пресет перекрывает глобальный дефолт.
	p, ok := compiled.Presets.Get("thumb")
	if !ok {
		t.Fatal("expected preset thumb")
	}
	or := p.Orientation()
	if or == nil {
		t.Fatal("expected preset orientation")
	}
	if or.AutoOrient || or.Rotate != processing.Rotation90 || or.Flip != processing.FlipHorizontal {
		t.Errorf("preset orientation = %v, want auto-orient off, rotate 90, flip horizontal", or)
	}
}

// TestParseRuntimeConfigTrimKeys проверяет, что глобальные ключи trim
// (processing.default-trim-mode/color/tolerance) принимаются строгим
// YAML-парсером и попадают в скомпилированный DefaultTrim.
func TestParseRuntimeConfigTrimKeys(t *testing.T) {
	rc, err := ParseRuntimeConfig([]byte(`
version: "1"
policy:
   presets:
     thumb:
       crop: center
       width: 120
       height: 80
       output-formats: [webp]
processing:
   default-trim-mode: color
   default-trim-color: "#f0f0f0"
   default-trim-tolerance: 0.1
`))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	compiled, err := rc.Pipeline.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	ts := compiled.DefaultTrim
	if ts == nil {
		t.Fatal("expected DefaultTrim")
	}
	if ts.Mode != processing.TrimModeColor || ts.Color != "#f0f0f0" || ts.Tolerance != 0.1 {
		t.Errorf("DefaultTrim = %+v, want {color, #f0f0f0, 0.1}", ts)
	}
}

// TestLoadConfigDirThreeLayers проверяет загрузку трёх слоёв: setting
// (обязательный), generate и failback (оба опциональные). Значения из всех
// слоёв объединяются в единый RuntimeConfig.
func TestLoadConfigDirThreeLayers(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, filepath.Join(dir, BaseConfigFile), `
version: "1"
server:
  addr: ":8080"
source:
  storage: fs
  path: /data/source
result:
  storage: fs
  path: /data/result
`)
	writeConfig(t, filepath.Join(dir, GenerateConfigFile), `
policy:
   path-policies:
     "/":
       presets: ["thumb"]
   presets:
     thumb:
       width: 200
       height: 200
       output-formats: [webp]
encoders:
   default-quality: 80
application:
   limits:
     output-bytes: 10485760
`)
	writeConfig(t, filepath.Join(dir, FailbackConfigFile), `
http:
  not-found:
    pixel: true
  source-fallback:
    enabled: true
    status: 200
`)

	rc, err := LoadConfigDir(dir)
	if err != nil {
		t.Fatalf("LoadConfigDir: %v", err)
	}
	// Из setting.
	if rc.Server.Addr != ":8080" {
		t.Errorf("Server.Addr = %q, want :8080", rc.Server.Addr)
	}
	// Из generate.
	if rc.Limits.OutputBytes != 10485760 {
		t.Errorf("Limits.OutputBytes = %d, want 10485760", rc.Limits.OutputBytes)
	}
	if len(rc.Pipeline.Policy.PathPolicies) != 1 {
		t.Errorf("PathPolicies = %d, want 1", len(rc.Pipeline.Policy.PathPolicies))
	}
	if rc.Encoders.DefaultQuality != 80 {
		t.Errorf("Encoders.DefaultQuality = %d, want 80", rc.Encoders.DefaultQuality)
	}
	// Из failback.
	if !rc.HTTP.NotFound.Pixel {
		t.Errorf("NotFound.Pixel = %v, want true", rc.HTTP.NotFound.Pixel)
	}
	if !rc.HTTP.SourceFallback.Enabled || rc.HTTP.SourceFallback.Status != 200 {
		t.Errorf("SourceFallback = %+v, want enabled + status 200", rc.HTTP.SourceFallback)
	}
}

// TestLoadConfigDirGenerateLocalOverride проверяет, что generate-local.yaml
// глубоко переопределяет generate.yaml (скаляр заменяется, отсутствующий
// ключ сохраняется из base).
func TestLoadConfigDirGenerateLocalOverride(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, filepath.Join(dir, BaseConfigFile), `version: "1"`)
	writeConfig(t, filepath.Join(dir, GenerateConfigFile), `
encoders:
  default-quality: 80
processing:
  default-trim-mode: auto
`)
	writeConfig(t, filepath.Join(dir, GenerateLocalFile), `
encoders:
  default-quality: 90
`)

	rc, err := LoadConfigDir(dir)
	if err != nil {
		t.Fatalf("LoadConfigDir: %v", err)
	}
	if rc.Encoders.DefaultQuality != 90 {
		t.Errorf("Encoders.DefaultQuality = %d, want 90 (overridden by local)", rc.Encoders.DefaultQuality)
	}
	if rc.Pipeline.Processing.DefaultTrimMode != "auto" {
		t.Errorf("DefaultTrimMode = %q, want auto (kept from base)", rc.Pipeline.Processing.DefaultTrimMode)
	}
}

// TestLoadConfigDirFailbackLocalOverride проверяет, что failback-local.yaml
// переопределяет failback.yaml.
func TestLoadConfigDirFailbackLocalOverride(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, filepath.Join(dir, BaseConfigFile), `version: "1"`)
	writeConfig(t, filepath.Join(dir, FailbackConfigFile), `
http:
  not-found:
    pixel: true
`)
	writeConfig(t, filepath.Join(dir, FailbackLocalFile), `
http:
  not-found:
    pixel: false
`)

	rc, err := LoadConfigDir(dir)
	if err != nil {
		t.Fatalf("LoadConfigDir: %v", err)
	}
	if rc.HTTP.NotFound.Pixel {
		t.Errorf("NotFound.Pixel = %v, want false (overridden by local)", rc.HTTP.NotFound.Pixel)
	}
}

func TestLoadConfigDirMissingGenerateFailback(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, filepath.Join(dir, BaseConfigFile), `
version: "1"
server:
  addr: ":9090"
source:
  storage: fs
  path: /data/source
result:
  storage: fs
  path: /data/result
`)

	rc, err := LoadConfigDir(dir)
	if err != nil {
		t.Fatalf("LoadConfigDir: %v", err)
	}
	if rc.Server.Addr != ":9090" {
		t.Errorf("Server.Addr = %q, want :9090", rc.Server.Addr)
	}
	// Умолчания схемы для отсутствующих слоёв.
	if rc.Limits.OutputBytes != 0 {
		t.Errorf("Limits.OutputBytes = %d, want 0 (default)", rc.Limits.OutputBytes)
	}
}

// TestLoadConfigDirTopLevelConflict проверяет, что при совпадении top-level
// ключа между базовыми файлами слоёв пишется warning, а deep merge выполняется
// в порядке setting -> generate -> failback (более специализированный слой
// выигрывает при конфликте скаляров).
func TestLoadConfigDirTopLevelConflict(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, filepath.Join(dir, BaseConfigFile), `
version: "1"
application:
  buffer-max-bytes: 524288000
`)
	writeConfig(t, filepath.Join(dir, GenerateConfigFile), `
application:
   limits:
     output-bytes: 10485760
`)

	prev := configLogger
	cl := &captureLogger{}
	configLogger = cl
	defer func() { configLogger = prev }()

	rc, err := LoadConfigDir(dir)
	if err != nil {
		t.Fatalf("LoadConfigDir: %v", err)
	}
	// Deep merge: оба ключа из разных слоёв сохраняются.
	if rc.BufferMaxBytes != 524288000 {
		t.Errorf("BufferMaxBytes = %d, want 524288000 (from setting)", rc.BufferMaxBytes)
	}
	if rc.Limits.OutputBytes != 10485760 {
		t.Errorf("Limits.OutputBytes = %d, want 10485760 (from generate)", rc.Limits.OutputBytes)
	}
	// Warning о конфликте top-level ключа "application".
	found := false
	for _, w := range cl.warnings {
		if contains(w, "application") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning about top-level key conflict, got warnings: %v", cl.warnings)
	}
}

// TestLoadConfigDirInvalidGenerateVersion проверяет, что невалидный version
// в generate.yaml — ошибка старта (защита от рассинхронизации версий слоёв).
func TestLoadConfigDirInvalidGenerateVersion(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, filepath.Join(dir, BaseConfigFile), `version: "1"`)
	writeConfig(t, filepath.Join(dir, GenerateConfigFile), `version: "2"`)
	if _, err := LoadConfigDir(dir); err == nil {
		t.Fatal("expected error for invalid version in generate.yaml")
	}
}

// TestLoadConfigDirUnknownFieldInGenerate проверяет strict-декодирование:
// неизвестное поле в generate.yaml — ошибка.
func TestLoadConfigDirUnknownFieldInGenerate(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, filepath.Join(dir, BaseConfigFile), `version: "1"`)
	writeConfig(t, filepath.Join(dir, GenerateConfigFile), "bogus-section:\n  foo: bar\n")
	if _, err := LoadConfigDir(dir); err == nil {
		t.Fatal("expected error for unknown field in generate.yaml")
	}
}

// TestLoadConfigDirUnknownFieldInFailback проверяет strict-декодирование:
// неизвестное поле в failback.yaml — ошибка.
func TestLoadConfigDirUnknownFieldInFailback(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, filepath.Join(dir, BaseConfigFile), `version: "1"`)
	writeConfig(t, filepath.Join(dir, FailbackConfigFile), "bogus: 42\n")
	if _, err := LoadConfigDir(dir); err == nil {
		t.Fatal("expected error for unknown field in failback.yaml")
	}
}

func TestLoadConfigDirSingleFile(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, filepath.Join(dir, BaseConfigFile), `
version: "1"
server:
   addr: ":8080"
policy:
   path-policies:
     "/":
       presets: ["thumb"]
   presets:
     thumb:
       width: 200
       height: 200
       output-formats: [webp]
encoders:
   default-quality: 80
http:
   not-found:
     pixel: true
application:
   limits:
     output-bytes: 10485760
`)

	rc, err := LoadConfigDir(dir)
	if err != nil {
		t.Fatalf("LoadConfigDir: %v", err)
	}
	if rc.Server.Addr != ":8080" {
		t.Errorf("Server.Addr = %q, want :8080", rc.Server.Addr)
	}
	if rc.Limits.OutputBytes != 10485760 {
		t.Errorf("Limits.OutputBytes = %d, want 10485760", rc.Limits.OutputBytes)
	}
	if !rc.HTTP.NotFound.Pixel {
		t.Errorf("NotFound.Pixel = %v, want true", rc.HTTP.NotFound.Pixel)
	}
}
