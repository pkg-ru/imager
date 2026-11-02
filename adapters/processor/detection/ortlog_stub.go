//go:build !(onnx && cgo)

package detection

// SetLogger — no-op без сборки "onnx && cgo": автодетекция ONNX Runtime
// (единственный источник сообщений) в этой сборке не компилируется.
// Функция сохранена, чтобы bootstrap вызывал её безусловно.
func SetLogger(_ Logger) {}
