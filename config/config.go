// Package config реализует typed config foundation для конвейера ассетов.
//
// Пакет разделяет три фазы:
//
//  1. Decode — десериализация сырых данных (например YAML) в typed DTO.
//  2. Normalize — приведение DTO к канонической форме (умолчания, регистр).
//  3. Validate — строгая валидация DTO; неизвестные/unsafe значения
//     отклоняются.
//
// Пакет НЕ связывает domain-слой с YAML: YAML-десериализация изолирована
// в decode.go, а domain-пакеты (asset, policy, processing) не импортируют
// этот пакет. Здесь только собираются доменные объекты из валидированного
// DTO.
//
// Поля DTO используют «снисходительные» обёртки пакета
// github.com/pkg-ru/dynamic: они принимают широкий спектр представлений
// (число или числовая строка, bool или "yes"/"no", duration-строка и т.п.)
// и нормализуют их в единое нативное значение. Доступ к нативному значению
// выполняется через Unwrap(), для nullable-полей — через Nullable.Value /
// Nullable.Set.
package config

import (
	"fmt"

	"github.com/pkg-ru/dynamic"
	"github.com/pkg-ru/imager/domain/asset"
	"github.com/pkg-ru/imager/domain/policy"
	"github.com/pkg-ru/imager/domain/processing"
)

// Config — typed DTO конфигурации конвейера ассетов.
type Config struct {
	// Version — версия конфигурации (должна быть "1").
	Version dynamic.String `yaml:"version"`
	// Watermarks — именованные декларации ватермарок. На них ссылаются
	// пресеты и path-policies по имени (watermark: <name>).
	Watermarks []WatermarkConfig `yaml:"watermarks"`
	// Policy — конфигурация политики.
	Policy policy.Config `yaml:"policy"`
	// Processing — конфигурация обработки.
	Processing ProcessingConfig `yaml:"processing"`
}

// WatermarkConfig — декларация ватермарки в конфигурации.
type WatermarkConfig struct {
	// Name — уникальное имя ватермарки (по нему ссылаются пресеты и
	// path-policies).
	Name dynamic.String `yaml:"name"`
	// Path — путь к файлу изображения ватермарки на диске.
	Path dynamic.String `yaml:"path"`
	// Position — позиция размещения:
	// top | bottom | left | right | center (CSS-подобно). Пусто = center.
	Position dynamic.String `yaml:"position"`
	// Repeat — режим заполнения копиями: no-repeat | repeat | repeat-x |
	// repeat-y | round | space (CSS-подобно). Пусто = no-repeat.
	Repeat dynamic.String `yaml:"repeat"`
	// Size — размер копии: contain | cover | "{width}px {height}px"
	// (CSS background-size). Пусто = contain.
	Size dynamic.String `yaml:"size"`
}

