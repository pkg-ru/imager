package httpapi

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v2"
)

// File names внутри каталога конфигурации.
const (
	// BaseConfigFile — обязательный базовый конфиг.
	BaseConfigFile = "setting.yaml"
	// LocalConfigFile — опциональный локальный конфиг (глубоко переопределяет
	// базовый). Отсутствие файла — нормальная ситуация.
	LocalConfigFile = "setting-local.yaml"
)

// LoadConfigDir загружает конфигурацию из каталога dir:
//   - `${dir}/setting.yaml` — обязательный базовый конфиг (ошибка, если
//     файл отсутствует или невалиден);
//   - `${dir}/setting-local.yaml` — опциональный локальный конфиг, который
//     глубоко переопределяет базовый (nested maps мержатся, скаляры
//     заменяются, списки заменяются целиком).
//
// Результат strict-декодируется в единый typed RuntimeConfig. Неизвестные
// поля в любом из файлов отклоняются (fail-fast).
func LoadConfigDir(dir string) (*RuntimeConfig, error) {
	base := filepath.Join(dir, BaseConfigFile)
	data, err := os.ReadFile(base)
	if err != nil {
		return nil, fmt.Errorf("httpapi: read base config %s: %w", base, err)
	}

	baseMap, err := yamlToMap(data)
	if err != nil {
		return nil, fmt.Errorf("httpapi: parse base config %s: %w", base, err)
	}

	// Опциональный local-конфиг. Если файла нет — используем base как есть.
	localPath := filepath.Join(dir, LocalConfigFile)
	localData, err := os.ReadFile(localPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("httpapi: read local config %s: %w", localPath, err)
		}
	} else {
		localMap, lerr := yamlToMap(localData)
		if lerr != nil {
			return nil, fmt.Errorf("httpapi: parse local config %s: %w", localPath, lerr)
		}
		baseMap = deepMerge(baseMap, localMap)
	}

	merged, err := yaml.Marshal(baseMap)
	if err != nil {
		return nil, fmt.Errorf("httpapi: re-encode merged config: %w", err)
	}
	return ParseRuntimeConfig(merged)
}

// yamlToMap десериализует YAML-документ в map. Пустой документ допустим и
// даёт пустую map (все умолчания применяются при typed decode).
func yamlToMap(data []byte) (map[interface{}]interface{}, error) {
	var m map[interface{}]interface{}
	if len(data) == 0 {
		return map[interface{}]interface{}{}, nil
	}
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	if m == nil {
		return map[interface{}]interface{}{}, nil
	}
	return m, nil
}

// deepMerge рекурсивно мержит override в base:
//   - map: рекурсивный merge по ключам (вложенные map мержатся);
//   - прочие значения (скаляры, списки, nil): значение override заменяет
//     значение base целиком.
//
// Списки заменяются целиком, а не мержатся — это контракт для конфигурации
// (например allowed-origins или disabled-coders нельзя "дополнить" в local).
func deepMerge(base, override map[interface{}]interface{}) map[interface{}]interface{} {
	for k, ov := range override {
		bv, ok := base[k]
		if !ok {
			base[k] = ov
			continue
		}
		bm, okB := bv.(map[interface{}]interface{})
		om, okO := ov.(map[interface{}]interface{})
		if okB && okO {
			base[k] = deepMerge(bm, om)
			continue
		}
		base[k] = ov
	}
	return base
}
