# Аудит отказоустойчивости и производительности

Сервис обработки изображений `d:/imager` (Go, net/http, libvips + ImageMagick, storage: FS / S3 / HTTP / FTP / SFTP).

## Методология

- Прочитаны ключевые файлы: composition root (`cmd/imager/main.go`), HTTP-адаптер (`internal/adapters/httpapi/*`), application use case (`internal/application/generatev2/*`), процессоры (`internal/adapters/processor/*`), storage-адаптеры (`internal/adapters/storage/*`), доменный слой (`internal/domain/*`), observability (`internal/observability/*`), конфигурация и Docker-окружение.
- Каждая проблема: файл(ы) + строки, описание, конкретное исправление с API/сигнатурами.
- Группировка по критичности: **КРИТИЧНО** (отказ/потеря данных), **ВАЖНО** (деградация/зависания), **УЛУЧШЕНИЕ** (производительность/надёжность).

Итого: **4 критичных**, **12 важных**, **11 улучшений**.

---

## КРИТИЧНО

### К1. S3 multipart: гонка конкурентного чтения общего reader → повреждение данных

- **Файлы:** [`internal/adapters/storage/s3/s3.go`](internal/adapters/storage/s3/s3.go:469) (publishMultipart, строки 469–564), [`internal/application/generatev2/service.go`](internal/application/generatev2/service.go:489) (publishFromBuffer, строки 489–533)
- **Описание:** при `application.output-limit: 0` (дефолт в [`config/setting.yaml`](config/setting.yaml)) `publishFromBuffer` передаёт в `ResultStore.Publish` сырой `BufferReader` без обёртки `boundedReader`. `publishMultipart` разбивает поток на парты ≥ 5 MB и читает их **конкурентно (8 воркеров) из одного общего `src io.Reader`**. [`BufferReader`](internal/adapters/storage/remote/buffer.go:379) не потокобезопасен: `Read`/`Seek` используют общее состояние (`offset`, `r`). Результат: перемешанные/повреждённые парты, битый объект в S3 при размере вывода > 5 MB (реально при лимитах `output-bytes: 10MiB` у обоих процессоров).
- **Исправление (минимальное, без изменения контракта):** сериализовать чтение партов — читать парт в память (`[]byte`, ≤ partSize), затем конкурентно загружать готовые байты:
  ```go
  // s3.go: publishMultipart
  type part struct {
      num  int
      data []byte
  }
  // 1) последовательно нарезать парты из src (src не потокобезопасен)
  // 2) конкурентно uploadPart(part.num, bytes.NewReader(part.data))
  ```
  **Альтернатива (в `service.go`):** всегда оборачивать буфер в reader, который порождает изолированный reader на парт:
  ```go
  // publishFromBuffer: если OutputLimit == 0, использовать buf.Size() как лимит
  // и передавать в Publish фабрику reader-ов вместо одного reader
  type partReaderFactory interface {
      NewReader() (io.ReadSeeker, error)
  }
  ```
  Рекомендуемый вариант — сериализация чтения в `s3.go`: она не зависит от типа reader и гарантирует корректность для любых источников.

### К2. libvips: операции не прерываются по контексту → зависания и утечки горутин

- **Файлы:** [`internal/adapters/processor/libvips/process_libvips.go`](internal/adapters/processor/libvips/process_libvips.go:76) (строки 76–108, 128–180, 211–261), [`internal/adapters/processor/libvips/processor.go`](internal/adapters/processor/libvips/processor.go:214)
- **Описание:** `ctx.Err()` проверяется только до/после вызовов libvips (`ThumbnailWithBuffer`, `ExportJpeg` и т.д.). Сама cgo-операция не отменяема. При зависшей операции (битый/нестандартный файл, деградация диска) `GenerateTimeout` (30s) и таймауты `http.Server` не сработают: горутина обработки и воркер семафора зависают навсегда; при shutdown `Shutdown(ctx)` упрётся в свой таймаут, клиентское соединение повиснет.
- **Исправление:** watchdog-обёртка над каждой тяжёлой операцией + проверки ctx между стадиями:
  ```go
  // process_libvips.go
  func runVips(ctx context.Context, fn func() error) error {
      done := make(chan error, 1)
      go func() { done <- fn() }()
      select {
      case err := <-done:
          return err
      case <-ctx.Done():
          return ctx.Err() // cgo-операция останется висеть, но сервис не блокируется
      }
  }
  // + ctx.Err() между стадиями: decode → thumbnail → export → applyAnimation (по кадрам)
  ```
  Дополнительно: проверить доступность kill-механизма libvips (`vips_image_set_kill` / `VipsOperationFlags`) в используемой версии govips — если есть, вызывать его по `ctx.Done()`.