// ProcessingConfig — конфигурация обработки.
type ProcessingConfig struct {
	// DefaultQuality — качество сжатия по умолчанию (0-100).
	DefaultQuality dynamic.Int64 `yaml:"default-quality"`
	// DefaultLoop — зацикливание анимации по умолчанию (nil = true).
	DefaultLoop dynamic.Nullable[dynamic.Bool] `yaml:"default-loop"`
	// DefaultWatermark — имя ватермарки по умолчанию (пусто = не
	// применяется). Используется, если ватермарка не задана ни в пресете,
	// ни в path-policy. Неизвестное имя — ошибка старта.
	DefaultWatermark dynamic.String `yaml:"default-watermark"`
	// DefaultAutoOrient — EXIF auto-orient по умолчанию (nil = true).
	// Применяется, если в пресете auto-orient не задан явно.
	DefaultAutoOrient dynamic.Nullable[dynamic.Bool] `yaml:"default-auto-orient"`
	// DefaultRotate — фиксированный поворот по умолчанию: ""/"none"/"90"/
	// "180"/"270" ("" = без поворота). Применяется, если в пресете rotate
	// не задан явно.
	DefaultRotate dynamic.String `yaml:"default-rotate"`
	// DefaultFlip — отражение по умолчанию: ""/"none"/"horizontal"/
	// "vertical" ("" = без отражения). horizontal = зеркало слева-направо,
	// vertical = сверху-вниз. Применяется, если в пресете flip не задан
	// явно.
	DefaultFlip dynamic.String `yaml:"default-flip"`
	// DefaultTrimMode — режим определения цвета однотонного поля для
	// независимого фильтра trim: "auto" (авто, по краевому пикселю) или
	// "color" (фиксированный цвет DefaultTrimColor). Дефолт: "auto".
	DefaultTrimMode dynamic.String `yaml:"default-trim-mode"`
	// DefaultTrimColor — фиксированный цвет фона для trim в hex-форме
	// "#RRGGBB" (только при default-trim-mode: color). Дефолт: "".
	DefaultTrimColor dynamic.String `yaml:"default-trim-color"`
	// DefaultTrimTolerance — допуск сравнения пикселей с фоновым цветом
	// для trim в диапазоне [0,1] (0 — точное совпадение). Дефолт: 0.
	DefaultTrimTolerance dynamic.Float64 `yaml:"default-trim-tolerance"`
	// DefaultVideoFramePercent — процент от длительности видео, на котором
	// выбирается кадр (0-100). Дефолт: 0.
	DefaultVideoFramePercent dynamic.Int64 `yaml:"default-video-frame-percent,omitempty"`
	// DefaultVideoMinContrast — минимальная контрастность кадра (0-1), ниже
	// которой кадр считается неудачным. Дефолт: 0.
	DefaultVideoMinContrast dynamic.Float64 `yaml:"default-video-min-contrast,omitempty"`
	// DefaultVideoFrameStep — на сколько кадров идти вперёд при неудачной
	// проверке контрастности. Дефолт: 0.
	DefaultVideoFrameStep dynamic.Int64 `yaml:"default-video-frame-step,omitempty"`
	// DefaultVideoAttempts — сколько всего попыток сделать. Дефолт: 0.
	DefaultVideoAttempts dynamic.Int64 `yaml:"default-video-attempts,omitempty"`
}

// SupportedVersion — поддерживаемая версия конфигурации.
const SupportedVersion = "1"

