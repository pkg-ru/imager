package imagemagick

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PolicyConfig — настройки deny-by-default policy.xml.
type PolicyConfig struct {
	// Enabled — включать ли генерацию policy.xml и передачу через
	// MAGICK_CONFIGURE_PATH. Если false, полагаемся на системную policy.
	Enabled bool
	// Dir — каталог, куда записывается policy.xml. Если пуст, используется
	// временный каталог (os.MkdirTemp).
	Dir string
	// MaxMemoryBytes, MaxMapBytes, MaxDiskBytes, MaxThreads, MaxTimeSeconds,
	// MaxWidth, MaxHeight, MaxPixels, MaxFrames — resource policies
	// (0 = не задавать).
	MaxMemoryBytes int64
	MaxMapBytes    int64
	MaxDiskBytes   int64
	MaxThreads     int
	MaxTimeSeconds int
	MaxWidth       int64
	MaxHeight      int64
	MaxPixels      int64
	MaxFrames      int
	// DisableNetwork — отключать network-capable delegates (URL, HTTPS, FTP,
	// MSL, MVG, ...). По умолчанию true.
	DisableNetwork bool
	// DisabledCoders — дополнительные coders для запрета (deny).
	DisabledCoders []string
	// DisabledDelegates — дополнительные delegates для запрета (deny).
	DisabledDelegates []string
}

