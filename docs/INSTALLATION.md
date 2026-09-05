# Установка

## Требования

| Компонент | Назначение | Обязательность |
|-----------|------------|----------------|
| Go ≥ 1.27 | Сборка из исходников | Да (для локальной сборки) |
| libvips ≥ 8.13 + заголовки (`vips-dev`) | Основной движок обработки, все форматы включая APNG | Рекомендуется |
| C-компилятор (`gcc`/`build-base`), `pkgconf`, `musl-dev` | cgo-сборка govips | Нужны при сборке с `-tags libvips` |
| Кодеки: `libheif`, `libde265`, `libjxl`, `librsvg`, `poppler`, `libraw` | HEIF/AVIF, JPEG XL, SVG, PDF, RAW в libvips | Для соответствующих форматов |
| ONNX Runtime (`libonnxruntime`) | Детекция лиц/объектов для `fc`/`oc` | Опциональна; сборка с `-tags onnx` |

> Для установки готовых релизов достаточно runtime-зависимостей из разделов
> ниже (Ubuntu/macOS/Windows/Docker); компиляторный toolchain не нужен.

## Быстрая установка (производственная, одной командой)

На Linux/macOS (curl или wget + git):

```bash
curl -fsSL https://raw.githubusercontent.com/pkg-ru/imager/main/docker/install-host.sh | sh
```

Установка конкретной версии:

```bash
curl -fsSL https://raw.githubusercontent.com/pkg-ru/imager/main/docker/install-host.sh | IMAGER_VERSION=1.0.0 sh
```

`install-host.sh` — оркестратор: при запуске не из клона репозитория он
выполняет `git clone --depth 1 https://gitverse.ru/pkg-ru/imager.git` во
временный каталог (fallback: `https://github.com/pkg-ru/imager.git`) и
запускается оттуда, затем:

1. определяет ОС/архитектуру (`lib.sh`: `detect_os`/`detect_arch`);
2. устанавливает системные зависимости (`install-deps-ubuntu.sh` /
   `install-deps-macos.sh` / `install-deps-windows.ps1`);
3. скачивает ONNX-модели (`download-models.sh`, каталог `IMAGER_MODELS_DIR`,
   по умолчанию `/etc/imager/models`);
4. скачивает и устанавливает релизный бинарь (`install-imager.sh`, в
   `INSTALL_DIR`, по умолчанию `/usr/local/bin`).

Полезные переменные окружения:

| Переменная | По умолчанию | Назначение |
|------------|--------------|------------|
| `IMAGER_VERSION` | `latest` | Тег релиза (`v1.2.3`, `1.0.0`) или `latest` |
| `INSTALL_DIR` | `/usr/local/bin` | Каталог установки бинаря |
| `IMAGER_MODELS_DIR` | `/etc/imager/models` | Каталог ONNX-моделей |
| `IMAGER_SKIP_DEPS=1` | — | Пропустить установку системных зависимостей |
| `IMAGER_SKIP_MODELS=1` | — | Пропустить скачивание моделей |

Пример установки в домашний каталог (без sudo):

```bash
curl -fsSL https://raw.githubusercontent.com/pkg-ru/imager/main/docker/install-host.sh \
    | IMAGER_VERSION=1.0.0 INSTALL_DIR=$HOME/.local/bin sh
```

## Сборка

### С libvips (основной сценарий)

```bash
go build -tags libvips -trimpath -ldflags="-s -w" -o imager ./cmd/imager
```

libvips работает in-process (govips, cgo) и покрывает все выходные форматы: JPEG, PNG, WebP, GIF, AVIF, HEIF, APNG, JPEG XL.

### Без libvips

```bash
go build -trimpath -ldflags="-s -w" -o imager ./cmd/imager
```

Такая сборка не содержит движка обработки изображений (libvips не скомпилирован)
и подходит только для разработки/CI. Для обработки изображений соберите с
`-tags libvips`.

### С детекцией лиц/объектов

```bash
go build -tags libvips,onnx -trimpath -ldflags="-s -w" -o imager ./cmd/imager
```

