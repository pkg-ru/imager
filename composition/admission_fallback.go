package composition

// AdmissionMaxConcurrentMultiplier — множитель, применяемый к
// application.limits.concurrency при вычислении HTTP admission-лимита, когда
// http.max-concurrent-requests не задан (0).
//
// Обоснование: один concurrent HTTP-запрос может занимать admission-слот
// дольше, чем собственно генерация, поэтому admission-лимит не должен быть
// равен limits.concurrency "один в один" (application.limits.concurrency —
// это число ОДНОВРЕМЕННЫХ ГЕНЕРАЦИЙ, а не HTTP-запросов). Слот удерживается
// на протяжении всего жизненного цикла запроса:
//   - ожидание входа в keyed singleflight (join к идущей генерации того же
//     ассета) — запрос может висеть без выполнения генерации;
//   - cache lookup / source fallback / serve-original (короткие, но всё равно
//     занимают слот);
//   - собственно генерация в процессоре;
//   - отдача результата клиенту (io.Copy через сеть);
//   - publish в remote (асинхронный, но может удерживать buffer).
//
// Если бы admission-лимит был равен limits.concurrency 1:1, при переполнении
// HTTP-слоя admission отклонил бы (503) запросы, которые в противном случае
// лишь дождались бы освобождения слота генерации или присоединились к идущей
// генерации — admission стал бы узким местом РАНЬШЕ, чем application-лимит,
// ради которого он добавлен. Множитель x4 даёт запас на "не-генерационную"
// часть жизненного цикла, позволяя реальному числу одновременных генераций
// оставаться под limits.concurrency, и при этом ограничивает поток
// входящих HTTP-запросов.
const AdmissionMaxConcurrentMultiplier = 4

// EffectiveAdmissionLimit вычисляет эффективный лимит одновременно
// обрабатываемых asset-запросов (admission control).
//
// Приоритет:
//  1. http.max-concurrent-requests (явный лимит HTTP-слоя) — если задан
//     (> 0), он БЕЗУСЛОВНО приоритетен и возвращается как есть.
//  2. application.limits.concurrency (лимит одновременных генераций) —
//     используется как базовый лимит admission с множителем
//     AdmissionMaxConcurrentMultiplier (см. комментарий выше) для
//     компенсации того, что HTTP-запрос может занимать слот дольше, чем
//     генерация.
//  3. Ни один не задан (0) — admission остаётся выключенным (0): текущее
//     поведение, никакой семафор не создаётся (NewAdmissionControl(0)).
func EffectiveAdmissionLimit(httpMaxConcurrent int, genConcurrency uint32) int {
	if httpMaxConcurrent > 0 {
		return httpMaxConcurrent
	}
	if genConcurrency > 0 {
		return int(genConcurrency) * AdmissionMaxConcurrentMultiplier
	}
	return 0
}
