// Пропорциональный resize с ОДНОЙ заданной осью (размер-грамматика
// "x200" — только высота, "200x" — только ширина).
//
// libvips vips_thumbnail_image требует ЯВНЫЕ ОБА измерения: width — required
// GObject-property (width=0 → ошибка "parameter width not set" и GLib
// critical "value 0 ... property 'width'"), а height=0 НЕ является "авто":
// GObject отклоняет 0 для свойства 'height' (GLib critical "value "0" of
// type 'gint' is invalid ... property 'height'"), свойство остаётся на
// дефолтном значении (200), и изображение вписывается в бокс
// (width × default-height) вместо пропорционального ресайза по одной оси.
// Поэтому размер-грамматика с одной осью ("x200" → план Size{0, 200},
// "200x" → план Size{200, 0}) требует вычисления недостающей оси из
// пропорций кадра ДО вызова thumbnail.
//
// Чистая функция не зависит от govips и тестируется в любой сборке
// (см. resizeoneaxis_test.go); применение — в applyOperation
// (process_libvips.go).
package libvips

// resolveResizeSize дополняет недостающую ось пропорционального resize
// (0 = авто) размерами кадра.
//
// Правила:
//   - оба измерения заданы (200x200) — план не меняется;
//   - ширина задана, высота 0 ("200x") — высота вычисляется из пропорций
//     текущего кадра с округлением к ближайшему (height=0 невалиден для
//     vips_thumbnail_image: GLib critical + fallback на дефолт свойства);
//   - ширина 0, высота задана ("x200") — ширина вычисляется из пропорций
//     текущего кадра (width=0 → ошибка "parameter width not set");
//   - для анимации передаётся высота ОДНОГО кадра, чтобы пропорция
//     считалась по кадру, а не по высоте всего стека страниц;
//   - неизвестные размеры кадра (<= 0) — план не меняется: движок вернёт
//     понятную ошибку вместо молча неверного результата (отказоустойчивость).
func resolveResizeSize(frameW, frameH, w, h int) (int, int) {
	if frameW <= 0 || frameH <= 0 {
		return w, h
	}
	switch {
	case w > 0 && h <= 0:
		// "200x": пропорциональная высота frameH * w / frameW, округление
		// к ближайшему; минимум 1 — height<=0 невалиден для thumbnail
		// (GLib critical + fallback на дефолт свойства 'height').
		nh := (frameH*w + frameW/2) / frameW
		if nh < 1 {
			nh = 1
		}
		return w, nh
	case w <= 0 && h > 0:
		// "x200": пропорциональная ширина frameW * h / frameH; минимум 1 —
		// width=0 невалиден для thumbnail ("parameter width not set").
		nw := (frameW*h + frameH/2) / frameH
		if nw < 1 {
			nw = 1
		}
		return nw, h
	default:
		// Оба измерения заданы или оба нулевые — план не меняется.
		return w, h
	}
}