Требуется установленная C-библиотека ONNX Runtime и модели в секции `detection` конфигурации (см. [PROCESSING.md](PROCESSING.md#детекция-лиц-и-объектов)).

## Установка на Ubuntu/Debian (пошагово)

Скрипты рассчитаны на root/sudo:

```bash
# 1. Системные зависимости (libvips + кодеки, ffmpeg, ONNX Runtime)
sudo sh docker/install-deps-ubuntu.sh

# 2. ONNX-модели (YuNet для fc, SSD MobileNet для oc) в каталог моделей
IMAGER_MODELS_DIR=${IMAGER_MODELS_DIR:-/etc/imager/models}
sudo mkdir -p "$IMAGER_MODELS_DIR"
sudo IMAGER_MODELS_DIR="$IMAGER_MODELS_DIR" sh docker/download-models.sh

# 3. Релизный бинарь imager (по умолчанию latest → /usr/local/bin)
sudo sh docker/install-imager.sh          # или IMAGER_VERSION=1.0.0
```

Пакеты apt: `libvips-dev`, `libheif-dev`, `libde265-0`, `libjxl-dev`,
`librsvg2-dev`, `libpoppler-dev`, `libpoppler-glib-dev`, `libraw-dev`, `ffmpeg`,
`pkg-config`, `build-essential`, `ca-certificates`, `curl`. ONNX Runtime
устанавливается из prebuilt `.tgz` с `github.com/microsoft/onnxruntime/releases`
в `/usr/local/lib` + `ldconfig`.

## Установка на macOS (пошагово)

Требуется [Homebrew](https://brew.sh):

```bash
# 1. Системные зависимости: vips, ffmpeg, onnxruntime, pkg-config
bash docker/install-deps-macos.sh

# 2. ONNX-модели
IMAGER_MODELS_DIR=${IMAGER_MODELS_DIR:-/etc/imager/models}
mkdir -p "$IMAGER_MODELS_DIR"
IMAGER_MODELS_DIR="$IMAGER_MODELS_DIR" sh docker/download-models.sh

# 3. Релизный бинарь imager
bash docker/install-imager.sh            # или IMAGER_VERSION=1.0.0
```

Для установки без sudo задайте `INSTALL_DIR` в каталог из `PATH`
(например `$HOME/.local/bin`).

## Установка на Windows (пошагово)

Скрипты PowerShell (требуются winget и доступ в интернет):

```powershell
# 1. Системные зависимости: FFmpeg (winget Gyan.FFmpeg), libvips (prebuilt),
#    onnxruntime (prebuilt); DLL кладутся в %LOCALAPPDATA%\imager\bin и
#    добавляются в PATH (user-scope)
powershell -ExecutionPolicy Bypass -File docker\install-deps-windows.ps1

# 2. ONNX-модели (через sh, если доступен; иначе вручную, см. download-models.sh)
#    sh docker/download-models.sh

# 3. Релизный бинарь imager (POSIX-оболочка: Git Bash / WSL)
#    sh docker/install-imager.sh
```

После шага 1 перезапустите оболочку, чтобы подхватился PATH. imager.exe
запускается только там, где рядом с ним (или в PATH) лежат `libvips-*.dll`,
`onnxruntime.dll`, `ffmpeg.dll` и сопутствующие DLL платформы libvips.

## Запуск

```bash
IMAGER_CONFIG_DIR=./setting ./imager
```

Переменные окружения: `IMAGER_CONFIG_DIR` (путь к каталогу с файлами конфигурации; по умолчанию текущий каталог) и `IMAGER_S3_ACCESS_KEY`/`IMAGER_S3_SECRET_KEY` (S3-credentials; значение из YAML приоритетнее). Обязателен `server.yaml`; остальные файлы (`server-local.yaml`, `generate.yaml`/`generate-local.yaml`, `failback.yaml`/`failback-local.yaml`) — опциональны. Три слоя конфигурации описаны в [CONFIGURATION.md](CONFIGURATION.md#загрузка-конфигурации).

## Docker (производственный образ)
### Готовый образ (рекомендуется)

```bash
docker pull altrap/imager
```

Запуск, hardening и bind-mounts — в [DEPLOYMENT.md](DEPLOYMENT.md#запуск).

### Сборка образа из релиза (GitHub releases)

`altrap/imager` — образ на Docker Hub (учётка `altrap`, не репозиторий кода).
Релизы кода публикуются в `gitverse.ru/pkg-ru/imager` (основной) и
`github.com/pkg-ru/imager` (зеркало).

```bash
docker build --target from-release --build-arg IMAGER_VERSION=1.0.0 -t altrap/imager .
# без версии — latest-релиз
docker build --target from-release -t altrap/imager .
```

> `--target from-release` обязателен: без него `docker build` собирает
> последнюю стадию Dockerfile (`from-source`) и `IMAGER_VERSION` игнорируется.

Target `from-release`: бинарь скачивается fetcher-стадией
(`alpine:3.23` + `ca-certificates`/`curl` + `docker/install-imager.sh` +
`docker/lib.sh`) из GitHub releases (`github.com/pkg-ru/imager`, fallback
теги `gitverse.ru/pkg-ru/imager`) — Go toolchain и исходники не нужны.

HEALTHCHECK образа опрашивает `http://127.0.0.1:8080/healthz`.

#### Build-args

| ARG | По умолчанию | Назначение |
|-----|--------------|------------|
| `IMAGER_VERSION` | `latest` | Тег релиза или `latest` (target `from-release`) |
| `GOFLAGS` | `-buildvcs=false` | Флаги Go (builder `from-source`) |
| `BUILD_TAGS` | `libvips,onnx` | Build tags (builder `from-source`) |

### Сборка образа из исходников

```bash
docker build --target from-source -t imager:from-source .
```

Target `from-source`: builder `golang:1.27.0-alpine3.23` + libvips/кодеки/onnxruntime,
бинарный файл собирается с `-tags libvips,onnx`.

Runtime-стадия (`alpine:3.23`) содержит libvips, heif, de265, jxl, poppler,
libraw, rsvg, ghostscript, ffmpeg, onnxruntime; non-root `imager` (uid 10001);
пакетные списки — единый источник [`docker/build-deps.sh`](../docker/build-deps.sh);
каталог конфигурации — `/etc/imager` (env `IMAGER_CONFIG_DIR`).

### Релизный цикл через Makefile

```bash
make docker-build-release IMAGER_VERSION=1.0.0   # build: altrap/imager:latest + altrap/imager:<version>
make docker-push IMAGER_VERSION=1.0.0            # push обоих тегов
make docker-release IMAGER_VERSION=1.0.0         # build + push
make docker-build-from-source                    # сборка из исходников
```

`VERSION_TAG` резолвится тем же механизмом, что и `docker/install-imager.sh`
(`docker/lib.sh`: GitHub API `releases/latest` на `github.com/pkg-ru/imager`
→ fallback `git ls-remote --tags` на `gitverse.ru/pkg-ru/imager`).

## Docker Compose

```bash
docker compose up -d --build
```

[`docker-compose.yaml`](../docker-compose.yaml) реализует production-hardening (tmpfs для `/tmp`, `cap_drop: ALL`, `no-new-privileges:true`, лимиты ресурсов, health-check по `/healthz`) и bind-mounts: `./setting` → `/etc/imager/setting:ro`, `./models` → `/etc/imager/models:rw` (entrypoint скачивает модели при старте), `./data/source` → `/data/source:ro`, `./data/result` → `/data/result:rw`. Подробности hardening — [DEPLOYMENT.md](DEPLOYMENT.md#укрепление-контейнера-hardening).

## Локальная разработка

Для упрощённой локальной разработки используйте цели [`Makefile`](../Makefile) (`make install`, `make build`, `make run`, `make stop`, `make restart`). Для production используйте Docker/Compose или прямую сборку `go build -tags libvips ./cmd/imager`.

## Проверка установки

```bash
# liveness/readiness
curl http://localhost:8080/healthz
curl http://localhost:8080/readyz

# метрики
curl http://localhost:8080/metrics

# генерация ассета (файл data/source/test.jpg должен существовать)
curl -o out.webp http://localhost:8080/test-jpg/thumb.webp
