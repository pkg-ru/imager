package adapter

import (
	"os"
	"os/exec"

	"github.com/pkg-ru/imager/internal/domain/logx"
)

func Pixel(file string, c Controller) bool {
	cmd := exec.Command(
		"magick",
		"-background", "rgba(255,255,255,0)",
		"null:", file,
	)
	_, err := cmd.CombinedOutput()
	if err != nil {
		c.LogSer(func(fl logx.FieldLogger) {
			fl.Errorf(`ошибка создания пикселя: %v`, err)
		})
		return false
	}
	os.Chmod(file, 0666)
	return true
}
