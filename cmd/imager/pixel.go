package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"

	"github.com/pkg-ru/imager/internal/adapters/processor/imagemagick"
)

// maxStderrLogBytes — максимальное число байт stderr, попадающих в ошибку/лог.
// П.3: сырой stderr подпроцесса не должен целиком утекать в ошибку/лог
// (может содержать чувствительные данные). Логируем только код ошибки и
// обрезанный, экранированный превью первых байт.
const maxStderrLogBytes = 256

// generatePixel генерирует прозрачный 1x1 пиксель в заданном формате через
// ImageMagick binary (stdout). Не использует shell: argv строится напрямую.
func generatePixel(ctx context.Context, binary, format string) ([]byte, error) {
	args := []string{
		"-background", "rgba(255,255,255,0)",
		"null:",
		format + ":-",
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	// Наследуем окружение процесса и добавляем пути к coders/filters/config
	// рядом с binary (portable Windows-сборки не находят их в %LOCALAPPDATA%).
	cmd.Env = imagemagick.MagickEnv(binary)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// П.3: не включаем сырой stderr в ошибку/лог. Только код ошибки и
		// обрезанный превью (первые maxStderrLogBytes байт), экранированный
		// от управляющих символов.
		preview := stderrPreview(stderr.Bytes())
		return nil, fmt.Errorf("imagemagick: pixel: %w (stderr: %s)", err, preview)
	}
	return out.Bytes(), nil
}

// stderrPreview возвращает обрезанный и экранированный превью stderr.
func stderrPreview(b []byte) string {
	if len(b) > maxStderrLogBytes {
		b = b[:maxStderrLogBytes]
	}
	var sb bytes.Buffer
	for _, c := range b {
		switch {
		case c == '\n' || c == '\r' || c == '\t':
			sb.WriteByte(c)
		case c >= 0x20 && c < 0x7f:
			sb.WriteByte(c)
		default:
			// Экранируем управляющие/не-ASCII байты.
			sb.WriteString(fmt.Sprintf("\\x%02x", c))
		}
	}
	return sb.String()
}
