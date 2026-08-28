// Command genfavicon генерирует favicon для сервиса imager.
//
// Дизайн: диагональный градиентный квадрат (indigo → pink) со скруглёнными
// углами и стилизованной белой лупой (символ обработки изображений).
//
// Выходные файлы (в web/static/):
//   - favicon.ico        — ICO-контейнер с PNG-изображениями 16/32/48/256
//   - favicon-16x16.png  — PNG 16x16
//   - favicon-32x32.png  — PNG 32x32
//
// Используется только stdlib (image, image/png). ICO собирается вручную из
// PNG-данных (современные браузеры поддерживают PNG внутри ICO).
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

const (
	// masterSize — размер мастер-изображения (рендерится с supersampling).
	masterSize = 512
	// ss — коэффициент supersampling для сглаживания краёв.
	ss = 4
)

// Цвета градиента (indigo → pink).
var (
	gradFrom = color.RGBA{R: 0x63, G: 0x66, B: 0xF1, A: 0xFF} // #6366F1
	gradTo   = color.RGBA{R: 0xEC, G: 0x48, B: 0x99, A: 0xFF} // #EC4899
)

// lerp линейно интерполирует a→b по t (0..1).
func lerp(a, b uint8, t float64) uint8 {
	return uint8(float64(a) + (float64(b)-float64(a))*t)
}

// lerpColor интерполирует два цвета.
func lerpColor(a, b color.RGBA, t float64) color.RGBA {
	return color.RGBA{
		R: lerp(a.R, b.R, t),
		G: lerp(a.G, b.G, t),
		B: lerp(a.B, b.B, t),
		A: 0xFF,
	}
}

// roundedRectAlpha возвращает alpha-покрытие точки (x,y) скруглённым
// прямоугольником (0..1). Используется для сглаживания краёв.
func roundedRectAlpha(x, y, size, r float64) float64 {
	if x < 0 || y < 0 || x >= size || y >= size {
		return 0
	}
	// Для каждого угла: если точка в угловой четверти, считаем расстояние
	// до центра угловой окружности.
	corners := [][2]float64{
		{r, r},
		{size - 1 - r, r},
		{r, size - 1 - r},
		{size - 1 - r, size - 1 - r},
	}
	for _, c := range corners {
		cx, cy := c[0], c[1]
		inCorner := false
		if cx < size/2 && cy < size/2 {
			inCorner = x <= cx && y <= cy
		} else if cx >= size/2 && cy < size/2 {
			inCorner = x >= cx && y <= cy
		} else if cx < size/2 && cy >= size/2 {
			inCorner = x <= cx && y >= cy
		} else {
			inCorner = x >= cx && y >= cy
		}
		if inCorner {
			dx := x - cx
			dy := y - cy
			dist := math.Sqrt(dx*dx + dy*dy)
			if dist <= r-0.5 {
				return 1
			}
			if dist >= r+0.5 {
				return 0
			}
			// Плавный переход на границе.
			return 1 - (dist - (r - 0.5))
		}
	}
	return 1
}

// lensAlpha возвращает alpha-покрытие точки (x,y) символом лупы
// (кольцо + ручка) в координатах size×size. Возвращает 0..1.
func lensAlpha(x, y, size float64) float64 {
	// Параметры лупы относительно размера.
	centerX := size * 0.44
	centerY := size * 0.44
	radius := size * 0.24
	thickness := size * 0.075

	// Кольцо.
	dx := x - centerX
	dy := y - centerY
	dist := math.Sqrt(dx*dx + dy*dy)
	if dist >= radius-thickness && dist <= radius {
		// Сглаживание краёв кольца.
		inner := dist - (radius - thickness)
		outer := radius - dist
		a := math.Min(inner, outer)
		if a < 0.5 {
			return math.Max(0, a)
		}
		return 1
	}

	// Ручка: отрезок от края кольца вниз-вправо под 45°.
	dirX, dirY := 1/math.Sqrt(2), 1/math.Sqrt(2)
	startX := centerX + radius*dirX
	startY := centerY + radius*dirY
	relX := x - startX
	relY := y - startY
	proj := relX*dirX + relY*dirY
	if proj < 0 {
		return 0
	}
	perpX := relX - proj*dirX
	perpY := relY - proj*dirY
	perp := math.Sqrt(perpX*perpX + perpY*perpY)
	handleLen := size * 0.30
	if proj > handleLen {
		return 0
	}
	halfW := thickness * 0.55
	if perp <= halfW {
		edge := halfW - perp
		if edge < 0.5 {
			return math.Max(0, edge)
		}
		return 1
	}
	return 0
}

