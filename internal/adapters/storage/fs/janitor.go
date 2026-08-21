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
// Реестр активных temp-файлов (active): Publish регистрирует создаваемый
// temp-путь через registerTemp и снимает регистрацию через unregisterTemp.
// CleanTemps не удаляет temp, который зарегистрирован на момент проверки,
// что исключает удаление активного temp текущей публикации (гонка К-2).
// Допустимая гонка: temp может быть зарегистрирован сразу после проверки
// и до удаления — это приемлемо, так как CleanTemps удаляет только файлы
// старше MaxAge, а свежезарегистрированный temp почти наверняка молодой.
type Janitor struct {
	root    string
	opts    JanitorOptions
	stop    chan struct{}
	stopped chan struct{}
	closed  bool

	// lifecycleMu защищает Start/Stop/close от гонок. Не удерживается во
	// время ожидания <-j.stopped, чтобы run мог закрыть канал.
	lifecycleMu sync.Mutex

	// activeMu защищает active (счётчик ссылок на активные temp-пути).
	activeMu sync.Mutex
	active   map[string]int
}

// NewJanitor создаёт Janitor для root.
func NewJanitor(root string, opts JanitorOptions) (*Janitor, error) {
	if root == "" {
		return nil, fmt.Errorf("fs: janitor: empty root")
	}
	return &Janitor{
		root:   filepath.Clean(root),
		opts:   opts,
		active: make(map[string]int),
	}, nil
}

// registerTemp регистрирует temp-путь как активный (инкремент счётчика).
// Вызывается Publish после создания temp-файла.
func (j *Janitor) registerTemp(path string) {
	j.activeMu.Lock()
	j.active[path] = j.active[path] + 1
	j.activeMu.Unlock()
}

// unregisterTemp снимает регистрацию temp-пути (декремент счётчика, удаляет
// запись при 0). Вызывается Publish после завершения работы с temp-файлом.
func (j *Janitor) unregisterTemp(path string) {
	j.activeMu.Lock()
	n := j.active[path]
	if n <= 1 {
		delete(j.active, path)
	} else {
		j.active[path] = n - 1
	}
	j.activeMu.Unlock()
}

// Start запускает периодический цикл. Повторный Start без Stop — ошибка.
func (j *Janitor) Start() error {
	j.lifecycleMu.Lock()
	defer j.lifecycleMu.Unlock()
	if j.closed {
		return fmt.Errorf("fs: janitor: already started/stopped")
	}
	if j.opts.Interval <= 0 {
		return nil // Interval=0: уборка отключена, но Start успешен.
	}
	j.stop = make(chan struct{})
	j.stopped = make(chan struct{})
	go j.run()
	return nil
}

// Stop останавливает цикл и ожидает завершения текущей уборки.
func (j *Janitor) Stop() {
	j.lifecycleMu.Lock()
	if j.stop == nil {
		j.closed = true
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
	j.closed = true
	j.stop = nil
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
		// Проверяем, не зарегистрирован ли temp как активный. Берём activeMu
		// только на время проверки, не держим его во время os.Remove (иначе
		// блокировка I/O). См. комментарий в описании типа.
		j.activeMu.Lock()
		active := j.active[path] > 0
		j.activeMu.Unlock()
		if active {
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
