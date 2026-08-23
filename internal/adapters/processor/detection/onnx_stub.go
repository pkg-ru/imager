//go:build !onnx

// Заглушка детектора для сборок БЕЗ тэка "onnx".
//
// Реального ONNX Runtime в этой сборке нет: Detector создаётся, но
// Available() возвращает false, а DetectFaces/DetectObjects возвращают
// понятную ошибку о том, что поддержка не скомпилирована.
package detection

import (
	"context"
	"errors"
)

var _ Detector = (*stubDetector)(nil)

// stubDetector — заглушка Detector, используемая в сборках без тэка "onnx".
type stubDetector struct{}

// NewDetector создаёт детектор. Возвращается неизменная заглушка: в сборке
// без тэка "onnx" реальная инференс-модель невозможна в принципе, поэтому
// конфигурация игнорируется.
func NewDetector(_ Options) Detector {
	return &stubDetector{}
}

// Available всегда false: поддержка ONNX не скомпилирована.
func (s *stubDetector) Available() bool { return false }

// DetectFaces возвращает ErrNotCompiled.
func (s *stubDetector) DetectFaces(_ context.Context, _ []byte, _, _ int) ([]Box, error) {
	return nil, ErrNotCompiled
}

// DetectObjects возвращает ErrNotCompiled.
func (s *stubDetector) DetectObjects(_ context.Context, _ []byte, _, _ int) ([]Box, error) {
	return nil, ErrNotCompiled
}

// ErrNotCompiled — ошибка, которую возвращает детектор, если пакет собран
// без тэка "onnx" (например, go test / go build без установленного
// ONNX Runtime).
var ErrNotCompiled = errors.New("detection: ONNX Runtime support not compiled in (build with -tags onnx)")