// Validate проверяет корректность DTO.
func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("config is nil")
	}
	if c.Version.Unwrap() != SupportedVersion {
		return fmt.Errorf("config: unsupported version %q, expected %q", c.Version.Unwrap(), SupportedVersion)
	}
	if q := c.Processing.DefaultQuality.Unwrap(); q < 0 || q > 100 {
		return fmt.Errorf("config: default-quality must be in [0,100], got %d", q)
	}
	if _, err := processing.ParseRotation(c.Processing.DefaultRotate.Unwrap()); err != nil {
		return fmt.Errorf("config: processing.default-rotate: %w", err)
	}
	if _, err := processing.ParseFlip(c.Processing.DefaultFlip.Unwrap()); err != nil {
		return fmt.Errorf("config: processing.default-flip: %w", err)
	}
	// Глобальные настройки trim: режим (auto/color), цвет (для color) и
	// допуск [0,1]. Валидируются через TrimSpec.
	if err := c.compileDefaultTrim().Validate(); err != nil {
		return fmt.Errorf("config: processing.default-trim: %w", err)
	}
	// Глобальные настройки видео-превью. Поля опциональны: валидируются
	// только заданные (ненулевые) значения.
	if p := c.Processing.DefaultVideoFramePercent; p != 0 && (p < 0 || p > 100) {
		return fmt.Errorf("config: default-video-frame-percent must be in [0,100], got %d", p)
	}
	if mc := c.Processing.DefaultVideoMinContrast; mc != 0 && (mc < 0 || mc > 1) {
		return fmt.Errorf("config: default-video-min-contrast must be in [0,1], got %v", mc)
	}
	if s := c.Processing.DefaultVideoFrameStep; s != 0 && s < 1 {
		return fmt.Errorf("config: default-video-frame-step must be >= 1, got %d", s)
	}
	if a := c.Processing.DefaultVideoAttempts; a != 0 && a < 1 {
		return fmt.Errorf("config: default-video-attempts must be >= 1, got %d", a)
	}
	// Ватермарки: валидность каждой декларации, уникальность имён.
	for i, w := range c.Watermarks {
		if _, err := processing.NewWatermarkSpec(w.Name.Unwrap(), w.Path.Unwrap(),
			processing.WatermarkPosition(w.Position.Unwrap()),
			processing.WatermarkRepeat(w.Repeat.Unwrap()), w.Size.Unwrap()); err != nil {
			return fmt.Errorf("config: watermarks[%d]: %w", i, err)
		}
	}
	seenWM := make(map[string]bool, len(c.Watermarks))
	for _, w := range c.Watermarks {
		name := w.Name.Unwrap()
		if seenWM[name] {
			return fmt.Errorf("config: watermarks: duplicate name %q", name)
		}
		seenWM[name] = true
	}
	// Ссылки на ватермарки: default-watermark, пресеты, customs.
	checkRef := func(name, what string) error {
		if name == "" {
			return nil
		}
		if !seenWM[name] {
			return fmt.Errorf("config: %s: unknown watermark %q", what, name)
		}
		return nil
	}
	if err := checkRef(c.Processing.DefaultWatermark.Unwrap(), "processing.default-watermark"); err != nil {
		return err
	}
	for i, p := range c.Policy.Presets {
		if err := checkRef(p.Watermark.Unwrap(), fmt.Sprintf("policy.presets[%d] (%s).watermark", i, p.Name.Unwrap())); err != nil {
			return err
		}
	}
	for path, pp := range c.Policy.PathPolicies {
		for cname, cc := range pp.Customs {
			if err := checkRef(cc.Watermark.Unwrap(), fmt.Sprintf("policy.path-policies.%s.customs.%s.watermark", path, cname)); err != nil {
				return err
			}
		}
	}
	if err := policy.ValidateConfig(&c.Policy); err != nil {
		return fmt.Errorf("config: policy: %w", err)
	}
	return nil
}

// watermarkRegistry собирает реестр скомпилированных спецификаций
// ватермарок по имени. Вызывать после Validate (декларации валидны,
// имена уникальны).
func (c *Config) watermarkRegistry() map[string]*processing.WatermarkSpec {
	reg := make(map[string]*processing.WatermarkSpec, len(c.Watermarks))
	for _, w := range c.Watermarks {
		spec, err := processing.NewWatermarkSpec(w.Name.Unwrap(), w.Path.Unwrap(),
			processing.WatermarkPosition(w.Position.Unwrap()),
			processing.WatermarkRepeat(w.Repeat.Unwrap()), w.Size.Unwrap())
		if err != nil {
			// Не должно случиться после Validate.
			continue
		}
		reg[spec.Name] = spec
	}
	return reg
}

// Normalize приводит DTO к канонической форме.
func (c *Config) Normalize() {
	if c.Version.Unwrap() == "" {
		c.Version = dynamic.String(SupportedVersion)
	}
}

