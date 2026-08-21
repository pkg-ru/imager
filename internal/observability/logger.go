package observability

import (
	"context"
	"fmt"
	"log/slog"
	"os"
)

// SlogLogger — структурированный логгер на stdlib log/slog.
//
// Реализует узкий интерфейс логирования (Debugf/Infof/Warnf/Errorf),
// используемый httpapi и generatev2, поверх slog. Все записи структурированы
// (JSON по умолчанию) и не содержат URL/query/raw user input или секретов.
type SlogLogger struct {
	l *slog.Logger
}

// NewSlogLogger создаёт JSON-логгер в stderr с указанным уровнем.
// Если level == nil, используется slog.LevelInfo.
func NewSlogLogger(level slog.Level) *SlogLogger {
	if level == 0 {
		level = slog.LevelInfo
	}
	h := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	return &SlogLogger{l: slog.New(h)}
}

// NewSlogLoggerWith создаёт логгер поверх переданного *slog.Logger.
func NewSlogLoggerWith(l *slog.Logger) *SlogLogger {
	if l == nil {
		return NewSlogLogger(slog.LevelInfo)
	}
	return &SlogLogger{l: l}
}

// Logger возвращает внутренний *slog.Logger.
func (s *SlogLogger) Logger() *slog.Logger { return s.l }

func (s *SlogLogger) Debugf(format string, args ...any) { s.l.Debug(fmt.Sprintf(format, args...)) }
func (s *SlogLogger) Infof(format string, args ...any)  { s.l.Info(fmt.Sprintf(format, args...)) }
func (s *SlogLogger) Warnf(format string, args ...any)  { s.l.Warn(fmt.Sprintf(format, args...)) }
func (s *SlogLogger) Errorf(format string, args ...any) { s.l.Error(fmt.Sprintf(format, args...)) }

// WithContext возвращает логгер, привязывающий request ID из контекста.
func (s *SlogLogger) WithContext(ctx context.Context) *SlogLogger {
	if s == nil || s.l == nil {
		return s
	}
	if id := RequestIDFrom(ctx); id != "" {
		return &SlogLogger{l: s.l.With("request_id", id)}
	}
	return s
}

// requestIDKey — тип ключа request ID в context.
type requestIDKey struct{}

// WithRequestID возвращает контекст с request ID.
func WithRequestID(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestIDFrom извлекает request ID из контекста (пустая строка, если нет).
func RequestIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(requestIDKey{}).(string); ok {
		return v
	}
	return ""
}
