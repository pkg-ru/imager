# CI-образ imager

Единый предварительно собранный toolchain для GitVerse-пайплайна
[`.gitverse/workflows/ci.yml`](../../workflows/ci.yml). Образ **не содержит**
исходников проекта — только инструменты и предзагруженный `GOMODCACHE`.

## Состав

| Компонент | Версия | Источник |
|-----------|--------|----------|
| Go | 1.27.0 | `golang:1.27.0-alpine3.23` |
| libvips + codecs | pinned (build-deps.sh) | Alpine 3.23 (mirror.yandex.ru) |
| ONNX Runtime | edge (C++23) | Alpine edge |
| ffmpeg / ffprobe | 8.0 | Alpine 3.23 (mirror.yandex.ru) |
| gofmt | встроен в Go 1.27 | `/usr/local/go/bin/gofmt` |
| govulncheck | latest | `go install golang.org/x/vuln/cmd/govulncheck@latest` |
| ONNX-модели | YuNet + SSD MobileNet + selfie.jpg | `/etc/imager/models` (download-models.sh) |
| GOMODCACHE | из `go.sum` | `go mod download` |

## Версионирование

Тег образа — **фиксированный** (immutable), не `latest`:
`gitverse.ru/pkg-ru/imager-ci:v<N>` (например `v1`).

Обновление образа — отдельное контролируемое изменение:

1. измените `Dockerfile` (или версии в `docker/build-deps.sh`);
2. соберите и опубликуйте новый тег (см. ниже);
3. обновите ссылку на образ в `.gitverse/workflows/ci.yml`.

## Сборка и публикация (локально, с Docker)

```bash
docker build -f .gitverse/docker/imager-ci/Dockerfile \
  -t gitverse.ru/pkg-ru/imager-ci:v1 .
docker login gitverse.ru
docker push gitverse.ru/pkg-ru/imager-ci:v1
```

## Проверка образа

```bash
docker run --rm gitverse.ru/pkg-ru/imager-ci:v1 sh -c 'go version && gofmt --version'