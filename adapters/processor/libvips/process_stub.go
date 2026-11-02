//go:build !libvips

package libvips

import (
	"context"
	"fmt"

	"github.com/pkg-ru/imager/domain/filemeta"
	"github.com/pkg-ru/imager/domain/processing"
	"github.com/pkg-ru/imager/ports/processor"
)

func init() {
	// Связываем фабрику движков общего кода (processor.go) с заглушкой.
	newBackend = newStubBackend
}

// Compiled сообщает, скомпилирована ли реальная поддержка libvips (govips).
// Возвращает false в сборках без тэка "libvips".
func Compiled() bool { return false }

// stubBackend — заглушка движка, используемая, когда пакет собран БЕЗ тэка
// "libvips" (например, go vet / go test на Windows без установленного
// libvips, или напускаемая статическая сборка без cgo).
//
// New всегда успешен (чтобы composition root и тесты могли собирать
// Processor), а первый Process возвращает понятную ошибку о том, что
// поддержка libvips не скомпилирована.
type stubBackend struct{}

func newStubBackend(Options) (backend, error) {
	return &stubBackend{}, nil
}

func (s *stubBackend) process(_ context.Context, _ []byte, _ *processing.ProcessingPlan, _ bool, _ []filemeta.PixelBox, _ *gateSlot) (*backendResult, error) {
	return nil, fmt.Errorf("%w: process_libvips.go: libvips (govips) is not available in this build; rebuild with -tags libvips", ErrNotCompiled)
}

func (s *stubBackend) prepareRGB(_ context.Context, _ []byte) (*processor.RGBFrame, error) {
	return nil, fmt.Errorf("%w: libvips (govips) is not available in this build; rebuild with -tags libvips", ErrNotCompiled)
}

func (s *stubBackend) close() error { return nil }
