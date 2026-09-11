package remote

import "context"

// Store — общая часть SourceStore и ResultStore адаптеров ftp/sftp:
// опции и доступ к пулу. Параметр C — тип соединения (conn/client),
// O — тип опций адаптера (ftp.Options / sftp.Options).
type Store[C any, O any] struct {
	// Opts — опции адаптера.
	Opts    O
	pool    *Pool[C]
	dial    func(ctx context.Context) (C, error)
	closeFn func(C) error
}

// NewStore валидирует опции и создаёт пул (если usePool). dial создаёт
// новое соединение, closeFn закрывает его. При usePool=false соединения
// создаются напрямую через dial (тестовый путь адаптеров: ftp.Dialer,
// sftp.Client).
func NewStore[C, O any](opts O, validate func(O) error, usePool bool, dial func(ctx context.Context) (C, error), closeFn func(C) error, maxConns int) (Store[C, O], error) {
	if err := validate(opts); err != nil {
		return Store[C, O]{}, err
	}
	s := Store[C, O]{Opts: opts, dial: dial, closeFn: closeFn}
	if usePool {
		s.pool = NewPool(dial, closeFn, maxConns)
	}
	return s, nil
}

// NewStoreDirect создаёт Store без пула и без валидации: соединения
// создаются напрямую через dial при каждом Acquire. Используется для
// одноразовых операций (например, ftp stat), где пул не требуется.
func NewStoreDirect[C, O any](opts O, dial func(ctx context.Context) (C, error), closeFn func(C) error) Store[C, O] {
	return Store[C, O]{Opts: opts, dial: dial, closeFn: closeFn}
}

// Acquire возвращает соединение из пула либо через прямой dial
// (внепуловая Session, закрывающая соединение при Discard).
func (s *Store[C, O]) Acquire(ctx context.Context) (*Session[C], error) {
	if s.pool != nil {
		e, err := s.pool.Acquire(ctx)
		if err != nil {
			return nil, err
		}
		return NewSession(e), nil
	}
	c, err := s.dial(ctx)
	if err != nil {
		return nil, err
	}
	return NewDirectSession(c, s.closeFn), nil
}

// Pooled — соединение внутри одной попытки retry-операции: значение
// соединения C плюс ссылка на общую сессию пула и колбэк передачи владения.
// Адаптеры (ftp/sftp) встраивают Pooled в свои pooledConn/pooledClient
// вместе с интерфейсом соединения, чтобы методы соединения промотировались.
type Pooled[C any] struct {
	// Value — само соединение (FTP-conn, SFTP-client и т.п.).
	Value C
	// Session — общая сессия пула (для Discard и передачи владения).
	Session *Session[C]
	keep    func()
}

// Discard закрывает соединение и освобождает слот пула. Идемпотентно.
func (p *Pooled[C]) Discard() {
	p.Session.Discard()
}

// KeepAlive передаёт владение соединением результату операции (стриминг):
// каркас не закроет соединение после успешного возврата — это сделает
// владелец потока через Session.Discard.
func (p *Pooled[C]) KeepAlive() {
	p.keep()
}

// WithRetry — retry-каркас операций удалённых хранилищ поверх Retry:
// acquire -> op -> классификация сырой ошибки политикой policy -> повторная
// попытка с новым соединением (до MaxAttempts) или немедленный возврат.
//
// Контракт op: возвращает результат, сырую ошибку raw (по ней policy
// решает, ретраить ли) и замапленную ошибку mapped (возвращается
// вызывающему; при успехе обе равны nil). Ошибки, не прошедшие policy, и
// исчерпанный ctx завершают цикл сразу; после исчерпания попыток
// возвращается последняя mapped-ошибка.
//
// Владение соединением: каркас закрывает соединение после каждой попытки;
// операция стриминга (ReadStream) передаёт владение потоку через
// Pooled.KeepAlive.
func WithRetry[C, T any](
	ctx context.Context,
	opts ConnOptions,
	acquire func(ctx context.Context) (*Session[C], error),
	mapDialErr func(error) error,
	policy func(error) bool,
	op func(c *Pooled[C]) (T, error, error),
) (T, error) {
	var owned bool
	spec := RetrySpec[*Session[C], T]{
		Acquire:        acquire,
		Discard:        func(c *Session[C]) { c.Discard() },
		Policy:         policy,
		MapDialErr:     mapDialErr,
		TakesOwnership: func(T) bool { return owned },
	}
	return Retry(ctx, opts, spec, func(c *Session[C]) (T, error, error) {
		owned = false
		return op(&Pooled[C]{Value: c.Value, Session: c, keep: func() { owned = true }})
	})
}