### К3. libvips: полная материализация входа и выхода в памяти → OOM

- **Файлы:** [`internal/adapters/processor/libvips/processor.go`](internal/adapters/processor/libvips/processor.go:214) (`io.ReadAll(in.Source)`), [`internal/adapters/processor/libvips/process_libvips.go`](internal/adapters/processor/libvips/process_libvips.go:128) (backend возвращает `[]byte`)
- **Описание:** весь вход читается в память без учёта `SourceBytes`; весь выход собирается в `[]byte`. Для remote-источников `Metadata.Size` может быть 0/unknown, поэтому `CheckLimits` не защищает. Суммарный пик: вход 10 MiB + декодированный битмап в libvips (пиксели в разы больше исходника) + `max-cache-mem: 50MiB` + выход 10 MiB может превысить лимит контейнера 512 MB → OOM-kill без graceful shutdown.
- **Исправление:**
  ```go
  // processor.go: ограниченное чтение входа
  limit := in.Limits.SourceBytes
  if limit <= 0 { limit = defaultSourceLimit } // например 10 MiB
  data, err := io.ReadAll(io.LimitReader(in.Source, limit))
  if int64(len(data)) >= limit {
      return nil, processor.LimitError{Kind: processor.LimitSourceBytes}
  }
  // проверка выхода до возврата:
  if int64(len(out)) > in.Limits.OutputBytes {
      return nil, processor.LimitError{Kind: processor.LimitOutputBytes}
  }
  ```
  Плюс: учитывать декодированные пиксели в бюджете памяти (ширина × высота × каналы) и ограничивать через `CheckLimits` до декодирования.

### К4. FTP/SFTP: пул из одного соединения, dial под мьютексом → блокировка всех операций

- **Файлы:** [`internal/adapters/storage/ftp/pool.go`](internal/adapters/storage/ftp/pool.go:47) (acquire, строки 47–69), [`internal/adapters/storage/sftp/pool.go`](internal/adapters/storage/sftp/pool.go:47) (аналогично)
- **Описание:** `connPool.acquire` держит `p.mu` на время `dial()` — сетевой операции с таймаутом до 30s+. При занятом/упавшем соединении любая операция (Lookup/Open/ReadStream других ключей) блокируется на время dial+retry. `MaxIdleConns` допускает только 0 или 1 — конкурентность отсутствует, весь трафик к FTP/SFTP сериализуется.
- **Исправление:**
  ```go
  // pool.go: dial вне блокировки + пул соединений
  func (p *connPool) acquire(ctx context.Context) (client, error) {
      select {
      case c := <-p.idle:
          return c, nil
      default:
      }
      // dial вне p.mu, с таймаутом из ctx; p.cur защищён мьютексом
      c, err := p.dial(ctx)
      if err != nil { return nil, err }
      return c, nil
  }
  // + конфиг max-connections (≥ 2), MaxIdleConns > 1
  ```
  Минимальный фикс: вынести `dial()` из-под `p.mu` (под мьютексом держать только счётчик активных), ограничить число одновременных dial-ов семафором.

---

## ВАЖНО

### В1. Перегрузка процессоров → HTTP 500 вместо 503 + Retry-After

