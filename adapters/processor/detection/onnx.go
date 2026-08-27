//go:build onnx

// Реализация OnnxDetector (ONNX Runtime) для сборки с тэком "onnx".
//
// Возможности:
//   - ленивая загрузка моделей (YuNet для лиц, SSD/YOLO для объектов)
//     с потокобезопасным кэшем (mutex + double-checked locking): модель
//     загружается при первом запросе fc/oc и остаётся в памяти до
//     завершения процесса (не выгружается);
//   - препроцессинг (resize к входу модели + нормализация) и
//     постпроцессинг (декодирование боксов, порог уверенности, NMS);
//   - если путь к модели пуст или файл отсутствует — Available() false,
//     Detect* возвращает типизированную ошибку БЕЗ загрузки модели.
//
// Инференс выполняется реальным Go-биндингом ONNX Runtime
// (github.com/yalue/onnxruntime_go): см. onnx_cgo.go (build tag
// "onnx && cgo"). Биндинг использует cgo + dlopen libonnxruntime, поэтому
// для сборок с CGO_ENABLED=0 (CI) предусмотрен onnx_nocgo.go
// ("onnx && !cgo"), возвращающий понятную ошибку.
package detection

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
)

var _ Detector = (*OnnxDetector)(nil)

// ErrModelNotConfigured — модель не сконфигурирована (пустой путь к файлу).
// Возвращается без попытки загрузить модель.
var ErrModelNotConfigured = errors.New("detection: model not configured (empty model path); set detection.face-model / detection.object-model")

// ErrModelNotFound — файл модели не существует.
var ErrModelNotFound = errors.New("detection: model file not found")

// modelBackend — абстракция инференса модели. Реальная реализация — сессия
// ONNX Runtime (см. onnx_cgo.go).
//
// Контракт реализации:
//   - resize входного RGB-кадра к входному размеру модели;
//   - нормализация (если требуется моделью);
//   - возврат сырых боксов ДО NMS в координатах ОРИГИНАЛЬНОГО кадра.
type modelBackend interface {
	// run выполняет инференс над кадром. rgb — RGB-пиксели исходного
	// кадра (len == width*height*3). Возвращает боксы в координатах
	// исходного кадра (до NMS).
	run(ctx context.Context, rgb []byte, width, height int) ([]Box, error)
}

// OnnxDetector — реализация Detector поверх ONNX Runtime.
//
// Модели загружаются лениво (при первом DetectFaces/DetectObjects) и
// кэшируются до завершения процесса: повторные запросы не перечитывают
// файл модели и не повторяют ошибки загрузки (состояние фиксируется один
// раз флажком ready).
type OnnxDetector struct {
	opts Options

	// mu защищает кэш моделей (double-checked locking).
	mu sync.Mutex
	// faceModel, objectModel — кэшированные бэкенды (nil = не загружено).
	faceModel   modelBackend
	objectModel modelBackend
	// faceReady / objectReady — попытка загрузки уже выполнялась
	// (успешно или с зафиксированной ошибкой).
	faceReady   bool
	objectReady bool
}

// NewDetector создаёт OnnxDetector из конфигурации. Модели НЕ загружаются
// при создании: загрузка выполняется лениво при первом запросе.
func NewDetector(opts Options) Detector {
	if opts.ConfidenceThreshold < 0 {
		opts.ConfidenceThreshold = 0
	}
	if opts.ConfidenceThreshold > 1 {
		opts.ConfidenceThreshold = 1
	}
	if opts.MaxObjects < 0 {
		opts.MaxObjects = 0
	}
	return &OnnxDetector{opts: opts}
}

// Available сообщает, сконфигурирован ли хотя бы один детектор (непустой
// путь к модели). НЕ выполняет загрузку и НЕ проверяет существование файла.
func (d *OnnxDetector) Available() bool {
	return d.opts.FaceModel != "" || d.opts.ObjectModel != ""
}

// DetectFaces обнаруживает лица. Модель YuNet загружается лениво при первом
// вызове и кэшируется на время жизни процесса.
func (d *OnnxDetector) DetectFaces(ctx context.Context, rgb []byte, width, height int) ([]Box, error) {
	backend, err := d.load(&d.faceModel, &d.faceReady, d.opts.FaceModel, "face")
	if err != nil {
		return nil, err
	}
	boxes, err := backend.run(ctx, rgb, width, height)
	if err != nil {
		return nil, err
	}
	return d.postprocess(boxes), nil
}

// DetectObjects обнаруживает объекты. Модель загружается лениво.
func (d *OnnxDetector) DetectObjects(ctx context.Context, rgb []byte, width, height int) ([]Box, error) {
	backend, err := d.load(&d.objectModel, &d.objectReady, d.opts.ObjectModel, "object")
	if err != nil {
		return nil, err
	}
	boxes, err := backend.run(ctx, rgb, width, height)
	if err != nil {
		return nil, err
	}
	return d.postprocess(boxes), nil
}

// load загружает модель один раз (double-checked locking): первый вызов
// выполняет загрузку под mutex, последующие — читают закэшированный
// результат или возвращают зафиксированную ранее ошибку.
func (d *OnnxDetector) load(slot *modelBackend, ready *bool, path, kind string) (modelBackend, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if *ready {
		if *slot == nil {
			return nil, ErrModelNotConfigured
		}
		return *slot, nil
	}
	backend, err := d.buildModel(path, kind)
	*ready = true
	if err != nil {
		return nil, err
	}
	*slot = backend
	return backend, nil
}

// postprocess применяет порог уверенности, NMS (IoU 0.45) и лимит
// max-objects. Вход — декодированные боксы в координатах исходного кадра.
func (d *OnnxDetector) postprocess(boxes []Box) []Box {
	if len(boxes) == 0 {
		return nil
	}
	// Порог уверенности (отбрасываем слабые боксы до NMS).
	filtered := boxes[:0]
	for _, b := range boxes {
		if b.Confidence >= d.opts.ConfidenceThreshold {
			filtered = append(filtered, b)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	// NMS подавляет дубли по IoU 0.45.
	out := NMS(filtered, 0.45)
	// Ограничение числа объектов (NMS уже упорядочивает по confidence убыванию).
	if d.opts.MaxObjects > 0 && len(out) > d.opts.MaxObjects {
		out = out[:d.opts.MaxObjects]
	}
	return out
}

// modelExists проверяет наличие файла модели и возвращает типизированную
// ошибку для пустого пути / отсутствующего файла. Используется общими
// реализациями buildModel (onnx_cgo.go / onnx_nocgo.go).
func modelExists(path, kind string) error {
	if path == "" {
		return ErrModelNotConfigured
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s (%s)", ErrModelNotFound, path, kind)
		}
		return fmt.Errorf("detection: stat %s: %w", path, err)
	}
	return nil
}
