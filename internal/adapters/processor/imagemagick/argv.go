package imagemagick

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/pkg-ru/imager/internal/domain/processing"
)

// allowlists операций, форматов и coders. Никакие пользовательские аргументы
// командной строки не допускаются: план маппится в фиксированный набор
// аргументов через строгие allowlists.

// allowedOps — допустимые операции.
var allowedOps = map[processing.Operation]bool{
	processing.OpResize:   true,
	processing.OpCrop:     true,
	processing.OpTrim:     true,
	processing.OpCropTrim: true,
}

// allowedFormats — допустимые выходные форматы (нижний регистр).
var allowedFormats = map[string]bool{
	"jpeg": true, "jpg": true, "png": true, "webp": true,
	"gif": true, "avif": true, "heif": true, "apng": true,
	"jxl": true, "jpegxl": true,
}

// allowedSourceFormats — допустимые входные форматы (нижний регистр).
// SVG намеренно исключён: растризация SVG выполняется через delegates
// (rsvg/inkscape) и несёт риски SSRF (xlink:href) и decompression bomb.
var allowedSourceFormats = map[string]bool{
	"jpeg": true, "jpg": true, "png": true, "webp": true,
	"gif": true, "avif": true, "heif": true, "apng": true,
	"tiff": true, "bmp": true, "ico": true,
	"jxl": true, "jpegxl": true,
}

// outputCoder — маппинг доменного формата в ImageMagick coder для вывода.
var outputCoder = map[processing.Format]string{
	processing.FormatJPEG:   "JPEG",
	processing.FormatPNG:    "PNG",
	processing.FormatWebP:   "WEBP",
	processing.FormatGIF:    "GIF",
	processing.FormatAVIF:   "AVIF",
	processing.FormatHEIF:   "HEIF",
	processing.FormatAPNG:   "APNG",
	processing.FormatJPEGXL: "JXL",
}

// sourceCoder — маппинг доменного формата в ImageMagick coder для ввода.
var sourceCoder = map[processing.Format]string{
	processing.FormatJPEG:   "JPEG",
	processing.FormatPNG:    "PNG",
	processing.FormatWebP:   "WEBP",
	processing.FormatGIF:    "GIF",
	processing.FormatAVIF:   "AVIF",
	processing.FormatHEIF:   "HEIF",
	processing.FormatAPNG:   "APNG",
	processing.FormatJPEGXL: "JXL",
}

