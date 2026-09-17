package processing

import "testing"

// TestIsVideoFormat — полный список видео-контейнеров, декодируемых ffmpeg
// на вход, и негативные кейсы (картинки, пустая строка).
func TestIsVideoFormat(t *testing.T) {
	video := []string{
		"mp4", "webm", "mov", "mkv", "avi", "m4v",
		"mpg", "mpeg", "wmv", "flv", "3gp", "ogv",
		"ts", "mts", "m2ts",
		"MP4", "WebM", "MOV", "M2TS",
	}
	for _, f := range video {
		if !IsVideoFormat(f) {
			t.Errorf("IsVideoFormat(%q) = false, want true", f)
		}
	}
	nonVideo := []string{
		"jpg", "jpeg", "png", "webp", "gif", "apng",
		"avif", "heif", "heic", "jxl", "svg",
		"html", "txt", "", "MP5",
	}
	for _, f := range nonVideo {
		if IsVideoFormat(f) {
			t.Errorf("IsVideoFormat(%q) = true, want false", f)
		}
	}
}
