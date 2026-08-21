// Package remote: spillable buffer для remote-хранилищ.
//
// Buffer — это ограниченный буфер, который сначала пишет данные в память
// процесса, а при превышении доступного остатка общего бюджета (BufferPool)
// сбрасывается (spill) во временный файл на диске. Обратно в память данные
// не перезагружаются: если буфер начал работать через файл, он так и
// работает до закрытия.
//
// Buffer удовлетворяет io.Reader, io.Seeker и io.Closer, поэтому может
// использоваться там, где раньше использовался Spool (временный файл),
// без изменения контрактов Artifact/Processor.
package remote

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/pkg-ru/imager/internal/application/ports/buffer"
)

// ErrBufferLimit — сигнал превышения лимита размера буфера.
var ErrBufferLimit = errors.New("buffer size limit exceeded")

// BufferOptions — параметры создания Buffer.
type BufferOptions struct {
	// Pool — общий пул памяти процесса. Если nil, буфер работает только
	// через память без spill (эквивалентно неограниченному in-memory).
	Pool *BufferPool
	// Dir — каталог для временных файлов при spill (пусто = os.TempDir).
	Dir string
	// MaxBytes — жёсткий лимит размера буфера (0 = без лимита).
	MaxBytes int64
}

// Buffer — spillable буфер: память до лимита, затем временный файл.
// Потокобезопасен для записи и чтения; поддерживает независимые reader'ы
// через NewReader.
//
// Жизненный цикл: владелец создаёт Buffer и вызывает Close, когда запись
// завершена. Ресурсы (память/файл) освобождаются, когда Close вызван И
// все reader'ы закрыты (reference counting). Это позволяет отдавать данные
// клиенту и публиковать в remote параллельно, не освобождая буфер
// преждевременно.
type Buffer struct {
	mu sync.Mutex

	pool *BufferPool
	dir  string

	// in-memory часть.
	mem []byte

	// файловая часть (после spill).
	f    *os.File
	size int64

	// файловый дескриптор для чтения (после spill), открывается лениво.
	rf *os.File

	// позиция чтения для io.Reader/io.Seeker интерфейса буфера.
	pos int64

	// жёсткий лимит (0 = без лимита).
	maxBytes int64

	// сколько байт зарезервировано в пуле.
	reserved int64

	// closed — запись завершена (владелец вызвал Close).
	closed bool
	// readers — число активных reader'ов.
	readers int
	// released — ресурсы освобождены.
	released bool
}

// NewBuffer создаёт пустой буфер.
func NewBuffer(opts BufferOptions) (*Buffer, error) {
	dir := opts.Dir
	if dir == "" {
		dir = os.TempDir()
	}
	return &Buffer{
		pool:     opts.Pool,
		dir:      dir,
		maxBytes: opts.MaxBytes,
	}, nil
}

// WriteFrom копирует данные из r в буфер, ограничивая размер MaxBytes.
// Возвращает ErrBufferLimit при превышении лимита.
func (b *Buffer) WriteFrom(r io.Reader, maxBytes int64) (int64, error) {
	if maxBytes > 0 {
		lr := &io.LimitedReader{R: r, N: maxBytes + 1}
		n, err := b.writeAll(lr)
		if err != nil {
			return n, err
		}
		if n > maxBytes {
			return n, ErrBufferLimit
		}
		return n, nil
	}
	return b.writeAll(r)
}

// Write реализует io.Writer: записывает p в буфер, спилля на диск при
// необходимости. Возвращает количество записанных байт.
func (b *Buffer) Write(p []byte) (int, error) {
	if err := b.write(p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// writeAll копирует все данные из r в буфер, спилля на диск при
// необходимости.
func (b *Buffer) writeAll(r io.Reader) (int64, error) {
	var buf [32 * 1024]byte
	var total int64
	for {
		n, err := r.Read(buf[:])
		if n > 0 {
			if err := b.write(buf[:n]); err != nil {
				return total, err
			}
			total += int64(n)
		}
		if err == io.EOF {
			return total, nil
		}
		if err != nil {
			return total, err
		}
	}
}

// write записывает p в буфер, спилля на диск при необходимости.
func (b *Buffer) write(p []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return errors.New("remote: buffer is closed")
	}
	// Если уже спиллен — пишем в файл.
	if b.f != nil {
		if _, err := b.f.Write(p); err != nil {
			return err
		}
		b.size += int64(len(p))
		return nil
	}

	// Пытаемся удержать в памяти: резервируем из пула.
	need := int64(len(p))
	if b.pool != nil {
		// Пробуем зарезервировать. Если не хватает — спилл.
		if !b.pool.tryReserve(need) {
			if err := b.spillLocked(); err != nil {
				return err
			}
			if _, err := b.f.Write(p); err != nil {
				return err
			}
			b.size += int64(len(p))
			return nil
		}
		b.reserved += need
	}
	b.mem = append(b.mem, p...)
	b.size += int64(len(p))
	return nil
}

// spillLocked сбрасывает накопленные данные в памяти на диск.
// Вызывается с удержанным b.mu.
func (b *Buffer) spillLocked() error {
	f, err := os.CreateTemp(b.dir, "imager-buffer-*")
	if err != nil {
		return fmt.Errorf("remote: create buffer file: %w", err)
	}
	if len(b.mem) > 0 {
		if _, err := f.Write(b.mem); err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
			return err
		}
	}
	// Освобождаем память и возвращаем резерв в пул.
	b.mem = nil
	if b.reserved > 0 && b.pool != nil {
		b.pool.release(b.reserved)
		b.reserved = 0
	}
	b.f = f
	return nil
}

