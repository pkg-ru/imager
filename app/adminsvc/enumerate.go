package adminsvc

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/pkg-ru/imager/domain/asset"
	"github.com/pkg-ru/imager/domain/policy"
)

// SourceRef — разобранный путь исходника (path/name.format).
type SourceRef struct {
	Path         string
	SourceName   string
	SourceFormat string
}

// parseSourceKey разбирает путь исходника вида "thumbs/photo.jpg" на
// path/name/format. Возвращает ошибку, если имя или формат пусты.
func parseSourceKey(source string) (*SourceRef, error) {
	ext := filepath.Ext(source)
	if ext == "" {
		return nil, errors.New("adminsvc: source has no extension")
	}
	format := strings.TrimPrefix(ext, ".")
	base := strings.TrimSuffix(source, ext)
	idx := strings.LastIndex(base, "/")
	var path, name string
	if idx < 0 {
		name = base
	} else {
		path = base[:idx]
		name = base[idx+1:]
	}
	if name == "" || format == "" {
		return nil, errors.New("adminsvc: invalid source key")
	}
	return &SourceRef{Path: path, SourceName: name, SourceFormat: format}, nil
}

// objectRefKey — алиас parseSourceKey (используется в DeleteBySource).
func objectRefKey(source string) (*SourceRef, error) {
	return parseSourceKey(source)
}

// assetPrefix возвращает префикс ключей ассетов исходника:
// "{path}/{name}-{format}/" (без path, если путь пуст).
func assetPrefix(ref *SourceRef) string {
	prefix := ref.SourceName + "-" + ref.SourceFormat + "/"
	if ref.Path != "" {
		prefix = ref.Path + "/" + prefix
	}
	return prefix
}

// enumerateAssets перечисляет канонические URL всех ассетов исходника по
// правилам политики и пресетам:
//
//   - пресеты: каждый пресет (имя × его output-format) → preset request →
//     policy.Authorize → canonical URL;
//   - канонические размеры: точные размеры из size-rules (диапазоны не
//     перечисляются) × допустимые output-форматы × dpr (1,2,3) × transform
//     (resize + crop-коды) → request → policy.Authorize → canonical URL.
//
// Если политика допускает произвольные размеры (unsafe authorization без
// size-rules) — возвращается ошибка «cannot enumerate» (→ HTTP 400).
func enumerateAssets(ref *SourceRef, pol *policy.Policy, presets *asset.PresetSet) ([]string, error) {
	if ref == nil || pol == nil || presets == nil {
		return nil, ErrInvalidRequest
	}
	seen := map[string]bool{}
	var urls []string
	add := func(u string) {
		if !seen[u] {
			seen[u] = true
			urls = append(urls, u)
		}
	}

	// 1) Пресеты: имя × output-format (у пресета фиксирован output format).
	for _, name := range presets.Names() {
		p, ok := presets.Get(name)
		if !ok {
			continue
		}
		dpr := p.DPR()
		if dpr == 0 {
			dpr = asset.DefaultDPR
		}
		req, err := asset.NewPresetRequest(
			ref.Path,
			asset.SourceName(ref.SourceName),
			asset.Format(ref.SourceFormat),
			asset.PresetName(name),
			dpr,
			p.OutputFormat(),
		)
		if err != nil {
			continue
		}
		if !pol.Authorize(req).Allowed {
			continue
		}
		u, err := req.Build()
		if err != nil {
			continue
		}
		add(u)
	}

	// 2) Канонические размеры, покрытые size-rules.
	if pol.Global.Authorization == policy.AuthUnsafe {
		return nil, errors.New("cannot enumerate assets: unsafe authorization without size rules")
	}
	for _, rule := range pol.Global.SizeRules {
		// Точные размеры: только если измерение задано как single value
		// (Min == Max). Диапазоны не перечисляются.
		var w, h *asset.Dimension
		if rule.Width != nil && rule.Width.Min == rule.Width.Max {
			d := asset.Dimension(rule.Width.Min)
			w = &d
		}
		if rule.Height != nil && rule.Height.Min == rule.Height.Max {
			d := asset.Dimension(rule.Height.Min)
			h = &d
		}
		if w == nil && h == nil {
			continue
		}
		size, err := asset.NewSize(w, h)
		if err != nil {
			continue
		}
		for _, out := range outputFormats {
			for _, dpr := range []asset.DPR{1, 2, 3} {
				for _, tr := range transforms {
					req, err := asset.NewRequest(
						ref.Path,
						asset.SourceName(ref.SourceName),
						asset.Format(ref.SourceFormat),
						tr,
						size,
						dpr,
						asset.Format(out),
					)
					if err != nil {
						continue
					}
					if !pol.Authorize(req).Allowed {
						continue
					}
					u, err := req.Build()
					if err != nil {
						continue
					}
					add(u)
				}
			}
		}
	}

	return urls, nil
}

// outputFormats — допустимые выходные форматы для канонических ассетов при
// перечислении.
var outputFormats = []string{"jpeg", "png", "webp", "gif", "avif", "heif", "apng"}

// transforms — transform-коды, перебираемые при перечислении канонических
// ассетов (resize + все crop/trim-коды; политика отфильтрует неразрешённые).
var transforms = []asset.Transform{
	"",
	asset.TransformCrop,
	asset.TransformTrim,
	asset.TransformCropTrim,
	asset.TransformSmartCrop,
	asset.TransformFaceCrop,
	asset.TransformObjectCrop,
	asset.TransformSmartCropTrim,
	asset.TransformFaceCropTrim,
	asset.TransformObjectCropTrim,
}
