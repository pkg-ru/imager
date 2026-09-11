//go:build onnx

package detection

import (
	"context"
	"errors"
	"image"
	_ "image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Тесты реальной ветки OnnxDetector (сборка с тэком "onnx").
//
// При cgo + установленной libonnxruntime (Docker) buildModel грузит модель
// через github.com/yalue/onnxruntime_go. При CGO_ENABLED=0 теги onnx
// собирают onnx_nocgo.go, и buildModel возвращает ошибку "requires cgo".
// Оба пути сохраняют контракт по типизированным ошибкам.

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

func TestOnnxDetectorFakeModelLoadError(t *testing.T) {
	// Существующий, но невалидный файл модели: попытка загрузки происходит,
	// возвращается ошибка загрузки (но НЕ ErrModelNotConfigured/NotFound).
	dir := t.TempDir()
	model := filepath.Join(dir, "face.onnx")
	if err := os.WriteFile(model, []byte("fake-onnx"), 0o644); err != nil {
		t.Fatalf("write model: %v", err)
	}
	d := NewDetector(Options{FaceModel: model})
	_, err := d.DetectFaces(context.Background(), make([]byte, 3*4*4), 4, 4)
	if err == nil {
		t.Fatal("DetectFaces err = nil, want load error for fake model")
	}
	if errors.Is(err, ErrModelNotConfigured) || errors.Is(err, ErrModelNotFound) {
		t.Fatalf("DetectFaces err = %v, want load error (not config/not-found)", err)
	}
}

func TestOnnxDetectorConfidenceClamp(t *testing.T) {
	// Порог уверенности зажимается в [0,1]; негативный MaxObjects → 0.
	d := NewDetector(Options{ConfidenceThreshold: 5, MaxObjects: -3})
	od := d.(*OnnxDetector)
	if od.opts.ConfidenceThreshold != 1 {
		t.Errorf("ConfidenceThreshold = %v, want 1 (clamped)", od.opts.ConfidenceThreshold)
	}
	if od.opts.MaxObjects != 0 {
		t.Errorf("MaxObjects = %d, want 0 (clamped)", od.opts.MaxObjects)
	}
}

// TestOnnxDetectorRuntimeLibOption проверяет, что путь к библиотеке ONNX
// Runtime из конфига (Options.OnnxRuntimeLib) сохраняется в детекторе и
// будет передан в initORT при загрузке модели. Путь задаётся через
// конфиг-файл (detection.onnx-runtime-lib), а не через env.
func TestOnnxDetectorRuntimeLibOption(t *testing.T) {
	d := NewDetector(Options{OnnxRuntimeLib: "/usr/lib/libonnxruntime.so.1.29.0"})
	od := d.(*OnnxDetector)
	if od.opts.OnnxRuntimeLib != "/usr/lib/libonnxruntime.so.1.29.0" {
		t.Errorf("OnnxRuntimeLib = %q, want /usr/lib/libonnxruntime.so.1.29.0", od.opts.OnnxRuntimeLib)
	}
	// Пустое значение = автодетекция по стандартным путям.
	d2 := NewDetector(Options{})
	od2 := d2.(*OnnxDetector)
	if od2.opts.OnnxRuntimeLib != "" {
		t.Errorf("OnnxRuntimeLib = %q, want empty (autodetect)", od2.opts.OnnxRuntimeLib)
	}
}

// ── Реальный инференс (требует libonnxruntime + модели в models/) ──────────

// realModelsPaths возвращает (yunetPath, ssdPath, imgPath, ok) для тестов
// реального инференса: модели и тестовое изображение из каталога моделей.
// ok=false, если файлы отсутствуют — тесты пропускаются.
//
// Каталог можно задать через env IMAGER_MODELS_DIR (как в Docker/compose,
// см. models/README.md); иначе — стандартные кандидаты: локальный ./models
// и смонтированный /etc/imager/models.
func realModelsPaths() (yunet, ssd, img string, ok bool) {
	var candidates []string
	if dir := os.Getenv("IMAGER_MODELS_DIR"); dir != "" {
		candidates = append(candidates, filepath.Join(dir, "face_detection_yunet_2023mar.onnx"))
	} else {
		candidates = []string{
			"../../../models/face_detection_yunet_2023mar.onnx",
			"/etc/imager/models/face_detection_yunet_2023mar.onnx",
		}
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return c,
				filepath.Join(filepath.Dir(c), "ssd_mobilenet_v1_12.onnx"),
				filepath.Join(filepath.Dir(c), "selfie.jpg"),
				true
		}
	}
	return "", "", "", false
}

// realRuntimeLibPath возвращает путь к библиотеке ONNX Runtime, который в
// реальном инференсе берётся из конфиг-файла (detection.onnx-runtime-lib) и
// передаётся через Options.OnnxRuntimeLib. Здесь используем первый
// существующий стандартный кандидат, чтобы подтвердить, что путь из конфига
// работает. Пусто = автодетекция.
//
// Список кандидатов кроссплатформенный (ort_library.go: Linux .so, Windows
// .dll, macOS .dylib), поэтому на каждой ОС реальный инференс находит свою
// библиотеку. Используем именно ЕГО, а не пустой путь, чтобы проверить путь
// из конфига (ветку initORT с непустым libPath).
func realRuntimeLibPath() string {
	return autodetectORTLib()
}

