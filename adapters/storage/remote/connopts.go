package remote

import (
	"context"
	"time"
)

// Дефолты HTTP(S)-транспорта: единый источник значений для HTTP-source
// адаптера и S3-клиента (ранее эти константы дублировались в
// adapters/storage/http и adapters/httpapi/storage_factory).
const (
	// DefaultDialTimeout — таймаут установки TCP-соединения.
	DefaultDialTimeout = 30 * time.Second
	// DefaultReadTimeout — таймаут чтения ответа / выполнения операции.
	DefaultReadTimeout = 60 * time.Second
	// DefaultMaxAttempts — число попыток запроса.
	DefaultMaxAttempts = 3
	// DefaultMaxIdleConns — максимум idle-соединений в пуле.
	DefaultMaxIdleConns = 100
	// DefaultMaxIdleConnsHost — максимум idle-соединений на хост.
	DefaultMaxIdleConnsHost = 10
	// DefaultIdleConnTimeout — таймаут idle-соединений.
	DefaultIdleConnTimeout = 90 * time.Second
	// DefaultKeepAlive — период TCP keep-alive.
	DefaultKeepAlive = 30 * time.Second
	// DefaultTLSHandshake — таймаут TLS handshake.
	DefaultTLSHandshake = 10 * time.Second
	// DefaultExpectContinue — таймаут Expect: 100-continue.
	DefaultExpectContinue = 1 * time.Second
	// DefaultMaxConnsPerHost — максимум соединений на хост.
	DefaultMaxConnsPerHost = 2048
)

// ConnOptions — общие параметры соединения удалённого хранилища: таймауты,
// число попыток и лимиты пулов. Встраивается в Options адаптеров
// (ftp/sftp/http), чтобы настройки не дублировались между конфигурацией
// httpapi и Options каждого адаптера.
//
// Семантика нулевых значений:
//   - DialTimeout/ReadTimeout/IdleConnTimeout = 0 — «без ограничения» либо
//     дефолт транспорта (см. методы *OrDefault);
//   - MaxAttempts = 0 — одна попытка (Attempts);
//   - MaxConns = 0 — минимум пула (2, см. NewPool);
//   - MaxIdleConns/MaxIdleConnsPerHost = 0 — дефолт транспорта.
type ConnOptions struct {
	// DialTimeout — таймаут установки соединения.
	DialTimeout time.Duration
	// ReadTimeout — таймаут операции (0 = без ограничения).
	ReadTimeout time.Duration
	// MaxAttempts — максимальное число попыток операции (0 = 1).
	MaxAttempts int
	// MaxConns — максимальное число одновременных соединений в пуле
	// (0 = 2). Позволяет конкурентным операциям работать параллельно.
	MaxConns int
	// MaxIdleConns — максимум idle-соединений в пуле (0 = дефолт).
	MaxIdleConns int
	// MaxIdleConnsPerHost — максимум idle-соединений на хост (0 = дефолт).
	MaxIdleConnsPerHost int
	// IdleConnTimeout — таймаут idle-соединений (0 = без ограничения).
	IdleConnTimeout time.Duration
}

// Attempts возвращает нормализованное число попыток операции (>= 1).
func (o ConnOptions) Attempts() int {
	if o.MaxAttempts < 1 {
		return 1
	}
	return o.MaxAttempts
}

// OpTimeout оборачивает ctx таймаутом операции, если задан положительный
// ReadTimeout. Возвращает cancel-функцию, которую вызывающий обязан вызвать
// (defer cancel()). При ReadTimeout <= 0 контекст не изменяется.
func (o ConnOptions) OpTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if o.ReadTimeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, o.ReadTimeout)
}

// DialTimeoutOrDefault возвращает DialTimeout или DefaultDialTimeout.
func (o ConnOptions) DialTimeoutOrDefault() time.Duration {
	if o.DialTimeout <= 0 {
		return DefaultDialTimeout
	}
	return o.DialTimeout
}

// ReadTimeoutOrDefault возвращает ReadTimeout или DefaultReadTimeout.
func (o ConnOptions) ReadTimeoutOrDefault() time.Duration {
	if o.ReadTimeout <= 0 {
		return DefaultReadTimeout
	}
	return o.ReadTimeout
}

// MaxAttemptsOrDefault возвращает MaxAttempts или DefaultMaxAttempts.
func (o ConnOptions) MaxAttemptsOrDefault() int {
	if o.MaxAttempts <= 0 {
		return DefaultMaxAttempts
	}
	return o.MaxAttempts
}

// MaxIdleConnsOrDefault возвращает MaxIdleConns или DefaultMaxIdleConns.
func (o ConnOptions) MaxIdleConnsOrDefault() int {
	if o.MaxIdleConns <= 0 {
		return DefaultMaxIdleConns
	}
	return o.MaxIdleConns
}

// MaxIdleConnsPerHostOrDefault возвращает MaxIdleConnsPerHost или
// DefaultMaxIdleConnsHost.
func (o ConnOptions) MaxIdleConnsPerHostOrDefault() int {
	if o.MaxIdleConnsPerHost <= 0 {
		return DefaultMaxIdleConnsHost
	}
	return o.MaxIdleConnsPerHost
}

// IdleConnTimeoutOrDefault возвращает IdleConnTimeout или
// DefaultIdleConnTimeout.
func (o ConnOptions) IdleConnTimeoutOrDefault() time.Duration {
	if o.IdleConnTimeout <= 0 {
		return DefaultIdleConnTimeout
	}
	return o.IdleConnTimeout
}
