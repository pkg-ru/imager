package controller

import (
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/pkg-ru/imager/internal/domain/adapter"
	imagerencode "github.com/pkg-ru/imager/pkg/imager/imager-encode"
	"github.com/pkg-ru/pkg/files"
)

func (c *Controler) process(w http.ResponseWriter, r *http.Request) bool {
	requestUri := strings.Split(r.RequestURI, "?")

	reg := regexp.MustCompile("(\\.?\\./|\x00|%00)")
	file := strings.TrimLeft(reg.ReplaceAllString(requestUri[0], ""), "/")

	lastIndex := strings.LastIndex(file, ".")
	format := file[lastIndex+1:]
	formatSmall := strings.ToLower(format)
	if slices.Contains(imagerencode.FormatsList(), formatSmall) {
		filePathSource := c.Setting.Paths.Source + "/" + file
		filePathResult := c.Setting.Paths.Result + "/" + file

		isContentType := slices.Contains([]string{"avif", "heif", "heic"}, formatSmall)
		if isContentType {
			w.Header().Set("Content-Type", "image/"+formatSmall)
		}

		if files.Exists(filePathSource) {
			c.File(filePathSource, w, r)
			return true
		}
		if files.Exists(filePathResult) {
			c.File(filePathResult, w, r)
			return true
		}
		if adapter.Get(file, c) {
			c.File(filePathResult, w, r)
			return true
		}
		if c.Setting.NotFound.Pixel {
			return c.pixel(w, r, formatSmall)
		}

		if isContentType {
			w.Header().Del("Content-Type")
		}
	}

	return false
}
