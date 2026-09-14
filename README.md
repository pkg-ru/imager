# Imager

Imager — сервис обработки изображений на лету: генерирует, кэширует и отдаёт
изображения по каноническим URL без предварительной генерации или этапа сборки.
Источники и результаты могут находиться в локальной файловой системе,
S3-совместимом объектном хранилище, SFTP, FTP/FTPS или читаться по HTTP.

```text
GET /photos/city-skyline-jpg/300x@2.webp
→ 200 OK (WebP, 300 px wide, DPR 2), Cache-Control: public, max-age=31536000, immutable
```

## Возможности

- **Канонические URL изображений** — детерминированные URL кодируют источник,
  преобразование, размер, DPR и формат вывода; результаты неизменяемы и
  дружественны к CDN.
- **Пресеты** — именованные преобразования (`thumb@2`), разрешённые в
  конфигурации.
- **Преобразования** — изменение размера, центральная обрезка, trim, smart-crop
  (attention-based), face-crop и object-crop через ONNX-модели детекции.
- **Форматы** — JPEG, PNG, WebP, GIF, AVIF, HEIF/HEIC, JPEG XL.
- **Анимированные изображения** — GIF/WebP с лимитами на кадры и
  длительность. APNG-входы читаются как анимированный PNG; запись APNG
  возможна только при самостоятельной сборке libvips с libspng
  (см. [INSTALLATION.md](docs/INSTALLATION.md)).
- **Водяные знаки** — настраиваемое наложение с кэшированием.
- **Политика deny-by-default** — path-policies по префиксам пути разрешают
  только явно перечисленные пресеты/custom-размеры; жёсткие лимиты (байты
  источника/результата, пиксели, кадры, длительность) — в `application.limits`.
- **Бэкенды хранилища** — `fs`, `s3`, `sftp`, `ftp`/`ftps` (источник и результат
  независимо друг от друга), read-only источники `http`.
- **Наблюдаемость** — структурированное JSON-логирование, метрики Prometheus на
  `/metrics`, health-check эндпоинты (`/healthz`, `/readyz`).
- **Безопасность по умолчанию** — строгая YAML-схема (`UnmarshalStrict`),
  ограниченные тела запросов, admission control, защищённые от symlink операции
  с файлами.

## Быстрый старт

### Готовый образ (Docker Hub)

Без клонирования репозитория и сборки — только готовый образ
`altrap/imager` и ваши каталоги с данными. Все базовые конфиги
(`server.yaml`, `generate.yaml`, `failback.yaml`) уже в образе: при старте
entrypoint подтянет их в смонтированный каталог конфигурации, если там их
нет.

```bash
# 1. Каталоги: конфигурация (можно пустой), исходники, результаты
mkdir -p setting data/source data/result
chmod -R a+rwX data/result            # запись нужна uid 10001 (imager)

docker run -d --name imager -p 8080:8080 \
  -v ./setting:/etc/imager/setting:rw \
  -v ./data/source:/data/source:ro \
  -v ./data/result:/data/result:rw \
  -e IMAGER_CONFIG_DIR=/etc/imager/setting \
  altrap/imager:latest

curl http://localhost:8080/healthz                      # {"status":"alive"}
curl -o out.webp http://localhost:8080/test-jpg/x.webp  # ассет в webp
```

Обязательные volumes: `setting` (конфигурация; пустой каталог заполнится
дефолтами образа при старте), `data/source` (исходники), `data/result`
(результаты). Опциональные: `./models:/etc/imager/models:rw` — каталог
ONNX-моделей (entrypoint скачает их при старте; без монтирования модели
попадут в анонимный volume и скачаются заново при пересоздании контейнера).
Порт — `8080` (plain HTTP, TLS — на reverse-proxy).

**Переопределение конфигурации** — два способа:

