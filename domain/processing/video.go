package processing

import "strings"

// videoFormats — множество видео-контейнеров, которые ffmpeg/ffprobe умеют
// декодировать на вход (для генерации превью/ассета из одного кадра).
// Список фиксирует, какие расширения исходников обрабатываются через
// VideoExtractor (см. app/generatev2), а не через libvips-процессор
// (процессоры не умеют декодировать видео).
var videoFormats = map[string]struct{}{
	"mp4": {}, "webm": {}, "mov": {}, "mkv": {}, "avi": {}, "m4v": {},
	"mpg": {}, "mpeg": {}, "wmv": {}, "flv": {}, "3gp": {}, "ogv": {},
	"ts": {}, "mts": {}, "m2ts": {},
}

// IsVideoFormat сообщает, является ли формат (расширение, регистронезависимо)
// видео-контейнером, поддерживаемым ffmpeg на вход.
func IsVideoFormat(f string) bool {
	_, ok := videoFormats[strings.ToLower(f)]
	return ok
}
