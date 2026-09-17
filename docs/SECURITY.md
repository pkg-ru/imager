# Безопасность

Связанные документы: [POLICIES.md](POLICIES.md) (модель политики), [ARCHITECTURE.md](ARCHITECTURE.md) (место политики и admission control в конвейере), [API.md](API.md) (коды ошибок), [CONFIGURATION.md](CONFIGURATION.md) (параметры), [DEPLOYMENT.md](DEPLOYMENT.md) (hardening контейнера), [OBSERVABILITY.md](OBSERVABILITY.md) (приватность метрик).

## Политика авторизации (deny-by-default)

Всё запрещено по умолчанию; разрешается только явно покрытое правилами. Реализация — `domain/policy`: конфигурация компилируется в неизменяемую политику на старте (fail-fast при невалидных правилах).

Политика применяется к ассет-URL вида `/{path}/{source_name}-{source_format}/{segment}@{dpr}.{output_format}`, где `segment` — имя пресета (`policy.presets`) или custom-имя (размер-грамматика `x`, `x200`, `200x`, `200x200`).

Разрешение запроса:

- `policy.path-policies` — map правил по префиксам пути; выбор — longest-prefix match, `"/"` — fallback для всех путей без более специфичного совпадения;
- каждая path-policy перечисляет доступные на этом пути `presets` и `customs`; сегмент URL допускается, только если найден в presets/customs пути;
- пресет становится доступным в URL только после включения его имени в какую-либо path-policy;
- `@dpr` и выходной формат URL обязаны удовлетворять настройкам пресета/custom (правила dpr — [CONFIGURATION.md](CONFIGURATION.md#правила-dpr));
- path-policy не имеет полей `dpr`/`crop`/`trim`: эти параметры задаются только пресетом/custom;
- если для пути нет подходящей path-policy (нет `"/"` и совпадений) — запрос отклоняется.

Отклонение запроса → `403 forbidden`. Описание секции `policy` и формат правил — [CONFIGURATION.md](CONFIGURATION.md#policy).

## Лимиты

Проверяются независимо от политики (`application.limits`, `0` = без ограничения):

| Лимит | Проверка |
|-------|----------|
| `source-bytes` | До обработки, по метаданным открытого источника |
| `width`/`height`/`pixels`/`dpr` | До обработки, по запросу (с учётом DPR-умножения) |
| `output-bytes` | После обработки по фактическому размеру выхода (bounded writer прерывает генерацию) |

Превышение → HTTP `403 forbidden`.

Дополнительно `libvips.limits.output-bytes` ограничивает выход на уровне движка, `libvips.limits.source-bytes` — вход (дефолт 10 MiB).

Поля `frames`/`duration` и `concurrency` валидируются конфигурацией, но на application-уровне НЕ проверяются: кадры/длительность анимаций контролируются лимитами движка (`libvips.limits`), а `concurrency` используется только как база fallback-лимита admission control (`application.limits.concurrency × 4`, когда `http.max-concurrent-requests` не задан — см. [HTTP hardening](#http-hardening)).

## Безопасность URL

Парсер asset URL (`domain/asset`) отклоняет:

- traversal-сегменты (`..`) в URL и имени исходника;
- encoded-разделители пути (`%2f`, `%2F`);
- control-символы и NUL (включая encoded `%00` и любые `%XX`, декодирующиеся в control-символ);
- разделители пути (`/`, `\`) внутри имени исходника;
- обратные слеши (`\`) в URL;
- длину компонентов: путь ≤512, имя ≤128, формат ≤16, пресет ≤64, весь URL ≤1024 символов (`414` на уровне HTTP-адаптера).

Ключи объектов во всех хранилищах (fs, s3, sftp, ftp/ftps, http) нормализуются: запрет `..`, обратных слешей, NUL и управляющих байтов; в fs-адаптере дополнительно зарезервированные сегменты (`.meta`, префикс `.tmp-`) и защита от symlink/junction-обхода.

## Защита файловой системы

Адаптер `storage/fs` включает:

- **secure open**: открытие файлов с защитой от symlink-атак и выхода за корневой каталог (на Linux — `openat2` с `RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS` с fallback на `openat`+`O_NOFOLLOW`; на Windows — запрет reparse points; generic-fallback для остальных платформ);
- **атомарную публикацию**: temp-файл (`.tmp-*`, права 0600 → 0644 перед публикацией) + rename, fsync файла и каталога; `no-overwrite` реализуется атомарно через hard link;
- **квоты**: жёсткий лимит суммарного размера каталога результатов (`QuotaBytes`, проверяется до записи) + soft-лимиты с LRU-eviction; ошибки ОС `ENOSPC`/`EDQUOT` маппятся в типизированную квоту;
- **janitor**: периодическое удаление осиротевших temp-файлов публикации (префикс `.tmp-`).

## Изоляция движков обработки

### libvips (единственный)

In-process без subprocess; ограничения: `libvips.limits.timeout` (context deadline), `output-bytes` (bounded writer), `concurrency` (слоты одновременных операций; 0 = дефолт 16), `source-bytes` (лимит чтения входа; 0 = дефолт 10 MiB), лимиты кэша и потоков. Тяжёлые ONNX-инференсы (fc/oc) выполняются под отдельным detection-семафором (`libvips.detection.concurrency`, дефолт `max(1, GOMAXPROCS/2)`; `max-wait`, дефолт 5s). При перегрузке детекции (переполнение очереди ожидания или истечение `max-wait`) запрос **деградирует** к center-crop (graceful degradation, счётчик `imager_detection_degraded_total`), а не получает 503, чтобы не голодать лёгкие операции.

## HTTP hardening

| Механизм | Настройка |
|----------|-----------|
| Security headers | `X-Content-Type-Options: nosniff`, `Referrer-Policy` (дефолт `no-referrer`), опциональный `Content-Security-Policy` |
| CORS | Deny-by-default allowlist; `"*"` + credentials запрещены валидацией; `Vary: Origin` ставится при любом `Origin` (в т.ч. denied); OPTIONS-ответы отдают `Allow: GET, HEAD, OPTIONS` |
| Таймауты сервера | read-header/read/write/idle — защита от slowloris и медленных клиентов |
| Лимит заголовков | `server.max-header-bytes` (дефолт 32 KiB) → `431` |
| Лимит тела | `server.max-body-bytes` (дефолт 4 KiB; сервис тело не принимает) |
| Лимит URL | `http.max-url-len` (дефолт 1024) → `414`; парсер asset URL дополнительно отклоняет traversal, encoded-разделители, control-символы, обратные слеши и ограничивает компоненты (путь ≤512, имя ≤128, формат ≤16, пресет ≤64) |
| Таймаут генерации | `http.generate-timeout` (дефолт 30s) → `504`; генерация выполняется на detached-контексте (результат сохраняется в кэш даже при разрыве соединения клиентом) |
| Admission control | `http.max-concurrent-requests` (или fallback из `application.limits.concurrency × 4`, если не задан) → `503` + динамический `Retry-After` (≥1s, растёт с заполненностью семафора); запросы, которые могут присоединиться к идущей singleflight-генерации того же ассета, пропускаются в обход семафора; health/metrics остаются доступными |
| Перегрузка процессора | Переполнение bounded-очереди libvips/detection-семафора → `503` + `Retry-After` (`http.retry-after`, дефолт 1s), а не `500` |
| Singleflight | Дедупликация конкурентных запросов одного ассета; таймаут ожидания владельца `application.singleflight-wait-timeout` (дефолт 60s) → `503` |
| Content-Disposition | Имя файла в source-fallback/serve-original санитизируется до `[A-Za-z0-9._-]` (защита от header injection) |
| ETag из метаданных | Валидируется (только печатные ASCII без кавычек/CR/LF), иначе fallback на вычисленный |
| Content-Type | Только из безопасного маппинга форматов, не из пользовательского ввода |
| Fallback-файлы | Отдаются с явным статусом `404`, без `http.ServeFile` |
| Panic recovery | Корневой Recover-middleware: паника в любой ветке mux → HTTP 500 (JSON envelope) вместо обрыва соединения; счётчик `imager_panics_total` |
| Gzip | JSON-ответы (error envelope) сжимаются при `Accept-Encoding: gzip`; изображения не сжимаются |
| Request ID | Заголовок `X-Request-Id` (входящий пробрасывается, иначе генерируется), попадает в логи |
| ETag/304 | Стабильный ETag (из метаданных хранилища или SHA-256 identity), поддержка `If-None-Match` → `304` |
| Admin-тело | Тело admin-запросов ограничено 1 MiB → `413` |

## Админ-эндпоинты

Админ-эндпоинты (`POST /admin/assets/generate`, `DELETE /admin/assets/delete`) **выключены по умолчанию** и регистрируются только при `admin.enabled: true` (обязателен непустой `admin.token`, иначе fail-fast при старте). Все админ-запросы требуют `Authorization: Bearer <token>`; токен сравнивается через SHA-256 + `crypto/subtle.ConstantTimeCompare` (constant-time, не раскрывает длину токена), неверный/отсутствующий токен → `403`. Подробный справочник API — [API.md](API.md#админ-эндпоинты); параметры — [CONFIGURATION.md](CONFIGURATION.md#admin).

Рекомендации:

- используйте сильный случайный токен (например `openssl rand -hex 32`) и храните его в `*-local.yaml` или секрет-менеджере; не зашивайте в базовые `*.yaml` и не передавайте через аргументы/URL;
- ротация токена требует перезапуска сервиса (конфиг читается на старте);
- не выставляйте `/admin/*` в публичный интернет: Bearer-токен — единственный барьер; ограничивайте доступ на уровне сети/фаервола или reverse-proxy;
- следите за `403` на `/admin/*` в логах — признак попыток несанкционированного доступа;
- admin-запросы принимают JSON-тело до 1 MiB (превышение → `413`), очередь задач ограничена `admin.queue-size` (переполнение → `503`).

## Секреты

- Пароли, приватные ключи и S3 credentials задаются ключами YAML (`password`, `private-key-file`, `access-key`, `secret-key`) или env `IMAGER_S3_ACCESS_KEY`/`IMAGER_S3_SECRET_KEY` (значение из YAML приоритетнее; env используется только если ключ YAML пуст).
- S3 credentials задаются только парой (`access-key` + `secret-key`); один без другого — ошибка старта.
- Секреты не логируются, не попадают в метрики и не включаются в URL.
- Размещайте секреты в `*-local.yaml` (не коммитятся, см. `.gitignore`); базовые `*.yaml` коммитятся без секретов.
- SFTP требует явного `host-key-fingerprint` (SHA-256) — защита от MITM (fail-fast при старте; без него соединение было бы уязвимо к MITM); FTPS требует проверки TLS-сертификата (`tls-verify: false` запрещён).
- HTTP-source не поддерживает query-параметры и fragment в `base-url` (исключает утечку секретов в URL; отклоняется валидацией).
- HTTP source имеет безопасный дефолт лимита spool 512 MiB (`spool-max-bytes`), чтобы неограниченный буфер не стал DoS-вектором.

## Приватность в observability

URL, query, raw user input и секреты не логируются и не попадают в метрики. Метрики используют фиксированные enum-метки (bounded cardinality): классы статусов, категории ошибок, типы операций хранилищ. Опциональный реестр top-paths проблемных путей (LRU, bounded) может работать в режиме `key-mode: hash` (SHA-256), если не хочется хранить пути исходников даже в памяти.

## Контейнерный hardening

См. [DEPLOYMENT.md](DEPLOYMENT.md#укрепление-контейнера-hardening): non-root (uid 10001), dropped capabilities, no-new-privileges, tmpfs для `/tmp`, restrictive permissions (бинарь 0755, конфиги 0640 root:imager, каталоги данных 0750). Read-only rootfs не используется (см. объяснение в [DEPLOYMENT.md](DEPLOYMENT.md#укрепление-контейнера-hardening)).

## Сканирование уязвимостей образа

Базовый образ — **Alpine 3.24** (стабильная ветка; `onnxruntime` доступен в
community, edge-репозиторий используется только для точечных фиксов CVE —
см. `docker/build-deps.sh`). Пакеты пиннуются (`~=`) для воспроизводимости;
`openssl`/`libcrypto3`/`libssl3` принудительно обновляются (`--upgrade`).

Сканирование в CI (`.github/workflows/docker-release.yml`):

- **Trivy** (`HIGH,CRITICAL`, `--ignore-unfixed`, `exit-code: 1`) — гейт
  релиза: найденные fixable-уязвимости блокируют публикацию (`needs: build`
  в джобе `publish`);
- **Docker Scout** (все severity, включая unfixed, `continue-on-error`) —
  мониторинг полной картины, той же, что показывает вкладка Security на
  Docker Hub; не блокирует релиз.

После публикации джоба `publish` дополнительно проверяет работоспособность
образа: контейнер запускается (с `IMAGER_SKIP_MODELS=1`) и опрашивается
`/healthz` (таймаут 60s).

Остаточные риски (на Alpine 3.24, все `not fixed` — фиксов нет в
репозиториях Alpine на момент сборки): `openexr` (3×HIGH), `cjson`
(2×HIGH, 2×MEDIUM), `jbig2dec` (1×MEDIUM), `libxml2` (3×LOW), `cairo`
(1×LOW), а также Go/Cargo-зависимости бинарника (UNSPECIFIED/LOW).
Эти CVE не имеют исправленных версий в стабильных репозиториях; обновление
доступно только после выхода фиксов в Alpine. Регулярно пересобирайте образ
и следите за отчётом Docker Scout.
