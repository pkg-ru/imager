package composition

import (
	"context"
	"fmt"
	"io"
	"os"
	"testing"

	"gitverse.ru/pkg-ru/imager/domain/filemeta"
	"gitverse.ru/pkg-ru/imager/ports/detector"
	"gitverse.ru/pkg-ru/imager/ports/processor"
)

// fakeProcessor — fake processor.Processor: копирует исходник в out.
type fakeProcessor struct{}

func (fakeProcessor) Process(ctx context.Context, in processor.Input, out io.Writer) (*processor.Result, error) {
	n, err := io.Copy(out, in.Source)
	if err != nil {
		return nil, err
	}
	return &processor.Result{Size: n}, nil
}

// fakeDetector — fake detector.Detector: всегда доступен, не находит лиц.
type fakeDetector struct{}

func (fakeDetector) DetectFaces(_ context.Context, _ []byte, _, _ int) ([]filemeta.FaceInfo, error) {
	return []filemeta.FaceInfo{}, nil
}

func (fakeDetector) DetectObjects(_ context.Context, _ []byte, _, _ int) ([]filemeta.ObjectInfo, error) {
	return []filemeta.ObjectInfo{}, nil
}

func (fakeDetector) Available() bool { return true }

func (fakeDetector) Describe() detector.DetectorInfo { return detector.DetectorInfo{Kind: "fake"} }

var _ detector.Detector = (*fakeDetector)(nil)

// captureLogger — логгер, собирающий warning'и (для проверки merge-конфликтов).
type captureLogger struct {
	warnings []string
}

func (c *captureLogger) Debugf(format string, args ...any) {}
func (c *captureLogger) Infof(format string, args ...any)  {}
func (c *captureLogger) Errorf(format string, args ...any) {}
func (c *captureLogger) Warnf(format string, args ...any) {
	c.warnings = append(c.warnings, sprintf(format, args...))
}

func sprintf(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}

// writeConfig записывает файл конфигурации.
func writeConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		(len(s) > 0 && indexOf(s, sub) >= 0))
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