// skipIfNoRuntime пропускает тест, если недоступен реальный ONNX Runtime
// (модели отсутствуют или сборка без cgo).
func skipIfNoRuntime(t *testing.T) {
	t.Helper()
	yunet, _, _, ok := realModelsPaths()
	if !ok {
		t.Skip("models not found in models/; real-inference tests skipped")
	}
	d := NewDetector(Options{FaceModel: yunet, OnnxRuntimeLib: realRuntimeLibPath()})
	if _, err := d.DetectFaces(context.Background(), make([]byte, 3*8*8), 8, 8); err != nil {
		if strings.Contains(err.Error(), "cgo") {
			t.Skipf("no real ONNX Runtime (cgo) available: %v", err)
		}
	}
}

// loadJPEGToRGB декодирует JPEG в RGB-срез и возвращает его с размерами.
func loadJPEGToRGB(t *testing.T, path string) ([]byte, int, int) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	rgb := make([]byte, w*h*3)
	i := 0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			rgb[i] = byte(r >> 8)
			rgb[i+1] = byte(g >> 8)
			rgb[i+2] = byte(bl >> 8)
			i += 3
		}
	}
	return rgb, w, h
}

// TestRealInferenceAutodetectNoLibPath — автодетект библиотеки ONNX Runtime
// при ПУСТОМ detection.onnx-runtime-lib (Options.OnnxRuntimeLib == ""):
// initORT выполняет автодетекцию по платформе (ort_library.go) и реальный
// инференс YuNet работает без явного пути к библиотеке.
// Тест идёт ПЕРВЫМ из реальных: sync.Once инициализирует глобальное
// ORT-окружение именно с пустым путём (ветка автодетекта, а не конфиг-путь).
func TestRealInferenceAutodetectNoLibPath(t *testing.T) {
	yunet, _, img, ok := realModelsPaths()
	if !ok {
		t.Skip("models not found in models/; real-inference tests skipped")
	}
	if !fileExists(autodetectORTLib()) {
		t.Skip("no ONNX Runtime library found by autodetect; real-inference tests skipped")
	}
	rgb, w, h := loadJPEGToRGB(t, img)
	d := NewDetector(Options{FaceModel: yunet, ConfidenceThreshold: 0.5})
	faces, err := d.DetectFaces(context.Background(), rgb, w, h)
	if err != nil {
		t.Fatalf("DetectFaces (autodetect, empty OnnxRuntimeLib): %v", err)
	}
	if len(faces) == 0 {
		t.Fatal("DetectFaces returned 0 faces on selfie.jpg with real YuNet (autodetect, want >= 1)")
	}
	for _, f := range faces {
		if f.W <= 0 || f.H <= 0 {
			t.Errorf("face has non-positive size: %+v", f)
		}
		if f.X < 0 || f.Y < 0 || f.X+f.W > w || f.Y+f.H > h {
			t.Errorf("face box out of frame: %+v (frame %dx%d)", f, w, h)
		}
	}
}

// TestRealYuNetInference — реальный инференс YuNet на официальном изображении:
// на selfie.jpg (2048x1150, множество лиц) должно найтись НЕСКОЛЬКО лиц.
func TestRealYuNetInference(t *testing.T) {
	skipIfNoRuntime(t)
	yunet, _, img, ok := realModelsPaths()
	if !ok {
		t.Skip("models not found")
	}
	rgb, w, h := loadJPEGToRGB(t, img)
	d := NewDetector(Options{FaceModel: yunet, ConfidenceThreshold: 0.5, OnnxRuntimeLib: realRuntimeLibPath()})
	faces, err := d.DetectFaces(context.Background(), rgb, w, h)
	if err != nil {
		t.Fatalf("DetectFaces: %v", err)
	}
	if len(faces) == 0 {
		t.Fatal("DetectFaces returned 0 faces on selfie.jpg with real YuNet (want >= 1)")
	}
	for _, f := range faces {
		if f.W <= 0 || f.H <= 0 {
			t.Errorf("face has non-positive size: %+v", f)
		}
		if f.X < 0 || f.Y < 0 || f.X+f.W > w || f.Y+f.H > h {
			t.Errorf("face box out of frame: %+v (frame %dx%d)", f, w, h)
		}
	}
}

// TestRealSSDInference — реальный инференс SSD на официальном изображении:
// на selfie.jpg (много людей) должен найтись person (COCO класс 1) хотя бы
// один с уверенностью выше 0.5.
func TestRealSSDInference(t *testing.T) {
	skipIfNoRuntime(t)
	_, ssd, img, ok := realModelsPaths()
	if !ok {
		t.Skip("models not found")
	}
	rgb, w, h := loadJPEGToRGB(t, img)
	d := NewDetector(Options{ObjectModel: ssd, ConfidenceThreshold: 0.5, OnnxRuntimeLib: realRuntimeLibPath()})
	objs, err := d.DetectObjects(context.Background(), rgb, w, h)
	if err != nil {
		t.Fatalf("DetectObjects: %v", err)
	}
	person := false
	for _, o := range objs {
		if o.Label == "COCO_person" {
			person = true
		}
		if o.W <= 0 || o.H <= 0 {
			t.Errorf("object has non-positive size: %+v", o)
		}
		if o.X < 0 || o.Y < 0 || o.X+o.W > w || o.Y+o.H > h {
			t.Errorf("object box out of frame: %+v (frame %dx%d)", o, w, h)
		}
	}
	if !person {
		t.Fatalf("DetectObjects: no person found on selfie.jpg (got %d objects)", len(objs))
	}
}
