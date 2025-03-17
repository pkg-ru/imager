package controller

import (
	"net/http"

	"github.com/pkg-ru/imager/internal/domain/adapter"
	"github.com/pkg-ru/pkg/files"
)

func (c *Controler) pixel(w http.ResponseWriter, r *http.Request, format string) bool {
	pixel := c.Setting.Paths.Result + "/not-found-pixel." + format
	if files.Exists(pixel) {
		w.WriteHeader(http.StatusNotFound)
		c.File(pixel, w, r)
		return true
	}
	if adapter.Pixel(pixel, c) {
		if files.Exists(pixel) {
			w.WriteHeader(http.StatusNotFound)
			c.File(pixel, w, r)
			return true
		}
	}

	return false
}