// Compiled — результат компиляции конфигурации в доменные объекты.
type Compiled struct {
	// Policy — скомпилированная политика.
	Policy *policy.Policy
	// Presets — набор пресетов.
	Presets *asset.PresetSet
	// DefaultQuality — качество по умолчанию.
	DefaultQuality int64
	// DefaultLoop — зацикливание по умолчанию.
	DefaultLoop *bool
	// Watermarks — реестр скомпилированных спецификаций ватермарок по
	// имени (ссылки уже разрешены в пресетах и path-policies).
	Watermarks map[string]*processing.WatermarkSpec
	// DefaultWatermark — ватермарка по умолчанию (nil = не задана).
	DefaultWatermark *processing.WatermarkSpec
	// DefaultOrientation — ориентация по умолчанию (EXIF auto-orient +
	// ручной rotate/flip). Никогда не nil: при отсутствии настроек содержит
	// {AutoOrient: true}.
	DefaultOrientation *processing.OrientationSpec
	// DefaultTrim — настройки независимого фильтра trim по умолчанию
	// (режим auto/color + tolerance из processing.default-trim-*). Никогда
	// не nil: при отсутствии настроек содержит {Mode: auto, Tolerance: 0}.
	DefaultTrim *processing.TrimSpec
	// DefaultVideoFramePercent — процент от длительности видео, на котором
	// выбирается кадр (0-100).
	DefaultVideoFramePercent int64
	// DefaultVideoMinContrast — минимальная контрастность кадра (0-1), ниже
	// которой кадр считается неудачным.
	DefaultVideoMinContrast float64
	// DefaultVideoFrameStep — на сколько кадров идти вперёд при неудачной
	// проверке контрастности.
	DefaultVideoFrameStep int64
	// DefaultVideoAttempts — сколько всего попыток сделать.
	DefaultVideoAttempts int64
}

// compileDefaultTrim собирает глобальные настройки trim из processing.default-*.
// Возвращает спецификацию по умолчанию ({auto, 0}), если режим не задан.
func (c *Config) compileDefaultTrim() *processing.TrimSpec {
	mode := processing.TrimMode(c.Processing.DefaultTrimMode.Unwrap())
	if mode == "" {
		mode = processing.TrimModeAuto
	}
	return &processing.TrimSpec{
		Mode:      mode,
		Color:     c.Processing.DefaultTrimColor.Unwrap(),
		Tolerance: c.Processing.DefaultTrimTolerance.Unwrap(),
	}
}

// Compile собирает доменные объекты из валидированного DTO.
func (c *Config) Compile() (*Compiled, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	reg := c.watermarkRegistry()
	// Ориентация по умолчанию вычисляется до компиляции политики: пресеты
	// наследуют её по-полево (auto-orient/rotate/flip).
	autoOrient := true
	if c.Processing.DefaultAutoOrient.Set {
		autoOrient = c.Processing.DefaultAutoOrient.Value.Unwrap()
	}
	defRot, err := processing.ParseRotation(c.Processing.DefaultRotate.Unwrap())
	if err != nil {
		return nil, fmt.Errorf("config: compile default orientation: %w", err)
	}
	defFlip, err := processing.ParseFlip(c.Processing.DefaultFlip.Unwrap())
	if err != nil {
		return nil, fmt.Errorf("config: compile default orientation: %w", err)
	}
	defOr, err := processing.NewOrientationSpec(autoOrient, defRot, defFlip)
	if err != nil {
		return nil, fmt.Errorf("config: compile default orientation: %w", err)
	}
	compiled, err := policy.Compile(c.Policy, reg, defOr)
	if err != nil {
		return nil, fmt.Errorf("config: compile policy: %w", err)
	}
	var defWM *processing.WatermarkSpec
	if wm := c.Processing.DefaultWatermark.Unwrap(); wm != "" {
		defWM = reg[wm]
	}
	var defLoop *bool
	if c.Processing.DefaultLoop.Set {
		loop := c.Processing.DefaultLoop.Value.Unwrap()
		defLoop = &loop
	}
	return &Compiled{
		Policy:                   compiled.Policy,
		Presets:                  compiled.Presets,
		DefaultQuality:           c.Processing.DefaultQuality.Unwrap(),
		DefaultLoop:              defLoop,
		Watermarks:               reg,
		DefaultWatermark:         defWM,
		DefaultOrientation:       defOr,
		DefaultTrim:              c.compileDefaultTrim(),
		DefaultVideoFramePercent: c.Processing.DefaultVideoFramePercent.Unwrap(),
		DefaultVideoMinContrast:  c.Processing.DefaultVideoMinContrast.Unwrap(),
		DefaultVideoFrameStep:    c.Processing.DefaultVideoFrameStep.Unwrap(),
		DefaultVideoAttempts:     c.Processing.DefaultVideoAttempts.Unwrap(),
	}, nil
}
