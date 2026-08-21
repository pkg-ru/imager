.PHONY: install
install:
	chmod +x ./bash/install && ./bash/install

.PHONY: build
build:
	./bash/build

.PHONY: restart
restart:
	./bash/restart

.PHONY: run
run:
	./bash/run

.PHONY: stop
stop:
	./bash/stop

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
	go test -run=^$$ -fuzz=^FuzzParse$$ -fuzztime=10s ./internal/domain/asset
	go test -run=^$$ -fuzz=^FuzzParseSize$$ -fuzztime=10s ./internal/domain/asset

.PHONY: build-prod
build-prod:
	cd cmd/imager && go build -trimpath -ldflags="-s -w" -o ../../imager .

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
