# Хранилища

Source и result настраиваются независимо секциями `source:` и `result:`. Тип задаётся ключом `storage`.

| Тип | Source | Result | Примечание |
|-----|--------|--------|------------|
| `fs` | ✅ | ✅ | Локальная файловая система |
| `s3` | ✅ | ✅ | S3 и совместимые (MinIO, Yandex Object Storage) |
| `sftp` | ✅ | ✅ | SSH File Transfer Protocol |
| `ftp` | ✅ | ✅ | Plain FTP |
| `ftps` | ✅ | ✅ | FTP over explicit TLS |
| `http` | ✅ | ❌ (ошибка старта) | Только чтение |

## Ключи исходников и результатов

Ключ объекта строится из asset URL: `{path}/{source_name}.{source_format}` для источника; канонический URL — ключ результата. Ключи нормализуются во всех remote-адаптерах: запрещены `..`, обратные слеши, NUL-байт.

## Общие параметры соединения (remote-хранилища)

Применимы к s3/sftp/ftp/ftps/http в обеих секциях:

| Ключ | По умолчанию | Описание |
|------|--------------|----------|
| `spool-dir` | системный tmp | Каталог временных spool-файлов при чтении remote-объектов |
| `spool-max-bytes` | `0` (нет) | Лимит размера spool; превышение → ошибка квоты |
| `dial-timeout` | `"30s"` | Таймаут установления соединения |
| `read-timeout` | `"60s"` (S3/HTTP) | Таймаут выполнения операции |
| `max-attempts` | `3` | Число попыток операции |
| `max-idle-conns` | `100` | Максимум idle-соединений в пуле |
| `max-idle-conns-per-host` | `10` | Максимум idle-соединений на хост (S3/HTTP) |
| `idle-conn-timeout` | `"90s"` | Время жизни idle-соединения |
| `metadata-ttl` | `"30s"` | TTL кэша метаданных S3 (`0` = кэш отключён) |

## fs

```yaml
source:
  storage: fs
  path: "/data/source"

result:
  storage: fs
  path: "/data/result"
```

Свойства:

- атомарная публикация результата: запись во временный файл + rename;
- fsync данных и каталога перед публикацией (платформенно-специфичные реализации);
- безопасное открытие файлов: защита от symlink-атак и выхода за корневой каталог (платформенные реализации secure-open);
- квота каталога результатов (проверка свободного места на платформах с поддержкой);
- janitor периодически удаляет осиротевшие temp-файлы публикации из каталога результатов (интервал 5 минут, возраст файла более 1 часа; останавливается при graceful shutdown).

## s3

```yaml
source:
  storage: s3
  bucket: "my-images-source"     # обязательно
  prefix: "source/"              # завершающий "/" нормализуется
  endpoint: "https://storage.yandexcloud.net"   # пусто = AWS
  region: "ru-central1"
  access-key: "AKIA..."          # или env IMAGER_S3_ACCESS_KEY
  secret-key: "..."              # или env IMAGER_S3_SECRET_KEY
```

| Ключ | Обязательность | Описание |
|------|----------------|----------|
| `bucket` | да | Имя bucket |
| `prefix` | нет | Префикс ключей внутри bucket |
| `endpoint` | нет | Endpoint S3-совместимого хранилища |
| `region` | нет | Регион |
| `access-key` / `secret-key` | только парой | Статические credentials; если не заданы — стандартная цепочка AWS SDK (env, instance role) |

Валидация: `bucket` обязателен; задание только одного из `access-key`/`secret-key` — ошибка старта.

Публикация результата — conditional PUT (`If-None-Match: "*"`); поддерживаются ETag и multipart через AWS SDK v2.

## sftp

```yaml
source:
  storage: sftp
  addr: "sftp.example.com:22"          # обязательно
  user: "imager"                        # обязательно
  private-key-file: "/etc/imager/id_ed25519"   # или password
  root: "/srv/images"
  host-key-fingerprint: "SHA256:AbCd..."       # ОБЯЗАТЕЛЬНО
```

| Ключ | Обязательность | Описание |
|------|----------------|----------|
| `addr` | да | Адрес `host:port` |
| `user` | да | Пользователь |
| `password` | один из двух | Парольная аутентификация |
| `private-key-file` | один из двух | Файл приватного ключа |
| `root` | нет | Корневой каталог на сервере (пусто = домашний) |
| `host-key-fingerprint` | да | SHA-256 fingerprint host key (`SHA256:...`) |

Fingerprint получается командой:

```bash
ssh-keyscan -t ed25519 host | ssh-keygen -lf -
```

Без `host-key-fingerprint` конфигурация отклоняется на старте. Публикация — temp-upload + rename (атомарно); no-overwrite — эксклюзивное создание (`O_EXCL`). Используются пул соединений и retry с backoff.

## ftp / ftps

```yaml
result:
  storage: ftps
  addr: "ftps.example.com:21"    # обязательно
  user: "imager"
  password: "..."
  root: "/srv/images"
  tls: true                       # для ftps всегда true (explicit TLS)
  tls-verify: true                # false запрещён
```

| Ключ | Обязательность | Описание |
|------|----------------|----------|
| `addr` | да | Адрес `host:port` |
| `user` / `password` | нет | Учётные данные |
| `root` | нет | Корневой каталог (пусто = корень) |
| `tls` | для ftps = true | Explicit TLS (AUTH TLS) |
| `tls-verify` | `true` | Проверка сертификата; отключение запрещено валидацией |

Особенности публикации:

- выполняется через temp-upload + rename и требует от сервера команд `STOR`, `RNFR`/`RNTO`, `DELE` (RFC 959); при их отсутствии публикация вернёт ошибку недоступности;
- no-overwrite — best-effort проверка существования перед rename (не атомарно);
- для самоподписанных сертификатов настройте доверенные CA в системе.

## http

Только source. Как result — ошибка старта.

```yaml
source:
  storage: http
  base-url: "https://cdn.example.com/images"   # обязательно
```

Ключ объекта добавляется к базовому пути:

```text
base-url: https://cdn.example.com/images
key:      foo/bar.jpg
URL:      https://cdn.example.com/images/foo/bar.jpg
```

Поведение:

- `Lookup` — HEAD, `Open` — GET;
- редиректы запрещены: любой `3xx` трактуется как недоступность хранилища;
- `404`/`410` → объект не найден; `401`/`403`, `408`, `429`, `5xx` → недоступен;
- размер ограничивается `spool-max-bytes`; при наличии `Content-Length` oversized-объект отклоняется до скачивания;
- метаданные заполняются из `Content-Length`, `Last-Modified`, `Content-Type`, `ETag`;
- `base-url` не должен содержать query-параметры или fragment (секреты в URL не поддерживаются).

## Метаданные детекции (sidecar)

Результаты ONNX-детекции (лица/объекты) и `largest_ai_asset` кэшируются в локальном sidecar-хранилище рядом с родительским файлом:

```yaml
metadata:
  enabled: true                  # дефолт true
  dir: "./data/meta"             # пусто = <локальный result-каталог>
```

- каждая модель вызывается ровно один раз на родительский файл; последующие запросы читают боксы из sidecar;
- расположение всегда локальное, независимо от типов source/result; при remote-result рекомендуется явный `dir`;
- обновление `largest_ai_asset` выполняется только для реальных ИИ-ассетов (выход больше родителя при тех же пропорциях).

## Janitor

Для `fs`-результатов запускается фоновая уборка осиротевших временных файлов публикации (параметры фиксированы: интервал 5 минут, максимальный возраст файла 1 час). Janitor останавливается при graceful shutdown.
