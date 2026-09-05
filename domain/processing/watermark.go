package processing

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// WatermarkPosition — позиция размещения ватермарки (CSS-подобные ключевые
// слова background-position). Одиночное ключевое слово: вторая ось — центр.
//
//	top    — верх по вертикали, центр по горизонтали;
//	bottom — низ по вертикали, центр по горизонтали;
//	left   — левый край по горизонтали, центр по вертикали;
//	right  — правый край по горизонтали, центр по вертикали;
//	center — центр обеих осей.
type WatermarkPosition string

const (
	WatermarkPositionTop    WatermarkPosition = "top"
	WatermarkPositionBottom WatermarkPosition = "bottom"
	WatermarkPositionLeft   WatermarkPosition = "left"
	WatermarkPositionRight  WatermarkPosition = "right"
	WatermarkPositionCenter WatermarkPosition = "center"
)

// ValidWatermarkPosition проверяет допустимость позиции.
func ValidWatermarkPosition(p WatermarkPosition) bool {
	switch p {
	case WatermarkPositionTop, WatermarkPositionBottom,
		WatermarkPositionLeft, WatermarkPositionRight, WatermarkPositionCenter:
		return true
	default:
		return false
	}
}

// WatermarkRepeat — режим заполнения холста копиями ватермарки
// (CSS-подобные значения background-repeat).
type WatermarkRepeat string

const (
	WatermarkRepeatNoRepeat WatermarkRepeat = "no-repeat"
	WatermarkRepeatRepeat   WatermarkRepeat = "repeat"
	WatermarkRepeatRepeatX  WatermarkRepeat = "repeat-x"
	WatermarkRepeatRepeatY  WatermarkRepeat = "repeat-y"
	WatermarkRepeatRound    WatermarkRepeat = "round"
	WatermarkRepeatSpace    WatermarkRepeat = "space"
)

// ValidWatermarkRepeat проверяет допустимость режима повторения.
func ValidWatermarkRepeat(r WatermarkRepeat) bool {
	switch r {
	case WatermarkRepeatNoRepeat, WatermarkRepeatRepeat, WatermarkRepeatRepeatX,
		WatermarkRepeatRepeatY, WatermarkRepeatRound, WatermarkRepeatSpace:
		return true
	default:
		return false
	}
}

// WatermarkSizeKind — способ задания размера ватермарки.
type WatermarkSizeKind int

const (
	// WatermarkSizeContain — масштабировать с сохранением пропорций так,
	// чтобы ватермарка целиком поместилась в холст (CSS contain).
	WatermarkSizeContain WatermarkSizeKind = iota
	// WatermarkSizeCover — масштабировать так, чтобы ватермарка покрыла
	// весь холст (CSS cover; излишек обрезается по центру).
	WatermarkSizeCover
	// WatermarkSizePixels — фиксированный размер "{width}px {height}px".
	WatermarkSizePixels
)

// ParseWatermarkSize разбирает значение поля size ватермарки:
//
//	"contain"    → (WatermarkSizeContain, 0, 0, nil)
//	"cover"      → (WatermarkSizeCover, 0, 0, nil)
//	"100px 50px" → (WatermarkSizePixels, 100, 50, nil)
func ParseWatermarkSize(s string) (WatermarkSizeKind, int, int, error) {
	switch strings.TrimSpace(s) {
	case "", "contain":
		return WatermarkSizeContain, 0, 0, nil
	case "cover":
		return WatermarkSizeCover, 0, 0, nil
	}
	parts := strings.Fields(s)
	if len(parts) != 2 {
		return 0, 0, 0, fmt.Errorf("invalid watermark size %q: must be \"contain\", \"cover\" or \"{width}px {height}px\"", s)
	}
	w, err := parsePx(parts[0])
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid watermark size %q: %w", s, err)
	}
	h, err := parsePx(parts[1])
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid watermark size %q: %w", s, err)
	}
	if w <= 0 || h <= 0 {
		return 0, 0, 0, fmt.Errorf("invalid watermark size %q: width and height must be positive", s)
	}
	return WatermarkSizePixels, w, h, nil
}

// parsePx разбирает строку вида "100px" (суффикс px обязателен).
func parsePx(s string) (int, error) {
	if !strings.HasSuffix(s, "px") {
		return 0, fmt.Errorf("value %q must end with \"px\"", s)
	}
	v, err := strconv.Atoi(strings.TrimSuffix(s, "px"))
	if err != nil {
		return 0, fmt.Errorf("value %q is not a number", s)
	}
	return v, nil
}

