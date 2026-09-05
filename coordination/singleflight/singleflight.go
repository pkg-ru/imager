// Package singleflight реализует in-process keyed singleflight адаптер для
// контракта coordinator.Keyed.
//
// Адаптер не использует внешних зависимостей: он построен на стандартной
// библиотеке (sync, context). Гарантирует, что один и тот же ключ выполняется
// ровно один раз в рамках процесса; остальные вызовы с тем же ключом ожидают
// завершения и получают тот же результат (dedup).
//
// Для защиты от неограниченного роста памяти введены явные ограничения:
//   - MaxKeyLen — максимальная длина ключа (в байтах). Более длинные ключи
//     отклоняются ошибкой ErrKeyTooLong.
//   - MaxKeys — максимальное число одновременно отслеживаемых ключей
//     (cardinality). При превышении новый ключ отклоняется ошибкой
//     ErrTooManyKeys, чтобы не допустить неограниченного роста map.
package singleflight

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"gitverse.ru/pkg-ru/imager/domain/object"
	"gitverse.ru/pkg-ru/imager/ports/coordinator"
)

// Ограничения по умолчанию.
const (
	// DefaultMaxKeyLen — максимальная длина ключа в байтах.
	DefaultMaxKeyLen = 1024
	// DefaultMaxKeys — максимальное число одновременно отслеживаемых ключей.
	// Поднято с 4096 до 16384 — больше одновременных ключей
	// без отклонения ErrTooManyKeys при высокой кардинальности URL.
	DefaultMaxKeys = 16384
	// DefaultWaitTimeout — таймаут ожидания завершения владельца (0 = без
	// таймаута). Защита от "зависших" владельцев, которые не завершились
	// (например, паника до вызова unlock в Acquire).
	DefaultWaitTimeout = 0
)

// Ошибки ограничений.
var (
	// ErrKeyTooLong — ключ длиннее MaxKeyLen.
	ErrKeyTooLong = errors.New("singleflight: key too long")
	// ErrTooManyKeys — превышено максимальное число отслеживаемых ключей.
	ErrTooManyKeys = errors.New("singleflight: too many concurrent keys")
	// ErrWaitTimeout — превышен таймаут ожидания завершения владельца.
	ErrWaitTimeout = errors.New("singleflight: wait timeout")
)

// Options — параметры адаптера.
type Options struct {
	// MaxKeyLen — максимальная длина ключа (0 → DefaultMaxKeyLen).
	MaxKeyLen int
	// MaxKeys — максимальное число одновременно отслеживаемых ключей
	// (0 → DefaultMaxKeys).
	MaxKeys int
	// WaitTimeout — таймаут ожидания завершения владельца (0 → без таймаута).
	// Защита от "зависших" владельцев (например, паника до unlock в Acquire).
	WaitTimeout time.Duration
}

// Group — in-process keyed singleflight группа.
type Group struct {
	mu          sync.Mutex
	inflight    map[string]*call
	maxKeyLen   int
	maxKeys     int
	waitTimeout time.Duration
}

// call — один выполняющийся вызов для ключа.
type call struct {
	done chan struct{}
	val  any
	err  error
}

// New создаёт Group с заданными ограничениями.
func New(opts Options) *Group {
	maxKeyLen := opts.MaxKeyLen
	if maxKeyLen <= 0 {
		maxKeyLen = DefaultMaxKeyLen
	}
	maxKeys := opts.MaxKeys
	if maxKeys <= 0 {
		maxKeys = DefaultMaxKeys
	}
	waitTimeout := opts.WaitTimeout
	if waitTimeout < 0 {
		waitTimeout = DefaultWaitTimeout
	}
	return &Group{
		inflight:    make(map[string]*call),
		maxKeyLen:   maxKeyLen,
		maxKeys:     maxKeys,
		waitTimeout: waitTimeout,
	}
}

// Do выполняет fn ровно один раз для данного key. Если другой вызов с тем же
// key уже выполняется, текущий ожидает его завершения и получает тот же
// результат. fn не вызывается рекурсивно.
func (g *Group) Do(ctx context.Context, key object.ObjectKey, fn func() (any, error)) (any, error) {
	if len(key) > g.maxKeyLen {
		return nil, ErrKeyTooLong
	}

	g.mu.Lock()
	if c, ok := g.inflight[string(key)]; ok {
		g.mu.Unlock()
		return g.wait(ctx, c)
	}
	if len(g.inflight) >= g.maxKeys {
		g.mu.Unlock()
		return nil, ErrTooManyKeys
	}
	c := &call{done: make(chan struct{})}
	g.inflight[string(key)] = c
	g.mu.Unlock()

	// Выполняем fn вне блокировки. C4: паника в fn не должна блокировать
	// ключ навсегда — defer гарантирует удаление из map и close(done), а
	// recover сохраняет панику как ошибку.
	var val any
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("singleflight: panic in fn: %v", r)
			}
		}()
		val, err = fn()
	}()
	if err != nil {
		// Если fn завершился из-за отмены ctx, сохраняем ctx.Err() как
		// каноническую причину, чтобы ожидающие получили согласованную ошибку.
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = ctxErr
		}
	}
	c.val, c.err = val, err

	// Атомарно: close(done) и delete(inflight) под одним g.mu (C4). Это
	// устраняет гонку, когда wait() уже прочитал c.done, а новый Do с тем же
	// ключом видит ещё не удалённый call.
	g.mu.Lock()
	close(c.done)
	delete(g.inflight, string(key))
	g.mu.Unlock()

	return val, err
}

// wait ожидает завершения выполняющегося вызова c, уважая отмену ctx.
// Если задан WaitTimeout, ожидание ограничено таймаутом (защита от
// "зависших" владельцев, которые не завершились).
func (g *Group) wait(ctx context.Context, c *call) (any, error) {
	if g.waitTimeout > 0 {
		select {
		case <-c.done:
			return c.val, c.err
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(g.waitTimeout):
			return nil, ErrWaitTimeout
		}
	}
	select {
	case <-c.done:
		return c.val, c.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Acquire захватывает блокировку key и возвращает функцию освобождения.
// Если блокировка уже занята — блокируется до освобождения или отмены ctx.
//
// C4: ожидание выполняется итеративно (без рекурсии), а отмена ожидающего
// по ctx.Done() не оставляет "висячих" записей в map.
func (g *Group) Acquire(ctx context.Context, key object.ObjectKey) (coordinator.Unlock, error) {
	if len(key) > g.maxKeyLen {
		return nil, ErrKeyTooLong
	}

	for {
		g.mu.Lock()
		if c, ok := g.inflight[string(key)]; ok {
			g.mu.Unlock()
			if _, err := g.wait(ctx, c); err != nil {
				return nil, err
			}
			// После завершения выполняющегося вызова повторяем попытку
			// захвата (итеративно, без рекурсии).
			continue
		}
		if len(g.inflight) >= g.maxKeys {
			g.mu.Unlock()
			return nil, ErrTooManyKeys
		}
		c := &call{done: make(chan struct{})}
		g.inflight[string(key)] = c
		g.mu.Unlock()

		var once sync.Once
		return func() {
			once.Do(func() {
				g.mu.Lock()
				delete(g.inflight, string(key))
				close(c.done)
				g.mu.Unlock()
			})
		}, nil
	}
}

var _ coordinator.Keyed = (*Group)(nil)
