package observability

// AssetErrorEvent — структурированное событие ошибки asset URL.
//
// Поля не содержат raw user input в unbounded виде: url — это путь запроса
// (без query), preset — имя пресета (если есть), reason — категория причины.
// Используется для структурного логирования ошибок asset URL.
type AssetErrorEvent struct {
	// Kind — категория ошибки: parse | preset_not_found | policy_forbidden |
	// invalid_plan.
	Kind string
	// URL — путь запроса (без query).
	URL string
	// Preset — имя пресета (если есть).
	Preset string
	// Reason — человекочитаемая причина.
	Reason string
}

// LogAssetError логирует событие ошибки asset URL на заданном уровне.
//
// Уровень задаётся строкой: debug|info|warn|error. Неизвестный уровень
// трактуется как warn. Если logger == nil, событие игнорируется.
func LogAssetError(log Logger, level string, ev AssetErrorEvent) {
	if log == nil {
		return
	}
	msg := "httpapi: asset error"
	switch level {
	case "debug":
		log.Debugf("%s: kind=%s url=%q preset=%q reason=%q", msg, ev.Kind, ev.URL, ev.Preset, ev.Reason)
	case "info":
		log.Infof("%s: kind=%s url=%q preset=%q reason=%q", msg, ev.Kind, ev.URL, ev.Preset, ev.Reason)
	case "error":
		log.Errorf("%s: kind=%s url=%q preset=%q reason=%q", msg, ev.Kind, ev.URL, ev.Preset, ev.Reason)
	default: // warn
		log.Warnf("%s: kind=%s url=%q preset=%q reason=%q", msg, ev.Kind, ev.URL, ev.Preset, ev.Reason)
	}
}
