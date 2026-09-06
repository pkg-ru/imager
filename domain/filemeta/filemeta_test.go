package filemeta

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNewFileMetadataDefaults(t *testing.T) {
	m := NewFileMetadata()
	if m.SchemaVersion != CurrentSchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", m.SchemaVersion, CurrentSchemaVersion)
	}
	if CurrentSchemaVersion != 1 {
		t.Fatalf("CurrentSchemaVersion = %d, want 1", CurrentSchemaVersion)
	}
	if m.CreatedAt.IsZero() || m.UpdatedAt.IsZero() {
		t.Fatalf("timestamps must be set, got created=%v updated=%v", m.CreatedAt, m.UpdatedAt)
	}
	if loc := m.CreatedAt.Location(); loc != time.UTC {
		t.Fatalf("CreatedAt location = %v, want UTC", loc)
	}
	if loc := m.UpdatedAt.Location(); loc != time.UTC {
		t.Fatalf("UpdatedAt location = %v, want UTC", loc)
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("Validate on fresh metadata: %v", err)
	}
}

func TestValidateBoxes(t *testing.T) {
	valid := func() *FileMetadata {
		return &FileMetadata{
			SchemaVersion: CurrentSchemaVersion,
			Faces:         []FaceInfo{{PixelBox: PixelBox{X: 10, Y: 20, Width: 64, Height: 64}, Confidence: 0.97}},
			Objects:       []ObjectInfo{{PixelBox: PixelBox{X: 0, Y: 0, Width: 1, Height: 1}, Confidence: 0, Label: "person"}},
		}
	}
	if err := valid().Validate(); err != nil {
		t.Fatalf("valid metadata rejected: %v", err)
	}

	cases := []struct {
		name string
		mut  func(m *FileMetadata)
	}{
		{"negative x", func(m *FileMetadata) { m.Faces[0].X = -1 }},
		{"negative y", func(m *FileMetadata) { m.Faces[0].Y = -5 }},
		{"zero width", func(m *FileMetadata) { m.Faces[0].Width = 0 }},
		{"negative height", func(m *FileMetadata) { m.Faces[0].Height = -3 }},
		{"confidence below range", func(m *FileMetadata) { m.Faces[0].Confidence = -0.01 }},
		{"confidence above range", func(m *FileMetadata) { m.Faces[0].Confidence = 1.01 }},
		{"object negative origin", func(m *FileMetadata) { m.Objects[0].Y = -2 }},
		{"object zero size", func(m *FileMetadata) { m.Objects[0].Width = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := valid()
			tc.mut(m)
			if err := m.Validate(); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("expected ErrCorrupt, got %v", err)
			}
		})
	}

	// Граничные значения confidence валидны.
	for _, c := range []float64{0, 1} {
		m := valid()
		m.Faces[0].Confidence = c
		if err := m.Validate(); err != nil {
			t.Fatalf("confidence %v should be valid: %v", c, err)
		}
	}
}

func TestValidateLimits(t *testing.T) {
	tooMany := make([]FaceInfo, MaxItemsPerSlice+1)
	for i := range tooMany {
		tooMany[i] = FaceInfo{PixelBox: PixelBox{Width: 1, Height: 1}, Confidence: 0.5}
	}
	m := &FileMetadata{SchemaVersion: CurrentSchemaVersion, Faces: tooMany}
	if err := m.Validate(); !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), "faces") {
		t.Fatalf("expected faces limit error, got %v", err)
	}

	objs := make([]ObjectInfo, MaxItemsPerSlice+1)
	for i := range objs {
		objs[i] = ObjectInfo{PixelBox: PixelBox{Width: 1, Height: 1}, Confidence: 0.5}
	}
	m = &FileMetadata{SchemaVersion: CurrentSchemaVersion, Objects: objs}
	if err := m.Validate(); !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), "objects") {
		t.Fatalf("expected objects limit error, got %v", err)
	}

	// Ровно на лимите — валидно.
	ok := make([]FaceInfo, MaxItemsPerSlice)
	for i := range ok {
		ok[i] = FaceInfo{PixelBox: PixelBox{Width: 1, Height: 1}, Confidence: 0.5}
	}
	m = &FileMetadata{SchemaVersion: CurrentSchemaVersion, Faces: ok}
	if err := m.Validate(); err != nil {
		t.Fatalf("exactly-at-limit slice should validate: %v", err)
	}
}

