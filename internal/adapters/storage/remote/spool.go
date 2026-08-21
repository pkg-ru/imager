package remote

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// ErrSpoolLimit — сигнал превышения лимита размера spool.
var ErrSpoolLimit = errors.New("spool size limit exceeded")

// SpoolOptions — параметры временного spool для source-потоков.
type SpoolOptions struct {
	// MaxBytes — максимальный размер spool в байтах (0 = без лимита).
	MaxBytes int64
	// Dir — каталог для временных файлов (пусто = os.TempDir).
	Dir string
}

// Spool — ограниченный временный файл, удовлетворяющий io.Reader,
// io.Seeker и io.Closer. Используется удалёнными source-адаптерами (S3,
// SFTP, FTP, FTPS), чтобы предоставить перематываемый поток исходного
// объекта, не удерживая его в памяти.
type Spool struct {
	f    *os.File
	size int64
}

// NewSpool создаёт пустой spool с опциями.
func NewSpool(opts SpoolOptions) (*Spool, error) {
	dir := opts.Dir
	if dir == "" {
		dir = os.TempDir()
	}
	f, err := os.CreateTemp(dir, "imager-spool-*")
	if err != nil {
		return nil, fmt.Errorf("remote: create spool: %w", err)
	}
	return &Spool{f: f}, nil
}

// WriteFrom копирует данные из r в spool, ограничивая размер MaxBytes.
// Возвращает ErrSpoolLimit при превышении лимита.
func (s *Spool) WriteFrom(r io.Reader, maxBytes int64) (int64, error) {
	if maxBytes > 0 {
		lr := &io.LimitedReader{R: r, N: maxBytes + 1}
		n, err := io.Copy(s.f, lr)
		if err != nil {
			return n, err
		}
		if n > maxBytes {
			return n, ErrSpoolLimit
		}
		s.size = n
		return n, nil
	}
	n, err := io.Copy(s.f, r)
	if err != nil {
		return n, err
	}
	s.size = n
	return n, nil
}

// Read реализует io.Reader.
func (s *Spool) Read(p []byte) (int, error) { return s.f.Read(p) }

// Seek реализует io.Seeker.
func (s *Spool) Seek(offset int64, whence int) (int64, error) {
	return s.f.Seek(offset, whence)
}

// Close закрывает и удаляет временный файл.
func (s *Spool) Close() error {
	name := s.f.Name()
	err := s.f.Close()
	_ = os.Remove(name)
	return err
}

// Size возвращает размер записанных данных.
func (s *Spool) Size() int64 { return s.size }

// File возвращает базовый файл (для тестов).
func (s *Spool) File() *os.File { return s.f }
