package learning

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitverse.ru/pkg-ru/imager/domain/policy"
)

// stateOf — хелпер построения state.
func stateOf(pairs ...[2]any) map[string]policy.PathPolicyConfig {
	m := make(map[string]policy.PathPolicyConfig, len(pairs))
	for _, p := range pairs {
		path, _ := p[0].(string)
		pp, _ := p[1].(policy.PathPolicyConfig)
		m[path] = pp
	}
	return m
}

func TestUpdatePathPoliciesNewFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "generate-local.yaml")
	state := stateOf([2]any{"/a/b", policy.PathPolicyConfig{
		Customs: customs([2]any{"120x60", sizeCustom("webp")}),
	}})
	if err := UpdatePathPolicies(file, state); err != nil {
		t.Fatalf("UpdatePathPolicies: %v", err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		`version: "1"`,
		"policy:",
		"learning-mode: true",
		"path-policies:",
		"/a/b:",
		"120x60:",
		"webp",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("file content missing %q:\n%s", want, got)
		}
	}
}

const fixtureWithComments = `# top-level head comment
version: "1" # version line comment

# policy head comment
policy:
  # learning-mode head comment
  learning-mode: true # learning-mode line comment
  path-policies:
    # /a/b head comment
    /a/b: # /a/b line comment
      customs:
        "120x60": # custom line comment
          output-formats: [webp]
    /a/c:
      presets: [thumb]

# foot comment
`

