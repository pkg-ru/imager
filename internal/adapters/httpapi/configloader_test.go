package httpapi

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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
	if rc.Source.DialTimeout.String() != "10s" {
		t.Errorf("Source.DialTimeout = %v, want 10s", rc.Source.DialTimeout)
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
	if rc.Source.DialTimeout.String() != "15s" {
		t.Errorf("DialTimeout = %v, want 15s", rc.Source.DialTimeout)
	}
	if rc.Source.ReadTimeout.String() != "45s" {
		t.Errorf("ReadTimeout = %v, want 45s", rc.Source.ReadTimeout)
	}
	if rc.Source.MaxAttempts != 5 {
		t.Errorf("MaxAttempts = %d, want 5", rc.Source.MaxAttempts)
	}
	if rc.Source.MaxIdleConns != 200 {
		t.Errorf("MaxIdleConns = %d, want 200", rc.Source.MaxIdleConns)
	}
	if rc.Source.MaxIdleConnsPerHost != 20 {
		t.Errorf("MaxIdleConnsPerHost = %d, want 20", rc.Source.MaxIdleConnsPerHost)
	}
	if rc.Source.IdleConnTimeout != 120*time.Second {
		t.Errorf("IdleConnTimeout = %v, want 120s", rc.Source.IdleConnTimeout)
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

func writeConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