- **Файлы:** [`internal/adapters/httpapi/handler.go`](internal/adapters/httpapi/handler.go:260) (mapError, строки 260–308), [`internal/application/generatev2/service.go`](internal/application/generatev2/service.go:441) (processAndPublish), [`internal/adapters/processor/imagemagick/processor.go`](internal/adapters/processor/imagemagick/processor.go:184), [`internal/adapters/processor/libvips/processor.go`](internal/adapters/processor/libvips/processor.go)
- **Описание:** при переполнении bounded-очереди процессоров (`ErrTooManyConcurrency`) возвращается `OutcomeProcessing` → 500. Клиенты не получают сигнал «повтори позже», их ретраи будут бить в 500.
- **Исправление:** новый типизированный исход + маппинг в 503:
  ```go
  // outcome.go
  const OutcomeOverloaded OutcomeKind = "overloaded"
  // handler.go mapError
  case outcome.IsOutcome(err, outcome.OutcomeOverloaded):
      w.Header().Set("Retry-After", "1")
      http.Error(w, "overloaded", http.StatusServiceUnavailable)
  // процессоры: ErrTooManyConcurrency → outcome.OutcomeOverloaded
  ```

### В2. Неограниченный рост etagCache

- **Файлы:** [`internal/adapters/httpapi/handler.go`](internal/adapters/httpapi/handler.go:44) (объявление, строка 44; использование, строки 228–242)
- **Описание:** `etagCache sync.Map` растёт без ограничений на каждый уникальный URL. При большом разнообразии пресетов/ключей — неограниченный расход памяти.
- **Исправление:** bounded LRU-кэш:
  ```go
  type etagCache struct {
      mu  sync.Mutex
      m   map[string]string // key -> etag
      lru *list.List        // для eviction
      max int               // например 4096
  }
  func (c *etagCache) Get(key string) (string, bool)
  func (c *etagCache) Set(key, etag string) // evict при len(m) > max
  ```

### В3. Неограниченный рост S3 metadataCache

- **Файлы:** [`internal/adapters/storage/s3/s3.go`](internal/adapters/storage/s3/s3.go:187) (строки 187–240)
- **Описание:** `metadataCache` с TTL 30s не имеет верхней границы по числу ключей — при шквале уникальных ключей память растёт.
- **Исправление:** LRU-ограничение поверх TTL:
  ```go
  type metadataCache struct {
      mu  sync.Mutex
      m   map[string]cacheEntry
      lru *list.List
      max int // например 10000
  }
  ```

### В4. FS Publish: копирование в temp-файл не отменяемо по контексту

- **Файлы:** [`internal/adapters/storage/fs/store.go`](internal/adapters/storage/fs/store.go:316) (writeTemp), [`internal/adapters/storage/fs/helpers.go`](internal/adapters/storage/fs/helpers.go:49)
- **Описание:** `io.CopyBuffer` в temp-файл не проверяет `ctx` в процессе копирования. При медленном источнике (S3/HTTP remote) публикация может висеть дольше `GenerateTimeout`/shutdown-таймаута.
- **Исправление:** обёртка reader, проверяющая ctx:
  ```go
  // helpers.go
  type ctxReader struct {
      ctx context.Context
      r   io.Reader
  }
  func (c ctxReader) Read(p []byte) (int, error) {
      if err := c.ctx.Err(); err != nil { return 0, err }
      return c.r.Read(p)
  }
  // store.go: io.CopyBuffer(dst, ctxReader{ctx, src}, buf)
  ```

### В5. LRU touch на каждом попадании в кэш под глобальным мьютексом

- **Файлы:** [`internal/adapters/storage/fs/quota.go`](internal/adapters/storage/fs/quota.go:194) (touch, строки 194–200), [`internal/adapters/storage/fs/store.go`](internal/adapters/storage/fs/store.go) (Lookup/Open/ReadStream)
- **Описание:** каждый hit `cache.touch(key)` делает `MoveToFront` под глобальным мьютексом `cacheManager` — на горячем пути чтения результатов это сериализует все обращения к кэшу.
- **Исправление (варианты):**
  - A: не двигать в LRU на каждом чтении; обновлять только счётчик обращений, LRU-порядок пересобирать периодически по тикеру;
  - B: sharded cache (`[]cacheManager` по хэшу ключа);
  - C: `sync.Map` + отдельный LRU-проход по таймеру.

