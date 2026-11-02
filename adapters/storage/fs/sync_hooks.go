package fs

import "os"

// Точки вызова durability-операций ResultStore. В проде это прямые вызовы
// os.File.Sync и fsyncDir; в тестах пакета подменяются счётчиками, чтобы
// проверить opt-out ResultStoreOptions.NoSync. Переопределяются только в
// тестах одного пакета (без параллельных подмен), поэтому гонок нет.
var (
	syncFile     = func(f *os.File) error { return f.Sync() }
	fsyncDirHook = fsyncDir
)