func TestValidateAIAsset(t *testing.T) {
	base := func() *AIAssetInfo {
		return &AIAssetInfo{Width: 4000, Height: 3000, Format: "webp", Key: "photos/cat-jpg/x4000@2.webp"}
	}
	if err := (&FileMetadata{LargestAIAsset: base()}).Validate(); err != nil {
		t.Fatalf("valid asset rejected: %v", err)
	}
	cases := []struct {
		name string
		mut  func(a *AIAssetInfo)
	}{
		{"zero width", func(a *AIAssetInfo) { a.Width = 0 }},
		{"negative height", func(a *AIAssetInfo) { a.Height = -10 }},
		{"empty format", func(a *AIAssetInfo) { a.Format = "" }},
		{"empty key", func(a *AIAssetInfo) { a.Key = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := base()
			tc.mut(a)
			if err := (&FileMetadata{LargestAIAsset: a}).Validate(); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("expected ErrCorrupt, got %v", err)
			}
		})
	}
}

func TestClone(t *testing.T) {
	src := &FileMetadata{
		SchemaVersion:  CurrentSchemaVersion,
		Faces:          []FaceInfo{{PixelBox: PixelBox{X: 1, Y: 2, Width: 3, Height: 4}, Confidence: 0.9}},
		Objects:        []ObjectInfo{{PixelBox: PixelBox{X: 5, Y: 6, Width: 7, Height: 8}, Confidence: 0.8, Label: "dog"}},
		LargestAIAsset: &AIAssetInfo{Width: 100, Height: 50, Format: "png", Key: "k"},
		VideoFrameKey:  "photos/cat-jpg/x.jpg",
		CreatedAt:      time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC),
		UpdatedAt:      time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC),
	}
	cp := src.Clone()
	if cp == src {
		t.Fatal("Clone returned same pointer")
	}
	if *cp.LargestAIAsset == *(src.LargestAIAsset) && cp.LargestAIAsset == src.LargestAIAsset {
		t.Fatal("LargestAIAsset not deep-copied")
	}
	if cp.VideoFrameKey != src.VideoFrameKey {
		t.Fatalf("VideoFrameKey not copied: got %q, want %q", cp.VideoFrameKey, src.VideoFrameKey)
	}

	// Мутация копии не затрагивает оригинал.
	cp.Faces[0].Confidence = 0.1
	cp.Objects[0].Label = "cat"
	cp.LargestAIAsset.Width = 999
	cp.VideoFrameKey = "other.jpg"
	if src.Faces[0].Confidence != 0.9 || src.Objects[0].Label != "dog" || src.LargestAIAsset.Width != 100 || src.VideoFrameKey != "photos/cat-jpg/x.jpg" {
		t.Fatalf("mutation of clone leaked into source: %+v", src)
	}

	// nil-безопасность.
	var nilMeta *FileMetadata
	if nilMeta.Clone() != nil {
		t.Fatal("Clone of nil must return nil")
	}

	// Семантика nil vs пустой срез сохраняется.
	empty := &FileMetadata{Faces: []FaceInfo{}}
	cpEmpty := empty.Clone()
	if cpEmpty.Faces == nil || len(cpEmpty.Faces) != 0 {
		t.Fatalf("empty-but-non-nil slice semantics lost: %#v", cpEmpty.Faces)
	}
}

