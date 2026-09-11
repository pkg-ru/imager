// Package filemeta определяет доменную модель sidecar-метаданных
// родительского файла (результаты ИИ-детекции и информация о крупнейшем
// «ИИ-ассете»).
//
// Пакет не зависит от адаптеров: он содержит только типы, инварианты
// (Validate) и sentinel-ошибки, общие для порта metadata.Store и его
// реализаций.
//
// Семантика «нет данных» vs «проверено, пусто» (критично для гарантии
// «1 вызов модели на родителя»):
//   - Faces == nil            — модель ещё не запускалась;
//   - Faces != nil, len == 0  — модель запускалась, лиц нет (кэшируется).
//
// Аналогично для Objects.
package filemeta

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"
)

// CurrentSchemaVersion — текущая версия схемы sidecar (поле schema_version).
const CurrentSchemaVersion = 1

// MaxItemsPerSlice — максимум элементов в каждом из срезов Faces/Objects
// (защита от аномальных/подменённых файлов).
const MaxItemsPerSlice = 1000

// Sentinel-ошибки домена.
var (
	// ErrNotFound — sidecar отсутствует (ленивое создание: это норма).
	ErrNotFound = errors.New("filemeta: metadata not found")
	// ErrCorrupt — битый JSON/IO при чтении либо нарушение инвариантов;
	// кэш считается промахом, перезапись разрешена.
	ErrCorrupt = errors.New("filemeta: corrupt metadata")
	// ErrSchemaTooNew — schema_version новее известной: читать/перезаписывать
	// нельзя (не топтать данные более новой версии сервиса).
	ErrSchemaTooNew = errors.New("filemeta: schema version is newer than supported")
)

// PixelBox — бокс детекции в пикселях оригинального изображения.
// Зеркало detection.Box из адаптера, но без зависимости домена от адаптеров.
//
// JSON-теги: x, y, w, h.
type PixelBox struct {
	// X — координата левого верхнего угла по горизонтали (px), >= 0.
	X int `json:"x"`
	// Y — координата левого верхнего угла по вертикали (px), >= 0.
	Y int `json:"y"`
	// Width — ширина бокса (px), > 0.
	Width int `json:"w"`
	// Height — высота бокса (px), > 0.
	Height int `json:"h"`
}

// FaceInfo — результат детекции одного лица.
// Встроенный PixelBox сериализуется «плоско» (x/y/w/h рядом с confidence).
type FaceInfo struct {
	PixelBox
	// Confidence — уверенность детектора, [0,1].
	Confidence float64 `json:"confidence"`
}

// ObjectInfo — результат детекции одного объекта.
type ObjectInfo struct {
	PixelBox
	// Confidence — уверенность детектора, [0,1].
	Confidence float64 `json:"confidence"`
	// Label — имя класса (опционально; у лиц отсутствует).
	Label string `json:"label,omitempty"`
}

// DetectionInfo — описание детектора, которым получены Faces/Objects.
// Записывается в sidecar вместе с результатами детекции для диагностики
// (какие модели использовались) и защиты от смешивания результатов разных
// конфигураций.
type DetectionInfo struct {
	// Detector — вид детектора (например "onnx").
	Detector string `json:"detector,omitempty"`
	// FaceModel — путь/имя модели лиц (пусто = модель не использовалась).
	FaceModel string `json:"face_model,omitempty"`
	// ObjectModel — путь/имя модели объектов (пусто = не использовалась).
	ObjectModel string `json:"object_model,omitempty"`
	// ConfidenceThreshold — порог уверенности детектора [0,1].
	ConfidenceThreshold float64 `json:"confidence_threshold,omitempty"`
}

// SourceFingerprint — отпечаток исходного файла, по которому выполнялась
// детекция. Используется для инвалидации кэша: если отпечаток текущего
// источника не совпадает с сохранённым — Faces/Objects устарели и модель
// должна быть вызвана заново.
type SourceFingerprint struct {
	// Size — размер исходного файла в байтах (>= 0).
	Size int64 `json:"size"`
	// ModTimeUnix — unix-время модификации источника (сек). Для источников
	// без mtime (извлечённый кадр) — 0, инвалидация только по Size/Hash.
	ModTimeUnix int64 `json:"mod_time_unix,omitempty"`
	// HashSHA256 — SHA-256 содержимого источника (64 hex-символа) или
	// пустая строка, если хеш не вычислялся.
	HashSHA256 string `json:"hash_sha256,omitempty"`
}