// buildArgv преобразует валидированный план в массив аргументов ImageMagick
// (без shell). Возвращает ошибку, если план содержит недопустимые значения.
//
// Аргументы строятся только из allowlisted операций/форматов и числовых
// значений плана. Никакие строки из пользовательского ввода не попадают
// в argv напрямую.
func buildArgv(plan *processing.ProcessingPlan, caps *Capabilities, limits Limits) ([]string, error) {
	if plan == nil {
		return nil, fmt.Errorf("imagemagick: nil plan")
	}
	if !allowedOps[plan.Operation] {
		return nil, fmt.Errorf("imagemagick: operation %q not allowed", plan.Operation)
	}
	if !allowedFormats[strings.ToLower(string(plan.OutputFormat))] {
		return nil, fmt.Errorf("imagemagick: output format %q not allowed", plan.OutputFormat)
	}
	if !allowedSourceFormats[strings.ToLower(string(plan.SourceFormat))] {
		return nil, fmt.Errorf("imagemagick: source format %q not allowed", plan.SourceFormat)
	}
	// Capability-aware validation: если снимок форматов доступен, проверяем
	// реальную поддержку.
	if caps != nil && caps.HasFormatList() {
		if !caps.SupportsFormat(string(plan.SourceFormat)) {
			return nil, fmt.Errorf("imagemagick: source format %q not supported by binary", plan.SourceFormat)
		}
		if !caps.SupportsFormat(string(plan.OutputFormat)) {
			return nil, fmt.Errorf("imagemagick: output format %q not supported by binary", plan.OutputFormat)
		}
	}

	// Версия IM: часть аргументов доступна только в IM7.
	major := 0
	if caps != nil {
		major = caps.Major
	}
	im7 := major == 0 || major >= 7 // major==0 (нет снимка) — считаем IM7-совместимым

	args := []string{"-quiet"}

	// Resource limits через -limit (ImageMagick 6/7 поддерживают).
	args = append(args, limitArgs(limits, im7)...)

	// Источник: читаем через stdin (magick -). Для неанимированных выходов
	// берём первый кадр.
	src := "-"
	if !plan.OutputFormat.Animated() {
		src = "-[0]"
	}
	args = append(args, src)

	// Фиксированный набор операций обработки (без пользовательских аргументов).
	// Порядок важен: -auto-orient ДО -strip (иначе EXIF Orientation удаляется
	// раньше, чем применяется поворот).
	args = append(args,
		"-auto-orient",
		"-strip",
		"-filter", "Triangle",
		"-define", "filter:support=2",
		"-unsharp", "0.25x0.08+8.3+0.045",
		"-dither", "None",
		"-posterize", "136",
		"-define", "filter-strength=40",
		"-define", "webp:thread-level=1",
		"-define", "webp:alpha-compression=1",
		"-define", "webp:alpha-filtering=2",
		"-define", "webp:auto-filter=true",
		"-define", "jpeg:fancy-upsampling=off",
		"-define", "png:compression-filter=5",
		"-define", "png:compression-strategy=1",
		"-interlace", "none",
		"-gravity", "center",
	)

	// Настраиваемые параметры сжатия (I5): webp:method и png:compression-level.
	if limits.WebPMethod > 0 {
		args = append(args, "-define", fmt.Sprintf("webp:method=%d", limits.WebPMethod))
	}
	if limits.PNGCompressionLevel > 0 {
		args = append(args, "-define", fmt.Sprintf("png:compression-level=%d", limits.PNGCompressionLevel))
	}

	// PNG: исключаем только метаданные-чанки, сохраняя tRNS (прозрачность),
	// gAMA/cHRM/iCCP (цвет) (I9).
	args = append(args, "-define", "png:exclude-chunk=tEXt,zTXt,eXIf,tIME")

	// Цветовое пространство: полагаемся на автоматическое преобразование
	// ImageMagick (I10). Принудительный -colorspace sRGB без -profile искажает
	// цвета CMYK-изображений; -strip уже удаляет ICC-профиль.
	// Для JPEG добавляем sampling-factor (N14).
	if strings.EqualFold(string(plan.OutputFormat), "jpeg") {
		args = append(args, "-sampling-factor", "4:2:0")
	}

	// Анимация: -coalesce/-layers OptimizePlus применяются только для
	// анимированных форматов (I6).
	if plan.OutputFormat.Animated() {
		args = append(args, "-layers", "OptimizePlus")
		args = append(args, "-coalesce")
	}

	// Зацикливание анимации.
	if plan.Loop != nil {
		if *plan.Loop {
			args = append(args, "-loop", "0")
		} else {
			args = append(args, "-loop", "1")
		}
	}

	// Ограничение числа кадров анимации: -limit list-length (IM7) или
	// -delete (IM6). 0 = без ограничения.
	if plan.Frames > 0 {
		if im7 {
			args = append(args, "-limit", "list-length", strconv.Itoa(plan.Frames))
		} else {
			args = append(args, "-delete", fmt.Sprintf("%d-", plan.Frames))
		}
	}

	// Ограничение длительности анимации: -limit time (секунды CPU).
	// ImageMagick не имеет прямого лимита длительности анимации, поэтому
	// duration (мс) конвертируется в лимит времени обработки (ceil).
	// 0 = без ограничения.
	if plan.Duration > 0 {
		secs := (plan.Duration + 999) / 1000
		if secs < 1 {
			secs = 1
		}
		args = append(args, "-limit", "time", strconv.Itoa(secs))
	}

	// Trim (для OpTrim — standalone, для OpCropTrim — до crop).
	if plan.Operation == processing.OpTrim || plan.Operation == processing.OpCropTrim {
		args = append(args, "-trim")
		if im7 {
			args = append(args, "-layers", "trim-bounds")
		}
	}

	// Размер: пропорциональный resize + extent до целевого размера.
	// Для OpCropTrim применяется centre-crop (как для OpCrop) после trim.
	// При Original (size=x) resize/extent не применяются — сохраняется
	// исходный размер изображения.
	if !plan.Size.Original && (plan.Size.Width > 0 || plan.Size.Height > 0) {
		crop := plan.Operation == processing.OpCrop || plan.Operation == processing.OpCropTrim
		resize := resizeString(plan.Size.Width, plan.Size.Height, crop)
		args = append(args, "-thumbnail", resize)
		// -extent применяется только для crop-операций (I7): для OpResize
		// letterboxing нежелателен, т.к. добавляет поля с фоном по умолчанию.
		if crop {
			// Явный фон для extent (N12): none = прозрачный для PNG/WebP/GIF,
			// для JPEG прозрачность невозможна — используем белый.
			bg := "none"
			if strings.EqualFold(string(plan.OutputFormat), "jpeg") {
				bg = "white"
			}
			args = append(args, "-background", bg)
			args = append(args, "-extent", resize)
		}
	}

	// Draft-декодирование (I11): при уменьшении изображения декодируем
	// только необходимое разрешение (jpeg:size). Применяется только когда
	// обе стороны целевого размера заданы и меньше потенциального источника.
	if !plan.Size.Original && plan.Size.Width > 0 && plan.Size.Height > 0 {
		args = append(args, "-define", fmt.Sprintf("jpeg:size=%dx%d", plan.Size.Width, plan.Size.Height))
	}

	// Компрессия.
	if plan.Quality > 0 {
		args = append(args, "-quality", strconv.Itoa(plan.Quality))
	}

	// Ватермарка (nil = не применяется). Аргументы вставляются ДО указания
	// выходного кодера, чтобы композит выполнялся над обработанным холстом.
	if plan.Watermark != nil {
		args = appendWatermarkArgs(args, plan.Watermark)
	}

	// Выход — в stdout в нужном формате.
	coder := outputCoder[plan.OutputFormat]
	if coder == "" {
		return nil, fmt.Errorf("imagemagick: no coder for output format %q", plan.OutputFormat)
	}
	args = append(args, coder+":-")

	return args, nil
}