### В6. warmCache — полный обход каталога при старте

- **Файлы:** [`internal/adapters/storage/fs/store.go`](internal/adapters/storage/fs/store.go:148) (строки 148–176)
- **Описание:** при старте `filepath.Walk` по всему дереву результатов; при большом каталоге старт сервиса затягивается, а до завершения обхода кэш не готов — первые запросы идут в обход кэша.
- **Исправление:** запускать warm-фазу в фоновой горутине с ограничением по времени (errgroup + timeout); либо ленивое заполнение кэша при первом Lookup + периодическая сверка; healthcheck не должен зависеть от warm-фазы.

### В7. evictIfNeeded — синхронные удаления файлов на пути публикации

- **Файлы:** [`internal/adapters/storage/fs/store.go`](internal/adapters/storage/fs/store.go:364) (вызов, строки 364/377), [`internal/adapters/storage/fs/quota.go`](internal/adapters/storage/fs/quota.go:226) (evictIfNeeded, строки 226–263)
- **Описание:** при превышении мягкой квоты удаление LRU-файлов происходит синхронно в вызывающей горутине (вне lock, но на пути Publish) — латентность публикации зависит от числа удаляемых файлов и скорости диска.
- **Исправление:** асинхронный eviction:
  ```go
  // quota.go: вернуть кандидатов на удаление
  func (c *cacheManager) evictIfNeeded() ([]string, error)
  // store.go: после recordPublish — удалить кандидатов в фоне
  go r.deleteAsync(candidates) // с учётом reference counting открытых артефактов
  ```

### В8. Жёсткая квота проверяется ПОСЛЕ записи temp-файла

- **Файлы:** [`internal/adapters/storage/fs/store.go`](internal/adapters/storage/fs/store.go:345)
- **Описание:** `reserveBytes(written)` вызывается после полной записи во временный файл. При превышении `QuotaBytes` файл уже занял место на диске и время на запись — лишняя нагрузка и риск переполнения диска.
- **Исправление:** резервировать до записи:
  ```go
  // store.go: до writeTemp
  if err := r.cache.reserveBytes(expectedSize); err != nil { return err }
  // после записи: commitBytes(actual) / releaseBytes(expected-actual)
  // expectedSize берётся из opts.Size или buf.Size()
  ```

### В9. health.go: потенциальная утечка горутины + аллокация таймера

- **Файлы:** [`internal/adapters/httpapi/health.go`](internal/adapters/httpapi/health.go:77) (dependenciesReady, строки 77–112)
- **Описание:** проверка запускается в горутине, ожидание через `time.After(timeout)`. Если проверка блокируется (медленный remote), горутина остаётся висеть (утечка при частых healthcheck-запросах); `time.After` аллоцирует таймер на каждый вызов.
- **Исправление:**
  ```go
  timer := time.NewTimer(timeout)
  defer timer.Stop()
  done := make(chan error, 1)
  go func() { done <- h.check(ctx) }()
  select {
  case err := <-done:
      return err
  case <-timer.C:
      return ErrDependenciesTimeout
  case <-ctx.Done():
      return ctx.Err()
  }
  // + ограничение конкурентных проверок (singleflight или семафор)
  ```

### В10. Контейнер 512 MB vs buffer pool 500 MiB + libvips + ImageMagick → OOM

- **Файлы:** [`docker-compose.yaml`](docker-compose.yaml), [`config/setting.yaml`](config/setting.yaml) (`buffer-max-bytes: 500MiB`, libvips `max-cache-mem: 50MiB`)
- **Описание:** spillable buffer 500 MiB + libvips-кэш 50 MiB + декодированные битмапы + ImageMagick subprocess (memory limit по policy) при лимите контейнера 512 MB — суммарный пик может превысить лимит → OOM-kill без graceful shutdown.
- **Исправление:** снизить `buffer-max-bytes` до 256 MiB (или ~40% лимита контейнера); задать `GOMEMLIMIT=384MiB` в docker-compose; при необходимости увеличить лимит памяти контейнера до 1 GB; `memswap_limit: 0`.

