package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gitverse.ru/pkg-ru/imager/domain/object"
)

// TestConnOptionsAttempts — число попыток нормализуется до >= 1.
func TestConnOptionsAttempts(t *testing.T) {
	tests := []struct {
		in   int
		want int
	}{
		{0, 1},
		{-5, 1},
		{1, 1},
		{3, 3},
	}
	for _, tt := range tests {
		if got := (ConnOptions{MaxAttempts: tt.in}).Attempts(); got != tt.want {
			t.Errorf("Attempts(%d) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

// TestConnOptionsOpTimeout — таймаут операции применяется только при
// положительном ReadTimeout; cancel всегда не nil.
func TestConnOptionsOpTimeout(t *testing.T) {
	// Без таймаута: контекст не меняется, deadline отсутствует.
	ctx, cancel := (ConnOptions{}).OpTimeout(context.Background())
	defer cancel()
	if _, ok := ctx.Deadline(); ok {
		t.Error("OpTimeout(0) added a deadline, want none")
	}

	// С таймаутом: deadline присутствует и примерно равен ReadTimeout.
	ctx2, cancel2 := (ConnOptions{ReadTimeout: time.Minute}).OpTimeout(context.Background())
	defer cancel2()
	dl, ok := ctx2.Deadline()
	if !ok {
		t.Fatal("OpTimeout(1m) has no deadline")
	}
	if d := time.Until(dl); d <= 0 || d > time.Minute {
		t.Errorf("deadline in %v, want (0, 1m]", d)
	}
}

// TestConnOptionsOrDefaults — нулевые значения подменяются дефолтами,
// положительные возвращаются как есть.
func TestConnOptionsOrDefaults(t *testing.T) {
	var zero ConnOptions
	if got := zero.DialTimeoutOrDefault(); got != DefaultDialTimeout {
		t.Errorf("DialTimeoutOrDefault() = %v, want %v", got, DefaultDialTimeout)
	}
	if got := zero.ReadTimeoutOrDefault(); got != DefaultReadTimeout {
		t.Errorf("ReadTimeoutOrDefault() = %v, want %v", got, DefaultReadTimeout)
	}
	if got := zero.MaxAttemptsOrDefault(); got != DefaultMaxAttempts {
		t.Errorf("MaxAttemptsOrDefault() = %d, want %d", got, DefaultMaxAttempts)
	}
	if got := zero.MaxIdleConnsOrDefault(); got != DefaultMaxIdleConns {
		t.Errorf("MaxIdleConnsOrDefault() = %d, want %d", got, DefaultMaxIdleConns)
	}
	if got := zero.MaxIdleConnsPerHostOrDefault(); got != DefaultMaxIdleConnsHost {
		t.Errorf("MaxIdleConnsPerHostOrDefault() = %d, want %d", got, DefaultMaxIdleConnsHost)
	}
	if got := zero.IdleConnTimeoutOrDefault(); got != DefaultIdleConnTimeout {
		t.Errorf("IdleConnTimeoutOrDefault() = %v, want %v", got, DefaultIdleConnTimeout)
	}

	custom := ConnOptions{
		DialTimeout:         time.Second,
		ReadTimeout:         2 * time.Second,
		MaxAttempts:         7,
		MaxIdleConns:        11,
		MaxIdleConnsPerHost: 3,
		IdleConnTimeout:     4 * time.Second,
	}
	if got := custom.DialTimeoutOrDefault(); got != time.Second {
		t.Errorf("DialTimeoutOrDefault() = %v, want 1s", got)
	}
	if got := custom.ReadTimeoutOrDefault(); got != 2*time.Second {
		t.Errorf("ReadTimeoutOrDefault() = %v, want 2s", got)
	}
	if got := custom.MaxAttemptsOrDefault(); got != 7 {
		t.Errorf("MaxAttemptsOrDefault() = %d, want 7", got)
	}
	if got := custom.MaxIdleConnsOrDefault(); got != 11 {
		t.Errorf("MaxIdleConnsOrDefault() = %d, want 11", got)
	}
	if got := custom.MaxIdleConnsPerHostOrDefault(); got != 3 {
		t.Errorf("MaxIdleConnsPerHostOrDefault() = %d, want 3", got)
	}
	if got := custom.IdleConnTimeoutOrDefault(); got != 4*time.Second {
		t.Errorf("IdleConnTimeoutOrDefault() = %v, want 4s", got)
	}
}

// TestIsConnErr — типизированные бизнес-ошибки (включая обёрнутые) не
// считаются ошибками соединения; всё остальное — считается.
func TestIsConnErr(t *testing.T) {
	business := []error{
		object.ErrNotFound,
		object.ErrConflict,
		object.ErrQuota,
		object.ErrUnsafePath,
		object.ErrUnavailable,
		object.ErrForbidden,
		&object.NotFoundError{Key: "k"},
		&object.ConflictError{Key: "k"},
		&object.QuotaError{Reason: "bytes"},
		fmt.Errorf("wrapped: %w", object.ErrForbidden),
	}
	for _, err := range business {
		if IsConnErr(err) {
			t.Errorf("IsConnErr(%v) = true, want false", err)
		}
	}
	if IsConnErr(nil) {
		t.Error("IsConnErr(nil) = true, want false")
	}
	if !IsConnErr(errors.New("io timeout")) {
		t.Error("IsConnErr(generic) = false, want true")
	}
}

// TestBufferArtifact — object.Artifact поверх Buffer: чтение, seek,
// метаданные, путь и закрытие делегируются буферу.
func TestBufferArtifact(t *testing.T) {
	b, err := NewBuffer(BufferOptions{})
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	if _, err := b.Write([]byte("hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	meta := object.ObjectMetadata{Key: "k", Size: 5, ContentType: "text/plain"}
	a := NewBufferArtifact(b, meta)

	if got := a.Metadata(); got.Key != "k" || got.Size != 5 || got.ContentType != "text/plain" {
		t.Errorf("Metadata() = %+v", got)
	}
	if p, ok := a.(interface{ Path() string }); !ok {
		t.Fatal("artifact does not expose Path()")
	} else if p.Path() != "" {
		t.Errorf("Path() = %q, want empty for in-memory buffer", p.Path())
	}
	if _, err := a.Seek(1, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	got, err := io.ReadAll(a)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "ello" {
		t.Errorf("read = %q, want %q", got, "ello")
	}
	if err := a.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// closerFunc — io.Closer на основе функции.
type closerFunc func() error

func (f closerFunc) Close() error { return f() }

// TestStreamArtifact — object.Stream поверх raw-потока: чтение, метаданные,
// закрытие (включая nil-closer).
func TestStreamArtifact(t *testing.T) {
	var closed atomic.Bool
	meta := object.ObjectMetadata{Key: "k", Size: 3}
	s := NewStreamArtifact(strings.NewReader("abc"), closerFunc(func() error {
		closed.Store(true)
		return nil
	}), meta)

	if got := s.Metadata(); got.Key != "k" || got.Size != 3 {
		t.Errorf("Metadata() = %+v", got)
	}
	got, err := io.ReadAll(s)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "abc" {
		t.Errorf("read = %q, want abc", got)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !closed.Load() {
		t.Error("Close did not call underlying closer")
	}

	// nil-closer: Close возвращает nil и не паникует.
	noCloser := NewStreamArtifact(strings.NewReader("x"), nil, object.ObjectMetadata{})
	if err := noCloser.Close(); err != nil {
		t.Errorf("Close(nil closer) = %v, want nil", err)
	}
}

// TestPoolAcquireDiscard — пул выдаёт соединение, Discard закрывает его и
// освобождает слот; повторный Discard идемпотентен.
func TestPoolAcquireDiscard(t *testing.T) {
	var dials, closes int
	p := NewPool(
		func(context.Context) (int, error) {
			dials++
			return dials, nil
		},
		func(int) error {
			closes++
			return nil
		},
		2,
	)
	ctx := context.Background()
	e, err := p.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if e.Value != 1 {
		t.Errorf("Value = %d, want 1", e.Value)
	}
	e.Discard()
	e.Discard() // идемпотентно
	if closes != 1 {
		t.Errorf("closes = %d, want 1", closes)
	}

	// nil-Entry безопасен.
	var nilEntry *Entry[int]
	nilEntry.Discard()
	// Entry без пула: ничего не закрывает и не падает.
	(&Entry[int]{}).Discard()
}

// TestPoolLimitAndContext — лимит одновременных соединений соблюдается;
// ожидание слота прерывается отменой ctx.
func TestPoolLimitAndContext(t *testing.T) {
	p := NewPool(
		func(context.Context) (int, error) { return 0, nil },
		func(int) error { return nil },
		1, // клампится до 2
	)
	ctx := context.Background()
	e1, err := p.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire #1: %v", err)
	}
	e2, err := p.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire #2: %v", err)
	}

	// Третий слот занят: Acquire ждёт до отмены ctx.
	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := p.Acquire(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Acquire #3 err = %v, want DeadlineExceeded", err)
	}

	// После освобождения слота Acquire снова работает.
	e1.Discard()
	if _, err := p.Acquire(ctx); err != nil {
		t.Fatalf("Acquire after Discard: %v", err)
	}
	e2.Discard()
}

// TestPoolDialErrorReleasesSlot — ошибка dial освобождает захваченный слот,
// поэтому следующая попытка не блокируется.
func TestPoolDialErrorReleasesSlot(t *testing.T) {
	dialErr := errors.New("dial failed")
	var calls int
	p := NewPool(
		func(context.Context) (int, error) {
			calls++
			if calls == 1 {
				return 0, dialErr
			}
			return calls, nil
		},
		func(int) error { return nil },
		2,
	)
	ctx := context.Background()
	if _, err := p.Acquire(ctx); !errors.Is(err, dialErr) {
		t.Fatalf("Acquire err = %v, want dialErr", err)
	}
	e, err := p.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire after dial error: %v", err)
	}
	e.Discard()
}

// TestSessionDiscard — Session закрывает соединение ровно один раз;
// пуловая и прямая сессии ведут себя одинаково.
func TestSessionDiscard(t *testing.T) {
	var closes int
	e := &Entry[int]{Value: 1, pool: NewPool(
		func(context.Context) (int, error) { return 1, nil },
		func(int) error { closes++; return nil },
		2,
	)}
	// Захватываем слот, чтобы Entry.Discard корректно его освободил.
	e.pool.slots <- struct{}{}
	s := NewSession(e)
	s.Discard()
	s.Discard()
	if closes != 1 {
		t.Errorf("pooled session closes = %d, want 1", closes)
	}

	// nil-Session безопасна.
	var nilSession *Session[int]
	nilSession.Discard()

	// Прямая сессия: closeFn вызывается один раз; nil closeFn — no-op.
	var directCloses int
	d := NewDirectSession(42, func(int) error { directCloses++; return nil })
	d.Discard()
	d.Discard()
	if directCloses != 1 {
		t.Errorf("direct session closes = %d, want 1", directCloses)
	}
	NewDirectSession(1, nil).Discard()
}

// TestStoreValidationAndAcquire — NewStore валидирует опции; пуловой и
// прямой пути Acquire возвращают рабочие сессии.
func TestStoreValidationAndAcquire(t *testing.T) {
	validateErr := errors.New("bad options")
	if _, err := NewStore[int, int](0, func(int) error { return validateErr }, true, nil, nil, 2); !errors.Is(err, validateErr) {
		t.Fatalf("NewStore err = %v, want validateErr", err)
	}

	var closes int
	dial := func(context.Context) (string, error) { return "conn", nil }
	closeFn := func(string) error { closes++; return nil }

	// Пуловой путь.
	pooled, err := NewStore[string, int](0, func(int) error { return nil }, true, dial, closeFn, 2)
	if err != nil {
		t.Fatalf("NewStore(pool): %v", err)
	}
	s, err := pooled.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire(pool): %v", err)
	}
	if s.Value != "conn" {
		t.Errorf("Value = %q, want conn", s.Value)
	}
	s.Discard()
	if closes != 1 {
		t.Errorf("closes = %d, want 1", closes)
	}

	// Прямой путь (usePool=false).
	direct, err := NewStore[string, int](0, func(int) error { return nil }, false, dial, closeFn, 2)
	if err != nil {
		t.Fatalf("NewStore(direct): %v", err)
	}
	s2, err := direct.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire(direct): %v", err)
	}
	s2.Discard()
	if closes != 2 {
		t.Errorf("closes = %d, want 2", closes)
	}

	// NewStoreDirect — без валидации и пула.
	d := NewStoreDirect(7, dial, closeFn)
	if d.Opts != 7 {
		t.Errorf("Opts = %d, want 7", d.Opts)
	}
	s3, err := d.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire(NewStoreDirect): %v", err)
	}
	s3.Discard()
	if closes != 3 {
		t.Errorf("closes = %d, want 3", closes)
	}
}

// TestStoreAcquireDialError — ошибка dial пробрасывается без сессии.
func TestStoreAcquireDialError(t *testing.T) {
	want := errors.New("dial")
	s := NewStoreDirect(0, func(context.Context) (int, error) { return 0, want }, nil)
	if _, err := s.Acquire(context.Background()); !errors.Is(err, want) {
		t.Fatalf("Acquire err = %v, want %v", err, want)
	}

	pooled, err := NewStore[int, int](0, func(int) error { return nil }, true,
		func(context.Context) (int, error) { return 0, want }, nil, 2)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := pooled.Acquire(context.Background()); !errors.Is(err, want) {
		t.Fatalf("pooled Acquire err = %v, want %v", err, want)
	}
}

// TestPooledDiscardKeepAlive — Pooled делегирует Discard сессии, а KeepAlive
// вызывает колбэк передачи владения.
func TestPooledDiscardKeepAlive(t *testing.T) {
	var closes, keeps int
	s := NewDirectSession(1, func(int) error { closes++; return nil })
	p := &Pooled[int]{Value: 1, Session: s, keep: func() { keeps++ }}
	p.KeepAlive()
	if keeps != 1 {
		t.Errorf("keeps = %d, want 1", keeps)
	}
	p.Discard()
	if closes != 1 {
		t.Errorf("closes = %d, want 1", closes)
	}
}

// TestRetrySuccess — успешная попытка возвращает значение и освобождает
// соединение.
func TestRetrySuccess(t *testing.T) {
	var discards int
	spec := RetrySpec[int, string]{
		Acquire:    func(context.Context) (int, error) { return 1, nil },
		Discard:    func(int) { discards++ },
		Policy:     NeverRetry,
		MapDialErr: func(err error) error { return err },
	}
	got, err := Retry(context.Background(), ConnOptions{MaxAttempts: 3}, spec,
		func(c int) (string, error, error) { return "ok", nil, nil })
	if err != nil {
		t.Fatalf("Retry err = %v", err)
	}
	if got != "ok" {
		t.Errorf("got = %q, want ok", got)
	}
	if discards != 1 {
		t.Errorf("discards = %d, want 1", discards)
	}
}

// TestRetryRetriesThenSucceeds — политика разрешает повтор: соединение
// пересоздаётся, вторая попытка успешна.
func TestRetryRetriesThenSucceeds(t *testing.T) {
	var acquires, discards, ops int
	connErr := errors.New("conn")
	mapped := errors.New("mapped")
	spec := RetrySpec[int, int]{
		Acquire:    func(context.Context) (int, error) { acquires++; return acquires, nil },
		Discard:    func(int) { discards++ },
		Policy:     func(error) bool { return true },
		MapDialErr: func(err error) error { return err },
	}
	got, err := Retry(context.Background(), ConnOptions{MaxAttempts: 3}, spec,
		func(c int) (int, error, error) {
			ops++
			if ops == 1 {
				return 0, connErr, mapped
			}
			return c, nil, nil
		})
	if err != nil {
		t.Fatalf("Retry err = %v", err)
	}
	if got != 2 {
		t.Errorf("got = %d, want 2 (second connection)", got)
	}
	if acquires != 2 || ops != 2 {
		t.Errorf("acquires=%d ops=%d, want 2/2", acquires, ops)
	}
	// Первая (неудачная) и вторая (успешная) попытки освобождают соединение.
	if discards != 2 {
		t.Errorf("discards = %d, want 2", discards)
	}
}

// TestRetryNonRetryable — ошибка вне политики возвращается сразу, без
// повторных попыток.
func TestRetryNonRetryable(t *testing.T) {
	mapped := errors.New("business")
	var ops, discards int
	spec := RetrySpec[int, int]{
		Acquire:    func(context.Context) (int, error) { return 1, nil },
		Discard:    func(int) { discards++ },
		Policy:     NeverRetry,
		MapDialErr: func(err error) error { return err },
	}
	_, err := Retry(context.Background(), ConnOptions{MaxAttempts: 5}, spec,
		func(int) (int, error, error) {
			ops++
			return 0, errors.New("raw"), mapped
		})
	if !errors.Is(err, mapped) {
		t.Fatalf("err = %v, want mapped", err)
	}
	if ops != 1 {
		t.Errorf("ops = %d, want 1", ops)
	}
	if discards != 1 {
		t.Errorf("discards = %d, want 1", discards)
	}
}

// TestRetryDialError — ошибка Acquire маппится и завершает цикл без op.
func TestRetryDialError(t *testing.T) {
	dialErr := errors.New("dial")
	mapped := errors.New("mapped dial")
	var ops int
	spec := RetrySpec[int, int]{
		Acquire:    func(context.Context) (int, error) { return 0, dialErr },
		Discard:    func(int) {},
		Policy:     func(error) bool { return true },
		MapDialErr: func(err error) error { return fmt.Errorf("%w: %v", mapped, err) },
	}
	_, err := Retry(context.Background(), ConnOptions{MaxAttempts: 3}, spec,
		func(int) (int, error, error) { ops++; return 0, nil, nil })
	if !errors.Is(err, mapped) {
		t.Fatalf("err = %v, want mapped", err)
	}
	if ops != 0 {
		t.Errorf("ops = %d, want 0", ops)
	}
}

// TestRetryContextCanceled — отмена ctx после ошибки соединения прекращает
// повторы.
func TestRetryContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	mapped := errors.New("mapped")
	var ops int
	spec := RetrySpec[int, int]{
		Acquire:    func(context.Context) (int, error) { return 1, nil },
		Discard:    func(int) {},
		Policy:     func(error) bool { return true },
		MapDialErr: func(err error) error { return err },
	}
	_, err := Retry(ctx, ConnOptions{MaxAttempts: 5}, spec,
		func(int) (int, error, error) {
			ops++
			cancel()
			return 0, errors.New("raw"), mapped
		})
	if !errors.Is(err, mapped) {
		t.Fatalf("err = %v, want mapped", err)
	}
	if ops != 1 {
		t.Errorf("ops = %d, want 1 (ctx canceled)", ops)
	}
}

// TestRetryExhausted — после исчерпания попыток возвращается последняя
// mapped-ошибка.
func TestRetryExhausted(t *testing.T) {
	var ops int
	spec := RetrySpec[int, int]{
		Acquire:    func(context.Context) (int, error) { return 1, nil },
		Discard:    func(int) {},
		Policy:     func(error) bool { return true },
		MapDialErr: func(err error) error { return err },
	}
	_, err := Retry(context.Background(), ConnOptions{MaxAttempts: 3}, spec,
		func(int) (int, error, error) {
			ops++
			return 0, errors.New("raw"), fmt.Errorf("mapped %d", ops)
		})
	if err == nil || err.Error() != "mapped 3" {
		t.Fatalf("err = %v, want last mapped (mapped 3)", err)
	}
	if ops != 3 {
		t.Errorf("ops = %d, want 3", ops)
	}
}

// TestRetryTakesOwnership — при передаче владения соединение НЕ закрывается
// каркасом (это делает владелец результата).
func TestRetryTakesOwnership(t *testing.T) {
	var discards int
	spec := RetrySpec[int, bool]{
		Acquire:        func(context.Context) (int, error) { return 1, nil },
		Discard:        func(int) { discards++ },
		Policy:         NeverRetry,
		MapDialErr:     func(err error) error { return err },
		TakesOwnership: func(bool) bool { return true },
	}
	got, err := Retry(context.Background(), ConnOptions{}, spec,
		func(int) (bool, error, error) { return true, nil, nil })
	if err != nil || !got {
		t.Fatalf("Retry = %v, %v; want true, nil", got, err)
	}
	if discards != 0 {
		t.Errorf("discards = %d, want 0 (ownership transferred)", discards)
	}

	// nil TakesOwnership трактуется как false → соединение закрывается.
	spec.TakesOwnership = nil
	if _, err := Retry(context.Background(), ConnOptions{}, spec,
		func(int) (bool, error, error) { return true, nil, nil }); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if discards != 1 {
		t.Errorf("discards = %d, want 1", discards)
	}
}

// TestWithRetry — обёртка над Retry: успех без KeepAlive закрывает сессию,
// с KeepAlive — передаёт владение.
func TestWithRetry(t *testing.T) {
	var closes int
	acquire := func(context.Context) (*Session[int], error) {
		return NewDirectSession(1, func(int) error { closes++; return nil }), nil
	}
	mapDial := func(err error) error { return err }

	// Без KeepAlive: сессия закрывается каркасом.
	if _, err := WithRetry(context.Background(), ConnOptions{},
		acquire, mapDial, NeverRetry,
		func(c *Pooled[int]) (int, error, error) { return c.Value, nil, nil }); err != nil {
		t.Fatalf("WithRetry: %v", err)
	}
	if closes != 1 {
		t.Errorf("closes = %d, want 1", closes)
	}

	// С KeepAlive: каркас не закрывает.
	if _, err := WithRetry(context.Background(), ConnOptions{},
		acquire, mapDial, NeverRetry,
		func(c *Pooled[int]) (int, error, error) {
			c.KeepAlive()
			return c.Value, nil, nil
		}); err != nil {
		t.Fatalf("WithRetry(KeepAlive): %v", err)
	}
	if closes != 1 {
		t.Errorf("closes = %d, want 1 (KeepAlive must not close)", closes)
	}
}

// TestBufferWriteFrom — WriteFrom копирует данные и соблюдает лимит.
func TestBufferWriteFrom(t *testing.T) {
	// Без лимита.
	b, err := NewBuffer(BufferOptions{})
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	defer func() { _ = b.Close() }()
	n, err := b.WriteFrom(strings.NewReader("abcdef"), 0)
	if err != nil {
		t.Fatalf("WriteFrom: %v", err)
	}
	if n != 6 || b.Size() != 6 {
		t.Errorf("n=%d Size=%d, want 6/6", n, b.Size())
	}

	// Лимит превышен: ErrBufferLimit.
	b2, err := NewBuffer(BufferOptions{})
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	defer func() { _ = b2.Close() }()
	if _, err := b2.WriteFrom(strings.NewReader("abcdef"), 3); !errors.Is(err, ErrBufferLimit) {
		t.Fatalf("WriteFrom(limit=3) err = %v, want ErrBufferLimit", err)
	}

	// Ровно в лимит — успех.
	b3, err := NewBuffer(BufferOptions{})
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	defer func() { _ = b3.Close() }()
	if n, err := b3.WriteFrom(strings.NewReader("abc"), 3); err != nil || n != 3 {
		t.Fatalf("WriteFrom(limit=3, exact) = %d, %v; want 3, nil", n, err)
	}
}

// TestBufferClosedWriteAndRead — запись после Close запрещена; чтение
// освобождённого буфера возвращает ошибку.
func TestBufferClosedWriteAndRead(t *testing.T) {
	b, err := NewBuffer(BufferOptions{})
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	if _, err := b.Write([]byte("x")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := b.Write([]byte("y")); err == nil {
		t.Fatal("Write after Close = nil, want error")
	}
	if _, err := b.Read(make([]byte, 1)); err == nil {
		t.Fatal("Read after Close = nil, want error (released)")
	}
	// Close идемпотентен.
	if err := b.Close(); err != nil {
		t.Errorf("second Close = %v, want nil", err)
	}
}

// TestBufferPoolSpill — превышение бюджета пула спилит буфер на диск;
// после Close временный файл удаляется.
func TestBufferPoolSpill(t *testing.T) {
	pool := NewBufferPool(4)
	if pool.Max() != 4 {
		t.Errorf("Max() = %d, want 4", pool.Max())
	}
	if pool.Available() != 4 {
		t.Errorf("Available() = %d, want 4", pool.Available())
	}
	b, err := NewBuffer(BufferOptions{Pool: pool, Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	if _, err := b.Write([]byte("123456")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if b.InMemory() {
		t.Fatal("InMemory() = true after exceeding pool budget, want false")
	}
	path := b.Path()
	if path == "" {
		t.Fatal("Path() empty for spilled buffer")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("spill file stat: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("spill file still exists after Close: %v", err)
	}
	if pool.Used() != 0 {
		t.Errorf("Used() = %d after Close, want 0", pool.Used())
	}
}

// TestBufferPoolHelpers — nil-pool безопасен; Available отражает лимит;
// Close идемпотентен.
func TestBufferPoolHelpers(t *testing.T) {
	var nilPool *BufferPool
	if nilPool.Max() != 0 || nilPool.Used() != 0 || nilPool.Available() != -1 {
		t.Errorf("nil pool: Max=%d Used=%d Available=%d", nilPool.Max(), nilPool.Used(), nilPool.Available())
	}
	if err := nilPool.Close(); err != nil {
		t.Errorf("nil pool Close = %v", err)
	}

	unlimited := NewBufferPool(0)
	if unlimited.Available() != -1 {
		t.Errorf("unlimited Available() = %d, want -1", unlimited.Available())
	}
	if !unlimited.tryReserve(10) || unlimited.Used() != 10 {
		t.Errorf("tryReserve(10) failed or Used=%d", unlimited.Used())
	}
	unlimited.release(10)
	if unlimited.Used() != 0 {
		t.Errorf("Used after release = %d, want 0", unlimited.Used())
	}

	// Двойной release не уводит счётчик в минус.
	unlimited.release(5)
	if unlimited.Used() != 0 {
		t.Errorf("Used after extra release = %d, want 0", unlimited.Used())
	}

	limited := NewBufferPool(5)
	if limited.tryReserve(6) {
		t.Error("tryReserve(6) with max=5 = true, want false")
	}
	if limited.Available() != 5 {
		t.Errorf("Available() = %d, want 5", limited.Available())
	}
	if err := limited.Close(); err != nil {
		t.Errorf("Close = %v", err)
	}
}

// TestBufferFactory — фабрика создаёт буфер с общим пулом.
func TestBufferFactory(t *testing.T) {
	pool := NewBufferPool(0)
	f := NewBufferFactory(pool, t.TempDir())
	b, err := f.NewBuffer()
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	if _, err := b.Write([]byte("data")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// TestBufferReaderInvalidSeek — отрицательная позиция и неизвестный whence
// отклоняются.
func TestBufferReaderInvalidSeek(t *testing.T) {
	b, err := NewBuffer(BufferOptions{})
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	defer func() { _ = b.Close() }()
	if _, err := b.Write([]byte("abc")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	r, err := b.NewReader()
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer func() { _ = r.Close() }()

	if _, err := r.Seek(-1, io.SeekStart); err == nil {
		t.Error("Seek(-1, Start) = nil, want error")
	}
	if _, err := r.Seek(0, 42); err == nil {
		t.Error("Seek(unknown whence) = nil, want error")
	}
	// Валидный seek от текущей позиции.
	if pos, err := r.Seek(2, io.SeekStart); err != nil || pos != 2 {
		t.Fatalf("Seek(2, Start) = %d, %v; want 2, nil", pos, err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "c" {
		t.Errorf("read = %q, want c", got)
	}
}

// TestBufferWriteReaderError — ошибка источника в writeAll пробрасывается.
func TestBufferWriteReaderError(t *testing.T) {
	b, err := NewBuffer(BufferOptions{})
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	defer func() { _ = b.Close() }()
	want := errors.New("read failed")
	n, err := b.WriteFrom(io.MultiReader(strings.NewReader("ab"), errReader{want}), 0)
	if !errors.Is(err, want) {
		t.Fatalf("WriteFrom err = %v, want %v", err, want)
	}
	if n != 2 {
		t.Errorf("n = %d, want 2 (bytes read before error)", n)
	}
}

// errReader — io.Reader, всегда возвращающий ошибку.
type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

// TestBufferInMemoryReadSeek — чтение/seek in-memory буфера через интерфейс
// самого Buffer (не BufferReader).
func TestBufferInMemoryReadSeek(t *testing.T) {
	b, err := NewBuffer(BufferOptions{})
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	defer func() { _ = b.Close() }()
	if _, err := b.Write([]byte("hello world")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if pos, err := b.Seek(6, io.SeekStart); err != nil || pos != 6 {
		t.Fatalf("Seek = %d, %v; want 6, nil", pos, err)
	}
	tail := make([]byte, 5)
	if n, err := b.Read(tail); err != nil || n != 5 {
		t.Fatalf("Read = %d, %v; want 5, nil", n, err)
	}
	if string(tail) != "world" {
		t.Errorf("tail = %q, want world", tail)
	}
	// Чтение за концом — EOF.
	if _, err := b.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Errorf("Read at end = %v, want EOF", err)
	}
}

// TestBufferReadFromFile — после spill чтение и seek работают через файл.
func TestBufferReadFromFile(t *testing.T) {
	b, err := NewBuffer(BufferOptions{Pool: NewBufferPool(2), Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	defer func() { _ = b.Close() }()
	payload := bytes.Repeat([]byte("x"), 16)
	if _, err := b.Write(payload); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if b.InMemory() {
		t.Fatal("buffer must spill")
	}
	if _, err := b.Seek(4, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	got := make([]byte, 4)
	if n, err := b.Read(got); err != nil || n != 4 {
		t.Fatalf("Read = %d, %v; want 4, nil", n, err)
	}
	if !bytes.Equal(got, []byte("xxxx")) {
		t.Errorf("read = %q, want xxxx", got)
	}
	// BufferReader по spill-файлу.
	r, err := b.NewReader()
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer func() { _ = r.Close() }()
	all, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(all, payload) {
		t.Errorf("reader got %d bytes, want %d", len(all), len(payload))
	}
}