// WatermarkSpec — спецификация ватермарки: immutable после создания.
//
// Path — путь к файлу изображения ватермарки на диске (задаётся
// администратором в конфиге; существование файла проверяется на старте).
// Спецификация НЕ содержит пользовательского ввода: имя используется только
// для ссылок из пресетов/path-policies, а остальные поля маппятся в
// фиксированные аргументы процессоров через allowlists.
type WatermarkSpec struct {
	// Name — уникальное имя ватермарки (ссылка из пресетов/path-policies).
	Name string
	// Path — путь к файлу изображения ватермарки.
	Path string
	// Position — позиция размещения (CSS-подобная).
	Position WatermarkPosition
	// Repeat — режим заполнения копиями (CSS-подобный).
	Repeat WatermarkRepeat
	// SizeKind — способ задания размера (contain/cover/pixels).
	SizeKind WatermarkSizeKind
	// WidthPx / HeightPx — фиксированный размер (только для SizePixels).
	WidthPx  int
	HeightPx int
}

// NewWatermarkSpec создаёт WatermarkSpec с валидацией всех полей.
func NewWatermarkSpec(name, path string, position WatermarkPosition, repeat WatermarkRepeat, size string) (*WatermarkSpec, error) {
	if name == "" {
		return nil, fmt.Errorf("watermark: empty name")
	}
	if path == "" {
		return nil, fmt.Errorf("watermark %q: empty path", name)
	}
	if !ValidWatermarkPosition(position) {
		return nil, fmt.Errorf("watermark %q: invalid position %q (must be top|bottom|left|right|center)", name, position)
	}
	if repeat == "" {
		repeat = WatermarkRepeatNoRepeat
	}
	if !ValidWatermarkRepeat(repeat) {
		return nil, fmt.Errorf("watermark %q: invalid repeat %q (must be no-repeat|repeat|repeat-x|repeat-y|round|space)", name, repeat)
	}
	if size == "" {
		size = "contain"
	}
	kind, w, h, err := ParseWatermarkSize(size)
	if err != nil {
		return nil, fmt.Errorf("watermark %q: %w", name, err)
	}
	return &WatermarkSpec{
		Name:     name,
		Path:     path,
		Position: position,
		Repeat:   repeat,
		SizeKind: kind,
		WidthPx:  w,
		HeightPx: h,
	}, nil
}

// TargetSize вычисляет целевой размер одной копии ватермарки для холста
// canvasW x canvasH с учётом натурального размера wmW x wmH.
//
//	contain — вписать в холст с сохранением пропорций;
//	cover   — покрыть холст с сохранением пропорций;
//	pixels  — фиксированный размер (натуральный размер не важен).
func (s *WatermarkSpec) TargetSize(canvasW, canvasH, wmW, wmH int) (int, int) {
	if canvasW <= 0 || canvasH <= 0 || wmW <= 0 || wmH <= 0 {
		return 1, 1
	}
	switch s.SizeKind {
	case WatermarkSizePixels:
		return s.WidthPx, s.HeightPx
	case WatermarkSizeContain:
		scale := math.Min(float64(canvasW)/float64(wmW), float64(canvasH)/float64(wmH))
		return clampDim(int(math.Round(float64(wmW) * scale))), clampDim(int(math.Round(float64(wmH) * scale)))
	case WatermarkSizeCover:
		scale := math.Max(float64(canvasW)/float64(wmW), float64(canvasH)/float64(wmH))
		return clampDim(int(math.Round(float64(wmW) * scale))), clampDim(int(math.Round(float64(wmH) * scale)))
	default:
		return wmW, wmH
	}
}

// anchorX возвращает X-координату размещения копии шириной ww на холсте W
// согласно позиции.
func (s *WatermarkSpec) anchorX(W, ww int) int {
	switch s.Position {
	case WatermarkPositionLeft:
		return 0
	case WatermarkPositionRight:
		return maxInt(0, W-ww)
	default:
		return maxInt(0, (W-ww)/2)
	}
}

// anchorY возвращает Y-координату размещения копии высотой wh на холсте H
// согласно позиции.
func (s *WatermarkSpec) anchorY(H, wh int) int {
	switch s.Position {
	case WatermarkPositionTop:
		return 0
	case WatermarkPositionBottom:
		return maxInt(0, H-wh)
	default:
		return maxInt(0, (H-wh)/2)
	}
}

// Point — точка размещения копии ватермарки (левый верхний угол).
type Point struct{ X, Y int }

