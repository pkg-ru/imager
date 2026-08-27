//go:build onnx

package detection

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Тесты реальной ветки OnnxDetector (сборка с тэком "onnx").
//
// Реальный ONNX Runtime биндинг в проекте НЕ подключён (см. TODO в onnx.go),
// поэтому buildModel всегда возвращает ошибку «backend not linked». Эти тесты
// фиксируют контракт инициализации и обработки ошибок моделей, который
// остаётся валидным и после подключения реального биндинга.

func TestOnnxDetectorAvailable(t *testing.T) {
	// Пустые пути — детектор не сконфигурирован.
	if d := NewDetector(Options{}); d.Available() {
		t.Error("Available() = true with empty models, want false")
	}
	// Непустой путь к модели — детектор сконфигурирован (файл не проверяется).
	if d := NewDetector(Options{FaceModel: "/models/face.onnx"}); !d.Available() {
		t.Error("Available() = false with face model set, want true")
	}
	if d := NewDetector(Options{ObjectModel: "/models/obj.onnx"}); !d.Available() {
		t.Error("Available() = false with object model set, want true")
	}
}

func TestOnnxDetectorEmptyModelNotConfigured(t *testing.T) {
	// Пустой путь к модели — типизированная ошибка БЕЗ загрузки модели.
	d := NewDetector(Options{})
	if _, err := d.DetectFaces(context.Background(), make([]byte, 3*4*4), 4, 4); !errors.Is(err, ErrModelNotConfigured) {
		t.Fatalf("DetectFaces err = %v, want ErrModelNotConfigured", err)
	}
	if _, err := d.DetectObjects(context.Background(), make([]byte, 3*4*4), 4, 4); !errors.Is(err, ErrModelNotConfigured) {
		t.Fatalf("DetectObjects err = %v, want ErrModelNotConfigured", err)
	}
}

func TestOnnxDetectorModelNotFound(t *testing.T) {
	// Несуществующий файл модели: типизированная ошибка ErrModelNotFound.
	missing := filepath.Join(t.TempDir(), "missing.onnx")
	d := NewDetector(Options{FaceModel: missing})
	if _, err := d.DetectFaces(context.Background(), make([]byte, 3*4*4), 4, 4); !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("DetectFaces err = %v, want ErrModelNotFound", err)
	}
}

func TestOnnxDetectorExistingModelBackendNotLinked(t *testing.T) {
	// Существующий файл модели: загрузка модели не выполняется, т.к. реальный
	// ONNX Runtime биндинг не подключён — возвращается понятная ошибка.
	dir := t.TempDir()
	model := filepath.Join(dir, "face.onnx")
	if err := os.WriteFile(model, []byte("fake-onnx"), 0o644); err != nil {
		t.Fatalf("write model: %v", err)
	}
	d := NewDetector(Options{FaceModel: model})
	_, err := d.DetectFaces(context.Background(), make([]byte, 3*4*4), 4, 4)
	if err == nil {
		t.Fatal("DetectFaces err = nil, want backend-not-linked error")
	}
	if errors.Is(err, ErrModelNotConfigured) || errors.Is(err, ErrModelNotFound) {
		t.Fatalf("DetectFaces err = %v, want backend-not-linked error (not config/not-found)", err)
	}
}

func TestOnnxDetectorConfidenceClamp(t *testing.T) {
	// Порог уверенности зажимается в [0,1]; отрицательный MaxObjects → 0.
	d := NewDetector(Options{ConfidenceThreshold: 5, MaxObjects: -3})
	od := d.(*OnnxDetector)
	if od.opts.ConfidenceThreshold != 1 {
		t.Errorf("ConfidenceThreshold = %v, want 1 (clamped)", od.opts.ConfidenceThreshold)
	}
	if od.opts.MaxObjects != 0 {
		t.Errorf("MaxObjects = %d, want 0 (clamped)", od.opts.MaxObjects)
	}
}