func TestUpdatePathPoliciesPreservesComments(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "generate-local.yaml")
	if err := os.WriteFile(file, []byte(fixtureWithComments), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	// Добавляем новый путь /x/y; существующие не трогаем.
	state := stateOf(
		[2]any{"/a/b", policy.PathPolicyConfig{
			Customs: customs([2]any{"120x60", sizeCustom("webp")}),
		}},
		[2]any{"/a/c", policy.PathPolicyConfig{
			Presets: fmts("thumb"),
		}},
		[2]any{"/x/y", policy.PathPolicyConfig{
			Customs: customs([2]any{"200x200", sizeCustom("avif")}),
		}},
	)
	if err := UpdatePathPolicies(file, state); err != nil {
		t.Fatalf("UpdatePathPolicies: %v", err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(data)
	// Побайтовая проверка нетронутых секций: комментарии и существующие
	// записи сохраняются.
	for _, want := range []string{
		"# top-level head comment",
		`version: "1" # version line comment`,
		"# policy head comment",
		"# learning-mode head comment",
		"learning-mode: true # learning-mode line comment",
		"# /a/b head comment",
		"/a/b: # /a/b line comment",
		`"120x60": # custom line comment`,
		"output-formats: [webp]",
		"presets: [thumb]",
		"# foot comment",
		"# added by learning-mode",
		"/x/y:",
		"200x200:",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("file content missing %q:\n%s", want, got)
		}
	}
}

func TestUpdatePathPoliciesMergeExisting(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "generate-local.yaml")
	initial := `policy:
  path-policies:
    /a/b:
      customs:
        "120x60":
          output-formats: [webp]
`
	if err := os.WriteFile(file, []byte(initial), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	// Существующий custom сохраняется, добавляется новый.
	state := stateOf([2]any{"/a/b", policy.PathPolicyConfig{
		Customs: customs(
			[2]any{"120x60", sizeCustom("webp")},
			[2]any{"300x300", sizeCustom("avif")},
		),
	}})
	if err := UpdatePathPolicies(file, state); err != nil {
		t.Fatalf("UpdatePathPolicies: %v", err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, `"120x60":`) || !strings.Contains(got, "webp") {
		t.Errorf("existing custom lost:\n%s", got)
	}
	if !strings.Contains(got, "300x300:") || !strings.Contains(got, "avif") {
		t.Errorf("new custom missing:\n%s", got)
	}
}

func TestUpdatePathPoliciesRemovesHoistedKeys(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "generate-local.yaml")
	initial := `policy:
  path-policies:
    /a/b:
      customs:
        "120x60":
          output-formats: [webp]
    /a/c:
      customs:
        "120x60":
          output-formats: [webp]
    /keep:
      presets: [thumb]
`
	if err := os.WriteFile(file, []byte(initial), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	// /a/b и /a/c hoist-нуты в /a; /keep остаётся.
	state := stateOf(
		[2]any{"/a", policy.PathPolicyConfig{
			Customs: customs([2]any{"120x60", sizeCustom("webp")}),
		}},
		[2]any{"/keep", policy.PathPolicyConfig{
			Presets: fmts("thumb"),
		}},
	)
	if err := UpdatePathPolicies(file, state); err != nil {
		t.Fatalf("UpdatePathPolicies: %v", err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(data)
	if strings.Contains(got, "/a/b:") || strings.Contains(got, "/a/c:") {
		t.Errorf("hoisted keys not removed:\n%s", got)
	}
	if !strings.Contains(got, "/a:") || !strings.Contains(got, "/keep:") {
		t.Errorf("expected keys missing:\n%s", got)
	}
}

func TestUpdatePathPoliciesIdempotent(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "generate-local.yaml")
	state := stateOf(
		[2]any{"/a/b", policy.PathPolicyConfig{
			Customs: customs([2]any{"120x60", sizeCustom("webp")}),
		}},
		[2]any{"/a/c", policy.PathPolicyConfig{
			Presets: fmts("thumb"),
		}},
	)
	if err := UpdatePathPolicies(file, state); err != nil {
		t.Fatalf("first write: %v", err)
	}
	first, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read first: %v", err)
	}
	if err := UpdatePathPolicies(file, state); err != nil {
		t.Fatalf("second write: %v", err)
	}
	second, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read second: %v", err)
	}
	if string(first) != string(second) {
		t.Errorf("double write not idempotent:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

func TestUpdatePathPoliciesSortedKeys(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "generate-local.yaml")
	state := stateOf(
		[2]any{"/z", policy.PathPolicyConfig{Presets: fmts("t")}},
		[2]any{"/a", policy.PathPolicyConfig{Presets: fmts("t")}},
		[2]any{"/m", policy.PathPolicyConfig{Presets: fmts("t")}},
	)
	if err := UpdatePathPolicies(file, state); err != nil {
		t.Fatalf("UpdatePathPolicies: %v", err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(data)
	ia := strings.Index(got, "/a:")
	im := strings.Index(got, "/m:")
	iz := strings.Index(got, "/z:")
	if ia < 0 || im < 0 || iz < 0 || !(ia < im && im < iz) {
		t.Errorf("keys not sorted:\n%s", got)
	}
}

func TestUpdatePathPoliciesAtomicNoTmpFiles(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "generate-local.yaml")
	state := stateOf([2]any{"/a", policy.PathPolicyConfig{Presets: fmts("t")}})
	if err := UpdatePathPolicies(file, state); err != nil {
		t.Fatalf("UpdatePathPolicies: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover tmp file: %s", e.Name())
		}
	}
	// Файл валиден и перезаписываем (rename сработал).
	if err := UpdatePathPolicies(file, state); err != nil {
		t.Fatalf("second write: %v", err)
	}
}

// TestSetLearningModeDisablesExisting — персистентный сброс learning-mode
// при shutdown: существующий learning-mode: true (в т.ч. с комментариями)
// заменяется на false, остальные секции и комментарии не трогаются.
func TestSetLearningModeDisablesExisting(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "generate-local.yaml")
	initial := `# top comment
policy:
  # learning-mode head comment
  learning-mode: true # learning-mode line comment
  path-policies:
    /a/b:
      customs:
        "120x60":
          output-formats: [webp]
`
	if err := os.WriteFile(file, []byte(initial), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := SetLearningMode(file, false); err != nil {
		t.Fatalf("SetLearningMode(false): %v", err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		"# top comment",
		"# learning-mode head comment",
		"learning-mode: false # learning-mode line comment",
		"/a/b:",
		`"120x60":`,
		"output-formats: [webp]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("file content missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "learning-mode: true") {
		t.Errorf("learning-mode not disabled:\n%s", got)
	}
}

// TestSetLearningModeCreatesMissingKey — в файле без ключа learning-mode
// (например, написанного вручную) ключ создаётся со значением false.
func TestSetLearningModeCreatesMissingKey(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "generate-local.yaml")
	initial := `policy:
  path-policies:
    /a/b:
      presets: [thumb]
`
	if err := os.WriteFile(file, []byte(initial), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := SetLearningMode(file, false); err != nil {
		t.Fatalf("SetLearningMode(false): %v", err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "learning-mode: false") {
		t.Errorf("learning-mode key not created:\n%s", got)
	}
	if !strings.Contains(got, "presets: [thumb]") {
		t.Errorf("existing sections lost:\n%s", got)
	}
}

// TestSetLearningModeMissingFile — файла нет: создаётся минимальный
// документ с learning-mode: false.
func TestSetLearningModeMissingFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "generate-local.yaml")
	if err := SetLearningMode(file, false); err != nil {
		t.Fatalf("SetLearningMode(false): %v", err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(data)
	for _, want := range []string{`version: "1"`, "policy:", "learning-mode: false"} {
		if !strings.Contains(got, want) {
			t.Errorf("file content missing %q:\n%s", want, got)
		}
	}
}

// TestSetLearningModeEnabledAndQuotedValue — включение режима и сброс
// закавыченного значения ("true" — строка, не bool): style сбрасывается,
// значение становится валидным bool.
func TestSetLearningModeEnabledAndQuotedValue(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "generate-local.yaml")
	initial := "policy:\n  learning-mode: \"true\"\n"
	if err := os.WriteFile(file, []byte(initial), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	// Сброс закавыченного значения.
	if err := SetLearningMode(file, false); err != nil {
		t.Fatalf("SetLearningMode(false): %v", err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(data)
	if strings.Contains(got, `"true"`) {
		t.Errorf("quoted value not normalized:\n%s", got)
	}
	if !strings.Contains(got, "learning-mode: false") {
		t.Errorf("learning-mode not disabled:\n%s", got)
	}
	// Включение обратно.
	if err := SetLearningMode(file, true); err != nil {
		t.Fatalf("SetLearningMode(true): %v", err)
	}
	data, err = os.ReadFile(file)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "learning-mode: true") {
		t.Errorf("learning-mode not enabled:\n%s", string(data))
	}
}

// TestSetLearningModeIdempotent — повторный сброс не меняет файл.
func TestSetLearningModeIdempotent(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "generate-local.yaml")
	if err := SetLearningMode(file, false); err != nil {
		t.Fatalf("first write: %v", err)
	}
	first, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read first: %v", err)
	}
	if err := SetLearningMode(file, false); err != nil {
		t.Fatalf("second write: %v", err)
	}
	second, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read second: %v", err)
	}
	if string(first) != string(second) {
		t.Errorf("double write not idempotent:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

// TestUpdatePathPoliciesExtendsExistingOutputFormats — фикс бага learning-mode:
// при уже существующем custom новый формат из state должен ДОБАВЛЯТЬСЯ в
// output-formats (а не игнорироваться), сохраняя существующие значения и
// комментарии.
func TestUpdatePathPoliciesExtendsExistingOutputFormats(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "generate-local.yaml")
	initial := `policy:
  path-policies:
    /test:
      customs:
        "220x200":
          output-formats: [gif]
`
	if err := os.WriteFile(file, []byte(initial), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	// В памяти AddObservation дополнил список форматами jpg и webp.
	state := stateOf([2]any{"/test", policy.PathPolicyConfig{
		Customs: customs([2]any{"220x200", sizeCustom("gif", "jpg", "webp")}),
	}})
	if err := UpdatePathPolicies(file, state); err != nil {
		t.Fatalf("UpdatePathPolicies: %v", err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(data)
	// Существующие форматы сохраняются, новые добавляются (порядок: сначала
	// существующие, затем добавленные; YAML-стиль — как в исходном файле).
	if !strings.Contains(got, "[gif, jpg, webp]") {
		t.Errorf("expected output-formats [gif, jpg, webp] after merge:\n%s", got)
	}
}

// TestUpdatePathPoliciesExtendIdempotent — повторная запись с тем же state
// не дублирует форматы (идемпотентность).
func TestUpdatePathPoliciesExtendIdempotent(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "generate-local.yaml")
	initial := `policy:
  path-policies:
    /test:
      customs:
        "220x200":
          output-formats: [gif, webp]
`
	if err := os.WriteFile(file, []byte(initial), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	state := stateOf([2]any{"/test", policy.PathPolicyConfig{
		Customs: customs([2]any{"220x200", sizeCustom("gif", "jpg", "webp")}),
	}})
	if err := UpdatePathPolicies(file, state); err != nil {
		t.Fatalf("first write: %v", err)
	}
	first, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read first: %v", err)
	}
	// Повторная запись того же state — файл не меняется.
	if err := UpdatePathPolicies(file, state); err != nil {
		t.Fatalf("second write: %v", err)
	}
	second, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read second: %v", err)
	}
	if string(first) != string(second) {
		t.Errorf("merge not idempotent:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	// Ровно по одному вхождению каждого формата (flow-стиль сохранён).
	got := string(second)
	for _, f := range []string{"gif", "jpg", "webp"} {
		if n := strings.Count(got, f); n != 1 {
			t.Errorf("format %q occurs %d times, want 1:\n%s", f, n, got)
		}
	}
}

// TestUpdatePathPoliciesExtendsOutputFormatsWhenKeyMissing — если у
// существующего custom нет ключа output-formats, он создаётся.
func TestUpdatePathPoliciesExtendsOutputFormatsWhenKeyMissing(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "generate-local.yaml")
	initial := `policy:
  path-policies:
    /test:
      customs:
        "220x200":
          quality: 80
`
	if err := os.WriteFile(file, []byte(initial), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	state := stateOf([2]any{"/test", policy.PathPolicyConfig{
		Customs: customs([2]any{"220x200", sizeCustom("avif")}),
	}})
	if err := UpdatePathPolicies(file, state); err != nil {
		t.Fatalf("UpdatePathPolicies: %v", err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "output-formats:") || !strings.Contains(got, "avif") {
		t.Errorf("output-formats key not created:\n%s", got)
	}
	if !strings.Contains(got, "quality: 80") {
		t.Errorf("existing field lost:\n%s", got)
	}
}

// TestUpdatePathPoliciesPresetFlowStyle — секция presets создаётся как
// flow-style sequence: `presets: [face-fix]`, а не block-списком.
func TestUpdatePathPoliciesPresetFlowStyle(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "generate-local.yaml")
	state := stateOf([2]any{"/test", policy.PathPolicyConfig{
		Presets: fmts("face-fix"),
	}})
	if err := UpdatePathPolicies(file, state); err != nil {
		t.Fatalf("UpdatePathPolicies: %v", err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := string(data); !strings.Contains(got, "presets: [face-fix]") {
		t.Errorf("expected flow-style presets: [face-fix]:\n%s", got)
	}
}

// TestUpdatePathPoliciesMergesIntoExistingPresets — новые пресеты
// добавляются в существующую секцию presets (в т.ч. block-style из
// исходного файла: узел переводится в flow-style), существующие имена
// сохраняются, дубликаты не создаются.
func TestUpdatePathPoliciesMergesIntoExistingPresets(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "generate-local.yaml")
	initial := `policy:
  path-policies:
    /test:
      presets:
        - face
        - object
`
	if err := os.WriteFile(file, []byte(initial), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	// В памяти AddPresetObservation дополнил список face-fix и smart.
	state := stateOf([2]any{"/test", policy.PathPolicyConfig{
		Presets: fmts("face", "object", "face-fix", "smart"),
	}})
	if err := UpdatePathPolicies(file, state); err != nil {
		t.Fatalf("UpdatePathPolicies: %v", err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "presets: [face, object, face-fix, smart]") {
		t.Errorf("expected merged flow-style presets:\n%s", got)
	}
	// Повторная запись — идемпотентность.
	if err := UpdatePathPolicies(file, state); err != nil {
		t.Fatalf("second write: %v", err)
	}
	second, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read second: %v", err)
	}
	if string(got) != string(second) {
		t.Errorf("presets merge not idempotent:\nfirst:\n%s\nsecond:\n%s", got, second)
	}
}

// TestUpdatePathPoliciesBlockOutputFormatsConvertsToFlow — существующий
// block-style output-formats при добавлении форматов переводится в
// flow-style (требование пользователя: списки в одну строку).
func TestUpdatePathPoliciesBlockOutputFormatsConvertsToFlow(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "generate-local.yaml")
	initial := `policy:
  path-policies:
    /test:
      customs:
        "220x200":
          output-formats:
            - gif
`
	if err := os.WriteFile(file, []byte(initial), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	state := stateOf([2]any{"/test", policy.PathPolicyConfig{
		Customs: customs([2]any{"220x200", sizeCustom("gif", "webp")}),
	}})
	if err := UpdatePathPolicies(file, state); err != nil {
		t.Fatalf("UpdatePathPolicies: %v", err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := string(data); !strings.Contains(got, "output-formats: [gif, webp]") {
		t.Errorf("expected flow-style output-formats [gif, webp]:\n%s", got)
	}
}

// TestUpdatePathPoliciesNullPathPolicies — фикс бага: если в YAML-конфиге
// ключ path-policies присутствует, но пуст (null: "path-policies:" без
// значения), запись learning-mode ДОЛЖНА происходить в block-стиле.
// Раньше null-узел не был mapping'ом — writer возвращал ошибку и ничего
// не записывал.
func TestUpdatePathPoliciesNullPathPolicies(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "generate-local.yaml")
	initial := `policy:
  learning-mode: true
  path-policies:
`
	if err := os.WriteFile(file, []byte(initial), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	state := stateOf([2]any{"/test", policy.PathPolicyConfig{
		Customs: customs([2]any{"200x200", sizeCustom("png", "webp")}),
	}})
	if err := UpdatePathPolicies(file, state); err != nil {
		t.Fatalf("UpdatePathPolicies: %v", err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		"path-policies:",
		"# added by learning-mode",
		"/test:",
		"customs:",
		"200x200:",
		"output-formats: [png, webp]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("file content missing %q:\n%s", want, got)
		}
	}
	// Block-стиль: path-policies НЕ сериализован внутри "{}".
	if strings.Contains(got, "path-policies: {") {
		t.Errorf("expected block-style path-policies, got flow:\n%s", got)
	}
	// Комментарий стоит ПЕРЕД новым путём.
	ic := strings.Index(got, "# added by learning-mode")
	ip := strings.Index(got, "/test:")
	if ic < 0 || ip < 0 || ic > ip {
		t.Errorf("comment must precede new path entry:\n%s", got)
	}
}

// TestUpdatePathPoliciesEmptyFlowMapBecomesBlock — фикс бага: пустой
// flow-map "path-policies: {}" при добавлении записей переводится в
// block-стиль (пустой flow-mapping сбрасывает Style, иначе новые записи
// сериализуются внутри "{}").
func TestUpdatePathPoliciesEmptyFlowMapBecomesBlock(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "generate-local.yaml")
	initial := `policy:
  learning-mode: true
  path-policies: {}
`
	if err := os.WriteFile(file, []byte(initial), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	state := stateOf([2]any{"/test", policy.PathPolicyConfig{
		Customs: customs([2]any{"200x200", sizeCustom("png", "webp")}),
	}})
	if err := UpdatePathPolicies(file, state); err != nil {
		t.Fatalf("UpdatePathPolicies: %v", err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(data)
	if strings.Contains(got, "path-policies: {") {
		t.Errorf("expected block-style path-policies after fill, got flow:\n%s", got)
	}
	for _, want := range []string{
		"# added by learning-mode",
		"/test:",
		"output-formats: [png, webp]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("file content missing %q:\n%s", want, got)
		}
	}
}

// TestUpdatePathPoliciesFlowStylePreserved — если path-policies записан в
// flow-стиле и содержит данные, новая запись добавляется с сохранением
// существующего содержимого и flow-стиля узла.
func TestUpdatePathPoliciesFlowStylePreserved(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "generate-local.yaml")
	initial := `policy:
  learning-mode: true
  path-policies: {/ava: {customs: {50x50: {output-formats: [png]}}}}
`
	if err := os.WriteFile(file, []byte(initial), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	state := stateOf(
		[2]any{"/ava", policy.PathPolicyConfig{
			Customs: customs([2]any{"50x50", sizeCustom("png")}),
		}},
		[2]any{"/test", policy.PathPolicyConfig{
			Customs: customs([2]any{"200x200", sizeCustom("png", "webp")}),
		}},
	)
	if err := UpdatePathPolicies(file, state); err != nil {
		t.Fatalf("UpdatePathPolicies: %v", err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(data)
	// Существующая запись /ava сохранена.
	for _, want := range []string{"/ava:", "50x50:", "png"} {
		if !strings.Contains(got, want) {
			t.Errorf("existing flow entry missing %q:\n%s", want, got)
		}
	}
	// Новая запись /test добавлена.
	if !strings.Contains(got, "/test:") || !strings.Contains(got, "200x200:") {
		t.Errorf("new flow entry missing:\n%s", got)
	}
	// Flow-стиль узла path-policies сохранён.
	if !strings.Contains(got, "path-policies: {") {
		t.Errorf("expected flow-style path-policies preserved:\n%s", got)
	}
}

// TestUpdatePathPoliciesBlockStyleAddsComment — path-policies в block-стиле
// с данными: новая запись добавляется в block-стиле с комментарием
// "# added by learning-mode" перед новым путём; существующие записи не
// трогаются.
func TestUpdatePathPoliciesBlockStyleAddsComment(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "generate-local.yaml")
	initial := `policy:
  learning-mode: true
  path-policies:
    /ava:
      customs:
        50x50:
          output-formats: [png]
`
	if err := os.WriteFile(file, []byte(initial), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	state := stateOf(
		[2]any{"/ava", policy.PathPolicyConfig{
			Customs: customs([2]any{"50x50", sizeCustom("png")}),
		}},
		[2]any{"/test", policy.PathPolicyConfig{
			Customs: customs([2]any{"200x200", sizeCustom("png", "webp")}),
		}},
	)
	if err := UpdatePathPolicies(file, state); err != nil {
		t.Fatalf("UpdatePathPolicies: %v", err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(data)
	if strings.Contains(got, "path-policies: {") {
		t.Errorf("expected block-style path-policies:\n%s", got)
	}
	for _, want := range []string{
		"/ava:",
		"50x50:",
		"output-formats: [png]",
		"# added by learning-mode",
		"/test:",
		"output-formats: [png, webp]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("file content missing %q:\n%s", want, got)
		}
	}
	// Комментарий стоит ПЕРЕД новым путём и ПОСЛЕ существующего.
	ic := strings.Index(got, "# added by learning-mode")
	iava := strings.Index(got, "/ava:")
	itest := strings.Index(got, "/test:")
	if ic < 0 || iava < 0 || itest < 0 || !(iava < ic && ic < itest) {
		t.Errorf("comment must be between existing and new entries:\n%s", got)
	}
}

func TestUpdatePathPoliciesEmptyState(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "generate-local.yaml")
	// Пустой state на существующем файле: path-policies очищается.
	initial := `policy:
  path-policies:
    /a/b:
      customs:
        "120x60":
          output-formats: [webp]
`
	if err := os.WriteFile(file, []byte(initial), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := UpdatePathPolicies(file, map[string]policy.PathPolicyConfig{}); err != nil {
		t.Fatalf("UpdatePathPolicies: %v", err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(data)
	if strings.Contains(got, "/a/b:") {
		t.Errorf("expected empty path-policies:\n%s", got)
	}
	if !strings.Contains(got, "path-policies:") {
		t.Errorf("path-policies key must remain:\n%s", got)
	}
}