// renderMaster рендерит мастер-изображение с supersampling.
func renderMaster() *image.RGBA {
	big := image.NewRGBA(image.Rect(0, 0, masterSize*ss, masterSize*ss))
	radius := float64(masterSize) * 0.22

	for y := 0; y < masterSize*ss; y++ {
		for x := 0; x < masterSize*ss; x++ {
			fx := float64(x) / float64(ss)
			fy := float64(y) / float64(ss)

			// Alpha скруглённого прямоугольника.
			rectA := roundedRectAlpha(fx, fy, float64(masterSize), radius)
			if rectA <= 0 {
				continue
			}

			// Диагональный градиент (от верхнего-левого к нижнему-правому).
			t := (fx + fy) / (2 * float64(masterSize))
			base := lerpColor(gradFrom, gradTo, t)

			// Лупа (белая).
			lensA := lensAlpha(fx, fy, float64(masterSize))
			col := base
			if lensA > 0 {
				white := color.RGBA{R: 0xFF, G: 0xFF, B: 0xFF, A: 0xFF}
				col = lerpColor(col, white, lensA)
			}

			// Применяем alpha прямоугольника.
			col.A = uint8(rectA * 255)
			big.SetRGBA(x, y, col)
		}
	}

	// Уменьшаем до masterSize с билинейной интерполяцией.
	return downscale(big, masterSize)
}

// downscale уменьшает изображение src до size×size (билинейная интерполяция).
func downscale(src *image.RGBA, size int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	srcW := src.Bounds().Dx()
	srcH := src.Bounds().Dy()
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			sx := (float64(x)+0.5)*float64(srcW)/float64(size) - 0.5
			sy := (float64(y)+0.5)*float64(srcH)/float64(size) - 0.5
			dst.SetRGBA(x, y, bilinear(src, sx, sy))
		}
	}
	return dst
}

// bilinear возвращает цвет в точке (x,y) с билинейной интерполяцией.
func bilinear(src *image.RGBA, x, y float64) color.RGBA {
	x0 := int(math.Floor(x))
	y0 := int(math.Floor(y))
	x1 := x0 + 1
	y1 := y0 + 1
	tx := x - float64(x0)
	ty := y - float64(y0)

	b := src.Bounds()
	clamp := func(v int) int {
		if v < b.Min.X {
			return b.Min.X
		}
		if v > b.Max.X-1 {
			return b.Max.X - 1
		}
		return v
	}
	clampY := func(v int) int {
		if v < b.Min.Y {
			return b.Min.Y
		}
		if v > b.Max.Y-1 {
			return b.Max.Y - 1
		}
		return v
	}

	c00 := src.RGBAAt(clamp(x0), clampY(y0))
	c10 := src.RGBAAt(clamp(x1), clampY(y0))
	c01 := src.RGBAAt(clamp(x0), clampY(y1))
	c11 := src.RGBAAt(clamp(x1), clampY(y1))

	lerpRGBA := func(a, b color.RGBA, t float64) color.RGBA {
		return color.RGBA{
			R: lerp(a.R, b.R, t),
			G: lerp(a.G, b.G, t),
			B: lerp(a.B, b.B, t),
			A: lerp(a.A, b.A, t),
		}
	}

	top := lerpRGBA(c00, c10, tx)
	bot := lerpRGBA(c01, c11, tx)
	return lerpRGBA(top, bot, ty)
}