// Read реализует io.Reader (читает с текущей позиции буфера).
func (b *Buffer) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.released {
		return 0, errors.New("remote: buffer is released")
	}
	if b.pos >= b.size {
		return 0, io.EOF
	}
	var n int
	if b.f != nil {
		// Читаем из файла, поддерживая позицию буфера.
		if b.rf == nil {
			rf, err := os.Open(b.f.Name())
			if err != nil {
				return 0, err
			}
			b.rf = rf
		}
		if _, err := b.rf.Seek(b.pos, io.SeekStart); err != nil {
			return 0, err
		}
		rn, err := b.rf.Read(p)
		if rn > 0 {
			b.pos += int64(rn)
		}
		return rn, err
	}
	// Читаем из памяти.
	avail := b.size - b.pos
	if int64(len(p)) < avail {
		avail = int64(len(p))
	}
	copy(p, b.mem[b.pos:b.pos+avail])
	b.pos += avail
	n = int(avail)
	return n, nil
}

// Seek реализует io.Seeker (для совместимости; позиция буфера).
func (b *Buffer) Seek(offset int64, whence int) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.released {
		return 0, errors.New("remote: buffer is released")
	}
	var np int64
	switch whence {
	case io.SeekStart:
		np = offset
	case io.SeekCurrent:
		np = b.pos + offset
	case io.SeekEnd:
		np = b.size + offset
	default:
		return 0, errors.New("remote: invalid whence")
	}
	if np < 0 {
		return 0, errors.New("remote: negative seek")
	}
	b.pos = np
	return np, nil
}

// NewReader создаёт независимый reader с собственной позицией чтения.
// Позволяет нескольким потребителям читать один и тот же буфер
// параллельно (например, отдача клиенту и publish в remote).
//
// Reader можно создать и после Close (запись завершена), пока ресурсы
// буфера не освобождены. Это позволяет каждому запросу singleflight
// получить собственный reader из общего буфера, даже если другой запрос
// уже закрыл свой reader (и тем самым пометил буфер closed).
func (b *Buffer) NewReader() (io.ReadSeekCloser, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.released {
		return nil, errors.New("remote: buffer is released")
	}
	b.readers++
	if b.f != nil {
		// Открываем собственный файловый дескриптор для независимого чтения.
		f, err := os.Open(b.f.Name())
		if err != nil {
			b.readers--
			return nil, err
		}
		return &BufferReader{buf: b, f: f, size: b.size}, nil
	}
	return &BufferReader{buf: b, mem: b.mem, size: b.size}, nil
}

// Close закрывает буфер для записи. Ресурсы (память/файл) освобождаются,
// когда Close вызван И все reader'ы закрыты.
func (b *Buffer) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	b.releaseIfDoneLocked()
	return nil
}

// releaseIfDoneLocked освобождает ресурсы, если запись завершена и нет
// активных reader'ов. Вызывается с удержанным b.mu.
func (b *Buffer) releaseIfDoneLocked() {
	if b.released || !b.closed || b.readers > 0 {
		return
	}
	b.released = true
	if b.f != nil {
		name := b.f.Name()
		_ = b.f.Close()
		_ = os.Remove(name)
		b.f = nil
	}
	if b.rf != nil {
		_ = b.rf.Close()
		b.rf = nil
	}
	b.mem = nil
	if b.reserved > 0 && b.pool != nil {
		b.pool.release(b.reserved)
		b.reserved = 0
	}
}

// Size возвращает размер записанных данных.
func (b *Buffer) Size() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.size
}

