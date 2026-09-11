//go:build onnx && !cgo

// Фолбэк для сборок с тэком "onnx", но CGO_ENABLED=0 (например, CI на
// ubuntu/windows без библиотеки libonnxruntime).
//
// Биндинг github.com/yalue/onnxruntime_go построен на cgo (dlopen) и при
// CGO_ENABLED=0 не имеет собираемых файлов, поэтому импортировать его нельзя.
// Чтобы `go build -tags onnx` (без cgo) продолжал работать, buildModel
// возвращает понятную ошибку о том, что требуется cgo + libonnxruntime.
package detection

// buildModel — фолбэк-версия: модель есть на диске, но реальный инференс
// невозможен без cgo-сборки. Возвращает понятную ошибку.
func (d *OnnxDetector) buildModel(path, kind string) (modelBackend, error) {
	if err := modelExists(path, kind); err != nil {
		return nil, err
	}
	return nil, errCGoRequired
}

// errCGoRequired — ошибка для сборки "onnx && !cgo".
var errCGoRequired = &cgoRequiredError{}

type cgoRequiredError struct{}

func (cgoRequiredError) Error() string {
	return "detection: ONNX Runtime requires cgo (build with CGO_ENABLED=1 and the libonnxruntime library)"
}
