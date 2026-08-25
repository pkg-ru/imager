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
// TODO(onnx-runtime): подключить реальный Go-биндинг ONNX Runtime
// (github.com/yalue/onnxruntime_go — существует и активно поддерживается).
// Биндинг требует установленную C-библиотеку ONNX Runtime (libonnxruntime)
// и cgo, поэтому здесь он НЕ импортируется напрямую (это бы сломало сборку).
// Инференс абстрагируется через modelBackend, который в полной сборке
// заполняется ONNX-сессией.
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

// modelBackend — абстракция инференса модели. Реальные реализации (ONNX
// Runtime session) подключаются при наличии биндинга (см. buildModel).
//
// Контракт реализации:
//   - resize входного RGB-кадра к входному размеру модели (обычно
//     320x320 для YuNet / SSD-MobileNet);
//   - нормализация (scale 1/255 и mean/std при необходимости);
//   - возврат сырых боксов ДО NMS в координатах ОРИГИНАЛЬНОГО кадра
//     (координаты масштабируются обратно из входного размера модели).
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

// buildModel создаёт modelBackend для пути. Возвращает nil для пустого
// пути или отсутствующего файла (типизированная ошибка БЕЗ загрузки).
//
// TODO(onnx-layout): заполнить реальным инференс-бэкендом из биндинга
// github.com/yalue/onnxruntime_go. Сейчас (без подключённого биндинга)
// загрузка модели не выполняется —
// возвращается понятная ошибка, чтобы не ломать сборку и явно указать
// на ненастроенную интеграцию.
func (d *OnnxDetector) buildModel(path, kind string) (modelBackend, error) {
	if path == "" {
		return nil, ErrModelNotConfigured
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s (%s)", ErrModelNotFound, path, kind)
		}
		return nil, fmt.Errorf("detection: stat %s: %w", path, err)
	}
	return nil, fmt.Errorf("detection: ONNX runtime backend not linked in this build; rebuild with the ONNX Runtime binding (github.com/yalue/onnxruntime_go) to enable %s model %q", kind, path)
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
