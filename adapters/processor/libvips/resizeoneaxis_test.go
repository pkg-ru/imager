// Тесты пропорционального resize с одной заданной осью ("x200"/"200x").
// Файл без build-tag: resolveResizeSize — чистая функция, не зависит от
// govips и тестируется в любой сборке (паттерн shrinkonload_test.go).
package libvips

import "testing"

// TestResolveResizeSize проверяет дополнение недостающей оси
// пропорционального resize размерами кадра.
func TestResolveResizeSize(t *testing.T) {
	cases := []struct {
		name           string
		frameW, frameH int
		w, h           int
		wantW, wantH   int
	}{
		{
			// "x200" (только высота): ширина вычисляется из пропорций кадра.
			// Регрессия: width=0 падал в libvips ("parameter width not set").
			name:   "height-only x200",
			frameW: 400, frameH: 200,
			w: 0, h: 200,
			wantW: 400, wantH: 200,
		},
		{
			// Округление к ближайшему: 300*100/200 = 150.
			name:   "height-only rounding",
			frameW: 300, frameH: 200,
			w: 0, h: 100,
			wantW: 150, wantH: 100,
		},
		{
			// Округление вверх: 301*100/200 = 150.5 → 151.
			name:   "height-only rounding half up",
			frameW: 301, frameH: 200,
			w: 0, h: 100,
			wantW: 151, wantH: 100,
		},
		{
			// "200x" (только ширина): высота вычисляется из пропорций кадра.
			// Регрессия: height=0 в vips_thumbnail_image даёт GLib critical
			// ("value "0" ... property 'height'"), свойство остаётся на
			// дефолте (200), и изображение вписывается в бокс
			// (width × default-height) вместо пропорционального ресайза
			// (400x600 → 133x200 вместо 200x300).
			name:   "width-only 200x",
			frameW: 400, frameH: 600,
			w: 200, h: 0,
			wantW: 200, wantH: 300,
		},
		{
			// Округление к ближайшему: 200*400/600 = 133.33 → 133.
			name:   "width-only rounding",
			frameW: 600, frameH: 200,
			w: 200, h: 0,
			wantW: 200, wantH: 67,
		},
		{
			// Округление вверх: 600*200/400 = 300.
			name:   "width-only exact",
			frameW: 400, frameH: 200,
			w: 200, h: 0,
			wantW: 200, wantH: 100,
		},
		{
			// Неизвестные размеры кадра, width-only — план не меняется
			// (отказоустойчивость: движок вернёт понятную ошибку вместо
			// молча неверного результата).
			name:   "width-only unknown frame dimensions unchanged",
			frameW: 0, frameH: 0,
			w: 200, h: 0,
			wantW: 200, wantH: 0,
		},
		{
			// Нулевая ширина кадра, width-only — план не меняется.
			name:   "width-only zero frame width unchanged",
			frameW: 0, frameH: 200,
			w: 200, h: 0,
			wantW: 200, wantH: 0,
		},
		{
			// Оба измерения заданы (200x200) — план не меняется.
			name:   "both dimensions unchanged",
			frameW: 400, frameH: 200,
			w: 200, h: 200,
			wantW: 200, wantH: 200,
		},
		{
			// Неизвестные размеры кадра — план не меняется (отказоустойчивость:
			// движок вернёт понятную ошибку вместо молча неверного результата).
			name:   "height-only unknown frame dimensions unchanged",
			frameW: 0, frameH: 0,
			w: 0, h: 200,
			wantW: 0, wantH: 200,
		},
		{
			// Нулевая высота цели без ширины — нечего дополнять.
			name:   "no target dimensions unchanged",
			frameW: 400, frameH: 200,
			w: 0, h: 0,
			wantW: 0, wantH: 0,
		},
		{
			// Защита от деления на ноль: кадр нулевой высоты.
			name:   "zero frame height unchanged",
			frameW: 400, frameH: 0,
			w: 0, h: 200,
			wantW: 0, wantH: 200,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotW, gotH := resolveResizeSize(c.frameW, c.frameH, c.w, c.h)
			if gotW != c.wantW || gotH != c.wantH {
				t.Errorf("resolveResizeSize(%d, %d, %d, %d) = (%d, %d), want (%d, %d)",
					c.frameW, c.frameH, c.w, c.h, gotW, gotH, c.wantW, c.wantH)
			}
		})
	}
}
