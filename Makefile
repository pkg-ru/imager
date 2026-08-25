# Imager — Makefile (единый источник истины для локальной разработки).
# Production-запуск — через docker-compose.yaml / Dockerfile.

# --- Локальная сборка и запуск (заменяют устаревшие bash/ скрипты) ---

.PHONY: install
install:
	go mod download
	go mod tidy

.PHONY: build
build:
	go build -tags libvips -trimpath -ldflags="-s -w" -o ./imager ./cmd/imager

.PHONY: run
run: build
	IMAGER_CONFIG_DIR=./config ./imager

.PHONY: stop
stop:
	@echo "Imager project stop"
	@taskkill /F /IM imager.exe 2>NUL || true

.PHONY: restart
restart: stop run

# --- Production quality gates (см. docs/PRODUCTION.md) ---

.PHONY: fmt
fmt:
	gofmt -l -w .

.PHONY: vet
vet:
	go vet ./...

.PHONY: test
test:
	go test ./...

.PHONY: race
race:
	go test -race ./...

.PHONY: fuzz
fuzz:
	go test -run=^$$ -fuzz=^FuzzParse$$ -fuzztime=10s ./domain/asset
	go test -run=^$$ -fuzz=^FuzzParseSize$$ -fuzztime=10s ./domain/asset

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

.PHONY: check
check: fmt vet test race