// File возвращает базовый файл (для тестов), или nil, если буфер в памяти.
func (b *Buffer) File() *os.File {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.f
}

// InMemory сообщает, находится ли буфер полностью в памяти.
func (b *Buffer) InMemory() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.f == nil
}

// BufferReader — независимый reader поверх Buffer с собственной позицией.
type BufferReader struct {
	buf  *Buffer
	f    *os.File // не nil, если буфер спиллен на диск
	mem  []byte   // снимок памяти, если буфер в памяти
	pos  int64
	size int64
}

// Read реализует io.Reader.
func (r *BufferReader) Read(p []byte) (int, error) {
	if r.f != nil {
		n, err := r.f.Read(p)
		if n > 0 {
			r.pos += int64(n)
		}
		return n, err
	}
	if r.pos >= int64(len(r.mem)) {
		return 0, io.EOF
	}
	n := copy(p, r.mem[r.pos:])
	r.pos += int64(n)
	return n, nil
}

// Seek реализует io.Seeker.
func (r *BufferReader) Seek(offset int64, whence int) (int64, error) {
	var base int64
	switch whence {
	case io.SeekStart:
		base = 0
	case io.SeekCurrent:
		base = r.pos
	case io.SeekEnd:
		base = r.size
	default:
		return 0, errors.New("remote: invalid whence")
	}
	np := base + offset
	if np < 0 {
		return 0, errors.New("remote: negative seek")
	}
	if r.f != nil {
		pos, err := r.f.Seek(np, io.SeekStart)
		if err != nil {
			return 0, err
		}
		r.pos = pos
		return pos, nil
	}
	r.pos = np
	return np, nil
}

// Close закрывает файловый дескриптор reader'а (если был) и уменьшает
// счётчик активных reader'ов буфера.
func (r *BufferReader) Close() error {
	var err error
	if r.f != nil {
		err = r.f.Close()
	}
	r.buf.mu.Lock()
	if r.buf.readers > 0 {
		r.buf.readers--
	}
	r.buf.releaseIfDoneLocked()
	r.buf.mu.Unlock()
	return err
}

// BufferPool — общий бюджет памяти процесса для spillable-буферов.
//
// Потокобезопасный учёт занятых байт. Буферы резервируют память из пула;
// если остатка не хватает, буфер спилляет на диск. Память возвращается в
// пул при Close.
type BufferPool struct {
	mu   sync.Mutex
	max  int64
	used int64
}

// NewBufferPool создаёт пул с бюджетом maxBytes (0 = без лимита).
func NewBufferPool(maxBytes int64) *BufferPool {
	return &BufferPool{max: maxBytes}
}

// Max возвращает максимальный бюджет пула.
func (p *BufferPool) Max() int64 { return p.max }

// Close сбрасывает учёт занятых байт. Используется при пересоздании
// приложения, чтобы старый пул не удерживал память (доп. замечание).
// Реализует io.Closer.
func (p *BufferPool) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.used = 0
	return nil
}

// Used возвращает текущее число занятых байт.
func (p *BufferPool) Used() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.used
}

// Available возвращает доступный остаток бюджета.
func (p *BufferPool) Available() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.max <= 0 {
		return -1 // без лимита
	}
	avail := p.max - p.used
	if avail < 0 {
		avail = 0
	}
	return avail
}

// tryReserve пытается зарезервировать n байт. Возвращает false, если
// бюджет исчерпан (или n превышает доступный остаток).
func (p *BufferPool) tryReserve(n int64) bool {
	if p == nil {
		return true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.max <= 0 {
		p.used += n
		return true
	}
	if p.used+n > p.max {
		return false
	}
	p.used += n
	return true
}

// release возвращает n байт в пул.
func (p *BufferPool) release(n int64) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.used -= n
	if p.used < 0 {
		p.used = 0
	}
}

// BufferFactory — адаптер buffer.Factory поверх BufferPool. Создаёт
// spillable-буферы, разделяющие общий бюджет памяти процесса.
type BufferFactory struct {
	pool *BufferPool
	dir  string
}

// NewBufferFactory создаёт фабрику буферов с общим пулом и каталогом
// временных файлов.
func NewBufferFactory(pool *BufferPool, dir string) *BufferFactory {
	return &BufferFactory{pool: pool, dir: dir}
}

// NewBuffer создаёт пустой spillable-буфер.
func (f *BufferFactory) NewBuffer() (buffer.Buffer, error) {
	return NewBuffer(BufferOptions{Pool: f.pool, Dir: f.dir})
}
