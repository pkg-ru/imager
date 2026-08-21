# Imager <sup><sup><sub>([Imager Client](https://github.com/pkg-ru/imager-client))</sub></sup></sup>
### WEB Микро сервис для генерации и компрессии миниатюр к картинкам на лету

Сервис принимает запрос на миниатюру (ассет) — генерирует его из исходного файла и сохраняет на диск в указанное место.\
При повторных запросах того же ассета сервис отдает ранее созданный, сжатый файл.

> Например, вы хотите сжать и уменьшить картинку. Для этого нужно сформировать [каноническую ссылку](https://github.com/pkg-ru/imager-client) (ассет) на картинку — при запросе которой сервис **Imager** на лету создаст сжатую миниатюру и вернет пользователю.
>
> <sub><sup>
> Исходник: example.com/my_image.jpg (не обязательно должен быть доступен из web)\
> Ассет на миниатюру: example.com/photos/my_image-jpg-c-120x80@2.webp \
> Ассет по пресету:   example.com/photos/my_image-jpg-thumb.webp
> </sup></sub>


---

## Формат ассет URL

Сервис принимает только **канонические** и **preset** URL. Byte-based кодирование не используется — URL читается напрямую.

### 

```
{path}/{source_name}-{source_format}-{transform}-{size}@{dpr}.{output_format}
```

- `transform` — код операции:
  - `c` — crop (обрезание по центру);
  - `t` — trim (обрезка краёв);
  - `ct` — crop, затем trim (последовательно).
  Любые другие коды (включая `tc`) недопустимы.
- `size` — размер миниатюры: `120x80`, `x50`, `180x`, диапазон `120-300x`, `x80-90`, `350-400x150`.
- `dpr` — целочисленный множитель (device pixel ratio): только `2` или `3`.

Пример: `photos/my-photo-jpg-c-120x80@2.webp` создаст ассет `120x80*2` из `my-photo.jpg`.

### Preset URL

```
{path}/{source_name}-{source_format}-{preset_name}.{output_format}
```

Пример: `photos/my-photo-jpg-thumb.webp` применит пресет `thumb` к исходнику `my-photo.jpg` (source name — `my-photo`, source format — `jpg`).

Пресеты определяются в `setting.yaml` (секция `policy.presets`), не содержат `source-format` (исходный формат определяется URL) и раскрываются в канонический запрос с параметрами пресета. `output-format` пресета обязан совпадать с расширением в URL.

---

## Запуск

> **Production**: новый production-конвейер собирается из
> [`cmd/imager`](cmd/imager/main.go) (composition root). Полное руководство по
> production-запуску, env/config, security assumptions, resource limits,
> health endpoints и storage roadmap — в [`docs/PRODUCTION.md`](docs/PRODUCTION.md).

Для запуска Imager можно использовать Docker. Воспользуйтесь следующими командами:

### Запуск с Docker

```bash
docker run -d -p 80:80 --volume ".:/app/example:rw" altrap/imager:v0.0.2
```

### Запуск с использованием `docker-compose`

```yaml
services:
  imager:
    image: altrap/imager:v0.0.2
    restart: always
    stop_signal: INT
    stop_grace_period: 5s
    ports:
      - 80:80
      - 443:443
    volumes:
      - ./:/app/example:rw
    working_dir: /app
    networks:
      - default
```

> **Примечание**: Настройки микро-сервиса можно указать в файле `setting.yaml`. Вы можете переопределить настройки, создав файл `setting-local.yaml` рядом с основным файлом конфигурации.

---

## Конфигурация

`setting.yaml` содержит общие настройки (http/unix/https, пути `source`/`result`) и секцию `asset` — конфигурацию конвейера генерации ассетов:

```yaml
asset:
  # Бинарь ImageMagick (по умолчанию "magick").
  # magick: "magick"

  policy:
    global:
      # safe — только размеры из size-rules и разрешённые пресеты.
      # unsafe — любые канонические запросы (поведение по умолчанию).
      authorization: "unsafe"
      # size-rules:
      #   - "120x80"
      # allowed-presets:
      #   - "thumb"
    # Переопределения политики для конкретных bucket.
    # buckets:
    #   - bucket: "private"
    #     authorization: "safe"
    presets:
      - name: "thumb"
        # Source format в пресете не задаётся: он определяется URL
        # ({source_name}-{source_format}-{preset_name}.{output_format}).
        transform: "c"        # "c" (crop), "t" (trim), "ct" (crop+trim)
        size: "120x80"
        dpr: 2                # только 2 или 3
        output-format: "webp"

  process:
    quality: 85
    trim:
      active: false
      rate: 10

  cache:
    cache-control: "public"
    max-age: 2592000
    s-maxage: 10800

  not-found:
    pixel: true
```

Форматы источников и выходов проверяются по реальному capability registry ImageMagick (`magick -list format`) при старте — без искусственного whitelist.

---

## Конфигурация (YAML, без env)

**Все** настройки приложения задаются исключительно в YAML. Прикладных
env-переменных и CLI-флагов нет. Единственная env-переменная —
`IMAGER_CONFIG_DIR` — путь к каталогу, где лежат:

- `setting.yaml` — **обязательный** базовый конфиг;
- `setting-local.yaml` — **опциональный**, глубоко переопределяет базовый
  (вложенные `map` мержатся, скаляры заменяются, списки заменяются целиком).

Неизвестные поля в любом файле отклоняются (strict decode, fail-fast).

### Хранилища (source / result)

Source и result настраиваются **независимо** секциями `source:` и `result:`.
Тип задаётся ключом `storage` (`fs`, `s3`, `sftp`, `ftp`, `ftps`, `http`).

```yaml
source:
  storage: fs
  path: /var/www/site.ru/images
result:
  storage: fs
  path: /var/cache/imager
```

> **Важно**: и FTP, и FTPS поддерживают и source, и result. Публикация
> выполняется через temp-upload + rename и требует от сервера команд
> `STOR`, `RNFR`/`RNTO` и `DELE` (базовый RFC 959). Если сервер не
> поддерживает эти команды, `Publish` вернёт ошибку `ErrUnavailable`.

Общие ключи (применимы к обеим секциям):

| Ключ | По умолчанию | Описание |
|-----|--------------|----------|
| `storage` | `fs` | Тип хранилища: `fs`, `s3`, `sftp`, `ftp`, `ftps`, `http`. |
| `path` | `./data/source` / `./data/result` | Локальный каталог для `fs`. |
| `spool-dir` | `os.TempDir()` | Каталог временных spool при чтении remote-объектов. |
| `spool-max-bytes` | `0` (нет) | Лимит размера spool при чтении (превышение → quota error). |
| `dial-timeout` | `30s` | Таймаут соединения для SFTP/FTP/FTPS и HTTP-запросов (например `10s`). |

#### S3 (`storage: s3`)

| Ключ | По умолчанию | Описание |
|-----|--------------|----------|
| `bucket` | — | Имя bucket (**обязательно**). |
| `prefix` | — | Префикс ключей внутри bucket (опционально). |
| `endpoint` | AWS | Endpoint для S3-совместимых хранилищ (MinIO и т.п.). |
| `region` | — | Регион AWS. |
| `access-key` | — | Access key. |
| `secret-key` | — | Secret key. |

Если `access-key`/`secret-key` не заданы, используется стандартная цепочка
credentials AWS SDK (env/instance role и т.д.). S3 поддерживает и source, и
result. `NoOverwrite` реализуется через conditional PUT (`If-None-Match: "*"`).

#### SFTP (`storage: sftp`)

| Ключ | По умолчанию | Описание |
|-----|--------------|----------|
| `addr` | — | Адрес `host:port` (**обязательно**). |
| `user` | — | Пользователь (**обязательно**). |
| `password` | — | Пароль (password auth). |
| `private-key-file` | — | Путь к файлу приватного ключа (key auth). |
| `root` | — | Корневой каталог внутри SFTP (пусто = домашний каталог). |

Требуется хотя бы один метод аутентификации: `password` или
`private-key-file`. Поддерживает и source, и result. Result публикуется через
temp-upload + rename (атомарно); `NoOverwrite` — через эксклюзивное создание
(`O_EXCL`).

> **SSH host key**: адаптер использует `ssh.InsecureIgnoreHostKey()` — проверка
> host key **отключена**. Используйте только в доверенных сетях или за
> VPN/SSH-туннелем.

#### FTPS (`storage: ftps`) и FTP (`storage: ftp`)

| Ключ | По умолчанию | Описание |
|-----|--------------|----------|
| `addr` | — | Адрес `host:port` (**обязательно**). |
| `user` | — | Пользователь (**обязательно**). |
| `password` | — | Пароль. |
| `root` | — | Корневой каталог (пусто = корень). |
| `tls` | `false` | Для `ftps` всегда `true` (explicit TLS, AUTH TLS). |

- **FTPS** (`ftps`): поддерживает и source, и result. Result публикуется через
  temp-upload + rename; `NoOverwrite` — best-effort проверка существования
  перед rename (не атомарно).
- **FTP** (`ftp`): поддерживает и source, и result (аналогично FTPS, но без
  TLS). Публикация требует команд `STOR`, `RNFR`/`RNTO` и `DELE`; при их
  отсутствии `Publish` вернёт `ErrUnavailable`. `NoOverwrite` — best-effort
  проверка существования перед rename (не атомарно).

> **TLS**: FTPS использует `tls.Config{InsecureSkipVerify: true}` — проверка
> сертификата **отключена**. Используйте только в доверенных сетях.

#### HTTP/HTTPS (`storage: http`)

HTTP/HTTPS — **source-only** backend: он реализует только чтение исходников
и **не может** использоваться как result.

```yaml
source:
  storage: http
  base-url: "https://addr.site/path_to_image/"
```

Ключ объекта безопасно канонизируется и добавляется к базовому пути:

```text
base-url: https://addr.site/path_to_image/
key:      foo/bar.jpg
URL:      https://addr.site/path_to_image/foo/bar.jpg
```

Поведение:

- `Lookup` — через `HEAD`, `Open` — через `GET`.
- **Redirects запрещены**: любой ответ `3xx` → `ErrUnavailable`.
- `404`/`410` → `ErrNotFound`; `401`/`403`, `408`, `429`, `5xx` и прочие
  non-2xx → `ErrUnavailable`.
- Размер ограничивается `spool-max-bytes` (превышение → `ErrQuota`); при
  наличии `Content-Length` объект отклоняется до скачивания.
- Метаданные — из `Content-Length`, `Last-Modified`, `Content-Type`, `ETag`.
- Таймаут запроса — `dial-timeout` (по умолчанию `30s`).
- `base-url` не должен содержать query-параметры или fragment.

### Примеры

Source из S3, result в локальный FS (фрагмент `setting-local.yaml`):

```yaml
source:
  storage: s3
  bucket: my-images
  region: eu-central-1
  access-key: "AKIA..."
  secret-key: "..."
```

Source из SFTP, result в S3 (фрагмент `setting-local.yaml`):

```yaml
source:
  storage: sftp
  addr: storage.example.com:22
  user: imager
  private-key-file: /run/secrets/id_ed25519
  root: /var/imager/source
result:
  storage: s3
  bucket: imager-cache
  prefix: thumbs
```

Source из HTTP/HTTPS, result в локальный FS (фрагмент `setting-local.yaml`):

```yaml
source:
  storage: http
  base-url: "https://addr.site/path_to_image/"
  spool-max-bytes: 10485760
```

---

## Пример настройки микро-сервиса с Nginx

Если вы хотите использовать Nginx для проксирования запросов, выполните следующие шаги.

### Запуск с Docker

```bash
docker run -d -p 8181:80 --volume ".:/app/example:rw" --restart=always altrap/imager:v0.0.2
```

### Конфигурация Nginx

Файлы должны быть доступны для Nginx. Если файл не существует, запрос будет перенаправлен на микро-сервис, который создаст превью изображения.

**Пример конфигурации для Nginx**:

```nginx
server {
    # ...
    # Обработка картинок: если файл не существует, проксируем запрос на Imager
    location ~ \.(jpg|jpeg|gif|png|apng|jpe|jif|jfif|jfi|webp|avif|heif|heic)$ {
        try_files $uri @imager;
    }

    location @imager {
        proxy_pass http://imager$uri$is_args$args;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
    }
    # ...
}

upstream imager {
    server http://127.0.0.1:8181;
}
```

> **Примечание**: Imager можно использовать как самостоятельный сервер. Для этого необходимо установить зависимости: 
> - [ImageMagick](https://imagemagick.org/script/download.php)
> - [FFmpeg](https://ffmpeg.org/download.html)

---

## Используйте библиотеки **Imager Client** в своих проектах для формирования ссылок на миниатюры

Вы можете использовать различные языки программирования для работы с Imager.

### [Golang](https://github.com/pkg-ru/imager-client/blob/master/doc/GO-RU.md)

Для установки клиента Golang:

```bash
go get github.com/pkg-ru/imager-client
```

### [PHP](https://github.com/pkg-ru/imager-client/blob/master/doc/PHP-RU.md)

Для установки клиента PHP:

```bash
composer require pkg-ru/imager-client
```

### [JavaScript (TypeScript)](https://github.com/pkg-ru/imager-client/blob/master/doc/TS-RU.md)

Для установки клиента JavaScript (или TypeScript):

```bash
npm i imager-client
```

### [Python3](https://github.com/pkg-ru/imager-client/blob/master/doc/PY-RU.md)

Для установки клиента Python:

```bash
pip install imager-client
```
