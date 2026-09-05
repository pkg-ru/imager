package composition

import (
	"fmt"
	"os"
	"path/filepath"

	"gitverse.ru/pkg-ru/imager/observability"
	"gopkg.in/yaml.v3"
)

// File names внутри каталога конфигурации.
const (
	// BaseConfigFile — обязательный базовый конфиг слоя setting.
	BaseConfigFile = "setting.yaml"
	// LocalConfigFile — опциональный локальный конфиг слоя setting (глубоко
	// переопределяет базовый). Отсутствие файла — нормальная ситуация.
	LocalConfigFile = "setting-local.yaml"
	// GenerateConfigFile — опциональный базовый конфиг слоя generate.
	GenerateConfigFile = "generate.yaml"
	// GenerateLocalFile — опциональный локальный конфиг слоя generate.
	GenerateLocalFile = "generate-local.yaml"
	// FailbackConfigFile — опциональный базовый конфиг слоя failback.
	FailbackConfigFile = "failback.yaml"
	// FailbackLocalFile — опциональный локальный конфиг слоя failback.
	FailbackLocalFile = "failback-local.yaml"
)

// configLogger — логгер для предупреждений загрузчика. По умолчанию no-op;
// может быть переопределён (например, в тестах) для проверки warning'ов.
var configLogger observability.Logger = observability.NopLogger()

// LoadConfigDir загружает конфигурацию из каталога dir: три слоя (setting,
// generate, failback), каждый с опциональным *-local файлом, deep merge
// base ← local и объединением в порядке setting → generate → failback.
// Результат strict-декодируется в единый RuntimeConfig (неизвестные поля
// отклоняются).
func LoadConfigDir(dir string) (*RuntimeConfig, error) {
	setting, err := loadLayer(dir, BaseConfigFile, LocalConfigFile, true)
	if err != nil {
		return nil, err
	}
	generate, err := loadLayer(dir, GenerateConfigFile, GenerateLocalFile, false)
	if err != nil {
		return nil, err
	}
	failback, err := loadLayer(dir, FailbackConfigFile, FailbackLocalFile, false)
	if err != nil {
		return nil, err
	}

	// Ключ version в опциональных слоях (generate/failback) опционален; если
	// присутствует — должен равняться "1" (защита от рассинхронизации версий).
	if err := checkLayerVersion(generate, GenerateConfigFile); err != nil {
		return nil, err
	}
	if err := checkLayerVersion(failback, FailbackConfigFile); err != nil {
		return nil, err
	}

	merged := mergeLayers(setting, generate, failback)

	data, err := yaml.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("composition: re-encode merged config: %w", err)
	}
	return ParseRuntimeConfig(data)
}

// loadLayer читает пару base+local и возвращает слитую map.
// requireBase=true требует наличия базового файла (для setting); иначе
// отсутствие базового файла — нормальная ситуация (пустая map).
func loadLayer(dir, baseName, localName string, requireBase bool) (map[string]any, error) {
	basePath := filepath.Join(dir, baseName)
	baseData, err := os.ReadFile(basePath)
	if err != nil {
		if os.IsNotExist(err) && !requireBase {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("composition: read base config %s: %w", basePath, err)
	}
	baseMap, err := yamlToMap(baseData)
	if err != nil {
		return nil, fmt.Errorf("composition: parse base config %s: %w", basePath, err)
	}

	// Опциональный local-конфиг. Если файла нет — используем base как есть.
	localPath := filepath.Join(dir, localName)
	localData, err := os.ReadFile(localPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("composition: read local config %s: %w", localPath, err)
		}
		return baseMap, nil
	}
	localMap, lerr := yamlToMap(localData)
	if lerr != nil {
		return nil, fmt.Errorf("composition: parse local config %s: %w", localPath, lerr)
	}
	return deepMerge(baseMap, localMap), nil
}

// checkLayerVersion проверяет, что ключ version в опциональном слое (если
// присутствует) равен "1". Отсутствие ключа — нормальная ситуация.
func checkLayerVersion(layer map[string]any, file string) error {
	v, ok := layer["version"]
	if !ok {
		return nil
	}
	if s, ok := v.(string); ok && s == "1" {
		return nil
	}
	return fmt.Errorf("composition: %s: unsupported version %v (only \"1\" allowed)", file, v)
}

// mergeLayers объединяет три слоя в порядке setting → generate → failback
// (более специализированный слой выигрывает при конфликте скаляров). При
// совпадении top-level ключа в нескольких базовых файлах в лог пишется warning
// с перечнем конфликтующих файлов.
func mergeLayers(setting, generate, failback map[string]any) map[string]any {
	// Собираем, какие базовые файлы содержат каждый top-level ключ.
	owners := map[string][]string{}
	for k := range setting {
		owners[k] = append(owners[k], BaseConfigFile)
	}
	for k := range generate {
		owners[k] = append(owners[k], GenerateConfigFile)
	}
	for k := range failback {
		owners[k] = append(owners[k], FailbackConfigFile)
	}
	for k, files := range owners {
		if len(files) > 1 {
			configLogger.Warnf("config: top-level key %q present in multiple base files: %v; merged in order setting -> generate -> failback", k, files)
		}
	}

	// Deep merge в фиксированном порядке: setting -> generate -> failback.
	return deepMerge(deepMerge(setting, generate), failback)
}

// yamlToMap десериализует YAML-документ в map. Пустой документ допустим и
// даёт пустую map (все умолчания применяются при typed decode).
func yamlToMap(data []byte) (map[string]any, error) {
	var m map[string]any
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	if m == nil {
		return map[string]any{}, nil
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
func deepMerge(base, override map[string]any) map[string]any {
	for k, ov := range override {
		bv, ok := base[k]
		if !ok {
			base[k] = ov
			continue
		}
		bm, okB := bv.(map[string]any)
		om, okO := ov.(map[string]any)
		if okB && okO {
			base[k] = deepMerge(bm, om)
			continue
		}
		base[k] = ov
	}
	return base
}
