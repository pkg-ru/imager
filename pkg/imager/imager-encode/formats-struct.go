package imagerencode

import "strings"

type format uint8
type formatList map[format]string

const (
	formatJpg  format = 1
	formatJpeg format = 2
	formatGif  format = 3
	formatPng  format = 4
	formatApng format = 5
	formatJpe  format = 6
	formatJif  format = 7
	formatJfif format = 8
	formatJfi  format = 9
	formatWebp format = 10
	formatAvif format = 11
	formatHeif format = 12
	formatHeic format = 13
)

var formats formatList = formatList{
	formatJpg:  "jpg",
	formatJpeg: "jpeg",
	formatGif:  "gif",
	formatPng:  "png",
	formatApng: "apng",
	formatJpe:  "jpe",
	formatJif:  "jif",
	formatJfif: "jfif",
	formatJfi:  "jfi",
	formatWebp: "webp",
	formatAvif: "avif",
	formatHeif: "heif",
	formatHeic: "heic",
}

// список форматов разделенный "|" для регулярных выражений
var FORMATS string

type listFormat map[string]format

var local struct {
	list  []string
	lists listFormat
}

func init() {
	if local.lists == nil {
		local.list = []string{}
		local.lists = listFormat{}
		for format, value := range formats {
			local.list = append(local.list, value)
			local.lists[value] = format
		}
		FORMATS = strings.Join(local.list, "|")
	}
}
