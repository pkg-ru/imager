package generatev2

import (
	"errors"
	"io"
	"sync"
)

// memChunkSize — размер сегмента chunked-буфера. Сегментированный буфер
// избегает O(n²) при больших данных: append к одному растущему слайсу
// заменяется добавлением фиксированных сегментов.
const memChunkSize = 32 * 1024

// memBuffer — in-memory реализация buffer.Buffer, используемая, когда
// фабрика spillable-буферов не задана. Хранит данные в памяти процесса.
//
// Память освобождается через reference counting — только когда закрыты
// все reader'ы (и сам буфер). В cache stampede один буфер разделяется между
// несколькими запросами (каждый получает собственный reader), поэтому
// обнулять данные при первом же Close нельзя: это сломало бы остальных
// читателей. refs считает открытые reader'ы; данные обнуляются, когда refs
// достигает 0.
//
// Данные хранятся сегментами фиксированного размера (chunked list),
// а не одним растущим слайсом: это устраняет O(n²) при больших буферах.
type memBuffer struct {
	mu     sync.Mutex
	chunks [][]byte
	size   int
	pos    int
	refs   int
	closed bool
}

func (b *memBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	orig := len(p)
	for len(p) > 0 {
		if len(b.chunks) == 0 || len(b.chunks[len(b.chunks)-1]) == memChunkSize {
			b.chunks = append(b.chunks, make([]byte, 0, memChunkSize))
		}
		last := &b.chunks[len(b.chunks)-1]
		n := memChunkSize - len(*last)
		if n > len(p) {
			n = len(p)
		}
		*last = append(*last, p[:n]...)
		b.size += n
		p = p[n:]
	}
	return orig, nil
}

// readAt копирует данные из сегментов, начиная с позиции off, в p.
// Возвращает число прочитанных байт. Вызывается под mu.
func (b *memBuffer) readAt(p []byte, off int) int {
	if off >= b.size {
		return 0
	}
	total := 0
	// Находим сегмент и смещение внутри него.
	seg := off / memChunkSize
	segOff := off % memChunkSize
	for seg < len(b.chunks) && total < len(p) {
		chunk := b.chunks[seg]
		n := len(chunk) - segOff
		if n <= 0 {
			seg++
			segOff = 0
			continue
		}
		if n > len(p)-total {
			n = len(p) - total
		}
		copy(p[total:total+n], chunk[segOff:segOff+n])
		total += n
		seg++
		segOff = 0
	}
	return total
}

func (b *memBuffer) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pos >= b.size {
		return 0, io.EOF
	}
	n := b.readAt(p, b.pos)
	b.pos += n
	return n, nil
}

func (b *memBuffer) Seek(offset int64, whence int) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var np int64
	switch whence {
	case io.SeekStart:
		np = offset
	case io.SeekCurrent:
		np = int64(b.pos) + offset
	case io.SeekEnd:
		np = int64(b.size) + offset
	default:
		return 0, errors.New("invalid whence")
	}
	if np < 0 {
		return 0, errors.New("negative seek position")
	}
	b.pos = int(np)
	return np, nil
}

func (b *memBuffer) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	// Освобождаем память через reference counting — только когда закрыты
	// все reader'ы. Сам буфер помечается closed, но данные живут, пока есть
	// открытые reader'ы (cache burst: один буфер, много читателей).
	b.releaseLocked()
	return nil
}

// releaseLocked уменьшает счётчик reader'ов и освобождает память, когда
// буфер закрыт и не осталось открытых reader'ов. Вызывается под mu.
func (b *memBuffer) releaseLocked() {
	if b.closed && b.refs <= 0 {
		b.chunks = nil
		b.size = 0
		b.pos = 0
	}
}

func (b *memBuffer) Size() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return int64(b.size)
}

func (b *memBuffer) NewReader() (io.ReadSeekCloser, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	// Reader'ы учитываются reference counting. После закрытия буфера новые
	// reader'ы не создаются (данные могут быть освобождены).
	if b.closed {
		return nil, errors.New("buffer closed")
	}
	b.refs++
	return &memBufferReader{buf: b}, nil
}

// memBufferReader — независимый reader поверх memBuffer с собственной
// позицией чтения (для параллельного чтения клиентом и publish).
type memBufferReader struct {
	buf    *memBuffer
	pos    int
	closed bool
}

func (r *memBufferReader) Read(p []byte) (int, error) {
	r.buf.mu.Lock()
	defer r.buf.mu.Unlock()
	if r.pos >= r.buf.size {
		return 0, io.EOF
	}
	n := r.buf.readAt(p, r.pos)
	r.pos += n
	return n, nil
}

func (r *memBufferReader) Seek(offset int64, whence int) (int64, error) {
	r.buf.mu.Lock()
	defer r.buf.mu.Unlock()
	var np int64
	switch whence {
	case io.SeekStart:
		np = offset
	case io.SeekCurrent:
		np = int64(r.pos) + offset
	case io.SeekEnd:
		np = int64(r.buf.size) + offset
	default:
		return 0, errors.New("invalid whence")
	}
	if np < 0 {
		return 0, errors.New("negative seek position")
	}
	r.pos = int(np)
	return np, nil
}

func (r *memBufferReader) Close() error {
	r.buf.mu.Lock()
	defer r.buf.mu.Unlock()
	// Идемпотентность: повторный Close этого reader'а не должен
	// декрементировать refs повторно, иначе счётчик «украдёт» decrement
	// другого активного reader'а и память будет освобождена преждевременно.
	if r.closed {
		return nil
	}
	r.closed = true
	if r.buf.refs > 0 {
		r.buf.refs--
	}
	r.buf.releaseLocked()
	return nil
}