// TestJSONRoundTrip проверяет JSON-схему: snake_case-теги, omitempty для
// отсутствующих данных.
func TestJSONRoundTrip(t *testing.T) {
	src := &FileMetadata{
		SchemaVersion:  CurrentSchemaVersion,
		Faces:          []FaceInfo{{PixelBox: PixelBox{X: 120, Y: 80, Width: 64, Height: 64}, Confidence: 0.97}},
		Objects:        []ObjectInfo{{PixelBox: PixelBox{X: 40, Y: 30, Width: 220, Height: 180}, Confidence: 0.88, Label: "person"}},
		LargestAIAsset: &AIAssetInfo{Width: 4000, Height: 3000, Format: "webp", Key: "photos/cat-jpg/x4000@2.webp"},
		VideoFrameKey:  "photos/cat-jpg/x.jpg",
		CreatedAt:      time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC),
		UpdatedAt:      time.Date(2026, 8, 24, 13, 5, 12, 0, time.UTC),
	}
	data, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, want := range []string{
		`"schema_version":1`,
		`"faces":[`,
		`"x":120`, `"y":80`, `"w":64`, `"h":64`, `"confidence":0.97`,
		`"objects":[`, `"label":"person"`,
		`"largest_ai_asset":{`, `"width":4000`, `"format":"webp"`, `"key":"photos/cat-jpg/x4000@2.webp"`,
		`"video_frame_key":"photos/cat-jpg/x.jpg"`,
		`"created_at":"2026-08-24T13:00:00Z"`,
		`"updated_at":"2026-08-24T13:05:12Z"`,
	} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("JSON missing %s in %s", want, data)
		}
	}

	var back FileMetadata
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	reData, _ := json.Marshal(&back)
	if string(reData) != string(data) {
		t.Fatalf("round-trip mismatch:\n%s\n%s", data, reData)
	}
}

// TestJSONOmitEmpty проверяет минимализм схемы: поля без данных отсутствуют.
func TestJSONOmitEmpty(t *testing.T) {
	data, err := json.Marshal(NewFileMetadata())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	s := string(data)
	for _, unwanted := range []string{"faces", "objects", "largest_ai_asset", "video_frame_key"} {
		if strings.Contains(s, unwanted) {
			t.Fatalf("empty field %q must be omitted, got %s", unwanted, s)
		}
	}
}

// TestSentinelErrors фиксирует наличие и различимость sentinel-ошибок.
func TestSentinelErrors(t *testing.T) {
	errs := []error{ErrNotFound, ErrCorrupt, ErrSchemaTooNew}
	for i, e := range errs {
		if e == nil {
			t.Fatalf("sentinel %d is nil", i)
		}
		for j, other := range errs {
			if i != j && errors.Is(e, other) {
				t.Fatalf("sentinels %d and %d must be distinct", i, j)
			}
		}
	}
}

const testHash = "a3f2b1c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f80"

// TestValidateSourceFingerprint — инварианты SourceFingerprint в Validate.
func TestValidateSourceFingerprint(t *testing.T) {
	valid := func() *FileMetadata {
		return &FileMetadata{
			SchemaVersion: CurrentSchemaVersion,
			Source:        &SourceFingerprint{Size: 1024, ModTimeUnix: 1700000000, HashSHA256: testHash},
		}
	}
	if err := valid().Validate(); err != nil {
		t.Fatalf("valid fingerprint rejected: %v", err)
	}
	// Без хеша (опционален) и без mtime — валидно.
	m := valid()
	m.Source.HashSHA256 = ""
	m.Source.ModTimeUnix = 0
	if err := m.Validate(); err != nil {
		t.Fatalf("fingerprint without hash/mtime must be valid: %v", err)
	}
	// Отрицательный Size.
	m = valid()
	m.Source.Size = -1
	if err := m.Validate(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("negative size: expected ErrCorrupt, got %v", err)
	}
	// Отрицательный ModTimeUnix.
	m = valid()
	m.Source.ModTimeUnix = -5
	if err := m.Validate(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("negative mod_time: expected ErrCorrupt, got %v", err)
	}
	// Некорректный хеш (не hex / не 64).
	for _, bad := range []string{"abc", strings.Repeat("z", 64), strings.Repeat("a", 63)} {
		m = valid()
		m.Source.HashSHA256 = bad
		if err := m.Validate(); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("bad hash %q: expected ErrCorrupt, got %v", bad, err)
		}
	}
}

