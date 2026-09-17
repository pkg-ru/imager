# Разработка

Документ описывает структуру проекта, сборку, тестирование и CI/CD для разработчиков Imager.

Связанные документы: [ARCHITECTURE.md](ARCHITECTURE.md), [DEPLOYMENT.md](DEPLOYMENT.md), [TROUNLESHOOTING.md](TROUNLESHOOTING.md).

## Структура проекта

```
adapters/          # Реализации портов (HTTP API, хранилища, процессоры, детекция, видео)
app/               # Application use cases (generatev2, adminsvc, learning)
bootstrap/         # Переиспользуемая composition-root логика
cmd/imager/        # Тонкая обёртка над публичным фасадом
composition/       # Composition root: сборка приложения, конфигурация
config/            # Typed config foundation
coordination/      # Singleflight
domain/            # Доменный слой (asset, policy, processing, encoding, filemeta, object)
govips/            # Vendored-форк govips (libvips bindings)
internal/          # Внутренние утилиты (testutil)
models/            # ONNX-модели (скачиваются)
observability/     # Метрики, логи, health
ports/             # Интерфейсы (storage, bounded)
setting/           # Примеры конфигурации (setting/, generate/, failback/)
web/               # Статические ресурсы
docker/            # Скрипты установки и entrypoint
```

## Требования

- Go 1.27.0 (модуль `gitverse.ru/pkg-ru/imager`).
- libvips ≥ 8.16 (CGO, govips), тег сборки `libvips`.
- ONNX Runtime (опционально), тег сборки `onnx`.
- ffmpeg/ffprobe (для видео).
- Для Alpine/musl: `CGO_LDFLAGS="-no-pie"` (фикс gcc 15 PIE/DT_TEXTREL).

## Makefile

Основные цели:

| Цель | Описание |
|------|----------|
| `make install` | Установка зависимостей |
| `make build` | Сборка (по умолчанию `TAGS=libvips onnx`) |
| `make run` / `make stop` / `make restart` | Управление сервисом |
| `make fmt` / `make fmt-check` | Форматирование / проверка |
| `make vet` | Статический анализ |
| `make tags-check` | Проверка build-тегов |
| `make build-onnx` | Сборка с ONNX |
| `make test-onnx` / `make test-onnx-all` | Тесты ONNX |
| `make test` | Все тесты |
| `make race` | Гонки (race detector) |
| `make fuzz` | Фаззинг (FuzzParse, FuzzParseSize) |
| `make build-prod` | Production-сборка |
| `make docker-build` / `docker-build-release` / `docker-push` / `docker-release` | Docker |
| `make docker-build-from-source` | Docker из исходников |
| `make docker-up` / `docker-down` | Docker Compose |
| `make docker-test` / `docker-test-race` / `docker-vet` / `docker-tags-check` / `docker-fmt-check` / `docker-govulncheck` / `docker-check` | Проверки в Docker |
| `make check` | Полная проверка |

Для Windows: `make.ps1` (аналог Makefile).

## Сборка

```bash
make install
make build          # TAGS=libvips onnx
make test
```

Сборка с ONNX:

```bash
make build-onnx
make test-onnx
```

## Тестирование

- Юнит-тесты: `make test`.
- Тесты гонок: `make race`.
- Фаззинг: `make fuzz` (цели `FuzzParse`, `FuzzParseSize` в `domain/asset`).
- Интеграционные тесты: в `composition/` (сборка всего приложения).

## CI/CD

### GitVerse (тесты)

`.gitverse/workflows/ci.yml` — три джобы в образе `imager-ci:v1`:

1. **fast-ci**: `gofmt` + тесты.
2. **race**: гонки.
3. **security**: `govulncheck` + сборка.

### GitHub Actions (публикация Docker Hub)

`.github/workflows/docker-release.yml`:

1. Сборка образа.
2. Trivy-сканирование уязвимостей.
3. Публикация в Docker Hub (`altrap/imager`).
4. Проверка `/healthz` после публикации.

## Локальная разработка

```bash
make install
make build
make run
```

Конфигурация загружается из `setting/` (см. [CONFIGURATION.md](CONFIGURATION.md)). Для быстрой итерации используйте `setting/failback.yaml` (not-found pixel, source-fallback).

## Добавление нового формата

1. Убедитесь, что libvips поддерживает формат (проверьте `vips --list formats`).
2. Добавьте формат в `domain/processing/processing.go` (enum `Format`, `Animated()`, `SupportsAlpha`).
3. Добавьте маппинг кодеков в `domain/encoding/mapping.go` (если нужны переопределения).
4. Обновите `bootstrap/bootstrap.go` (LibvipsCaps: `Formats`, `SourceFormats`).
5. Добавьте тесты в `domain/processing/processing_test.go` и `domain/encoding/encoding_test.go`.
6. Обновите [FORMATS.md](FORMATS.md).

## Добавление новой операции

1. Добавьте операцию в enum `Operation` в `domain/processing/processing.go`.
2. Реализуйте обработку в `adapters/processor/libvips/processor.go`.
3. Добавьте валидацию в `domain/policy/compile.go` (если операция влияет на политику).
4. Добавьте тесты.

## Связанные документы

- [ARCHITECTURE.md](ARCHITECTURE.md) — архитектура и слои.
- [DEPLOYMENT.md](DEPLOYMENT.md) — развёртывание и Docker.
- [TROUNLESHOOTING.md](TROUNLESHOOTING.md) — диагностика проблем.
- [AI.md](AI.md) — сборка с ONNX.