### В11. Нет admission control / rate limiting на уровне HTTP

- **Файлы:** [`internal/adapters/httpapi/mux.go`](internal/adapters/httpapi/mux.go), [`internal/adapters/httpapi/runtime.go`](internal/adapters/httpapi/runtime.go)
- **Описание:** нет глобального ограничения числа одновременных запросов и rate limiting. При шквале запросов все горутины уходят в процессоры/сеть, bounded-очереди процессоров переполняются → массовые 500 (см. В1), возможен отказ в обслуживании.
- **Исправление:** middleware с семафором:
  ```go
  type admissionControl struct{ sem chan struct{} }
  func (a *admissionControl) Wrap(next http.Handler) http.Handler {
      return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
          select {
          case a.sem <- struct{}{}:
              defer func() { <-a.sem }()
              next.ServeHTTP(w, r)
          default:
              w.Header().Set("Retry-After", "1")
              http.Error(w, "too many requests", http.StatusServiceUnavailable)
          }
      })
  }
  // + опционально per-IP rate limit (golang.org/x/time/rate)
  ```

### В12. BufferPool: глобальный мьютекс на каждом чанке записи

- **Файлы:** [`internal/adapters/storage/remote/buffer.go`](internal/adapters/storage/remote/buffer.go:478) (tryReserve), [`internal/adapters/storage/remote/buffer.go`](internal/adapters/storage/remote/buffer.go:121) (writeAll, строки 121–139)
- **Описание:** `tryReserve` берёт глобальный `p.mu` на каждый чанк 32 KB при записи в spillable buffer — на горячем пути генерации (каждый результат пишется в буфер) это глобальная сериализация и contention.
- **Исправление:** атомарный бюджет вместо мьютекса:
  ```go
  type BufferPool struct {
      budget atomic.Int64 // доступный бюджет в байтах
  }
  func (p *BufferPool) tryReserve(n int64) bool {
      // CAS-цикл: бюджет -= n, отказ если уходим в минус
  }
  ```
  Либо резервировать блоками (например, 1 MB) вместо каждого чанка.

---

## УЛУЧШЕНИЕ

### У1. Ретраи HTTP-источника без джиттера

- **Файлы:** [`internal/adapters/storage/http/http.go`](internal/adapters/storage/http/http.go:183) (do, строки 183–210)
- **Описание:** линейный backoff `(i+1)*100ms` без случайного джиттера — при синхронных сбоях множество клиентов бьют в источник одновременно (thundering herd).
- **Исправление:** экспоненциальный backoff с джиттером: `backoff = base * (1 << i) + rand.Int63n(jitter)`.

### У2. Нет gzip для JSON-ответов

- **Файлы:** [`internal/adapters/httpapi/handler.go`](internal/adapters/httpapi/handler.go)
- **Описание:** ошибки и JSON-ответы отдаются без сжатия.
- **Исправление:** middleware gzip (стандартный `compress/gzip` + обёртка `ResponseWriter`) для `Content-Type: application/json` с учётом `Accept-Encoding`.

### У3. Нет If-Modified-Since

- **Файлы:** [`internal/adapters/httpapi/handler.go`](internal/adapters/httpapi/handler.go:168) (строки 168–172, 228–242)
- **Описание:** поддерживается только ETag/If-None-Match; кэширующие клиенты без ETag не могут получить 304.
- **Исправление:** хранить `Last-Modified` (из метаданных объекта) и обрабатывать `If-Modified-Since`.

### У4. Нет readiness-эндпоинта для оркестратора

- **Файлы:** [`internal/adapters/httpapi/mux.go`](internal/adapters/httpapi/mux.go), [`Dockerfile`](Dockerfile) (HEALTHCHECK использует /healthz — только liveness)
- **Описание:** `/healthz` проверяет только liveness; при недоступности зависимостей (S3/FS) под не выводится из ротации.
- **Исправление:** добавить `/readyz` на основе `dependenciesReady()` (с bounded timeout, см. В9); в Dockerfile HEALTHCHECK использовать `/readyz` (или комбинацию).

### У5. Janitor для temp-файлов не подключён

