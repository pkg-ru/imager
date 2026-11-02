//go:build onnx && cgo

package detection

import "testing"

// TestAutodetectLogHelpers проверяет, что лог-обёртки не паникуют при
// nil-логгере (логирование опционально, SetLogger может не вызываться).
func TestAutodetectLogHelpers(t *testing.T) {
	defer SetLogger(nil) // восстановить глобальное состояние
	SetLogger(nil)
	logORTInfo("autodetect info: %s", "probe")
	logORTWarn("autodetect warn: %s", "probe")
	// С непустым логгером — заглушка фиксирует вызовы.
	fake := &fakeDetectionLogger{}
	SetLogger(fake)
	logORTInfo("info %d", 1)
	logORTWarn("warn %d", 2)
	if fake.infos != 1 || fake.warns != 1 {
		t.Errorf("logger calls: infos=%d warns=%d, want 1/1", fake.infos, fake.warns)
	}
}

// fakeDetectionLogger — заглушка Logger для тестов.
type fakeDetectionLogger struct {
	infos, warns int
}

func (f *fakeDetectionLogger) Infof(string, ...any) { f.infos++ }
func (f *fakeDetectionLogger) Warnf(string, ...any) { f.warns++ }