// limitArgs строит -limit аргументы из Limits.
//
// Площадь (pixels) применяется через `-limit area` (C2): лимит каждой
// стороны через width/height не защищает от изображения 1×10⁹. Отдельные
// лимиты сторон (Width/Height) задаются из политики.
func limitArgs(limits Limits, im7 bool) []string {
	var args []string
	if limits.MemoryBytes > 0 {
		args = append(args, "-limit", "memory", strconv.FormatInt(limits.MemoryBytes, 10))
	}
	if limits.MapBytes > 0 {
		args = append(args, "-limit", "map", strconv.FormatInt(limits.MapBytes, 10))
	}
	if limits.DiskBytes > 0 {
		args = append(args, "-limit", "disk", strconv.FormatInt(limits.DiskBytes, 10))
	}
	if limits.Threads > 0 {
		args = append(args, "-limit", "threads", strconv.Itoa(limits.Threads))
	}
	if limits.TimeSeconds > 0 {
		args = append(args, "-limit", "time", strconv.Itoa(limits.TimeSeconds))
	}
	if limits.Width > 0 {
		args = append(args, "-limit", "width", strconv.FormatInt(limits.Width, 10))
	}
	if limits.Height > 0 {
		args = append(args, "-limit", "height", strconv.FormatInt(limits.Height, 10))
	}
	if limits.Pixels > 0 {
		args = append(args, "-limit", "area", strconv.FormatInt(limits.Pixels, 10))
	}
	if limits.Frames > 0 && im7 {
		args = append(args, "-limit", "list-length", strconv.Itoa(limits.Frames))
	}
	return args
}

// resizeString строит аргумент -thumbnail/-extent: "WxH^" при crop.
func resizeString(w, h int, crop bool) string {
	var sb strings.Builder
	if w > 0 {
		sb.WriteString(strconv.Itoa(w))
	}
	sb.WriteString("x")
	if h > 0 {
		sb.WriteString(strconv.Itoa(h))
	}
	if crop {
		sb.WriteString("^")
	}
	return sb.String()
}

// watermarkGravity маппит CSS-подобную позицию ватермарки в значение
// -gravity ImageMagick. Только allowlisted значения (спецификация приходит
// из доверенного конфига и валидирована на старте).
func watermarkGravity(p processing.WatermarkPosition) string {
	switch p {
	case processing.WatermarkPositionTop:
		return "North"
	case processing.WatermarkPositionBottom:
		return "South"
	case processing.WatermarkPositionLeft:
		return "West"
	case processing.WatermarkPositionRight:
		return "East"
	default:
		return "Center"
	}
}

// watermarkResizeGeometry возвращает геометрию масштабирования ватермарки
// для fallback-движка ImageMagick.
//
// Ограничение fallback-движка: argv строится без знания размеров холста,
// поэтому size: contain/cover рендерятся в НАТУРАЛЬНОМ размере файла
// ватермарки (пустая геометрия). Точный CSS-подобный масштаб реализует
// основной движок libvips. size "{w}px {h}px" масштабируется точно
// ({w}x{h}!).
func watermarkResizeGeometry(wm *processing.WatermarkSpec) string {
	if wm.SizeKind == processing.WatermarkSizePixels {
		return fmt.Sprintf("%dx%d!", wm.WidthPx, wm.HeightPx)
	}
	return ""
}

// appendWatermarkArgs добавляет аргументы наложения ватермарки.
//
// no-repeat: одна копия с позиционированием через -gravity:
//
//	( wm [-resize G] ) -gravity <pos> -geometry +0+0 -compose over -composite
//
// repeat*: масштабированная ватермарка кладётся в mpr-регистр, затем клон
// холста заполняется плиткой (-tile mpr:wm + -draw color reset) и
// накладывается поверх оригинала через -compose over -composite (альфа
// плитки корректно сохраняется). Для repeat* позиции (gravity) не
// применяются — плитка покрывает весь холст от (0,0).
//
// Путь к файлу ватермарки приходит из доверенного конфигурации (не из URL)
// и передаётся как отдельный argv-элемент без shell.
func appendWatermarkArgs(args []string, wm *processing.WatermarkSpec) []string {
	geom := watermarkResizeGeometry(wm)
	args = append(args, "(")
	if geom != "" {
		args = append(args, wm.Path, "-resize", geom)
	} else {
		args = append(args, wm.Path)
	}
	if wm.Repeat == processing.WatermarkRepeatNoRepeat {
		args = append(args,
			")",
			"-gravity", watermarkGravity(wm.Position),
			"-geometry", "+0+0",
			"-compose", "over",
			"-composite",
		)
		return args
	}
	args = append(args,
		"-write", "mpr:wm", "+delete",
		")",
		"-clone", "0",
		"-tile", "mpr:wm",
		"-draw", "color 0,0 reset",
		"-compose", "over",
		"-composite",
	)
	return args
}