- **Файлы:** [`internal/adapters/storage/fs/janitor.go`](internal/adapters/storage/fs/janitor.go:133) (CleanTemps, строки 133–172), [`internal/adapters/storage/fs/store.go`](internal/adapters/storage/fs/store.go), [`cmd/imager/main.go`](cmd/imager/main.go)
- **Описание:** `Janitor.CleanTemps` реализован, но не найден вызов `Start`/`Stop`/`CleanTemps` в composition root и store — осиротевшие temp-файлы (после сбоя) не чистятся.
- **Исправление:** подключить janitor в `main.go` (или store): запуск по тикеру (например, каждые 5 минут, файлы старше 1 часа), остановка при shutdown.

### У6. ftpStreamCloser: незащищённый флаг once

- **Файлы:** [`internal/adapters/storage/ftp/ftp.go`](internal/adapters/storage/ftp/ftp.go)
- **Описание:** `once bool` в `Close()` не защищён — при конкурентных Close (читатель + defer) возможна гонка.
- **Исправление:** `sync.Once` или `atomic.Bool`.

### У7. S3 http.Client.Timeout распространяется на чтение тела ответа

- **Файлы:** [`internal/adapters/httpapi/storage_factory.go`](internal/adapters/httpapi/storage_factory.go) (S3 client: ReadTimeout 60s)
- **Описание:** `http.Client.Timeout` включает чтение тела — для больших объектов (10 MB+) при медленном канале 60s может быть недостаточно, обрывая валидные передачи.
- **Исправление:** `http.Client{Timeout: 0}` + `Transport{ResponseHeaderTimeout}`; чтение тела контролировать через контекст и `io.LimitReader`.

### У8. Пробелы в метриках

- **Файлы:** [`internal/observability/metrics.go`](internal/observability/metrics.go), [`internal/adapters/httpapi/mux.go`](internal/adapters/httpapi/mux.go)
- **Описание:** нет метрик: глубина очередей процессоров, in-flight запросов, числа ключей в singleflight, размера buffer pool, количества eviction-файлов, размера etagCache/metadataCache.
- **Исправление:** добавить gauges: `processor_queue_depth`, `http_inflight`, `singleflight_keys`, `buffer_pool_bytes`, `cache_evictions_total`, `cache_entries`.

### У9. singleflight WaitTimeout не настроен

- **Файлы:** [`internal/adapters/coordination/singleflight/singleflight.go`](internal/adapters/coordination/singleflight/singleflight.go:103) (Do, строки 103–152), [`internal/adapters/httpapi/runtimeconfig.go`](internal/adapters/httpapi/runtimeconfig.go)
- **Описание:** `WaitTimeout=0` — ожидающие ключ горутины ждут без ограничения; при зависшей генерации (см. К2) все ожидающие висят до GenerateTimeout.
- **Исправление:** задать `WaitTimeout` из конфига (например, равным GenerateTimeout) — по истечении возвращать ошибку вместо бесконечного ожидания.

### У10. fsyncDir: ошибки игнорируются

- **Файлы:** [`internal/adapters/storage/fs/fsync_unix.go`](internal/adapters/storage/fs/fsync_unix.go:1), [`internal/adapters/storage/fs/store.go`](internal/adapters/storage/fs/store.go)
- **Описание:** ошибка `fsync` директории (после rename) не проверяется/не логируется — при сбое питания возможна потеря записи без сигнала.
- **Исправление:** возвращать ошибку из `fsyncDir` и прокидывать в результат Publish (или логировать).

### У11. Retry-After отсутствует на 429/503

- **Файлы:** [`internal/adapters/httpapi/handler.go`](internal/adapters/httpapi/handler.go:260)
- **Описание:** 503 (перегрузка) отдаётся без `Retry-After` — клиенты не знают, когда повторить.
- **Исправление:** см. В1 — добавить `Retry-After` при `OutcomeOverloaded` и в admission control (В11).

---

## ПЛАН ОПТИМИЗАЦИЙ

Приоритизированный список изменений, готовых к реализации в Code mode (по убыванию приоритета):

