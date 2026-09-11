// Тесты ICC color management: парсинг политики, определение
// sRGB-совместимых профилей по сигнатуре/имени (без lcms) и решение о
// необходимости конверсии. Файл без build-tag: чистая логика.
package libvips

import (
	"encoding/binary"
	"testing"
)

// buildICCProfile строит минимальный валидный ICC-профиль с одним тегом
// 'desc' (описание). colorSpace — строка цветового пространства из 4 байт
// ("RGB " или "CMYK"). При name=="" тег desc не добавляется (профиль без
// описания — одиночный заголовок).
func buildICCProfile(name, colorSpace string) []byte {
	const headerSize = 128
	// Заголовок (128) + счётчик тегов (4) + одна запись таблицы тегов (12).
	data := make([]byte, headerSize+16)
	// Класс устройства (offset 12): 'mntr'.
	copy(data[12:16], "mntr")
	// Цветовое пространство (offset 16).
	copy(data[16:20], colorSpace)
	// PCS (offset 20): 'XYZ '.
	copy(data[20:24], "XYZ ")
	// Сигнатура профиля (offset 36): 'acsp'.
	copy(data[36:40], "acsp")
	if name == "" {
		return data
	}
	// Заголовок: 128 байт. Счётчик тегов — offset 128:132 (=1). Запись
	// таблицы тегов — offset 132:144 (сигнатура 'desc', offset данных,
	// размер). Данные тега начинаются на offset 144.
	binary.BigEndian.PutUint32(data[128:132], 1)
	copy(data[132:136], "desc")
	tagDataOff := uint32(headerSize + 12 + 4) // 144
	binary.BigEndian.PutUint32(data[136:140], tagDataOff)
	binary.BigEndian.PutUint32(data[140:144], uint32(12+len(name)))
	// Данные тега 'desc' (12 байт заголовка + ASCII-строка) дополняем снизу.
	data = append(data, make([]byte, 12+len(name))...)
	copy(data[tagDataOff:tagDataOff+4], "desc")
	// reserved (tagDataOff+4) остаётся 0.
	binary.BigEndian.PutUint32(data[tagDataOff+8:tagDataOff+12], uint32(len(name)))
	copy(data[tagDataOff+12:], name)
	return data
}

func TestParseColorMode(t *testing.T) {
	cases := []struct {
		in      string
		want    ColorMode
		wantErr bool
	}{
		{in: "", want: ColorStrip},
		{in: "strip", want: ColorStrip},
		{in: "transform", want: ColorTransform},
		{in: "keep", want: ColorKeep},
		{in: "TRANsFORM", wantErr: true}, // строгая валидация: только нижний регистр
		{in: "icm", wantErr: true},
		{in: "  strip  ", wantErr: true},
	}
	for _, tc := range cases {
		got, err := ParseColorMode(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseColorMode(%q): want error, got %q", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseColorMode(%q): unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseColorMode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestColorModeValid(t *testing.T) {
	for _, m := range []ColorMode{ColorStrip, ColorTransform, ColorKeep} {
		if !m.Valid() {
			t.Errorf("%q: Valid() = false, want true", m)
		}
	}
	if ColorMode("bogus").Valid() {
		t.Error("bogus: Valid() = true, want false")
	}
}

func TestIsSRGBProfile(t *testing.T) {
	cases := []struct {
		name  string
		bytes []byte
		want  bool
	}{
		{
			name:  "sRGB IEC61966-2.1 (стандартный профиль)",
			bytes: buildICCProfile("sRGB IEC61966-2.1", "RGB "),
			want:  true,
		},
		{
			name:  "sRGB v2",
			bytes: buildICCProfile("sRGB v2", "RGB "),
			want:  true,
		},
		{
			name:  "Adobe RGB (1998) — не sRGB",
			bytes: buildICCProfile("Adobe RGB (1998)", "RGB "),
			want:  false,
		},
		{
			name:  "Display P3 — не sRGB",
			bytes: buildICCProfile("Display P3", "RGB "),
			want:  false,
		},
		{
			name:  "CMYK профиль",
			bytes: buildICCProfile("U.S. Web Coated (SWOP) v2", "CMYK"),
			want:  false,
		},
		{
			name:  "sRGB в CMYK пространстве — не проходит (профиль обязан быть RGB)",
			bytes: buildICCProfile("sRGB IEC61966-2.1", "CMYK"),
			want:  false,
		},
		{
			name:  "битый профиль без тега desc",
			bytes: buildICCProfile("", "RGB "),
			want:  false,
		},
		{
			name:  "пустые данные",
			bytes: nil,
			want:  false,
		},
		{
			name:  "слишком короткий профиль",
			bytes: []byte{0x61, 0x63, 0x73, 0x70},
			want:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSRGBProfile(tc.bytes); got != tc.want {
				t.Errorf("isSRGBProfile = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestColorNeedsTransform(t *testing.T) {
	cases := []struct {
		name             string
		mode             ColorMode
		hasICC           bool
		srgbProfile      bool
		colorspaceIsSRGB bool
		want             bool
	}{
		// Режим strip: конверсия никогда не требуется (профиль удаляется
		// при экспорте).
		{name: "strip + не-sRGB профиль", mode: ColorStrip, hasICC: true, want: false},
		{name: "strip + sRGB colorspace", mode: ColorStrip, colorspaceIsSRGB: true, want: false},
		// Режим transform: fast-path уже в sRGB без профиля.
		{name: "transform + sRGB colorspace без профиля", mode: ColorTransform, colorspaceIsSRGB: true, want: false},
		// Режим transform: fast-path sRGB-совместимый профиль.
		{name: "transform + sRGB профиль", mode: ColorTransform, hasICC: true, srgbProfile: true, want: false},
		// Режим transform: не-sRGB профиль → конверсия.
		{name: "transform + не-sRGB профиль", mode: ColorTransform, hasICC: true, want: true},
		// Режим transform: без профиля в не-sRGB colorspace (CMYK) → конверсия.
		{name: "transform + CMYK без профиля", mode: ColorTransform, want: true},
		// Режим keep: конверсия не выполняется (профиль сохраняется как есть).
		{name: "keep + не-sRGB профиль", mode: ColorKeep, hasICC: true, want: false},
		// Неизвестный режим: консервативно false.
		{name: "bogus режим", mode: ColorMode("bogus"), hasICC: true, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := colorNeedsTransform(tc.mode, tc.hasICC, tc.srgbProfile, tc.colorspaceIsSRGB)
			if got != tc.want {
				t.Errorf("colorNeedsTransform(%q, hasICC=%v, srgb=%v, srgbCS=%v) = %v, want %v",
					tc.mode, tc.hasICC, tc.srgbProfile, tc.colorspaceIsSRGB, got, tc.want)
			}
		})
	}
}
