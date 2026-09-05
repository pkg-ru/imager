# Imager — Makefile (единый источник истины для локальной разработки).
# Production-запуск — через docker-compose.yaml / Dockerfile.

# --- Локальная сборка и запуск ---

.PHONY: install
install:
	go mod download
	go mod tidy

# Основная сборка: с libvips + onnx (production-сценарий). Для сборки без
# тегов используйте `go build ./cmd/imager` или `make build TAGS=`.
# onnx требует C-библиотеку ONNX Runtime (libonnxruntime) и cgo.
TAGS ?= libvips onnx

.PHONY: build
build:
	go build -tags "$(TAGS)" -trimpath -ldflags="-s -w" -o ./imager ./cmd/imager

.PHONY: run
run: build
	IMAGER_CONFIG_DIR=./setting ./imager

.PHONY: stop
stop:
	@echo "Imager project stop"
	-taskkill /F /IM imager.exe 2>NUL
	-pkill -f './imager' 2>/dev/null || true

.PHONY: restart
restart: stop run

# --- Quality gates ---

.PHONY: fmt
fmt:
	gofmt -l -w .

# Проверка форматирования без изменения файлов (как в CI).
.PHONY: fmt-check
fmt-check:
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "Unformatted files:"; echo "$$unformatted"; exit 1; \
	fi

.PHONY: vet
vet:
	go vet ./...

# Проверка всех комбинаций build tags: stub-ветки (!libvips/!onnx) и полные.
# Комбинация с libvips требует установленного libvips + cgo; при отсутствии
# зависимостей используйте `make tags-check` на машине с libvips или CI.
.PHONY: tags-check
tags-check:
	go build ./...
	go vet ./...
	go build -tags onnx ./...
	go vet -tags onnx ./...
	go build -tags "libvips onnx" ./...
	go vet -tags "libvips onnx" ./...

# --- ONNX Runtime ---

# Сборка с реальным ONNX Runtime (требует libonnxruntime + cgo).
.PHONY: build-onnx
build-onnx:
	go build -tags "libvips onnx" -trimpath -ldflags="-s -w" -o ./imager ./cmd/imager

# Тесты реального инференса (YuNet + SSD) с тегом onnx.
# Требует модели в ./models и libonnxruntime.
.PHONY: test-onnx
test-onnx:
	go test -tags onnx ./adapters/processor/detection/... -count=1

# Полный прогон тестов с libvips + onnx (как в Docker).
.PHONY: test-onnx-all
test-onnx-all:
	go test -tags "libvips onnx" ./... -count=1

.PHONY: test
test:
	go test ./... -count=1

.PHONY: race
race:
	go test -race ./... -count=1

.PHONY: fuzz
fuzz:
	go test -run=^$$ -fuzz=^FuzzParse$$ -fuzztime=10s ./domain/asset
	go test -run=^$$ -fuzz=^FuzzParseSize$$ -fuzztime=10s ./domain/asset

# --- Production / Docker ---

# Имя образа и версия для docker-build-release/docker-push/docker-release.
# IMAGER_IMAGE — образ на Docker Hub (altrap/imager); это УЧЁТКА Docker Hub,
# не репозиторий кода. Репозитории кода: gitverse.ru/pkg-ru/imager (основной)
# и github.com/pkg-ru/imager (зеркало).
# IMAGER_VERSION принимает тег релиза ("v1.2.3", "1.0.0") или "latest" —
# конкретная версия резолвится тем же механизмом, что docker/install-imager.sh
# (docker/lib.sh: GitHub API releases/latest github.com/pkg-ru/imager,
# fallback git ls-remote --tags gitverse.ru/pkg-ru/imager).
IMAGER_IMAGE ?= altrap/imager
IMAGER_VERSION ?= latest

VERSION_TAG := $(shell sh -c '. "docker/lib.sh" 2>/dev/null && resolve_release_version "$(IMAGER_VERSION)" 2>/dev/null')
ifeq ($(strip $(VERSION_TAG)),)
VERSION_TAG := $(IMAGER_VERSION)
endif

.PHONY: build-prod
build-prod:
	cd cmd/imager && go build -tags libvips -trimpath -ldflags="-s -w" -o ../../imager .

# Сборка образа из исходников (локально/CI).
.PHONY: docker-build
docker-build:
	docker build -t imager:production .

# Сборка прод-образа из GitHub releases (default target from-release); см.
# Dockerfile. Сборка из исходников — docker-build-from-source.
# Пример: make docker-build-release IMAGER_VERSION=1.0.0
# --target from-release обязателен: без него docker build собирает последнюю
# стадию Dockerfile (from-source) и IMAGER_VERSION игнорируется.
.PHONY: docker-build-release
docker-build-release:
	docker build --target from-release --build-arg IMAGER_VERSION=$(IMAGER_VERSION) \
		-t $(IMAGER_IMAGE):latest \
		-t $(IMAGER_IMAGE):$(VERSION_TAG) .

# Публикация прод-образа в registry (после docker-build-release).
.PHONY: docker-push
docker-push:
	docker push $(IMAGER_IMAGE):latest
	docker push $(IMAGER_IMAGE):$(VERSION_TAG)

# Сборка + публикация релиза.
.PHONY: docker-release
docker-release: docker-build-release docker-push

# Сборка прод-образа из исходников (builder-стадия, target from-source).
.PHONY: docker-build-from-source
docker-build-from-source:
	docker build --target from-source -t $(IMAGER_IMAGE):from-source .

.PHONY: docker-up
docker-up:
	docker compose up -d --build

.PHONY: docker-down
docker-down:
	docker compose down

# Полный локальный прогон, эквивалентный CI (без fuzz).
.PHONY: check
check: fmt-check vet test race
