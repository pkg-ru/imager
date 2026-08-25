package fs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// JanitorOptions — параметры периодической уборки temp-файлов.
type JanitorOptions struct {
	// Interval — период запуска уборки (0 = отключено).
	Interval time.Duration
	// MaxAge — возраст temp-файла, после которого он считается брошенным.
	MaxAge time.Duration
}

// Janitor — периодический сборщик мусора для временных файлов публикации
// (temp-файлы вида ".tmp-*" внутри root). Это best-effort утилита: она
// использует filepath.Walk и не блокирует другие операции; запускается в
// отдельной goroutine через Start/Stop.
//
// Жизненный цикл симметричен и повторно используем: каждый Start создаёт
// новые stop/stopped каналы и запускает горутину; Stop завершает её и
// закрывает stopped. После Stop можно снова вызвать Start. Stop без
// активного Start — no-op.
type Janitor struct {
	root    string
	opts    JanitorOptions
	stop    chan struct{}
	stopped chan struct{}

	// lifecycleMu защищает Start/Stop от гонок. Не удерживается во время
	// ожидания <-j.stopped, чтобы run мог закрыть канал.
	lifecycleMu sync.Mutex
}

// NewJanitor создаёт Janitor для root.
func NewJanitor(root string, opts JanitorOptions) (*Janitor, error) {
	if root == "" {
		return nil, fmt.Errorf("fs: janitor: empty root")
	}
	return &Janitor{
		root: filepath.Clean(root),
		opts: opts,
	}, nil
}

// Start запускает периодический цикл. Повторный Start без Stop — ошибка.
// При Interval <= 0 уборка отключена: Start успешен, но горутина не
// запускается (Stop остаётся no-op).
func (j *Janitor) Start() error {
	j.lifecycleMu.Lock()
	defer j.lifecycleMu.Unlock()
	if j.stop != nil {
		return fmt.Errorf("fs: janitor: already started")
	}
	if j.opts.Interval <= 0 {
		return nil // Interval=0: уборка отключена, но Start успешен.
	}
	j.stop = make(chan struct{})
	j.stopped = make(chan struct{})
	go j.run()
	return nil
}

// Stop останавливает цикл и ожидает завершения текущей уборки. Если цикл не
// запущен (Start не вызывался, Interval=0 или уже остановлен) — no-op.
func (j *Janitor) Stop() {
	j.lifecycleMu.Lock()
	if j.stop == nil {
		j.lifecycleMu.Unlock()
		return
	}
	stop := j.stop
	stopped := j.stopped
	// Освобождаем мьютекс перед ожиданием, чтобы run мог закрыть канал.
	j.lifecycleMu.Unlock()
	close(stop)
	<-stopped
	j.lifecycleMu.Lock()
	j.stop = nil
	j.stopped = nil
	j.lifecycleMu.Unlock()
}

func (j *Janitor) run() {
	defer close(j.stopped)
	ticker := time.NewTicker(j.opts.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-j.stop:
			return
		case <-ticker.C:
			_, _ = j.CleanTemps()
		}
	}
}

// CleanTemps удаляет temp-файлы (префикс ".tmp-") старше MaxAge. Возвращает
// число удалённых. Безопасно вызывать вручную для тестов и из janitor.
func (j *Janitor) CleanTemps() (int64, error) {
	if j.opts.MaxAge <= 0 {
		return 0, nil
	}
	cutoff := time.Now().Add(-j.opts.MaxAge)
	var removed int64
	err := filepath.Walk(j.root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		if !strings.HasPrefix(base, ".tmp-") {
			return nil
		}
		if info.ModTime().After(cutoff) || info.ModTime().Equal(cutoff) {
			return nil
		}
		if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
			return rmErr
		}
		removed++
		return nil
	})
	return removed, err
}
