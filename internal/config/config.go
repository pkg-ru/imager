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
)

// Config — typed DTO конфигурации конвейера ассетов.
type Config struct {
	// Version — версия конфигурации (должна быть "1").
	Version string `yaml:"version"`
	// Policy — конфигурация политики.
	Policy policy.Config `yaml:"policy"`
	// Processing — конфигурация обработки.
	Processing ProcessingConfig `yaml:"processing"`
}

// ProcessingConfig — конфигурация обработки.
type ProcessingConfig struct {
	// DefaultQuality — качество сжатия по умолчанию (0-100).
	DefaultQuality int `yaml:"default-quality"`
	// DefaultLoop — зацикливание анимации по умолчанию (nil = true).
	DefaultLoop *bool `yaml:"default-loop"`
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
	if err := policy.ValidateConfig(&c.Policy); err != nil {
		return fmt.Errorf("config: policy: %w", err)
	}
	return nil
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
}

// Compile собирает доменные объекты из валидированного DTO.
func (c *Config) Compile() (*Compiled, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	compiled, err := policy.Compile(&c.Policy)
	if err != nil {
		return nil, fmt.Errorf("config: compile policy: %w", err)
	}
	return &Compiled{
		Policy:         compiled.Policy,
		Presets:        compiled.Presets,
		DefaultQuality: c.Processing.DefaultQuality,
		DefaultLoop:    c.Processing.DefaultLoop,
	}, nil
}
