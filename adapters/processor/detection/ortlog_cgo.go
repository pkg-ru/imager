//go:build onnx && cgo

package detection

// ortLogger — опциональный логгер (nil = логирование отключено).
// Устанавливается через SetLogger (см. bootstrap).
var ortLogger Logger

// SetLogger устанавливает опциональный логгер детектора. Передавайте
// observability.Logger / bootstrap.Logger — интерфейс совместим.
func SetLogger(l Logger) { ortLogger = l }

// logORTInfo / logORTWarn — обёртки: не паникуют при nil-логгере.
// Вызываются из onnx_cgo.go (автодетекция библиотеки ONNX Runtime).
func logORTInfo(format string, args ...any) {
	if ortLogger != nil {
		ortLogger.Infof(format, args...)
	}
}

func logORTWarn(format string, args ...any) {
	if ortLogger != nil {
		ortLogger.Warnf(format, args...)
	}
}
