package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"

	"github.com/pkg-ru/imager/internal/adapters/processor/imagemagick"
)

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
		return nil, fmt.Errorf("imagemagick: pixel: %w: %s", err, stderr.String())
	}
	return out.Bytes(), nil
}
