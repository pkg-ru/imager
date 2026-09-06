# Безопасность

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
| `frames`/`duration` | Для анимаций |
| `output-bytes` | После обработки по фактическому размеру выхода (bounded writer прерывает генерацию) |
| `concurrency` | Максимум одновременных операций от одного клиента |

Превышение → HTTP `403 forbidden`. Дополнительно `libvips.limits.output-bytes` ограничивает выход на уровне движка.

## Безопасность URL

Парсер asset URL (`domain/asset`) отклоняет:

- traversal-сегменты (`..`) в URL и имени исходника;
- encoded-разделители пути (`%2f`, `%2F`);
- control-символы и NUL;
- разделители пути (`/`, `\`) внутри имени исходника;
- длину компонентов: путь ≤512, имя ≤128, формат ≤16, пресет ≤64, весь URL ≤1024 символов (`414`).

Ключи объектов во всех remote-хранилищах нормализуются: запрет `..`, обратных слешей, NUL.

## Защита файловой системы

Адаптер `storage/fs` включает:

- **secure open**: открытие файлов с защитой от symlink-атак и выхода за корневой каталог (платформенные реализации для Linux/Unix/Windows);
- **атомарную публикацию**: temp-файл + rename, fsync файла и каталога;
- **квоты**: проверка свободного места каталога результатов;
- **janitor**: удаление осиротевших temp-файлов публикации.

## Изоляция движков обработки

### libvips (единственный)

In-process без subprocess; ограничения: `libvips.limits.timeout` (context deadline), `output-bytes` (bounded writer), `concurrency` (слоты одновременных операций), лимиты кэша и потоков.

## HTTP hardening

| Механизм | Настройка |
|----------|-----------|
| Security headers | `X-Content-Type-Options: nosniff`, `Referrer-Policy`, опциональный `Content-Security-Policy` |
| CORS | Deny-by-default allowlist; `"*"` + credentials запрещены валидацией |
| Таймауты сервера | read-header/read/write/idle — защита от slowloris и медленных клиентов |
| Лимит заголовков | `server.max-header-bytes` → `431` |
| Лимит тела | `server.max-body-bytes` (сервис тело не принимает) |
| Лимит URL | `http.max-url-len` → `414` |
| Таймаут генерации | `http.generate-timeout` → `504` |
| Admission control | `http.max-concurrent-requests` → `503` + `Retry-After: 1`; health/metrics остаются доступными |
| Content-Type | Только из безопасного маппинга форматов, не из пользовательского ввода |
| Fallback-файлы | Отдаются с явным статусом `404`, без `http.ServeFile` |

## Админ-эндпоинты

Админ-эндпоинты (`POST /admin/assets/generate`, `DELETE /admin/assets/delete`) **выключены по умолчанию** и регистрируются только при `admin.enabled: true` (обязателен непустой `admin.token`, иначе fail-fast при старте). Все админ-запросы требуют `Authorization: Bearer <token>`; токен сравнивается через `crypto/subtle.ConstantTimeCompare` (constant-time), неверный/отсутствующий токен → `403`. Подробный справочник API — [API.md](API.md#админ-эндпоинты); параметры — [CONFIGURATION.md](CONFIGURATION.md#admin).

Рекомендации:

- используйте сильный случайный токен (например `openssl rand -hex 32`) и храните его в `*-local.yaml` или секрет-менеджере; не зашивайте в базовые `*.yaml` и не передавайте через аргументы/URL;
- ротация токена требует перезапуска сервиса (конфиг читается на старте);
- не выставляйте `/admin/*` в публичный интернет: Bearer-токен — единственный барьер; ограничивайте доступ на уровне сети/фаервола или reverse-proxy;
- следите за `403` на `/admin/*` в логах — признак попыток несанкционированного доступа.

## Секреты

- Пароли, приватные ключи и S3 credentials задаются ключами YAML (`password`, `private-key-file`, `access-key`, `secret-key`) или env `IMAGER_S3_ACCESS_KEY`/`IMAGER_S3_SECRET_KEY` (значение из YAML приоритетнее).
- Секреты не логируются, не попадают в метрики и не включаются в URL.
- Размещайте секреты в `*-local.yaml` (не коммитятся, см. `.gitignore`); базовые `*.yaml` коммитятся без секретов.
- SFTP требует явного `host-key-fingerprint` (SHA-256) — защита от MITM; FTPS требует проверки TLS-сертификата (`tls-verify: false` запрещён).
- HTTP-source не поддерживает query-параметры в `base-url` (исключает утечку секретов в URL).

## Приватность в observability

URL, query, raw user input и секреты не логируются и не попадают в метрики. Метрики используют фиксированные enum-метки (bounded cardinality): классы статусов, категории ошибок, типы операций хранилищ.

## Контейнерный hardening

См. [DEPLOYMENT.md](DEPLOYMENT.md#укрепление-контейнера-hardening): non-root, dropped capabilities, no-new-privileges, tmpfs для `/tmp`. Read-only rootfs не используется (см. объяснение в [DEPLOYMENT.md](DEPLOYMENT.md#укрепление-контейнера-hardening)).
