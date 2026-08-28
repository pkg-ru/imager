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
//   - глобальные пресеты: каждый пресет (имя × его output-formats) → preset
//     request → policy.Authorize → canonical URL;
//   - path-policy пресеты: для каждой path-policy, совпадающей с путём
//     исходника, её пресеты (имя × output-formats) → preset request →
//     policy.Authorize → canonical URL;
//   - custom-сегменты path-policy: имя custom (размер-грамматика, опционально
//     с @dpr) × output-formats из whitelist → segment request → canonical URL.
//
// Канонические размеры (size-rules) в новой архитектуре отсутствуют —
// произвольные размеры задаются только через custom-сегменты path-policy.
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

	// 1) Глобальные пресеты: имя × output-formats (у пресета фиксирован
	// output format). Authorize отфильтрует пресеты, не разрешённые на пути
	// исходника (path-policy).
	for _, name := range presets.Names() {
		p, ok := presets.Get(name)
		if !ok {
			continue
		}
		dpr := p.DPR()
		if dpr == 0 {
			dpr = asset.DefaultDPR
		}
		for _, out := range p.OutputFormats() {
			req, err := asset.NewPresetRequest(
				ref.Path,
				asset.SourceName(ref.SourceName),
				asset.Format(ref.SourceFormat),
				asset.PresetName(name),
				dpr,
				out,
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

	// 2) Path-policy пресеты и custom-сегменты для путей, совпадающих с
	// путём исходника. Path-policy пресеты — подмножество глобальных по
	// именам; custom-сегменты — произвольные размеры пути (размер-грамматика,
	// опционально с @dpr).
	pp := pol.MatchPath(ref.Path)
	if pp != nil {
		// 2a) Пресеты path-policy (подмножество глобальных).
		if pp.Presets != nil {
			for _, name := range pp.Presets.Names() {
				p, ok := pp.Presets.Get(name)
				if !ok {
					continue
				}
				dpr := p.DPR()
				if dpr == 0 {
					dpr = asset.DefaultDPR
				}
				for _, out := range p.OutputFormats() {
					req, err := asset.NewPresetRequest(
						ref.Path,
						asset.SourceName(ref.SourceName),
						asset.Format(ref.SourceFormat),
						asset.PresetName(name),
						dpr,
						out,
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
		// 2b) Custom-сегменты: имя (размер-грамматика, опционально с @dpr)
		// × output-formats из whitelist пресета.
		for cname, c := range pp.Customs {
			dpr := c.DPR()
			if dpr == 0 {
				dpr = asset.DefaultDPR
			}
			for _, out := range c.OutputFormats() {
				req, err := asset.NewPresetRequest(
					ref.Path,
					asset.SourceName(ref.SourceName),
					asset.Format(ref.SourceFormat),
					asset.PresetName(cname),
					dpr,
					out,
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

	return urls, nil
}
