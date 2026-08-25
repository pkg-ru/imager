# Безопасность

## Политика авторизации (deny-by-default)

Всё запрещено по умолчанию; разрешается только явно покрытое правилами. Реализация — `domain/policy` (компиляция конфигурации в неизменяемую политику на старте, fail-fast при невалидных правилах).

Два режима `policy.global.authorization`:

- **`safe`** (рекомендуется для production):
  - preset URL разрешён только если имя пресета целиком входит в `allowed-presets`;
  - канонический URL разрешён только если запрошенный размер покрыт хотя бы одним правилом `size-rules`;
  - пустой список `size-rules` = все канонические запросы отклоняются.
- **`unsafe`**: любые произвольные параметры; лимиты продолжают действовать.

### Правила размеров

Формат `"minW-maxWxminH-maxH"`:

```yaml
size-rules:
  - "0-2000x0-2000"   # ширина и высота от 0 до 2000 px
  - "500x"            # точная ширина 500, высота любая
```

### Path-policies

Ужесточают глобальную политику по префиксам пути канонических URL (longest prefix match):

| Поле | Действие |
|------|----------|
| `dpr: "0-1"` | Ограничение диапазона DPR |
| `crop: center \| smart \| face \| object` | Единственный разрешённый режим кропа |
| `crop: [smart, face]` | Whitelist режимов |
| `crop: none` | Любой кроп запрещён |
| `trim: true/false` | Trim обязателен / запрещён |

Режим автоматически покрывает свой trim-вариант (`center` → `c` и `ct`). Path-policies не применяются к пресетам и не могут расширять права глобальной политики.

## Лимиты

Проверяются в любом режиме авторизации (`policy.global.limits`, `0` = без ограничения):

| Лимит | Проверка |
|-------|----------|
| `source-bytes` | До обработки, по метаданным открытого источника |
| `width`/`height`/`pixels`/`dpr` | До обработки, по запросу (с учётом DPR-умножения) |
| `frames`/`duration` | Для анимаций |
| `output-bytes` | После обработки по фактическому размеру выхода |

Превышение → HTTP `403 forbidden`. Дополнительно `application.output-limit` прерывает генерацию при превышении размера выхода (bounded writer), а `http.max-concurrent-requests` ограничивает число одновременных asset-запросов (`503` + `Retry-After: 1`; health/metrics остаются доступными).

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

### ImageMagick (fallback)

Три слоя ограничений subprocess:

1. `-limit` аргументы командной строки (memory/map/disk/threads/time/pixels/frames);
2. генерируемый deny-by-default `policy.xml` (через `MAGICK_CONFIGURE_PATH`): все coders/delegates запрещены, разрешён только безопасный whitelist форматов; network- и scripting-coders (URL/HTTPS/FTP/MSL/MVG/LABEL/TEXT/PS/PDF/SVG…) и delegates (curl, wget, ssh, rsvg, inkscape…) явно заблокированы; `imagemagick.policy.disable-network` держите включённым в production (риск SSRF);
3. application-level: bounded writer на stdout (лимит выхода) и context deadline (убийство процесса по таймауту).

Защита от decompression bomb: `max-pixels` / `pixels` (по умолчанию 256 MP).

### libvips (основной)

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
| Admission control | `http.max-concurrent-requests` → `503` + `Retry-After` |
| Content-Type | Только из безопасного маппинга форматов, не из пользовательского ввода |
| Fallback-файлы | Отдаются с явным статусом `404`, без `http.ServeFile` |

## Админ-эндпоинты

Административные эндпоинты (`POST /admin/assets/generate`, `DELETE /admin/assets/delete`) управляют ассетами: фоновая генерация и удаление. Они **выключены по умолчанию** (`admin.enabled: false`) и регистрируются в mux только при включении. При `admin.enabled: true` обязателен непустой `admin.token`, иначе старт завершится ошибкой (fail-fast) — эндпоинты не могут работать с пустой авторизацией.

### Bearer-токен

Все админ-запросы требуют заголовок:

```
Authorization: Bearer <token>
```

Токен сравнивается через `crypto/subtle.ConstantTimeCompare` (constant-time), что защищает от timing-атак. Неверный или отсутствующий токен → `403` (JSON error envelope). Токен не логируется и не попадает в метрики.

### Рекомендации

- **Храните токен в секретах.** Размещайте `admin.token` в `setting-local.yaml` (не коммитится, см. `.gitignore`) или в секрет-менеджере; не зашивайте в `setting.yaml` и не передавайте через аргументы/URL.
- **Используйте сильный случайный токен** (например, `openssl rand -hex 32`). Не используйте короткие/предсказуемые значения.
- **Ротация.** Периодически меняйте токен; при компрометации — немедленно. Ротация требует перезапуска сервиса (конфиг читается на старте).
- **Отключение по умолчанию.** Держите `admin.enabled: false`, пока админ-эндпоинты не нужны. Включайте только при необходимости и ограничивайте доступ к `/admin/*` на уровне сети/фаервола (например, только из внутренней сети или через reverse-proxy с дополнительной аутентификацией).
- **Не выставляйте `/admin/*` в публичный интернет** без дополнительной защиты. Bearer-токен — единственный барьер; при его утечке злоумышленник получит возможность генерировать и удалять ассеты.
- **Мониторинг:** следите за `403` на `/admin/*` в логах — это может указывать на попытки несанкционированного доступа.

## Секреты

- Пароли, приватные ключи и S3 credentials задаются ключами YAML (`password`, `private-key-file`, `access-key`, `secret-key`) или env `IMAGER_S3_ACCESS_KEY`/`IMAGER_S3_SECRET_KEY`.
- Секреты не логируются, не попадают в метрики и не включаются в URL.
- Размещайте секреты в `setting-local.yaml` (не коммитится, см. `.gitignore`); базовый `setting.yaml` коммитится без секретов.
- SFTP требует явного `host-key-fingerprint` (SHA-256) — защита от MITM; FTPS требует проверки TLS-сертификата (`tls-verify: false` запрещён).
- HTTP-source не поддерживает query-параметры в `base-url` (исключает утечку секретов в URL).

## Приватность в observability

URL, query, raw user input и секреты не логируются и не попадают в метрики. Метрики используют фиксированные enum-метки (bounded cardinality): классы статусов, категории ошибок, типы операций хранилищ.

## Контейнерный hardening

См. [DEPLOYMENT.md](DEPLOYMENT.md#hardening-контейнера): non-root, read-only root fs, dropped capabilities, no-new-privileges, tmpfs для `/tmp`.
