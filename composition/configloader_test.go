package composition

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/pkg-ru/imager/domain/asset"
	"github.com/pkg-ru/imager/domain/policy"
	"github.com/pkg-ru/imager/domain/processing"
)

// TestLoadConfigDirBaseOnly проверяет, что загрузка одного обязательного
// setting.yaml работает, а отсутствие local-файла — нормальная ситуация.
func TestLoadConfigDirBaseOnly(t *testing.T) {
	dir := t.TempDir()
	base := `
version: "1"
server:
  addr: ":9090"
http:
  cache-control: "public, max-age=2592000"
policy:
  global:
    authorization: unsafe
processing:
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
	if rc.OutputLimit != 0 {
		t.Errorf("OutputLimit = %d, want 0", rc.OutputLimit)
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
imagemagick:
  binary: /usr/bin/magick
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
	// Ключ, добавленный только в local (imagemagick.binary), присутствует.
	if rc.ImageMagick.Binary != "/usr/bin/magick" {
		t.Errorf("ImageMagick.Binary = %q, want /usr/bin/magick", rc.ImageMagick.Binary)
	}
	// Значение по умолчанию для отсутствующего раздела.
	if rc.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want info", rc.LogLevel)
	}
}

// TestLoadConfigDirMissingBaseFailFast проверяет, что отсутствие
// обязательного setting.yaml — ошибка (fail-fast).
func TestLoadConfigDirMissingBaseFail(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadConfigDir(dir); err == nil {
		t.Fatal("expected error when setting.yaml is missing")
	}
}

// TestLoadConfigDirBrokenLocalIsError проверяет, что невалидный
// setting-local.yaml — ошибка (не игнорируется).
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

// TestParseRuntimeConfigImageMagickDefaults проверяет умолчания ImageMagick:
// policy включена, network отключён, binary magick.
func TestParseRuntimeConfigImageMagickDefaults(t *testing.T) {
	rc, err := ParseRuntimeConfig([]byte(`
version: "1"
`))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	if rc.ImageMagick.Binary != "magick" {
		t.Errorf("Binary = %q, want magick", rc.ImageMagick.Binary)
	}
	if rc.ImageMagick.Policy.Enabled != true {
		t.Errorf("Policy.Enabled = %v, want true", rc.ImageMagick.Policy.Enabled)
	}
	if rc.ImageMagick.Policy.DisableNetwork != true {
		t.Errorf("Policy.DisableNetwork = %v, want true", rc.ImageMagick.Policy.DisableNetwork)
	}

	// Явное отключение policy.
	rc2, err := ParseRuntimeConfig([]byte(`
version: "1"
imagemagick:
  policy:
    enabled: false
`))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	if rc2.ImageMagick.Policy.Enabled != false {
		t.Errorf("Policy.Enabled = %v, want false (explicit)", rc2.ImageMagick.Policy.Enabled)
	}
	// DisableNetwork — независимый дефолт безопасности (true), сохраняется
	// даже при отключённой policy (policy.xml просто не генерируется).
	if rc2.ImageMagick.Policy.DisableNetwork != true {
		t.Errorf("Policy.DisableNetwork = %v, want true (security default)", rc2.ImageMagick.Policy.DisableNetwork)
	}
}

// TestParseRuntimeConfigLimits проверяет парсинг resource limits.
func TestParseRuntimeConfigLimits(t *testing.T) {
	rc, err := ParseRuntimeConfig([]byte(`
version: "1"
imagemagick:
  limits:
    memory-bytes: 1048576
    threads: 2
    timeout: 30s
  policy:
    max-memory-bytes: 524288
    disabled-coders:
      - PDF
      - PS
`))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	if rc.ImageMagick.Limits.MemoryBytes != 1048576 {
		t.Errorf("Limits.MemoryBytes = %d, want 1048576", rc.ImageMagick.Limits.MemoryBytes)
	}
	if rc.ImageMagick.Limits.Threads != 2 {
		t.Errorf("Limits.Threads = %d, want 2", rc.ImageMagick.Limits.Threads)
	}
	if rc.ImageMagick.Limits.Timeout.String() != "30s" {
		t.Errorf("Limits.Timeout = %v, want 30s", rc.ImageMagick.Limits.Timeout)
	}
	if rc.ImageMagick.Policy.MaxMemoryBytes != 524288 {
		t.Errorf("Policy.MaxMemoryBytes = %d, want 524288", rc.ImageMagick.Policy.MaxMemoryBytes)
	}
}

// TestLoadConfigDirImageMagickAbsolutePath проверяет, что абсолютный путь к
// ImageMagick binary из local-конфига сохраняется в rc.ImageMagick.Binary.
// Это важно на Windows, где имя `magick` может не разрешаться через PATH.
func TestLoadConfigDirImageMagickAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, filepath.Join(dir, BaseConfigFile), `version: "1"`)
	writeConfig(t, filepath.Join(dir, LocalConfigFile), `
imagemagick:
  binary: "D:/OSPanel/addons/ImageMagick-vs17/magick.exe"
`)

	rc, err := LoadConfigDir(dir)
	if err != nil {
		t.Fatalf("LoadConfigDir: %v", err)
	}
	if rc.ImageMagick.Binary != "D:/OSPanel/addons/ImageMagick-vs17/magick.exe" {
		t.Errorf("ImageMagick.Binary = %q, want absolute Windows path", rc.ImageMagick.Binary)
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
  global:
    authorization: safe
    allowed-presets: ["thumb"]
    size-rules: ["0-2000x0-2000"]
    limits:
      source-bytes: 10485760
      output-bytes: 10485760
  presets:
    - name: thumb
      crop: center
      size: 120x80
      output-format: webp
  path-policies:
    - path: "/"
      dpr: "0-1"
      crop: none
    - path: "/users"
      dpr: "2-3"
      crop: center
      trim: false
    - path: "basket/products"
      dpr: "0-1"
    - path: "/basket/users/"
      dpr: "2-3"
`))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	if len(rc.Pipeline.Policy.PathPolicies) != 4 {
		t.Fatalf("PathPolicies = %d, want 4", len(rc.Pipeline.Policy.PathPolicies))
	}
	// Лимиты из global декодируются из YAML.
	if rc.Pipeline.Policy.Global.Limits.SourceBytes != 10485760 {
		t.Errorf("Global.Limits.SourceBytes = %d, want 10485760", rc.Pipeline.Policy.Global.Limits.SourceBytes)
	}
	if rc.Pipeline.Policy.Global.Limits.OutputBytes != 10485760 {
		t.Errorf("Global.Limits.OutputBytes = %d, want 10485760", rc.Pipeline.Policy.Global.Limits.OutputBytes)
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
// поля crop в path-policies: строка-режим, список режимов, "none" — и их
// компиляцию в доменные правила. Булевы значения crop запрещены (crop —
// только строка).
func TestParseRuntimeConfigPathPolicyCropModes(t *testing.T) {
	rc, err := ParseRuntimeConfig([]byte(`
version: "1"
policy:
  global:
    authorization: unsafe
  path-policies:
    - path: "/center"
      crop: center
    - path: "/deny"
      crop: none
    - path: "/smart"
      crop: smart
    - path: "/list"
      crop: [center, face]
    - path: "/none"
      crop: none
`))
	if err != nil {
		t.Fatalf("ParseRuntimeConfig: %v", err)
	}
	compiled, err := rc.Pipeline.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	byPath := map[string]*policy.PathPolicy{}
	for i := range compiled.Policy.PathPolicies {
		pp := &compiled.Policy.PathPolicies[i]
		byPath[pp.Path] = pp
	}
	cases := []struct {
		path      string
		transform asset.Transform
		allowed   bool
	}{
		// crop: center → разрешены только c/ct.
		{"/center", asset.TransformCrop, true},
		{"/center", asset.TransformCropTrim, true},
		{"/center", asset.TransformSmartCrop, false},
		// crop: none → все crop-режимы запрещены, остальное разрешено.
		{"/deny", asset.TransformCrop, false},
		{"/deny", asset.TransformCropTrim, false},
		{"/deny", asset.TransformSmartCrop, false},
		{"/deny", asset.TransformObjectCropTrim, false},
		{"/deny", "", true},
		// crop: smart → sc/sct.
		{"/smart", asset.TransformSmartCrop, true},
		{"/smart", asset.TransformSmartCropTrim, true},
		{"/smart", asset.TransformCrop, false},
		// crop: [center, face] → c/ct/fc/fct.
		{"/list", asset.TransformCrop, true},
		{"/list", asset.TransformFaceCropTrim, true},
		{"/list", asset.TransformObjectCrop, false},
		// crop: none → все crop-режимы запрещены.
		{"/none", asset.TransformCrop, false},
		{"/none", asset.TransformObjectCropTrim, false},
		{"/none", "", true},
	}
	for _, tc := range cases {
		pp := byPath[tc.path]
		if pp == nil {
			t.Fatalf("path policy %q not found", tc.path)
		}
		if got := pp.Crop.Allows(tc.transform); got != tc.allowed {
			t.Errorf("%s: Allows(%q) = %v, want %v", tc.path, tc.transform, got, tc.allowed)
		}
	}
}

// TestParseRuntimeConfigPathPolicyCropInvalid проверяет, что неизвестный
// crop-режим в path-policy отклоняется при загрузке конфигурации.
func TestParseRuntimeConfigPathPolicyCropInvalid(t *testing.T) {
	_, err := ParseRuntimeConfig([]byte(`
version: "1"
policy:
  global:
    authorization: unsafe
  path-policies:
    - path: "/"
      crop: bogus
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
  global:
    authorization: unsafe
  presets:
    - name: thumb
      crop: center
      size: 120x80
      output-format: webp
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
  global:
    authorization: unsafe
  presets:
    - name: thumb
      crop: center
      size: 120x80
      output-format: webp
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
  global:
    authorization: safe
    allowed-presets: ["thumb"]
  presets:
    - name: thumb
      size: 200x200
      output-format: webp
processing:
  default-quality: 80
application:
  output-limit: 10485760
`)
	writeConfig(t, filepath.Join(dir, FailbackConfigFile), `
imagemagick:
  binary: /usr/bin/magick
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
	if rc.OutputLimit != 10485760 {
		t.Errorf("OutputLimit = %d, want 10485760", rc.OutputLimit)
	}
	if rc.Pipeline.Policy.Global.Authorization != "safe" {
		t.Errorf("Authorization = %q, want safe", rc.Pipeline.Policy.Global.Authorization)
	}
	if rc.Pipeline.Processing.DefaultQuality != 80 {
		t.Errorf("DefaultQuality = %d, want 80", rc.Pipeline.Processing.DefaultQuality)
	}
	// Из failback.
	if rc.ImageMagick.Binary != "/usr/bin/magick" {
		t.Errorf("ImageMagick.Binary = %q, want /usr/bin/magick", rc.ImageMagick.Binary)
	}
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
processing:
  default-quality: 80
  default-trim-mode: auto
`)
	writeConfig(t, filepath.Join(dir, GenerateLocalFile), `
processing:
  default-quality: 90
`)

	rc, err := LoadConfigDir(dir)
	if err != nil {
		t.Fatalf("LoadConfigDir: %v", err)
	}
	if rc.Pipeline.Processing.DefaultQuality != 90 {
		t.Errorf("DefaultQuality = %d, want 90 (overridden by local)", rc.Pipeline.Processing.DefaultQuality)
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
imagemagick:
  binary: magick
`)
	writeConfig(t, filepath.Join(dir, FailbackLocalFile), `
imagemagick:
  binary: "D:/OSPanel/addons/ImageMagick-vs17/magick.exe"
`)

	rc, err := LoadConfigDir(dir)
	if err != nil {
		t.Fatalf("LoadConfigDir: %v", err)
	}
	if rc.ImageMagick.Binary != "D:/OSPanel/addons/ImageMagick-vs17/magick.exe" {
		t.Errorf("ImageMagick.Binary = %q, want overridden Windows path", rc.ImageMagick.Binary)
	}
}

// TestLoadConfigDirMissingGenerateFailback проверяет обратную совместимость:
// отсутствие generate.yaml / failback.yaml — нормальная ситуация, сервис
// работает как раньше (только setting.yaml).
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
	if rc.OutputLimit != 0 {
		t.Errorf("OutputLimit = %d, want 0 (default)", rc.OutputLimit)
	}
	if rc.ImageMagick.Binary != "magick" {
		t.Errorf("ImageMagick.Binary = %q, want default magick", rc.ImageMagick.Binary)
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
  output-limit: 10485760
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
	if rc.OutputLimit != 10485760 {
		t.Errorf("OutputLimit = %d, want 10485760 (from generate)", rc.OutputLimit)
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

// TestLoadConfigDirBackwardCompatSingleFile проверяет обратную совместимость:
// старый монолитный setting.yaml, содержащий секции, которые "переехали" в
// generate/failback, продолжает работать без ошибок.
func TestLoadConfigDirBackwardCompatSingleFile(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, filepath.Join(dir, BaseConfigFile), `
version: "1"
server:
  addr: ":8080"
policy:
  global:
    authorization: safe
    allowed-presets: ["thumb"]
  presets:
    - name: thumb
      size: 200x200
      output-format: webp
processing:
  default-quality: 80
imagemagick:
  binary: /usr/bin/magick
http:
  not-found:
    pixel: true
application:
  output-limit: 10485760
`)

	rc, err := LoadConfigDir(dir)
	if err != nil {
		t.Fatalf("LoadConfigDir: %v", err)
	}
	if rc.Server.Addr != ":8080" {
		t.Errorf("Server.Addr = %q, want :8080", rc.Server.Addr)
	}
	if rc.OutputLimit != 10485760 {
		t.Errorf("OutputLimit = %d, want 10485760", rc.OutputLimit)
	}
	if rc.ImageMagick.Binary != "/usr/bin/magick" {
		t.Errorf("ImageMagick.Binary = %q, want /usr/bin/magick", rc.ImageMagick.Binary)
	}
	if !rc.HTTP.NotFound.Pixel {
		t.Errorf("NotFound.Pixel = %v, want true", rc.HTTP.NotFound.Pixel)
	}
}