// Matches сверяет отпечаток с other: совпадают все ЗАДАННЫЕ компоненты.
// Пустой хеш с обеих сторон считается совпадающим (хеш опционален);
// nil-приёмник всегда не совпадает.
func (f *SourceFingerprint) Matches(other *SourceFingerprint) bool {
	if f == nil || other == nil {
		return false
	}
	if f.Size != other.Size {
		return false
	}
	if f.ModTimeUnix != 0 && other.ModTimeUnix != 0 && f.ModTimeUnix != other.ModTimeUnix {
		return false
	}
	if f.HashSHA256 != "" && other.HashSHA256 != "" && f.HashSHA256 != other.HashSHA256 {
		return false
	}
	return true
}

// AIAssetInfo — крупнейший ИИ-ассет: обе стороны не меньше сторон родителя,
// пропорции совпадают с родительскими (кандидат на будущее ИИ-увеличение).
type AIAssetInfo struct {
	// Width — ширина ассета (px), > 0.
	Width int `json:"width"`
	// Height — высота ассета (px), > 0.
	Height int `json:"height"`
	// Format — выходной формат (jpeg|png|webp|gif|avif|heif|apng|jxl).
	Format string `json:"format"`
	// Key — канонический ключ ассета в result-store.
	Key string `json:"key"`
}

// FileMetadata — содержимое sidecar одного родительского файла.
type FileMetadata struct {
	// SchemaVersion — версия схемы. Новые объекты создаются с
	// CurrentSchemaVersion (см. NewFileMetadata).
	SchemaVersion int `json:"schema_version"`
	// Faces — боксы лиц в пикселях оригинала; nil = нет данных,
	// len == 0 после явной записи = «лиц нет».
	Faces []FaceInfo `json:"faces,omitempty"`
	// Objects — боксы объектов в пикселях оригинала.
	Objects []ObjectInfo `json:"objects,omitempty"`
	// LargestAIAsset — крупнейший ИИ-ассет; nil = ещё не зафиксирован.
	LargestAIAsset *AIAssetInfo `json:"largest_ai_asset,omitempty"`
	// VideoFrameKey — ключ основного кадра видео (файл x.jpg); пусто = ещё
	// не зафиксирован.
	VideoFrameKey string `json:"video_frame_key,omitempty"`
	// Detection — описание детектора, которым получены Faces/Objects;
	// nil = детекция выполнялась вне sidecar (или ещё не выполнялась).
	Detection *DetectionInfo `json:"detection,omitempty"`
	// Source — отпечаток источника, по которому выполнялась детекция;
	// nil = отпечаток неизвестен (кэш считается валидным — backward-compat
	// со sidecar, записанными до появления fingerprint).
	Source *SourceFingerprint `json:"source,omitempty"`
	// CreatedUnix — unix-время создания первого ассета (сек). 0 = ещё не
	// записано. Записывается лениво/асинхронно при первом создании ассета.
	CreatedUnix int64 `json:"created_unix,omitempty"`
	// CreatedAt — момент первой записи файла (UTC).
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt — момент последней успешной записи (UTC).
	UpdatedAt time.Time `json:"updated_at"`
}

