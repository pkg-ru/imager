//go:build !onnx

package detection

import (
	"context"
	"errors"
	"testing"
)

// Тесты stub-ветки (сборка БЕЗ тэка "onnx"): детектор создаётся, но
// Available() всегда false, а DetectFaces/DetectObjects возвращают
// ErrNotCompiled — реальный ONNX Runtime в этой сборке невозможен.

func TestStubDetectorAvailable(t *testing.T) {
	// Даже с заданными путями к моделям stub не готов: поддержка ONNX
	// не скомпилирована.
	d := NewDetector(Options{
		FaceModel:   "/models/face.onnx",
		ObjectModel: "/models/obj.onnx",
	})
	if d.Available() {
		t.Error("stub detector Available() = true, want false (no onnx tag)")
	}
}

func TestStubDetectorDetectFacesNotCompiled(t *testing.T) {
	d := NewDetector(Options{FaceModel: "/models/face.onnx"})
	_, err := d.DetectFaces(context.Background(), make([]byte, 3*4*4), 4, 4)
	if !errors.Is(err, ErrNotCompiled) {
		t.Fatalf("DetectFaces err = %v, want ErrNotCompiled", err)
	}
}

func TestStubDetectorDetectObjectsNotCompiled(t *testing.T) {
	d := NewDetector(Options{ObjectModel: "/models/obj.onnx"})
	_, err := d.DetectObjects(context.Background(), make([]byte, 3*4*4), 4, 4)
	if !errors.Is(err, ErrNotCompiled) {
		t.Fatalf("DetectObjects err = %v, want ErrNotCompiled", err)
	}
}