- **Только `*-local.yaml`** (рекомендуется): монтируйте пустой `./setting`
  и кладите туда только `server-local.yaml` / `generate-local.yaml` /
  `failback-local.yaml` — они глубоко мержатся поверх базовых конфигов
  образа (см. [docs/CONFIGURATION.md](docs/CONFIGURATION.md#загрузка-конфигурации));
- **Все конфиги целиком**: положите в `./setting` полный набор
  `server.yaml` + `generate.yaml` + `failback.yaml` (+ `*-local.yaml`) —
  они полностью заменят дефолты образа.

### Docker Compose

Тот же запуск через compose (минимальный вариант; опции для production
закомментированы):

```yaml
services:
  imager:
    image: altrap/imager:latest
    restart: unless-stopped
    stop_signal: INT
    stop_grace_period: 15s
    ports:
      - "8080:8080"
    environment:
      IMAGER_CONFIG_DIR: /etc/imager/setting
    volumes:
      - ./setting:/etc/imager/setting:rw
      - ./data/source:/data/source:ro
      - ./data/result:/data/result:rw
      # - ./models:/etc/imager/models:rw   # опционально (ONNX-модели)

    # --- Опции для production (раскомментируйте при необходимости) ---
    # Resource limits (обязательные для production).
    # deploy:
    #   resources:
    #     limits:
    #       cpus: "2.0"
    #       memory: 2G
    #     reservations:
    #       cpus: "0.25"
    #       memory: 128M
```

```bash
docker compose up -d
```

Production-вариант с hardening (лимиты ресурсов, health-check, tmpfs) —
[`docker-compose.yaml`](docker-compose.yaml) в корне репозитория. Подробнее —
[docs/DEPLOYMENT.md](docs/DEPLOYMENT.md#быстрый-старт-готовый-образ).

### Сборка из исходников

Требуется **Go ≥ 1.27**. Сборка по умолчанию использует процессоры-заглушки и
подходит для разработки и CI:

```bash
go build -o imager ./cmd/imager
IMAGER_CONFIG_DIR=./setting ./imager
```

Продакшен-сборка включает libvips (нужен CGO):

```bash
# Debian/Ubuntu: sudo apt-get install libvips-dev build-essential pkg-config
go build -tags libvips -trimpath -ldflags="-s -w" -o imager ./cmd/imager
```

С детекцией лиц/объектов (ONNX Runtime):

```bash
go build -tags "libvips,onnx" -trimpath -ldflags="-s -w" -o imager ./cmd/imager
```

Все варианты сборки и зависимости кодеков — в
[docs/INSTALLATION.md](docs/INSTALLATION.md).

## Требования

| Компонент | Назначение | Обязательность |
|-----------|------------|----------------|
| Go ≥ 1.27 | Сборка из исходников | Да (для локальных сборок) |
| libvips ≥ 8.16 + заголовки | Основной движок обработки (все форматы) | Рекомендуется |
| C-компилятор, `pkg-config` | CGO-сборка govips (`-tags libvips`) | При `-tags libvips` |
| Кодеки: libheif, libde265, libjxl, librsvg, poppler, libraw | HEIF/AVIF, JPEG XL, SVG, PDF, RAW | Для соответствующих форматов |
| ONNX Runtime (`libonnxruntime`) | Детекция лиц/объектов (преобразования `fc`/`oc`) | Опционально (`-tags onnx`) |
| ffmpeg | Извлечение кадров видео | Опционально |

## Конфигурация

Все настройки задаются в YAML; CLI-флагов у приложения нет. Переменные
окружения: `IMAGER_CONFIG_DIR` (каталог с файлами конфигурации),
`IMAGER_MODELS_DIR` (каталог ONNX-моделей; fallback для путей
`detection.face-model`/`detection.object-model`) и
`IMAGER_S3_ACCESS_KEY`/`IMAGER_S3_SECRET_KEY` (S3-credentials; значение из YAML
приоритетнее). Конфигурация
разделена на три слоя, каждый переопределяется файлом `-local.yaml`,
игнорируемым git:

| Слой | Файлы | Содержимое |
|------|-------|------------|
| setting | `server.yaml` + `server-local.yaml` | Сервер, хранилище, наблюдаемость, admin, единая секция кодирования `encoders` (обязательный базовый файл) |
| generate | `generate.yaml` + `generate-local.yaml` | Пресеты, политика, native-переопределения кодеков в пресетах, водяные знаки, детекция |
| failback | `failback.yaml` + `failback-local.yaml` | Обработка not-found, source-fallback |

Секреты хранятся в файлах `*-local.yaml` (не коммитятся). Полный справочник —
в [docs/CONFIGURATION.md](docs/CONFIGURATION.md), примеры с комментариями —
в [setting/](setting/).

## Структура проекта

```text
imager.go              Public library facade (NewServer/New)
cmd/imager/            Binary entry point
adapters/
  httpapi/             HTTP transport, config loading, runtime wiring
  processor/
    libvips/           libvips engine (build tag: libvips)
    detection/         ONNX face/object detection (build tag: onnx)
    routing/           Processor selection
  storage/             fs, s3, sftp, ftp/ftps, http adapters
  videoframe/ffmpeg/   Video frame extraction
app/                   Application services (generatev2, adminsvc)
domain/                Pure domain logic (asset parsing, policy, processing)
ports/                 Interface contracts between layers
coordination/          In-process singleflight
observability/         Logging, metrics, middleware
bootstrap/             Process bootstrap helpers
setting/               Example configuration files
docs/                  Documentation
```

Проект построен по архитектуре ports-and-adapters: `domain` не имеет внешних
зависимостей, `ports` определяет интерфейсы, `adapters` их реализуют. Build
tags `libvips` и `onnx` переключают реализации адаптеров; без внешних
C-зависимостей компилируются заглушки, поэтому любая комбинация собирается.

## Документация

| Документ | Содержимое |
|----------|------------|
| [docs/API.md](docs/API.md) | Формат URL изображений, эндпоинты, преобразования |
| [docs/CONFIGURATION.md](docs/CONFIGURATION.md) | Полный справочник конфигурации |
| [docs/INSTALLATION.md](docs/INSTALLATION.md) | Зависимости и инструкции по сборке |
| [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md) | Продакшен-развёртывание, защита контейнера |
| [docs/PROCESSING.md](docs/PROCESSING.md) | Конвейер обработки, форматы, водяные знаки |
| [docs/STORAGE.md](docs/STORAGE.md) | Бэкенды хранилищ и их настройки |
| [docs/SECURITY.md](docs/SECURITY.md) | Политика авторизации, лимиты, безопасность URL |
| [docs/NGINX.md](docs/NGINX.md) | Настройка reverse-proxy |

## Разработка

На Linux/macOS (без установки libvips/onnxruntime на хост — через
предварительно собранный CI-образ [`.gitverse/docker/imager-ci`](.gitverse/docker/imager-ci/README.md)):

```bash
make docker-test        # go test -tags "libvips onnx" ./...
make docker-test-race   # go test -race -tags "libvips onnx" ./...
make docker-check       # fmt-check + test + race + govulncheck (как CI)
make docker-govulncheck # govulncheck ./...
```

Локально (требуются libvips + ONNX Runtime на хосте):

```bash
make install   # download and tidy modules
make test      # run all tests
make race      # run tests with the race detector
make vet       # go vet
make fmt       # gofmt
make check     # fmt + vet + test + race
make fuzz      # fuzz smoke tests
```

На Windows — PowerShell-раннер [`make.ps1`](make.ps1) (аналог Makefile):

```powershell
.\make.ps1 install       # go mod download + tidy
.\make.ps1 test          # go test ./...
.\make.ps1 docker-test   # go test (libvips,onnx) в CI-образе (Docker Desktop)
.\make.ps1 check         # fmt-check + vet + test + race
.\make.ps1 help          # список всех целей
```

CI ([.gitverse/workflows/ci.yml](.gitverse/workflows/ci.yml)) собирает и тестирует
все комбинации build tags (`default`/`onnx` на Linux и Windows, `libvips`/
`libvips,onnx` на Linux), запускает `go vet`, `go test -race` (Linux),
`gofmt`, `govulncheck`, fuzz smoke-тесты и сканирование контейнера Trivy.

## Лицензия и правообладатель

© 2025 [Алтухов Владислав Владимирович](https://altuh.ru/about).

Проект распространяется по лицензии [GNU General Public License v3.0](LICENSE).
