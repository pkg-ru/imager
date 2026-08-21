package imagemagick

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// Capabilities — immutable снимок возможностей ImageMagick binary.
//
// Снимок создаётся один раз на экземпляр Processor (не глобально) и
// содержит identity binary (путь, версия) и доступные форматы. Ошибки
// обнаружения не кэшируются глобально: каждый экземпляр получает свой
// снимок, и ошибка не "заражает" другие экземпляры.
type Capabilities struct {
	// Binary — путь к бинарю.
	Binary string
	// Version — версия ImageMagick (из `-version`).
	Version string
	// Major — старшая версия (6 или 7).
	Major int
	// Formats — отсортированный список форматов (нижний регистр),
	// поддерживающих и чтение, и запись.
	Formats []string
	// formatSet — множество для O(1) проверки.
	formatSet map[string]struct{}
}

// SupportsFormat возвращает true, если формат поддерживается (регистронезависимо).
func (c *Capabilities) SupportsFormat(format string) bool {
	if c == nil || c.formatSet == nil {
		return false
	}
	_, ok := c.formatSet[strings.ToLower(format)]
	return ok
}

// HasFormatList сообщает, был ли получен реальный список форматов.
func (c *Capabilities) HasFormatList() bool {
	return c != nil && c.formatSet != nil
}

// detectCapabilities запускает `binary -version` и `binary -list format`
// и строит immutable снимок. Ошибка возвращается без глобального кэширования.
func detectCapabilities(ctx context.Context, binary string) (*Capabilities, error) {
	version, err := detectVersion(ctx, binary)
	if err != nil {
		return nil, err
	}
	formats, err := detectFormats(ctx, binary)
	if err != nil {
		return nil, err
	}
	set := make(map[string]struct{}, len(formats))
	for _, f := range formats {
		set[f] = struct{}{}
	}
	return &Capabilities{
		Binary:    binary,
		Version:   version,
		Major:     majorVersion(version),
		Formats:   formats,
		formatSet: set,
	}, nil
}

// detectVersion запускает `binary -version` и извлекает версию.
func detectVersion(ctx context.Context, binary string) (string, error) {
	cmd := exec.CommandContext(ctx, binary, "-version")
	cmd.Env = MagickEnv(binary)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("imagemagick: %s -version: %w: %s", binary, err, strings.TrimSpace(string(out)))
	}
	// Первая строка: "Version: ImageMagick 7.1.1-35 Q16-HDRI x86_64 ..."
	line := firstLine(string(out))
	fields := strings.Fields(line)
	for i, f := range fields {
		if f == "ImageMagick" && i+1 < len(fields) {
			return fields[i+1], nil
		}
	}
	return "", fmt.Errorf("imagemagick: %s -version: cannot parse version from %q", binary, line)
}

// majorVersion извлекает старшую версию из строки версии.
func majorVersion(version string) int {
	// "7.1.1-35" -> 7
	dot := strings.IndexByte(version, '.')
	if dot <= 0 {
		return 0
	}
	var major int
	for _, ch := range version[:dot] {
		if ch < '0' || ch > '9' {
			return 0
		}
		major = major*10 + int(ch-'0')
	}
	return major
}

// detectFormats запускает `binary -list format` и возвращает список форматов,
// поддерживающих и чтение, и запись.
func detectFormats(ctx context.Context, binary string) ([]string, error) {
	cmd := exec.CommandContext(ctx, binary, "-list", "format")
	cmd.Env = MagickEnv(binary)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("imagemagick: %s -list format: %w: %s", binary, err, strings.TrimSpace(string(out)))
	}
	list, err := parseFormatList(string(out), binary)
	if err != nil {
		return nil, err
	}
	sort.Strings(list)
	return list, nil
}

// parseFormatList разбирает вывод `binary -list format`.
//
// Пример строки:
//
//	PNG  rw-  Portable Network Graphics...
//
// Нас интересует первый столбец (имя формата) и режим (второй столбец):
// 'r' — чтение, 'w' — запись. Сохраняем только форматы, которые
// поддерживают и чтение, и запись, в нижнем регистре.
// parseFormatList разбирает вывод `binary -list format`.
//
// Формат строки зависит от версии ImageMagick:
//
//   - PNG  rw-  Portable Network Graphics...   (IM6: FORMAT MODE DESCRIPTION)
//     PNG* PNG  rw-  Portable Network Graphics... (IM7: FORMAT MODULE MODE DESCRIPTION)
//
// Нас интересует имя формата (первый столбец) и режим (столбец, состоящий
// только из 'r'/'w'/'-'/'+'): 'r' — чтение, 'w' — запись. Сохраняем только
// форматы, которые поддерживают и чтение, и запись, в нижнем регистре.
func parseFormatList(out string, binary string) ([]string, error) {
	set := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		// В IM: "*" выступает маркером формата по умолчанию. Он может быть
		// префиксом ("* PNG  rw-  ...") или суффиксом ("PNG* PNG  rw-  ...").
		name := fields[0]
		if name == "*" {
			if len(fields) < 3 {
				continue
			}
			name = fields[1]
		}
		// Суффикс "*" (например "PNG*") — маркер формата по умолчанию.
		// Убираем его, чтобы имя формата было чистым ("PNG").
		name = strings.TrimSuffix(name, "*")
		// Пропускаем строки-заголовки.
		if name == "Format" || strings.HasPrefix(name, "---") {
			continue
		}
		// Режим — столбец, состоящий только из 'r'/'w'/'-'/'+'. Позиция
		// столбца различается между IM6 (FORMAT MODE) и IM7 (FORMAT MODULE
		// MODE), поэтому ищем его по содержимому, а не по индексу.
		var mode string
		for _, f := range fields[1:] {
			if isModeField(f) {
				mode = f
				break
			}
		}
		if mode == "" {
			continue
		}
		if !strings.Contains(mode, "r") || !strings.Contains(mode, "w") {
			continue
		}
		set[strings.ToLower(name)] = true
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(set) == 0 {
		return nil, fmt.Errorf("imagemagick: no readable/writable formats from '%s -list format'", binary)
	}
	list := make([]string, 0, len(set))
	for f := range set {
		list = append(list, f)
	}
	return list, nil
}

// isModeField возвращает true, если поле — это столбец режима формата
// (состоит только из 'r'/'w'/'-'/'+', например "rw-", "r--", "rw+").
func isModeField(s string) bool {
	if s == "" {
		return false
	}
	for _, ch := range s {
		switch ch {
		case 'r', 'w', '-', '+':
		default:
			return false
		}
	}
	return true
}

// firstLine возвращает первую непустую строку.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			return strings.TrimSpace(line)
		}
	}
	return ""
}
