package imagemagick

import (
	"bytes"
	"context"
	"os/exec"
	"testing"

	"github.com/pkg-ru/imager/domain/processing"
	"github.com/pkg-ru/imager/ports/processor"
)

// findMagickBinary ищет валидный ImageMagick binary. На Windows `convert`
// может быть системной утилитой (не ImageMagick), поэтому проверяем вывод
// `-version` на наличие "ImageMagick". Возвращает "" если не найден.
func findMagickBinary() string {
	for _, name := range []string{"magick", "convert"} {
		path, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		out, err := exec.Command(path, "-version").Output()
		if err == nil && bytes.Contains(out, []byte("ImageMagick")) {
			// Возвращаем полный путь, а не имя: на Windows portable-сборка
			// ImageMagick ищет coder-модули по MAGICK_CODER_MODULE_PATH,
			// который envForBinary выставляет из каталога binary. По имени
			// (без пути) каталог не определяется, и `-list format` падает.
			return path
		}
	}
	return ""
}

// TestIntegration_RealBinary — интеграционный тест с реальным ImageMagick.
// Пропускается, если binary не установлен.
func TestIntegration_RealBinary(t *testing.T) {
	binary := findMagickBinary()
	if binary == "" {
		t.Skip("ImageMagick not installed; skipping integration test")
	}

	p, err := New(Options{Binary: binary, DetectCapabilities: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.Capabilities() == nil || !p.Capabilities().HasFormatList() {
		t.Fatal("expected capabilities with format list")
	}
	if p.Capabilities().Major != 6 && p.Capabilities().Major != 7 {
		t.Errorf("unexpected major version %d", p.Capabilities().Major)
	}

	// Генерируем 1x1 PNG через сам ImageMagick как источник. Задаём то же
	// окружение (MAGICK_CODER_MODULE_PATH и т.п.), что и detectCapabilities,
	// иначе на Windows portable-сборка не найдёт coder-модули.
	gen := exec.Command(binary, "-size", "1x1", "xc:red", "png:-")
	gen.Env = MagickEnv(binary)
	srcData, err := gen.Output()
	if err != nil {
		t.Fatalf("generate source: %v", err)
	}

	plan, err := processing.NewProcessingPlan(
		processing.OpResize, processing.FormatPNG, processing.FormatPNG,
		processing.Size{Width: 1, Height: 1}, 1, 80, nil, 0, 0,
	)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	var buf bytes.Buffer
	res, err := p.Process(context.Background(), processor.Input{
		Source: &fakeSource{data: srcData},
		Plan:   plan,
	}, &buf)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.Size <= 0 {
		t.Errorf("expected non-zero output size, got %d", res.Size)
	}
	if buf.Len() == 0 {
		t.Error("expected non-empty output")
	}
}

// TestIntegration_DetectCapabilities — проверка обнаружения capabilities
// через реальный binary (skip, если нет).
func TestIntegration_DetectCapabilities(t *testing.T) {
	binary := findMagickBinary()
	if binary == "" {
		t.Skip("ImageMagick not installed; skipping integration test")
	}
	caps, err := detectCapabilities(context.Background(), binary)
	if err != nil {
		t.Fatalf("detectCapabilities: %v", err)
	}
	if caps.Version == "" {
		t.Error("expected version")
	}
	if !caps.SupportsFormat("png") {
		t.Error("expected png support")
	}
}
