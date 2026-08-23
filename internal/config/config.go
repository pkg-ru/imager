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
package config

import (
	"fmt"

	"github.com/pkg-ru/imager/internal/domain/asset"
	"github.com/pkg-ru/imager/internal/domain/policy"
	"github.com/pkg-ru/imager/internal/domain/processing"
)

// Config — typed DTO конфигурации конвейера ассетов.
type Config struct {
	// Version — версия конфигурации (должна быть "1").
	Version string `yaml:"version"`
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
	Name string `yaml:"name"`
	// Path — путь к файлу изображения ватермарки на диске.
	Path string `yaml:"path"`
	// Position — позиция размещения:
	// top | bottom | left | right | center (CSS-подобно). Пусто = center.
	Position string `yaml:"position"`
	// Repeat — режим заполнения копиями: no-repeat | repeat | repeat-x |
	// repeat-y | round | space (CSS-подобно). Пусто = no-repeat.
	Repeat string `yaml:"repeat"`
	// Size — размер копии: contain | cover | "{width}px {height}px"
	// (CSS background-size). Пусто = contain.
	Size string `yaml:"size"`
}

// ProcessingConfig — конфигурация обработки.
type ProcessingConfig struct {
	// DefaultQuality — качество сжатия по умолчанию (0-100).
	DefaultQuality int `yaml:"default-quality"`
	// DefaultLoop — зацикливание анимации по умолчанию (nil = true).
	DefaultLoop *bool `yaml:"default-loop"`
	// DefaultWatermark — имя ватермарки по умолчанию (пусто = не
	// применяется). Используется, если ватермарка не задана ни в пресете,
	// ни в path-policy. Неизвестное имя — ошибка старта.
	DefaultWatermark string `yaml:"default-watermark"`
	// DefaultAutoOrient — EXIF auto-orient по умолчанию (nil = true).
	// Применяется, если в пресете auto-orient не задан явно.
	DefaultAutoOrient *bool `yaml:"default-auto-orient"`
	// DefaultRotate — фиксированный поворот по умолчанию: ""/"none"/"90"/
	// "180"/"270" ("" = без поворота). Применяется, если в пресете rotate
	// не задан явно.
	DefaultRotate string `yaml:"default-rotate"`
	// DefaultFlip — отражение по умолчанию: ""/"none"/"horizontal"/
	// "vertical" ("" = без отражения). horizontal = зеркало слева-направо,
	// vertical = сверху-вниз. Применяется, если в пресете flip не задан
	// явно.
	DefaultFlip string `yaml:"default-flip"`
}

// SupportedVersion — поддерживаемая версия конфигурации.
const SupportedVersion = "1"

// Validate проверяет корректность DTO.
func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("config is nil")
	}
	if c.Version != SupportedVersion {
		return fmt.Errorf("config: unsupported version %q, expected %q", c.Version, SupportedVersion)
	}
	if c.Processing.DefaultQuality < 0 || c.Processing.DefaultQuality > 100 {
		return fmt.Errorf("config: default-quality must be in [0,100], got %d", c.Processing.DefaultQuality)
	}
	if _, err := processing.ParseRotation(c.Processing.DefaultRotate); err != nil {
		return fmt.Errorf("config: processing.default-rotate: %w", err)
	}
	if _, err := processing.ParseFlip(c.Processing.DefaultFlip); err != nil {
		return fmt.Errorf("config: processing.default-flip: %w", err)
	}
	// Ватермарки: валидность каждой декларации, уникальность имён.
	for i, w := range c.Watermarks {
		if _, err := processing.NewWatermarkSpec(w.Name, w.Path,
			processing.WatermarkPosition(w.Position),
			processing.WatermarkRepeat(w.Repeat), w.Size); err != nil {
			return fmt.Errorf("config: watermarks[%d]: %w", i, err)
		}
	}
	seenWM := make(map[string]bool, len(c.Watermarks))
	for _, w := range c.Watermarks {
		if seenWM[w.Name] {
			return fmt.Errorf("config: watermarks: duplicate name %q", w.Name)
		}
		seenWM[w.Name] = true
	}
	// Ссылки на ватермарки: default-watermark, пресеты, path-policies.
	checkRef := func(name, what string) error {
		if name == "" {
			return nil
		}
		if !seenWM[name] {
			return fmt.Errorf("config: %s: unknown watermark %q", what, name)
		}
		return nil
	}
	if err := checkRef(c.Processing.DefaultWatermark, "processing.default-watermark"); err != nil {
		return err
	}
	for i, p := range c.Policy.Presets {
		if err := checkRef(p.Watermark, fmt.Sprintf("policy.presets[%d] (%s).watermark", i, p.Name)); err != nil {
			return err
		}
	}
	for i, pp := range c.Policy.PathPolicies {
		if err := checkRef(pp.Watermark, fmt.Sprintf("policy.path-policies[%d] (%s).watermark", i, pp.Path)); err != nil {
			return err
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
		spec, err := processing.NewWatermarkSpec(w.Name, w.Path,
			processing.WatermarkPosition(w.Position),
			processing.WatermarkRepeat(w.Repeat), w.Size)
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
	if c.Version == "" {
		c.Version = SupportedVersion
	}
}

// Compiled — результат компиляции конфигурации в доменные объекты.
type Compiled struct {
	// Policy — скомпилированная политика.
	Policy *policy.Policy
	// Presets — набор пресетов.
	Presets *asset.PresetSet
	// DefaultQuality — качество по умолчанию.
	DefaultQuality int
	// DefaultLoop — зацикливание по умолчанию.
	DefaultLoop *bool
	// Watermarks — реестр скомпилированных спецификаций ватермарок по
	// имени (ссылки уже разрешены в пресетах и path-policies).
	Watermarks map[string]*processing.WatermarkSpec
	// DefaultWatermark — ватермарка по умолчанию (nil = не задана).
	DefaultWatermark *processing.WatermarkSpec
	// DefaultOrientation — ориентация по умолчанию (EXIF auto-orient +
	// ручной rotate/flip). Никогда не nil: при отсутствии настроек содержит
	// {AutoOrient: true} (историческое поведение движков).
	DefaultOrientation *processing.OrientationSpec
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
	if c.Processing.DefaultAutoOrient != nil {
		autoOrient = *c.Processing.DefaultAutoOrient
	}
	defRot, err := processing.ParseRotation(c.Processing.DefaultRotate)
	if err != nil {
		return nil, fmt.Errorf("config: compile default orientation: %w", err)
	}
	defFlip, err := processing.ParseFlip(c.Processing.DefaultFlip)
	if err != nil {
		return nil, fmt.Errorf("config: compile default orientation: %w", err)
	}
	defOr, err := processing.NewOrientationSpec(autoOrient, defRot, defFlip)
	if err != nil {
		return nil, fmt.Errorf("config: compile default orientation: %w", err)
	}
	compiled, err := policy.Compile(&c.Policy, reg, defOr)
	if err != nil {
		return nil, fmt.Errorf("config: compile policy: %w", err)
	}
	var defWM *processing.WatermarkSpec
	if c.Processing.DefaultWatermark != "" {
		defWM = reg[c.Processing.DefaultWatermark]
	}
	return &Compiled{
		Policy:             compiled.Policy,
		Presets:            compiled.Presets,
		DefaultQuality:     c.Processing.DefaultQuality,
		DefaultLoop:        c.Processing.DefaultLoop,
		Watermarks:         reg,
		DefaultWatermark:   defWM,
		DefaultOrientation: defOr,
	}, nil
}