// encodePNG кодирует изображение в PNG-байты.
func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// buildICO собирает ICO-контейнер из PNG-изображений.
// sizes — размеры (16/32/48/256). PNG внутри ICO поддерживается
// современными браузерами.
func buildICO(pngs map[int][]byte, sizes []int) ([]byte, error) {
	n := len(sizes)
	var buf bytes.Buffer

	// ICONDIR.
	_ = binary.Write(&buf, binary.LittleEndian, uint16(0)) // reserved
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1)) // type: icon
	_ = binary.Write(&buf, binary.LittleEndian, uint16(n)) // count

	// ICONDIRENTRY (16 байт на изображение).
	offset := 6 + 16*n
	for _, s := range sizes {
		data := pngs[s]
		_ = binary.Write(&buf, binary.LittleEndian, byte(s%256)) // width (0=256)
		_ = binary.Write(&buf, binary.LittleEndian, byte(s%256)) // height
		_ = binary.Write(&buf, binary.LittleEndian, byte(0))     // color count
		_ = binary.Write(&buf, binary.LittleEndian, byte(0))     // reserved
		_ = binary.Write(&buf, binary.LittleEndian, uint16(1))   // planes
		_ = binary.Write(&buf, binary.LittleEndian, uint16(32))  // bit count
		_ = binary.Write(&buf, binary.LittleEndian, uint32(len(data)))
		_ = binary.Write(&buf, binary.LittleEndian, uint32(offset))
		offset += len(data)
	}

	// Данные изображений.
	for _, s := range sizes {
		buf.Write(pngs[s])
	}

	return buf.Bytes(), nil
}

func main() {
	outDir := "web/static"
	if len(os.Args) > 1 {
		outDir = os.Args[1]
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir: %v\n", err)
		os.Exit(1)
	}

	master := renderMaster()

	// PNG для каждого размера.
	sizes := []int{16, 32, 48, 256}
	pngs := map[int][]byte{}
	for _, s := range sizes {
		img := downscale(master, s)
		data, err := encodePNG(img)
		if err != nil {
			fmt.Fprintf(os.Stderr, "encode %d: %v\n", s, err)
			os.Exit(1)
		}
		pngs[s] = data
	}

	// favicon-16x16.png и favicon-32x32.png.
	for _, s := range []int{16, 32} {
		path := filepath.Join(outDir, fmt.Sprintf("favicon-%dx%d.png", s, s))
		if err := os.WriteFile(path, pngs[s], 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write %s: %v\n", path, err)
			os.Exit(1)
		}
		fmt.Printf("wrote %s (%d bytes)\n", path, len(pngs[s]))
	}

	// favicon.ico (16/32/48/256).
	ico, err := buildICO(pngs, sizes)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ico: %v\n", err)
		os.Exit(1)
	}
	icoPath := filepath.Join(outDir, "favicon.ico")
	if err := os.WriteFile(icoPath, ico, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write %s: %v\n", icoPath, err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s (%d bytes)\n", icoPath, len(ico))

	// Проверка: декодируем обратно, чтобы убедиться в валидности.
	if _, err := png.Decode(bytes.NewReader(pngs[32])); err != nil {
		fmt.Fprintf(os.Stderr, "decode check failed: %v\n", err)
		os.Exit(1)
	}
	// Валидируем ICO-структуру.
	if err := validateICO(ico, sizes); err != nil {
		fmt.Fprintf(os.Stderr, "ico validation failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("ok")
}

// validateICO проверяет структуру ICO-контейнера: заголовок, число записей,
// смещения и валидность PNG-данных каждой записи.
func validateICO(data []byte, sizes []int) error {
	if len(data) < 6 {
		return fmt.Errorf("too short")
	}
	if data[0] != 0 || data[1] != 0 {
		return fmt.Errorf("bad reserved field")
	}
	if binary.LittleEndian.Uint16(data[2:4]) != 1 {
		return fmt.Errorf("bad type field")
	}
	count := int(binary.LittleEndian.Uint16(data[4:6]))
	if count != len(sizes) {
		return fmt.Errorf("count = %d, want %d", count, len(sizes))
	}
	if len(data) < 6+16*count {
		return fmt.Errorf("truncated entries")
	}
	for i := 0; i < count; i++ {
		ent := data[6+16*i : 6+16*(i+1)]
		w := int(ent[0])
		h := int(ent[1])
		if w == 0 {
			w = 256
		}
		if h == 0 {
			h = 256
		}
		if w != sizes[i] || h != sizes[i] {
			return fmt.Errorf("entry %d: size %dx%d, want %dx%d", i, w, h, sizes[i], sizes[i])
		}
		off := int(binary.LittleEndian.Uint32(ent[12:16]))
		sz := int(binary.LittleEndian.Uint32(ent[8:12]))
		if off < 0 || off+sz > len(data) {
			return fmt.Errorf("entry %d: out of range offset=%d size=%d", i, off, sz)
		}
		if _, err := png.Decode(bytes.NewReader(data[off : off+sz])); err != nil {
			return fmt.Errorf("entry %d: invalid png: %v", i, err)
		}
	}
	return nil
}
