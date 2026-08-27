# Imager — Makefile (единый источник истины для локальной разработки).
# Production-запуск — через docker-compose.yaml / Dockerfile.

# --- Локальная сборка и запуск ---

.PHONY: install
install:
	go mod download
	go mod tidy

# Основная сборка: с libvips (production-сценарий). Для сборки без тегов
# используйте `go build ./cmd/imager` или `make build TAGS=`.
TAGS ?= libvips

.PHONY: build
build:
	go build -tags "$(TAGS)" -trimpath -ldflags="-s -w" -o ./imager ./cmd/imager

.PHONY: run
run: build
	IMAGER_CONFIG_DIR=./config ./imager

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

.PHONY: build-prod
build-prod:
	cd cmd/imager && go build -tags libvips -trimpath -ldflags="-s -w" -o ../../imager .

.PHONY: docker-build
docker-build:
	docker build -t imager:production .

.PHONY: docker-up
docker-up:
	docker compose up -d --build

.PHONY: docker-down
docker-down:
	docker compose down

# Полный локальный прогон, эквивалентный CI (без fuzz).
.PHONY: check
check: fmt-check vet test race
