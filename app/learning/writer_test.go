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
