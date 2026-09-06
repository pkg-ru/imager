// Фильтрация и маршрутизация логов libvips/govips по configured
// observability.log-level.
//
// Первопричина утечки info-логов ([govips.info]/[VIPS.info]): imager никогда
// не вызывал vips.LoggingSettings, поэтому при vips.Startup govips
// устанавливал свой дефолт — verbosity LogLevelInfo и дефолтный stderr-хендлер
// (см. govips/vips/govips.go: "if !currentLoggingOverridden"). Configured
// observability.log-level применялся только к slog-логгеру приложения и
// никак не влиял на govips.
//
// Решение: перед vips.Startup вызывается vips.LoggingSettings(handler,
// verbosity), где verbosity выведен из observability.log-level. Семантика
// фильтра govips: сообщение проходит, если messageLevel <= verbosity, где
// уровни — битовое поле glib (ERROR=1 < CRITICAL=2 < WARNING=4 < MESSAGE=8 <
// INFO=16 < DEBUG=32).
//
// Файл свободен от cgo (собирается без тэка "libvips") — чтобы логика
// фильтрации была покрыта unit-тестами на любой платформе. Привязка к типам
// govips выполняется в process_libvips.go.
package libvips

import (
	"gitverse.ru/pkg-ru/imager/observability"
)

// Зеркало G_LOG_LEVEL_* (битовое поле glib; значения важны: govips фильтрует
// сравнением messageLevel <= verbosity).
const (
	glibLevelError    = 1 << 0 // 1
	glibLevelCritical = 1 << 1 // 2
	glibLevelWarning  = 1 << 2 // 4
	glibLevelMessage  = 1 << 3 // 8
	glibLevelInfo     = 1 << 4 // 16
	glibLevelDebug    = 1 << 5 // 32
)

// VipsVerbosityFor отображает configured observability.log-level
// (debug|info|warn|error) в verbosity-порог govips (бит G_LOG_LEVEL_*).
//
// Пустое/неизвестное значение → warning (fail-safe: по умолчанию тихо,
// как и slog-логгер приложения при warn... для slog дефолт info; здесь
// консервативный дефолт warning, чтобы мусор libvips не шумел).
func VipsVerbosityFor(level string) int {
	switch level {
	case "debug":
		return glibLevelDebug
	case "info":
		return glibLevelInfo
	case "error":
		return glibLevelError
	default: // "", "warn" и неизвестные значения
		return glibLevelWarning
	}
}

// VipsLogAllowed повторяет семантику фильтра govips (govips/vips/logging.go:
// govipsLog): сообщение уровня glibLevel проходит при level <= verbosity.
// Вынесено для unit-тестирования без cgo.
func VipsLogAllowed(glibLevel, verbosity int) bool {
	return glibLevel <= verbosity
}

// RouteVipsLog маршрутизирует сообщение libvips/govips в структурный логгер
// приложения. Маппинг уровней (требование задачи):
//
//	error/critical → Errorf
//	warning        → Warnf
//	message/info   → Infof
//	debug          → Debugf
//
// Вызывается только после фильтра по verbosity, т.е. routeVipsLog не решает,
// показывать ли сообщение — только на каком slog-уровне.
func RouteVipsLog(log observability.Logger, domain string, glibLevel int, message string) {
	if log == nil {
		return
	}
	msg := "vips[" + domain + "]: " + message
	switch {
	case glibLevel <= glibLevelCritical:
		log.Errorf("%s", msg)
	case glibLevel == glibLevelWarning:
		log.Warnf("%s", msg)
	case glibLevel == glibLevelMessage || glibLevel == glibLevelInfo:
		log.Infof("%s", msg)
	default: // glibLevelDebug и прочие более verbose
		log.Debugf("%s", msg)
	}
}