1. **S3 multipart race** (К1) — [`internal/adapters/storage/s3/s3.go`](internal/adapters/storage/s3/s3.go:469) + [`internal/application/generatev2/service.go`](internal/application/generatev2/service.go:489): сериализовать чтение партов (парт в `[]byte` → конкурентная загрузка). Критично: повреждение данных.
2. **libvips watchdog** (К2) — [`internal/adapters/processor/libvips/process_libvips.go`](internal/adapters/processor/libvips/process_libvips.go:76): watchdog + проверки ctx между стадиями.
3. **libvips bounded memory** (К3) — [`internal/adapters/processor/libvips/processor.go`](internal/adapters/processor/libvips/processor.go:214): `io.LimitReader` по SourceBytes, проверка OutputBytes до возврата.
4. **FTP/SFTP pool** (К4) — [`internal/adapters/storage/ftp/pool.go`](internal/adapters/storage/ftp/pool.go:47), [`internal/adapters/storage/sftp/pool.go`](internal/adapters/storage/sftp/pool.go:47): dial вне мьютекса, пул ≥ 2.
5. **503 + Retry-After** (В1, У11) — [`internal/application/generatev2/outcome.go`](internal/application/generatev2/outcome.go): `OutcomeOverloaded`; [`internal/adapters/httpapi/handler.go`](internal/adapters/httpapi/handler.go:260): маппинг в 503.
6. **Bounded кэши** (В2, В3) — [`internal/adapters/httpapi/handler.go`](internal/adapters/httpapi/handler.go:44) (etagCache), [`internal/adapters/storage/s3/s3.go`](internal/adapters/storage/s3/s3.go:187) (metadataCache): LRU + max.
7. **ctx-отменяемое копирование в FS** (В4) — [`internal/adapters/storage/fs/helpers.go`](internal/adapters/storage/fs/helpers.go:49): ctxReader.
8. **Quota до записи** (В8) — [`internal/adapters/storage/fs/store.go`](internal/adapters/storage/fs/store.go:345): reserveBytes до writeTemp.
9. **Асинхронный eviction** (В7) — [`internal/adapters/storage/fs/quota.go`](internal/adapters/storage/fs/quota.go:226).
10. **LRU touch** (В5) — [`internal/adapters/storage/fs/quota.go`](internal/adapters/storage/fs/quota.go:194): счётчики + периодическая пересборка.
11. **warmCache фоном** (В6) — [`internal/adapters/storage/fs/store.go`](internal/adapters/storage/fs/store.go:148).
12. **health.go** (В9) — [`internal/adapters/httpapi/health.go`](internal/adapters/httpapi/health.go:77): timer + канал + singleflight.
13. **Admission control** (В11) — middleware с семафором в [`internal/adapters/httpapi/admission.go`](internal/adapters/httpapi/admission.go:19), подключается в [`internal/adapters/httpapi/mux.go`](internal/adapters/httpapi/mux.go) (оборачивает asset handler "/").
14. **BufferPool атомарный бюджет** (В12) — [`internal/adapters/storage/remote/buffer.go`](internal/adapters/storage/remote/buffer.go:478).
15. **Память контейнера** (В10) — [`docker-compose.yaml`](docker-compose.yaml), [`config/setting.yaml`](config/setting.yaml): buffer-max-bytes 256 MiB, GOMEMLIMIT.
16. **Janitor** (У5) — подключить в [`cmd/imager/main.go`](cmd/imager/main.go).
17. **Метрики** (У8) — [`internal/observability/metrics.go`](internal/observability/metrics.go).
18. **Ретраи с джиттером** (У1) — [`internal/adapters/storage/http/http.go`](internal/adapters/storage/http/http.go:183).
19. **Readiness /readyz** (У4) — [`internal/adapters/httpapi/mux.go`](internal/adapters/httpapi/mux.go), [`Dockerfile`](Dockerfile).
20. **Прочее** (У2, У3, У6, У7, У9, У10) — gzip, If-Modified-Since, sync.Once в ftpStreamCloser, S3 client timeout, singleflight WaitTimeout, обработка ошибок fsyncDir.