// LayoutCount вычисляет число копий ватермарки размером ww x wh на холсте
// W x H согласно режиму repeat и позиции — БЕЗ материализации среза точек.
// Используется процессорами для проверки лимита числа тайлов ДО вызова
// Layout (защита от аллокации огромного среза при патологическом тайлинге).
func (s *WatermarkSpec) LayoutCount(W, H, ww, wh int) int {
	if W <= 0 || H <= 0 || ww <= 0 || wh <= 0 {
		return 0
	}
	switch s.Repeat {
	case WatermarkRepeatNoRepeat:
		return 1
	case WatermarkRepeatRepeatX:
		return maxInt(1, ceilDiv(W, ww))
	case WatermarkRepeatRepeatY:
		return maxInt(1, ceilDiv(H, wh))
	case WatermarkRepeatSpace:
		return maxInt(1, W/ww) * maxInt(1, H/wh)
	case WatermarkRepeatRound:
		sw, sh := s.RoundStep(W, H, ww, wh)
		return maxInt(1, ceilDiv(W, sw)) * maxInt(1, ceilDiv(H, sh))
	default: // WatermarkRepeatRepeat
		return maxInt(1, ceilDiv(W, ww)) * maxInt(1, ceilDiv(H, wh))
	}
}

// Layout вычисляет список позиций (левый верхний угол) копий ватермарки
// размером ww x wh на холсте W x H согласно режиму repeat и позиции.
//
// Семантика CSS background-*:
//
//	no-repeat — одна копия в позиции;
//	repeat    — плитка от (0,0) с шагом ww x wh;
//	repeat-x  — горизонтальный ряд на Y позиции;
//	repeat-y  — вертикальный столбец на X позиции;
//	round     — как repeat, но число копий округляется так, чтобы копии
//	            точно укладывались по осям (шаг сетки см. RoundStep);
//	space     — как repeat, но копии распределяются с равными промежутками.
func (s *WatermarkSpec) Layout(W, H, ww, wh int) []Point {
	if W <= 0 || H <= 0 || ww <= 0 || wh <= 0 {
		return nil
	}
	switch s.Repeat {
	case WatermarkRepeatNoRepeat:
		return []Point{{X: s.anchorX(W, ww), Y: s.anchorY(H, wh)}}

	case WatermarkRepeatRepeatX:
		var pts []Point
		y := s.anchorY(H, wh)
		for x := 0; x < W; x += ww {
			pts = append(pts, Point{X: x, Y: y})
		}
		return pts

	case WatermarkRepeatRepeatY:
		var pts []Point
		x := s.anchorX(W, ww)
		for y := 0; y < H; y += wh {
			pts = append(pts, Point{X: x, Y: y})
		}
		return pts

	case WatermarkRepeatSpace:
		cols := maxInt(1, W/ww)
		rows := maxInt(1, H/wh)
		gapX := 0
		gapY := 0
		if cols > 1 {
			gapX = (W - cols*ww) / (cols - 1)
		}
		if rows > 1 {
			gapY = (H - rows*wh) / (rows - 1)
		}
		var pts []Point
		for r := 0; r < rows; r++ {
			for c := 0; c < cols; c++ {
				pts = append(pts, Point{X: c * (ww + gapX), Y: r * (wh + gapY)})
			}
		}
		return pts

	case WatermarkRepeatRound:
		// Число копий округляется до целого так, чтобы копии укладывались
		// ровно по осям; шаг сетки (итоговый размер копии) вычисляется
		// через RoundStep — процессор масштабирует копию до этого размера.
		sw, sh := s.RoundStep(W, H, ww, wh)
		cols := maxInt(1, ceilDiv(W, sw))
		rows := maxInt(1, ceilDiv(H, sh))
		var pts []Point
		for r := 0; r < rows; r++ {
			for c := 0; c < cols; c++ {
				pts = append(pts, Point{X: c * sw, Y: r * sh})
			}
		}
		return pts

	default: // WatermarkRepeatRepeat
		var pts []Point
		for y := 0; y < H; y += wh {
			for x := 0; x < W; x += ww {
				pts = append(pts, Point{X: x, Y: y})
			}
		}
		return pts
	}
}

// RoundStep вычисляет шаг сетки (целевой размер одной копии) для режима
// round: число копий округляется ВВЕРХ до целого, а шаг — до размера,
// гарантирующего полное покрытие холста копиями. Используется вместе с
// Layout (режим round).
func (s *WatermarkSpec) RoundStep(W, H, ww, wh int) (int, int) {
	cols := maxInt(1, int(math.Ceil(float64(W)/float64(ww))))
	rows := maxInt(1, int(math.Ceil(float64(H)/float64(wh))))
	return maxInt(1, ceilDiv(W, cols)), maxInt(1, ceilDiv(H, rows))
}

// ceilDiv делит a на b с округлением вверх.
func ceilDiv(a, b int) int {
	return (a + b - 1) / b
}

// RoundScale сообщает, требует ли режим repeat пересчёта размера копии под
// холст (round): процессор обязан масштабировать копию до RoundStep.
func (s *WatermarkSpec) RoundScale() bool { return s.Repeat == WatermarkRepeatRound }

func clampDim(v int) int {
	if v < 1 {
		return 1
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
