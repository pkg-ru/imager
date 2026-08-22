package asset

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// CanonicalID — стабильный канонический идентификатор asset URL.
//
// Идентификатор вычисляется из канонической формы URL (после нормализации)
// и не зависит от регистра форматов, порядка полей или дублирующих
// разделителей. Это гарантирует, что один и тот же ассет всегда имеет один
// и тот же cache identity.
type CanonicalID struct {
	// url — каноническая форма URL (без ведущего "/").
	url string
	// hash — SHA-256 канонической формы.
	hash string
}

// NewCanonicalID вычисляет CanonicalID из канонического URL.
func NewCanonicalID(url string) (CanonicalID, error) {
	if url == "" {
		return CanonicalID{}, fmt.Errorf("canonical id: empty url")
	}
	if len(url) > MaxURLLen {
		return CanonicalID{}, fmt.Errorf("canonical id: url length %d exceeds maximum %d", len(url), MaxURLLen)
	}
	sum := sha256.Sum256([]byte(url))
	return CanonicalID{url: url, hash: hex.EncodeToString(sum[:])}, nil
}

// URL возвращает каноническую форму URL.
func (id CanonicalID) URL() string { return id.url }

// Hash возвращает hex-представление SHA-256 канонической формы.
func (id CanonicalID) Hash() string { return id.hash }

// String возвращает hash (используется как cache key).
func (id CanonicalID) String() string { return id.hash }

// Equal сообщает, равен ли идентификатор другому.
func (id CanonicalID) Equal(other CanonicalID) bool {
	return id.hash == other.hash && id.url == other.url
}

// Canonicalizer нормализует компоненты URL в каноническую форму.
type Canonicalizer struct{}

// NewCanonicalizer создаёт Canonicalizer.
func NewCanonicalizer() *Canonicalizer { return &Canonicalizer{} }

// CanonicalPath нормализует путь: удаляет ведущие/хвостовые "/", схлопывает
// повторяющиеся "/", удаляет "." сегменты и запрещает ".." traversal.
func (c *Canonicalizer) CanonicalPath(p string) (string, error) {
	if p == "" {
		return "", nil
	}
	if strings.Contains(p, "..") {
		return "", fmt.Errorf("path contains traversal segment")
	}
	// Удаляем "." сегменты и схлопываем "/".
	segs := strings.Split(p, "/")
	out := make([]string, 0, len(segs))
	for _, s := range segs {
		switch s {
		case "", ".":
			continue
		case "..":
			return "", fmt.Errorf("path contains traversal segment")
		default:
			out = append(out, s)
		}
	}
	joined := strings.Join(out, "/")
	if len(joined) > MaxPathLen {
		return "", fmt.Errorf("path length %d exceeds maximum %d", len(joined), MaxPathLen)
	}
	return joined, nil
}

// CanonicalFormat нормализует формат в нижний регистр.
func (c *Canonicalizer) CanonicalFormat(f Format) Format {
	return Format(strings.ToLower(string(f)))
}

// CanonicalizeURL собирает канонический URL из компонентов запроса.
// Возвращает каноническую форму (без ведущего "/") и CanonicalID.
func (c *Canonicalizer) CanonicalizeURL(req *Request) (string, CanonicalID, error) {
	if req == nil {
		return "", CanonicalID{}, fmt.Errorf("canonicalize: nil request")
	}
	url, err := req.Build()
	if err != nil {
		return "", CanonicalID{}, err
	}
	id, err := NewCanonicalID(url)
	if err != nil {
		return "", CanonicalID{}, err
	}
	return url, id, nil
}
