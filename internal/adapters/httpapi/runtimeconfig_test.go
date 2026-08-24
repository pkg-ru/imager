package httpapi

import (
	"testing"
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

// TestParseRuntimeConfigMetadataDirAccepted проверяет, что metadata.dir теперь
// ПОДДЕРЖИВАЕТСЯ: явный локальный корень sidecar-хранилища, независимый от
// хранилищ source/result. Пусто = дефолт (пустая строка → деривация в DI).
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
// metadata.dir поле остаётся пустым (дефолт <resultRoot>/.meta применяется
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