// NewFileMetadata создаёт пустые метаданные с текущей версией схемы и
// UTC-временем создания/обновления.
func NewFileMetadata() *FileMetadata {
	now := time.Now().UTC()
	return &FileMetadata{
		SchemaVersion: CurrentSchemaVersion,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
}

// Validate проверяет инварианты метаданных:
//   - confidence каждого бокса в [0,1] (NaN/Inf отклоняются);
//   - координаты боксов >= 0, размеры > 0;
//   - размер каждого среза (Faces/Objects) <= MaxItemsPerSlice;
//   - LargestAIAsset (если задан): Width/Height > 0, Format и Key непусты.
func (m *FileMetadata) Validate() error {
	if m == nil {
		return fmt.Errorf("%w: nil metadata", ErrCorrupt)
	}
	if len(m.Faces) > MaxItemsPerSlice {
		return fmt.Errorf("%w: faces count %d exceeds limit %d", ErrCorrupt, len(m.Faces), MaxItemsPerSlice)
	}
	for i, f := range m.Faces {
		if err := validateBox(f.PixelBox, f.Confidence); err != nil {
			return fmt.Errorf("faces[%d]: %w", i, err)
		}
	}
	if len(m.Objects) > MaxItemsPerSlice {
		return fmt.Errorf("%w: objects count %d exceeds limit %d", ErrCorrupt, len(m.Objects), MaxItemsPerSlice)
	}
	for i, o := range m.Objects {
		if err := validateBox(o.PixelBox, o.Confidence); err != nil {
			return fmt.Errorf("objects[%d]: %w", i, err)
		}
	}
	if a := m.LargestAIAsset; a != nil {
		if a.Width <= 0 || a.Height <= 0 {
			return fmt.Errorf("%w: largest_ai_asset dimensions must be positive (got %dx%d)", ErrCorrupt, a.Width, a.Height)
		}
		if a.Format == "" {
			return fmt.Errorf("%w: largest_ai_asset.format is empty", ErrCorrupt)
		}
		if a.Key == "" {
			return fmt.Errorf("%w: largest_ai_asset.key is empty", ErrCorrupt)
		}
	}
	if s := m.Source; s != nil {
		if s.Size < 0 {
			return fmt.Errorf("%w: source.size must be >= 0 (got %d)", ErrCorrupt, s.Size)
		}
		if s.ModTimeUnix < 0 {
			return fmt.Errorf("%w: source.mod_time_unix must be >= 0 (got %d)", ErrCorrupt, s.ModTimeUnix)
		}
		if !isValidHex64(s.HashSHA256) {
			return fmt.Errorf("%w: source.hash_sha256 must be empty or 64 hex chars (got %q)", ErrCorrupt, s.HashSHA256)
		}
	}
	return nil
}

// isValidHex64 проверяет, что s — пустая строка либо ровно 64 hex-символа
// (lower/upper), т.е. hex-кодировка SHA-256.
func isValidHex64(s string) bool {
	if s == "" {
		return true
	}
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// validateBox проверяет инварианты одного бокса.
func validateBox(b PixelBox, confidence float64) error {
	if b.X < 0 || b.Y < 0 {
		return fmt.Errorf("%w: negative box origin (%d,%d)", ErrCorrupt, b.X, b.Y)
	}
	if b.Width <= 0 || b.Height <= 0 {
		return fmt.Errorf("%w: box dimensions must be positive (got %dx%d)", ErrCorrupt, b.Width, b.Height)
	}
	if math.IsNaN(confidence) || math.IsInf(confidence, 0) || confidence < 0 || confidence > 1 {
		return fmt.Errorf("%w: confidence %v outside [0,1]", ErrCorrupt, confidence)
	}
	return nil
}

// fileMetadataWire — промежуточная структура сериализации. Faces/Objects
// кодируются вручную: стандартный omitempty не различает nil («нет данных»)
// и пустой срез («проверено, пусто»), а схема требует оба случая:
// поле отсутствует ⇔ модели не запускались; "faces":[] ⇔ запускались, лиц нет.
type fileMetadataWire struct {
	SchemaVersion  int                `json:"schema_version"`
	Faces          json.RawMessage    `json:"faces,omitempty"`
	Objects        json.RawMessage    `json:"objects,omitempty"`
	LargestAIAsset *AIAssetInfo       `json:"largest_ai_asset,omitempty"`
	VideoFrameKey  string             `json:"video_frame_key,omitempty"`
	Detection      *DetectionInfo     `json:"detection,omitempty"`
	Source         *SourceFingerprint `json:"source,omitempty"`
	CreatedUnix    int64              `json:"created_unix,omitempty"`
	CreatedAt      time.Time          `json:"created_at"`
	UpdatedAt      time.Time          `json:"updated_at"`
}

// MarshalJSON сериализует метаданные: nil-срезы опускаются, пустые
// non-nil срезы записываются как явный пустой массив [].
func (m *FileMetadata) MarshalJSON() ([]byte, error) {
	if m == nil {
		return []byte("null"), nil
	}
	w := fileMetadataWire{
		SchemaVersion:  m.SchemaVersion,
		LargestAIAsset: m.LargestAIAsset,
		VideoFrameKey:  m.VideoFrameKey,
		Detection:      m.Detection,
		Source:         m.Source,
		CreatedUnix:    m.CreatedUnix,
		CreatedAt:      m.CreatedAt,
		UpdatedAt:      m.UpdatedAt,
	}
	var err error
	if w.Faces, err = marshalOptionalSlice(m.Faces); err != nil {
		return nil, err
	}
	if w.Objects, err = marshalOptionalSlice(m.Objects); err != nil {
		return nil, err
	}
	return json.Marshal(w)
}

// marshalOptionalSlice: nil → поле отсутствует (nil RawMessage),
// non-nil (включая len==0) → явный JSON-массив ("[]" для пустого).
func marshalOptionalSlice[T any](s []T) (json.RawMessage, error) {
	if s == nil {
		return nil, nil
	}
	b, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// UnmarshalJSON разбирает метаданные (отсутствующие поля остаются nil).
func (m *FileMetadata) UnmarshalJSON(data []byte) error {
	// Псевдоним типа разрывает рекурсию в MarshalJSON/UnmarshalJSON.
	type fileMetadataAlias FileMetadata
	var a fileMetadataAlias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*m = FileMetadata(a)
	return nil
}

// maxAspectDeviation — максимальное относительное отклонение пропорций
// ассета от пропорций родителя, при котором ассет считается кандидатом
// на largest_ai_asset.
const maxAspectDeviation = 0.01

// ShouldTrackAsAIAsset проверяет критерий кандидата на largest_ai_asset:
// ассет больше родителя по обеим сторонам ИЛИ по площади, а пропорции
// совпадают с родительскими с отклонением ≤1%.
//
//	кандидат ⇔ outW >= srcW ∧ outH >= srcH ∧ outW*outH > srcW*srcH
//	           ∧ |outW/outH − srcW/srcH| / (srcW/srcH) ≤ 0.01
//
// Нулевые/неположительные размеры или неизвестная площадь родителя
// возвращают false (обновление largest_ai_asset пропускается).
func ShouldTrackAsAIAsset(parentW, parentH, assetW, assetH int) bool {
	if parentW <= 0 || parentH <= 0 || assetW <= 0 || assetH <= 0 {
		return false
	}
	if assetW < parentW || assetH < parentH {
		return false
	}
	parentArea := float64(parentW) * float64(parentH)
	assetArea := float64(assetW) * float64(assetH)
	if assetArea <= parentArea {
		return false
	}
	parentAspect := float64(parentW) / float64(parentH)
	if parentAspect == 0 {
		return false
	}
	deviation := (float64(assetW)/float64(assetH) - parentAspect) / parentAspect
	if deviation < 0 {
		deviation = -deviation
	}
	return deviation <= maxAspectDeviation
}

// Clone возвращает глубокую копию метаданных (слайсы и указатель копируются),
// чтобы вызывающийся код не мог менять состояние, которое разделяется со store.
func (m *FileMetadata) Clone() *FileMetadata {
	if m == nil {
		return nil
	}
	out := *m
	if m.Faces != nil {
		out.Faces = make([]FaceInfo, len(m.Faces))
		copy(out.Faces, m.Faces)
	}
	if m.Objects != nil {
		out.Objects = make([]ObjectInfo, len(m.Objects))
		copy(out.Objects, m.Objects)
	}
	if m.LargestAIAsset != nil {
		a := *m.LargestAIAsset
		out.LargestAIAsset = &a
	}
	if m.Detection != nil {
		d := *m.Detection
		out.Detection = &d
	}
	if m.Source != nil {
		s := *m.Source
		out.Source = &s
	}
	return &out
}
