package learning

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/pkg-ru/imager/domain/policy"
	"gopkg.in/yaml.v3"
)

// Comment-preserving запись state в generate-local.yaml через yaml.Node.
//
// yaml.Node сохраняет комментарии (HeadComment/LineComment/FootComment)
// и структуру документа: обновляется только секция policy.path-policies,
// все остальные ключи документа и policy остаются как есть.

// addedByLearningMode — HeadComment для новых пар ключ/value.
const addedByLearningMode = "# added by learning-mode"

// UpdatePathPolicies записывает state в YAML-файл file с сохранением
// комментариев.
//
//   - Если файла нет — создаётся минимальный документ:
//     version: "1" / policy: { learning-mode: true, path-policies: {...} }.
//   - Если файл есть — парсится в yaml.Node; находится mapping-узел policy
//     (создаётся при отсутствии), в нём ключ path-policies (создаётся при
//     отсутствии). Остальные ключи не трогаются.
//   - Для каждого пути из state: существующий value-узел мержится
//     рекурсивно (существующие custom-записи и presets сохраняются как
//     есть, добавляются только новые custom-ключи); новый путь добавляется
//     с HeadComment "# added by learning-mode".
//   - Ключи путей, отсутствующие в state, удаляются из path-policies.
//   - Ключи path-policies сортируются лексикографически.
//
// Запись атомарная: временный файл в том же каталоге + os.Rename, права
// 0o644.
func UpdatePathPolicies(file string, state map[string]policy.PathPolicyConfig) error {
	data, err := os.ReadFile(file)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("learning: read %s: %w", file, err)
		}
		return writeNewDocument(file, state)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("learning: parse %s: %w", file, err)
	}
	if doc.Kind == 0 {
		// Пустой файл — минимальный документ.
		return writeNewDocument(file, state)
	}

	root := documentRoot(&doc)
	policyNode := findOrCreateMappingValue(root, "policy", false)
	if policyNode == nil {
		return fmt.Errorf("learning: %s: policy is not a mapping", file)
	}
	ppNode := findOrCreateMappingValue(policyNode, "path-policies", true)
	if ppNode == nil {
		return fmt.Errorf("learning: %s: path-policies is not a mapping", file)
	}

	if err := mergePathPolicies(ppNode, state); err != nil {
		return fmt.Errorf("learning: %s: %w", file, err)
	}

	out, err := encodeNode(&doc)
	if err != nil {
		return fmt.Errorf("learning: encode %s: %w", file, err)
	}
	return atomicWrite(file, out)
}

// writeNewDocument создаёт файл с минимальным документом.
func writeNewDocument(file string, state map[string]policy.PathPolicyConfig) error {
	doc := &yaml.Node{Kind: yaml.MappingNode}
	appendScalarKey(doc, "version", "1")

	policyNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	appendBoolKey(policyNode, "learning-mode", true)
	ppNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if err := mergePathPolicies(ppNode, state); err != nil {
		return err
	}
	appendNodeKey(policyNode, "path-policies", ppNode)
	appendNodeKey(doc, "policy", policyNode)

	out, err := encodeNode(doc)
	if err != nil {
		return fmt.Errorf("learning: encode %s: %w", file, err)
	}
	return atomicWrite(file, out)
}

// documentRoot возвращает корневой mapping-узел документа (распаковывает
// внешний document-узел).
func documentRoot(doc *yaml.Node) *yaml.Node {
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		return doc.Content[0]
	}
	return doc
}

// findOrCreateMappingValue ищет в mapping-узле m пару с ключом key и
// возвращает value-узел. Если пары нет — создаёт её (value — пустой
// mapping) и возвращает новый узел. Если value существующей пары — не
// mapping, возвращает nil.
func findOrCreateMappingValue(m *yaml.Node, key string, withComment bool) *yaml.Node {
	if m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			v := m.Content[i+1]
			if v.Kind != yaml.MappingNode {
				return nil
			}
			return v
		}
	}
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	valNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if withComment {
		keyNode.HeadComment = addedByLearningMode
	}
	m.Content = append(m.Content, keyNode, valNode)
	return valNode
}

