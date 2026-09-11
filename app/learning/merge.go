package learning

import (
	"sort"
	"strings"

	"github.com/pkg-ru/dynamic"
	"gitverse.ru/pkg-ru/imager/domain/asset"
	"gitverse.ru/pkg-ru/imager/domain/policy"
)

// Чистые функции слияния наблюдений в состояние path-policies.
//
// state — map: ключ = путь-префикс (нормализованный, с ведущим "/"),
// значение = policy.PathPolicyConfig. Функции мутируют state на месте
// (кроме NormalizeState, возвращающей новую map) и сообщают об изменениях
// возвращаемым bool.

// AddObservation добавляет наблюдение (path-префикс, size — custom-имя
// вида "120x60", format — например "webp") в state.
//
// Алгоритм:
//   - дедуп через предков: если у существующего предка path уже есть
//     custom size с тем же или надмножеством output-formats — no-op;
//   - если state[path].Customs[size] нет — создать {output-formats: [format]};
//   - если есть и формата нет — дополнить список (сортировка для
//     детерминизма);
//   - после добавления вызвать HoistIdenticalCustoms.
//
// path нормализуется (normalizePath-семантика: ведущий "/", без хвостового).
func AddObservation(state map[string]policy.PathPolicyConfig, path, size, format string) bool {
	path = normalizePrefix(path)
	if path == "" || path == "/" {
		return false
	}
	if _, err := asset.ParseSize(size); err != nil {
		return false
	}
	if format == "" {
		return false
	}

	// Дедуп через предков: у предка уже есть custom size, покрывающий
	// формат (тот же формат или надмножество) — наблюдение избыточно.
	for ancestor := range state {
		if !isAncestorOrSelf(ancestor, path) || ancestor == path {
			continue
		}
		pp, ok := state[ancestor]
		if !ok {
			continue
		}
		custom, ok := pp.Customs[size]
		if !ok {
			continue
		}
		for _, f := range custom.OutputFormats {
			if string(f) == format {
				return false
			}
		}
	}

	pp := state[path]
	if pp.Customs == nil {
		pp.Customs = map[string]policy.PresetConfig{}
	}
	custom, ok := pp.Customs[size]
	if !ok {
		pp.Customs[size] = policy.PresetConfig{
			OutputFormats: dynamic.StringSlice{dynamic.String(format)},
		}
		state[path] = pp
		HoistIdenticalCustoms(state)
		return true
	}
	// Custom есть: дополнить output-formats, если формата нет.
	found := false
	for _, f := range custom.OutputFormats {
		if string(f) == format {
			found = true
			break
		}
	}
	if found {
		return false
	}
	custom.OutputFormats = append(custom.OutputFormats, dynamic.String(format))
	sortFormats(custom.OutputFormats)
	pp.Customs[size] = custom
	state[path] = pp
	HoistIdenticalCustoms(state)
	return true
}

// AddPresetObservation добавляет наблюдение пресета (path-префикс, preset —
// имя пресета из конфигурации) в state.
//
// В отличие от AddObservation (customs), пресетные наблюдения пополняют
// pp.Presets — список имён глобальных пресетов, разрешённых на пути.
// Дедуп: имя уже в списке — no-op. Hoist для пресетов не выполняется:
// Presets — simple список имён без форматов, сравнение бессмысленно.
//
// path нормализуется (normalizePrefix-семантика: ведущий "/", без
// хвостового).
func AddPresetObservation(state map[string]policy.PathPolicyConfig, path, preset string) bool {
	path = normalizePrefix(path)
	if path == "" || path == "/" {
		return false
	}
	if preset == "" {
		return false
	}
	pp := state[path]
	for _, p := range pp.Presets {
		if string(p) == preset {
			return false
		}
	}
	pp.Presets = append(pp.Presets, dynamic.String(preset))
	sort.Slice(pp.Presets, func(i, j int) bool { return string(pp.Presets[i]) < string(pp.Presets[j]) })
	state[path] = pp
	return true
}

// HoistIdenticalCustoms спускает (поднимает) идентичные custom-записи
// к общему предку.
//
// Для каждой пары путей P, Q, где один является предком другого ИЛИ они
// "братья" с общим предком A (longest common prefix по сегментам):
//   - если наборы customs у P и Q содержат ИДЕНТИЧНЫЕ custom-записи
//     (совпадают имена и полностью PresetConfig) — удалить их из P и Q
//     и записать в A (если у A ещё нет идентичного custom);
//   - если после удаления у пути не осталось ни customs, ни presets —
//     удалить путь из state целиком;
//   - пути с непустым presets не удаляются, но их customs поднимаются;
//   - если A == P или A == Q — просто слить в общего предка.
//
// Повторяется до фикспойнта: hoist может открыть новые возможности
// слияния. Возвращает true, если state изменился.
func HoistIdenticalCustoms(state map[string]policy.PathPolicyConfig) bool {
	changed := false
	for {
		if !hoistOnce(state) {
			return changed
		}
		changed = true
	}
}

