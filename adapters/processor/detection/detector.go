package detection

import "context"

// Detector — интерфейс детектора лиц/объектов.
//
// Реализации: OnnxDetector (onnx.go, build tag "onnx") с ленивой загрузкой
// моделей ONNX Runtime, и заглушка stubDetector (onnx_stub.go, build tag
// "!onnx"), возвращающая понятную ошибку «собрано без тэка onnx».
//
// Контракт:
//   - DetectFaces/DetectObjects принимают RGB-пиксели изображения
//     (3 байта на пиксель, порядок R,G,B) и его размеры; возвращают боксы
//     в пиксельных координатах исходного изображения;
//   - Available() сообщает, сконфигурирован ли хотя бы один детектор
//     (непустой путь к модели и собранная поддержка ONNX);
//   - если модель не сконфигурирована (пустой путь) — Detect* возвращает
//     типизированную ошибку ErrModelNotConfigured БЕЗ загрузки модели;
//   - если сборка без тэка "onnx" — Detect* возвращает ErrNotCompiled.
type Detector interface {
	// DetectFaces обнаруживает лица на изображении. rgb — непрерывный
	// массив RGB (len == width*height*3).
	DetectFaces(ctx context.Context, rgb []byte, width, height int) ([]Box, error)
	// DetectObjects обнаруживает объекты на изображении.
	DetectObjects(ctx context.Context, rgb []byte, width, height int) ([]Box, error)
	// Available сообщает, готов ли хотя бы один детектор к работе
	// (сконфигурированы модели и присутствует сборка с ONNX).
	Available() bool
}

// Options — конфигурация детектора.
//
// Пустой путь к модели (FaceModel/ObjectModel) означает, что
// соответствующий детектор не используется: модель не загружается,
// DetectFaces/DetectObjects возвращают ErrModelNotConfigured.
type Options struct {
	// FaceModel — путь к ONNX-модели YuNet для детекции лиц.
	// Пусто = face-crop не используется.
	FaceModel string
	// ObjectModel — путь к ONNX-моде SSD/YOLO для детекции объектов.
	// Пусто = object-crop не используется.
	ObjectModel string
	// OnnxRuntimeLib — путь к библиотеке libonnxruntime (dlopen). Пусто =
	// автодетекция по стандартным путям (см. onnx_cgo.go). Задаётся через
	// конфиг-файл (detection.onnx-runtime-lib), а не через env.
	OnnxRuntimeLib string
	// ConfidenceThreshold — порог уверенности в интервале [0,1].
	// Боксы с Confidence < порога отбрасываются до NMS.
	ConfidenceThreshold float64
	// MaxObjects — максимальное число объектов, сохраняемых после NMS
	// (первый N самых уверенных). 0 = без ограничения.
	MaxObjects int
}