// mergePathPolicies синхронизирует mapping-узел ppNode с state:
// merge существующих, добавление новых (с HeadComment), удаление
// отсутствующих в state, сортировка ключей.
func mergePathPolicies(ppNode *yaml.Node, state map[string]policy.PathPolicyConfig) error {
	if ppNode.Kind != yaml.MappingNode {
		return fmt.Errorf("path-policies is not a mapping")
	}

	// Индекс существующих пар: ключ → (keyNode, valNode).
	type entry struct {
		keyNode *yaml.Node
		valNode *yaml.Node
	}
	existing := map[string]*entry{}
	var order []string
	for i := 0; i+1 < len(ppNode.Content); i += 2 {
		k := ppNode.Content[i].Value
		if _, dup := existing[k]; !dup {
			order = append(order, k)
		}
		existing[k] = &entry{keyNode: ppNode.Content[i], valNode: ppNode.Content[i+1]}
	}

	// Удалить ключи, отсутствующие в state.
	var kept []*yaml.Node
	for _, k := range order {
		if _, ok := state[k]; ok {
			kept = append(kept, existing[k].keyNode, existing[k].valNode)
		}
	}

	// Merge/add: для каждого пути из state. Существующие пары уже в kept
	// (после удаления отсутствующих) — только merge value-узла.
	for _, path := range sortedStateKeys(state) {
		pp := state[path]
		if e, ok := existing[path]; ok {
			if err := mergePathPolicyValue(e.valNode, pp); err != nil {
				return fmt.Errorf("path %q: %w", path, err)
			}
			continue
		}
		keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: path, HeadComment: addedByLearningMode}
		valNode := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		if err := mergePathPolicyValue(valNode, pp); err != nil {
			return fmt.Errorf("path %q: %w", path, err)
		}
		kept = append(kept, keyNode, valNode)
	}

	// Сортировка ключей лексикографически.
	type pair struct {
		k, v *yaml.Node
	}
	pairs := make([]pair, 0, len(kept)/2)
	for i := 0; i+1 < len(kept); i += 2 {
		pairs = append(pairs, pair{k: kept[i], v: kept[i+1]})
	}
	sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].k.Value < pairs[j].k.Value })

	ppNode.Content = ppNode.Content[:0]
	for _, p := range pairs {
		ppNode.Content = append(ppNode.Content, p.k, p.v)
	}
	return nil
}

// mergePathPolicyValue мержит PathPolicyConfig в существующий (или новый)
// value-узел пути: presets не трогаются, существующие customs сохраняются
// как есть, добавляются только новые custom-ключи.
func mergePathPolicyValue(valNode *yaml.Node, pp policy.PathPolicyConfig) error {
	if valNode.Kind != yaml.MappingNode {
		return fmt.Errorf("path policy value is not a mapping")
	}
	// Найти узел customs (если есть).
	var customsNode *yaml.Node
	for i := 0; i+1 < len(valNode.Content); i += 2 {
		if valNode.Content[i].Value == "customs" {
			customsNode = valNode.Content[i+1]
			break
		}
	}
	if len(pp.Customs) == 0 {
		return nil
	}
	if customsNode == nil {
		// Секция customs отсутствует — добавить целиком.
		keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "customs"}
		customsNode = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		valNode.Content = append(valNode.Content, keyNode, customsNode)
	} else if customsNode.Kind != yaml.MappingNode {
		return fmt.Errorf("customs is not a mapping")
	}
	for _, name := range sortedCustomNames(pp.Customs) {
		if hasKey(customsNode, name) {
			continue // существующий custom не трогается
		}
		keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}
		val := presetConfigNode(pp.Customs[name])
		customsNode.Content = append(customsNode.Content, keyNode, val)
	}
	return nil
}

// presetConfigNode строит YAML-узел custom-записи. Learning-mode создаёт
// customs только с output-formats, поэтому узел строится вручную (без
// сериализации dynamic-полей PresetConfig, которые дали бы шум "0"/"").
func presetConfigNode(cfg policy.PresetConfig) *yaml.Node {
	m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if len(cfg.OutputFormats) > 0 {
		k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "output-formats"}
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, f := range cfg.OutputFormats {
			seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: string(f)})
		}
		m.Content = append(m.Content, k, seq)
	}
	return m
}

// hasKey проверяет наличие ключа в mapping-узле.
func hasKey(m *yaml.Node, key string) bool {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return true
		}
	}
	return false
}

// appendScalarKey добавляет в mapping пару key/scalar-значение.
func appendScalarKey(m *yaml.Node, key, value string) {
	k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	v := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
	m.Content = append(m.Content, k, v)
}

// appendBoolKey добавляет в mapping пару key/bool.
func appendBoolKey(m *yaml.Node, key string, value bool) {
	k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	v := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: fmt.Sprintf("%v", value)}
	m.Content = append(m.Content, k, v)
}

// appendNodeKey добавляет в mapping пару key/узел.
func appendNodeKey(m *yaml.Node, key string, value *yaml.Node) {
	k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	m.Content = append(m.Content, k, value)
}

// encodeNode сериализует узел с отступом 2.
func encodeNode(doc *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// atomicWrite атомарно записывает data в file: временный файл в том же
// каталоге + os.Rename, права 0o644.
func atomicWrite(file string, data []byte) error {
	dir := filepath.Dir(file)
	tmp, err := os.CreateTemp(dir, ".learning-*.tmp")
	if err != nil {
		return fmt.Errorf("learning: create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		// При ошибке (ok == false) — убрать мусорный tmp-файл.
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("learning: write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("learning: close temp file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("learning: chmod temp file: %w", err)
	}
	if err := os.Rename(tmpName, file); err != nil {
		return fmt.Errorf("learning: rename %s -> %s: %w", tmpName, file, err)
	}
	ok = true
	return nil
}

// sortedStateKeys возвращает сортированные ключи state.
func sortedStateKeys(m map[string]policy.PathPolicyConfig) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// sortedCustomNames возвращает сортированные имена customs.
func sortedCustomNames(m map[string]policy.PresetConfig) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