// hoistOnce выполняет один проход подъёма; true — были изменения.
func hoistOnce(state map[string]policy.PathPolicyConfig) bool {
	paths := sortedKeys(state)
	for _, p := range paths {
		pp, ok := state[p]
		if !ok {
			continue
		}
		if len(pp.Customs) == 0 {
			continue
		}
		for _, q := range paths {
			if q == p {
				continue
			}
			qq, ok := state[q]
			if !ok || len(qq.Customs) == 0 {
				continue
			}
			// Общий предок A: если один — предок другого, A = более
			// короткий; иначе longest common prefix по сегментам.
			a := commonAncestor(p, q)
			if a == "" || a == "/" {
				continue
			}
			// Идентичные custom-записи в P и Q.
			identical := map[string]policy.PresetConfig{}
			for name, cfg := range pp.Customs {
				if qc, ok := qq.Customs[name]; ok && samePresetConfig(cfg, qc) {
					identical[name] = cfg
				}
			}
			if len(identical) == 0 {
				continue
			}
			// Удалить из P и Q, записать в A (если там ещё нет).
			// A == P или A == Q — просто слить в общего предка.
			for name, cfg := range identical {
				delete(pp.Customs, name)
				delete(qq.Customs, name)
				ap, ok := state[a]
				if !ok {
					ap = policy.PathPolicyConfig{}
				}
				if ap.Customs == nil {
					ap.Customs = map[string]policy.PresetConfig{}
				}
				if _, exists := ap.Customs[name]; !exists {
					ap.Customs[name] = cfg
				}
				state[a] = ap
			}
			// Путь без customs и presets удаляется целиком.
			if len(pp.Customs) == 0 && len(pp.Presets) == 0 {
				delete(state, p)
			} else {
				state[p] = pp
			}
			if len(qq.Customs) == 0 && len(qq.Presets) == 0 {
				delete(state, q)
			} else {
				state[q] = qq
			}
			return true
		}
	}
	return false
}

// NormalizeState возвращает нормализованную копию state: сортированные
// output-formats, удаление пустых path-policy записей.
func NormalizeState(state map[string]policy.PathPolicyConfig) map[string]policy.PathPolicyConfig {
	out := make(map[string]policy.PathPolicyConfig, len(state))
	for path, pp := range state {
		if len(pp.Presets) == 0 && len(pp.Customs) == 0 {
			continue
		}
		norm := policy.PathPolicyConfig{Presets: pp.Presets}
		if len(pp.Customs) > 0 {
			norm.Customs = make(map[string]policy.PresetConfig, len(pp.Customs))
			for name, cfg := range pp.Customs {
				formats := append(dynamic.StringSlice(nil), cfg.OutputFormats...)
				sortFormats(formats)
				cfg.OutputFormats = formats
				norm.Customs[name] = cfg
			}
		}
		out[path] = norm
	}
	return out
}

// normalizePrefix нормализует префикс пути: ведущий "/", без хвостового
// "/" (кроме ровно "/"). Пустая строка остаётся пустой.
func normalizePrefix(path string) string {
	if path == "" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if path != "/" {
		path = strings.TrimSuffix(path, "/")
	}
	return path
}

// isAncestorOrSelf проверяет, что ancestor — префикс path по сегментам
// (оба нормализованы). "/" — предок любого пути.
func isAncestorOrSelf(ancestor, path string) bool {
	if ancestor == path {
		return true
	}
	if ancestor == "/" {
		return true
	}
	return strings.HasPrefix(path, ancestor+"/")
}

// commonAncestor возвращает общего предка путей p и q: если один — предок
// другого, возвращает более короткий; иначе longest common prefix по
// сегментам. Возвращает "" для корня (hoist в "/" не выполняется).
func commonAncestor(p, q string) string {
	if isAncestorOrSelf(p, q) {
		return p
	}
	if isAncestorOrSelf(q, p) {
		return q
	}
	ps := strings.Split(strings.TrimPrefix(p, "/"), "/")
	qs := strings.Split(strings.TrimPrefix(q, "/"), "/")
	n := len(ps)
	if len(qs) < n {
		n = len(qs)
	}
	i := 0
	for i < n && ps[i] == qs[i] {
		i++
	}
	if i == 0 {
		return ""
	}
	return "/" + strings.Join(ps[:i], "/")
}

// samePresetConfig сравнивает PresetConfig по значению (все поля,
// включая вложенные).
func samePresetConfig(a, b policy.PresetConfig) bool {
	if a.Width != b.Width || a.Height != b.Height {
		return false
	}
	if !sameStringSlice(a.OutputFormats, b.OutputFormats) {
		return false
	}
	if a.DPR.Set != b.DPR.Set {
		return false
	}
	if a.DPR.Set && a.DPR.Value != b.DPR.Value {
		return false
	}
	if a.Crop != b.Crop || a.Trim != b.Trim {
		return false
	}
	if a.Quality != b.Quality || a.Frames != b.Frames ||
		a.Duration != b.Duration || a.Loop != b.Loop {
		return false
	}
	if a.Watermark != b.Watermark || a.AutoOrient != b.AutoOrient {
		return false
	}
	if a.Rotate != b.Rotate || a.Flip != b.Flip {
		return false
	}
	return true
}

// sameStringSlice сравнивает dynamic.StringSlice без учёта порядка.
func sameStringSlice(a, b dynamic.StringSlice) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]int, len(a))
	for _, s := range a {
		set[string(s)]++
	}
	for _, s := range b {
		set[string(s)]--
		if set[string(s)] < 0 {
			return false
		}
	}
	return true
}

// sortFormats сортирует dynamic.StringSlice лексикографически.
func sortFormats(s dynamic.StringSlice) {
	sort.Slice(s, func(i, j int) bool { return string(s[i]) < string(s[j]) })
}

// sortedKeys возвращает сортированные ключи map.
func sortedKeys(m map[string]policy.PathPolicyConfig) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