// policyXML строит deny-by-default policy.xml.
//
// Политика запрещает (deny) все coders/delegates по умолчанию и разрешает
// (allow) только безопасный whitelist. Network-capable delegates и опасные
// coders (MSL, MVG, URL, HTTPS, FTP, ...) отключаются явно. Никакого shell
// execution не добавляется.
func policyXML(cfg PolicyConfig) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE policymap [
  <!ELEMENT policymap (policy)*>
  <!ATTLIST policymap xmlns CDATA #FIXED "">
  <!ELEMENT policy EMPTY>
  <!ATTLIST policy domain CDATA #REQUIRED
    name CDATA #IMPLIED
    rights CDATA #IMPLIED
    pattern CDATA #IMPLIED
    value CDATA #IMPLIED>
]>
<policymap>
`)

	// Resource limits.
	if cfg.MaxMemoryBytes > 0 {
		fmt.Fprintf(&b, "  <policy domain=\"resource\" name=\"memory\" value=\"%d\"/>\n", cfg.MaxMemoryBytes)
	}
	if cfg.MaxMapBytes > 0 {
		fmt.Fprintf(&b, "  <policy domain=\"resource\" name=\"map\" value=\"%d\"/>\n", cfg.MaxMapBytes)
	}
	if cfg.MaxDiskBytes > 0 {
		fmt.Fprintf(&b, "  <policy domain=\"resource\" name=\"disk\" value=\"%d\"/>\n", cfg.MaxDiskBytes)
	}
	if cfg.MaxThreads > 0 {
		fmt.Fprintf(&b, "  <policy domain=\"resource\" name=\"thread\" value=\"%d\"/>\n", cfg.MaxThreads)
	}
	if cfg.MaxTimeSeconds > 0 {
		fmt.Fprintf(&b, "  <policy domain=\"resource\" name=\"time\" value=\"%d\"/>\n", cfg.MaxTimeSeconds)
	}
	if cfg.MaxWidth > 0 {
		fmt.Fprintf(&b, "  <policy domain=\"resource\" name=\"width\" value=\"%d\"/>\n", cfg.MaxWidth)
	}
	if cfg.MaxHeight > 0 {
		fmt.Fprintf(&b, "  <policy domain=\"resource\" name=\"height\" value=\"%d\"/>\n", cfg.MaxHeight)
	}
	if cfg.MaxPixels > 0 {
		// area — лимит площади (width*height), защита от decompression bomb
		// (C2). width/height задаются отдельно из политики.
		fmt.Fprintf(&b, "  <policy domain=\"resource\" name=\"area\" value=\"%d\"/>\n", cfg.MaxPixels)
	}
	if cfg.MaxFrames > 0 {
		fmt.Fprintf(&b, "  <policy domain=\"resource\" name=\"list-length\" value=\"%d\"/>\n", cfg.MaxFrames)
	}

	// Deny-by-default: запрещаем все coders и delegates.
	b.WriteString("  <policy domain=\"coder\" rights=\"none\" pattern=\"*\"/>\n")
	b.WriteString("  <policy domain=\"delegate\" rights=\"none\" pattern=\"*\"/>\n")

	// Разрешаем безопасный whitelist coders.
	for _, c := range safeCoders {
		fmt.Fprintf(&b, "  <policy domain=\"coder\" rights=\"read|write\" pattern=\"%s\"/>\n", c)
	}

	// Явно запрещаем network-capable и опасные coders (двойная защита).
	for _, c := range dangerousCoders {
		fmt.Fprintf(&b, "  <policy domain=\"coder\" rights=\"none\" pattern=\"%s\"/>\n", c)
	}
	for _, c := range cfg.DisabledCoders {
		fmt.Fprintf(&b, "  <policy domain=\"coder\" rights=\"none\" pattern=\"%s\"/>\n", c)
	}

	// Запрещаем network-capable delegates.
	if cfg.DisableNetwork {
		for _, d := range networkDelegates {
			fmt.Fprintf(&b, "  <policy domain=\"delegate\" rights=\"none\" pattern=\"%s\"/>\n", d)
		}
	}
	for _, d := range cfg.DisabledDelegates {
		fmt.Fprintf(&b, "  <policy domain=\"delegate\" rights=\"none\" pattern=\"%s\"/>\n", d)
	}

	b.WriteString("</policymap>\n")
	return b.Bytes(), nil
}

// safeCoders — whitelist безопасных coders (чтение/запись).
// SVG исключён (C6): растризация SVG выполняется через delegates
// (rsvg/inkscape) и несёт риски SSRF (xlink:href) и decompression bomb.
var safeCoders = []string{
	"JPEG", "JPG", "PNG", "WEBP", "GIF", "AVIF", "HEIC", "HEIF", "APNG",
	"JXL", "MIFF", "PPM", "PGM", "PBM", "PNM", "TIFF", "BMP", "ICO",
}

// dangerousCoders — coders, которые запрещаем явно (network/scripting).
// SVG добавлен: даже если whitelist не сработает, SVG запрещён явно.
var dangerousCoders = []string{
	"URL", "HTTPS", "HTTP", "FTP", "MSL", "MVG", "LABEL", "TEXT", "TXT",
	"PLASMA", "XC", "HALD", "WPG", "PS", "PDF", "EPI", "EPS", "EPT", "XPS",
	"SVG", "SVGZ",
}

// networkDelegates — network-capable delegates для запрета.
// rsvg/inkscape — delegates растризации SVG (C6): запрещены явно.
var networkDelegates = []string{
	"https", "http", "ftp", "ftps", "sftp", "scp", "curl", "wget", "ssh",
	"rsvg", "inkscape",
}

// writePolicyXML записывает policy.xml в каталог и возвращает путь к каталогу
// для MAGICK_CONFIGURE_PATH. Возвращает пустую строку, если политика
// отключена.
//
// Запись атомарна: данные пишутся во временный файл в том же каталоге
// и переименовываются в policy.xml, чтобы ImageMagick никогда не увидел
// частично записанный файл.
func writePolicyXML(cfg PolicyConfig) (string, error) {
	if !cfg.Enabled {
		return "", nil
	}
	dir := cfg.Dir
	if dir == "" {
		var err error
		dir, err = os.MkdirTemp("", "imager-magick-policy-*")
		if err != nil {
			return "", fmt.Errorf("imagemagick: create policy dir: %w", err)
		}
	}
	dir = sanitizePolicyDir(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("imagemagick: mkdir policy dir: %w", err)
	}
	data, err := policyXML(cfg)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "policy.xml")
	tmp, err := os.CreateTemp(dir, "policy-*.tmp")
	if err != nil {
		return "", fmt.Errorf("imagemagick: create temp policy: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op после успешного rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", fmt.Errorf("imagemagick: write temp policy: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return "", fmt.Errorf("imagemagick: chmod temp policy: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("imagemagick: close temp policy: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", fmt.Errorf("imagemagick: rename policy.xml: %w", err)
	}
	return dir, nil
}

// sanitizePolicyDir проверяет, что каталог политики не содержит опасных
// символов для env var (не используется в shell, но для гигиены). Возвращает
// очищенный путь (trim). Пустой результат означает, что каталог не задан.
func sanitizePolicyDir(dir string) string {
	return strings.TrimSpace(dir)
}