// TestSourceFingerprintMatches — семантика сравнения отпечатков.
func TestSourceFingerprintMatches(t *testing.T) {
	base := &SourceFingerprint{Size: 100, ModTimeUnix: 1700000000, HashSHA256: testHash}

	// Идентичные — совпадают.
	if !base.Matches(&SourceFingerprint{Size: 100, ModTimeUnix: 1700000000, HashSHA256: testHash}) {
		t.Fatal("identical fingerprints must match")
	}
	// Разный размер — не совпадают.
	if base.Matches(&SourceFingerprint{Size: 200, ModTimeUnix: 1700000000, HashSHA256: testHash}) {
		t.Fatal("different size must not match")
	}
	// Разный хеш — не совпадают.
	if base.Matches(&SourceFingerprint{Size: 100, ModTimeUnix: 1700000000, HashSHA256: "b" + testHash[1:]}) {
		t.Fatal("different hash must not match")
	}
	// Разный mtime (оба ненулевые) — не совпадают.
	if base.Matches(&SourceFingerprint{Size: 100, ModTimeUnix: 1700000001, HashSHA256: testHash}) {
		t.Fatal("different mtime must not match")
	}
	// Пустой mtime на одной из сторон игнорируется (источник без mtime).
	if !base.Matches(&SourceFingerprint{Size: 100, HashSHA256: testHash}) {
		t.Fatal("zero mtime on one side must be ignored")
	}
	// Пустой хеш на одной из сторон игнорируется (хеш опционален).
	if !base.Matches(&SourceFingerprint{Size: 100, ModTimeUnix: 1700000000}) {
		t.Fatal("empty hash on one side must be ignored")
	}
	// nil-приёмник/аргумент — не совпадают.
	var nilFp *SourceFingerprint
	if nilFp.Matches(base) || base.Matches(nil) {
		t.Fatal("nil fingerprint must never match")
	}
}

// TestJSONDetectionAndSourceFields — сериализация detection/source и
// backward-compat: старый sidecar без этих полей читается (nil), новый
// round-trip сохраняет данные.
func TestJSONDetectionAndSourceFields(t *testing.T) {
	src := &FileMetadata{
		SchemaVersion: CurrentSchemaVersion,
		Faces:         []FaceInfo{{PixelBox: PixelBox{X: 1, Y: 2, Width: 3, Height: 4}, Confidence: 0.9}},
		Detection:     &DetectionInfo{Detector: "onnx", FaceModel: "face.onnx", ObjectModel: "obj.onnx", ConfidenceThreshold: 0.6},
		Source:        &SourceFingerprint{Size: 42, ModTimeUnix: 1700000000, HashSHA256: testHash},
		CreatedAt:     time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC),
		UpdatedAt:     time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC),
	}
	data, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, want := range []string{
		`"detection":{`,
		`"detector":"onnx"`, `"face_model":"face.onnx"`, `"object_model":"obj.onnx"`, `"confidence_threshold":0.6`,
		`"source":{`, `"size":42`, `"mod_time_unix":1700000000`, `"hash_sha256":"` + testHash + `"`,
	} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("JSON missing %s in %s", want, data)
		}
	}
	var back FileMetadata
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if back.Detection == nil || back.Detection.Detector != "onnx" || back.Detection.ConfidenceThreshold != 0.6 {
		t.Fatalf("detection round-trip lost: %+v", back.Detection)
	}
	if back.Source == nil || back.Source.Size != 42 || back.Source.HashSHA256 != testHash {
		t.Fatalf("source round-trip lost: %+v", back.Source)
	}

	// Backward-compat: v1-структура без detection/source читается как nil.
	var old FileMetadata
	if err := json.Unmarshal([]byte(`{"schema_version":1,"created_at":"2026-09-06T12:00:00Z","updated_at":"2026-09-06T12:00:00Z"}`), &old); err != nil {
		t.Fatalf("Unmarshal legacy: %v", err)
	}
	if old.Detection != nil || old.Source != nil {
		t.Fatalf("legacy sidecar must parse with nil detection/source: %+v %+v", old.Detection, old.Source)
	}
	if err := old.Validate(); err != nil {
		t.Fatalf("legacy sidecar must be valid: %v", err)
	}
}

// TestCloneDetectionAndSource — Clone глубокая копирует Detection и Source.
func TestCloneDetectionAndSource(t *testing.T) {
	src := &FileMetadata{
		Detection: &DetectionInfo{Detector: "onnx", FaceModel: "f.onnx"},
		Source:    &SourceFingerprint{Size: 7, HashSHA256: testHash},
	}
	cp := src.Clone()
	if cp.Detection == src.Detection || cp.Source == src.Source {
		t.Fatal("Clone must deep-copy Detection/Source pointers")
	}
	cp.Detection.FaceModel = "mutated.onnx"
	cp.Source.Size = 999
	if src.Detection.FaceModel != "f.onnx" || src.Source.Size != 7 {
		t.Fatalf("mutation of clone leaked into source: %+v %+v", src.Detection, src.Source)
	}
}
