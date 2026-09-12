# Production-развёртывание

## Запуск

### Быстрый старт (готовый образ)

Минимальный запуск готового образа `altrap/imager` с Docker Hub — без
клонирования репозитория и без сборки. Все базовые конфиги (`server.yaml`,
`generate.yaml`, `failback.yaml`) уже в образе: при старте entrypoint
подтянет их в смонтированный каталог конфигурации, если там их нет.

```bash
# 1. Каталоги: конфигурация (можно пустой), исходники, результаты
mkdir -p setting data/source data/result
chmod -R a+rwX data/result            # запись нужна uid 10001 (imager)

# 2. Исходник и запуск
cp /path/to/photo.jpg data/source/test.jpg
docker run -d --name imager -p 8080:8080 \
  -v ./setting:/etc/imager/setting:rw \
  -v ./data/source:/data/source:ro \
  -v ./data/result:/data/result:rw \
  -e IMAGER_CONFIG_DIR=/etc/imager/setting \
  altrap/imager:latest

# 3. Проверка
curl http://localhost:8080/healthz                      # {"status":"alive"}
curl -o out.webp http://localhost:8080/test-jpg/x.webp  # ассет в webp
```

#### Volumes: обязательные и опциональные

| Volume | Обязательность | Назначение |
|--------|----------------|------------|
| `./setting:/etc/imager/setting:rw` | **обязателен** | Конфигурация. Может быть **пустым**: entrypoint скопирует базовые конфиги (`server.yaml`, `generate.yaml`, `failback.yaml`) и шаблоны `*-local.yaml.example` из образа при первом старте. `:rw` — чтобы entrypoint мог создавать файлы |
| `./data/source:/data/source:ro` | **обязателен** | Исходные файлы (fs-source из конфига) |
| `./data/result:/data/result:rw` | **обязателен** | Результаты генерации (fs-result из конфига); uid 10001 должен иметь запись |
| `./models:/etc/imager/models:rw` | опционален | ONNX-модели. Без монтирования entrypoint скачает их в анонимный volume — при пересоздании контейнера скачивание повторится. Монтируйте, чтобы модели сохранялись на хосте |

#### Переопределение конфигурации

Два способа (см. [CONFIGURATION.md](CONFIGURATION.md#загрузка-конфигурации)):

- **Только `*-local.yaml`** (рекомендуется): монтируйте пустой `./setting`
  и кладите туда только `server-local.yaml` / `generate-local.yaml` /
  `failback-local.yaml`. Базовые конфиги подтянутся из образа, а `-local`
  файлы глубоко мержатся поверх них. Существующие файлы entrypoint никогда
  не перезаписывает.
- **Все конфиги целиком**: положите в `./setting` полный набор
  `server.yaml` + `generate.yaml` + `failback.yaml` (+ `*-local.yaml`) —
  они полностью заменят дефолты образа.

Что происходит при старте:

- entrypoint ([`docker/entrypoint.sh`](../docker/entrypoint.sh)) копирует
  отсутствующие базовые конфиги и шаблоны `*-local.yaml.example` из
  `/etc/imager` (дефолты образа) в `IMAGER_CONFIG_DIR`, затем создаёт
  `*-local.yaml` из шаблонов (только если файла ещё нет);
- entrypoint скачивает ONNX-модели в `IMAGER_MODELS_DIR` (по умолчанию
  `/etc/imager/models`; идемпотентно; без сети сервис всё равно стартует —
  детекция опциональна);
- конфигурация читается из `IMAGER_CONFIG_DIR` (три слоя: `server.yaml`,
  `generate.yaml`, `failback.yaml` + `*-local.yaml`);
- порт только `8080` (plain HTTP; TLS терминируется на reverse-proxy —
  см. [NGINX.md](NGINX.md)); портов 80/443, как в ранних версиях образа, нет.

Тот же запуск через docker-compose — положите `docker-compose.yaml` рядом
с каталогами `setting/`, `data/` (опции для production закомментированы):

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

> **Права на каталоги.** Контейнер работает от non-root `imager` (uid 10001):
> `./data/result` (и `./models`, если монтируете) должны быть доступны ему
> на запись (`chmod -R a+rwX` или `chown -R 10001:10001`), иначе публикация
> результатов и автоскачивание моделей не сработают (сервис при этом
> стартует с warning).

Для production используйте полный вариант ниже (hardening, лимиты ресурсов,
health-check) — [`docker-compose.yaml`](../docker-compose.yaml) в корне
репозитория или ручной `docker run` из раздела «Docker вручную».

### Pull готового образа (основной путь)

```bash
docker pull altrap/imager:latest
# или конкретная версия
docker pull altrap/imager:1.0.0
```

Образ `altrap/imager` берётся с Docker Hub (учётка `altrap`, не репозиторий
кода) и собирается из релизов (`Dockerfile`, target `from-release`):
бинарь `imager` скачивается fetcher-стадией с `gitverse.ru/pkg-ru/imager`
(основной; fallback — теги зеркала `github.com/pkg-ru/imager`)
(`IMAGER_VERSION`, см. [INSTALLATION.md](INSTALLATION.md#build-args)).
Теги публикуются через `make docker-release IMAGER_VERSION=<tag>` или
автоматически при релизе (GitHub Actions, workflow
[`.github/workflows/docker-release.yml`](../.github/workflows/docker-release.yml);
GitVerse — зеркало, см. [CI](#ci)).

Последующие шаги (mounts, пользователь, env) — одинаковы для pull'нутого и
для собранного вручную образа.

### Docker Compose (рекомендуется)

```bash
docker compose up -d --build
```

Конфигурация монтируется из `./setting` в `/etc/imager/setting` read-only, каталог моделей `./models` — в `/etc/imager/models` **read-write**. Модели **не входят в образ** и **не требуют ручного размещения**: при старте контейнера entrypoint (`docker/entrypoint.sh`) скачивает их в смонтированный каталог (`docker/download-models.sh`, источники — OpenCV Zoo и ONNX Model Zoo по умолчанию) и сохраняет на хосте в `./models`, так что при перезапуске скачивание не повторяется. Если скачивание не удалось (нет сети/зеркала) — контейнер продолжает запуск с предупреждением: детекция опциональна, операции `fc`/`oc` просто недоступны (см. [CONFIGURATION.md](CONFIGURATION.md#detection)).

> **Права на каталог.** Контейнер работает от non-root `imager` (uid 10001). Чтобы entrypoint мог скачивать модели, сделайте хост-каталог `./models` доступным на запись этому uid: `chmod -R a+rwX ./models` (либо `chown 10001:10001 ./models`).

Env-переменные: `IMAGER_CONFIG_DIR=/etc/imager/setting` (каталог конфигурации) и `IMAGER_MODELS_DIR=/etc/imager/models` (каталог ONNX-моделей — **и** для автоскачивания, **и** fallback для пустых `detection.face-model`/`object-model`, см. [CONFIGURATION.md](CONFIGURATION.md#detection)). Опциональные `IMAGER_MODEL_FACE_URL` / `IMAGER_MODEL_OBJECT_URL` переопределяют источники скачивания (приватные зеркала), `IMAGER_SKIP_MODELS=1` отключает автоскачивание. Три слоя конфигурации (`setting`/`generate`/`failback`) описаны в [CONFIGURATION.md](CONFIGURATION.md#загрузка-конфигурации). Порт `8080`.

### Docker вручную

```bash
docker build --target from-release -t imager:production .
docker run -d \
  --tmpfs /tmp:rw,noexec,nosuid,size=64m \
  --security-opt no-new-privileges:true \
  --cap-drop ALL \
  -p 8080:8080 \
  -v /host/setting:/etc/imager/setting:ro \
  -v /host/models:/etc/imager/models:rw \
  -v /host/source:/data/source:ro \
  -v /host/result:/data/result:rw \
  -e IMAGER_CONFIG_DIR=/etc/imager/setting \
  -e IMAGER_MODELS_DIR=/etc/imager/models \
  imager:production
```

### Без Docker

Сборка и зависимости — [INSTALLATION.md](INSTALLATION.md). Конфигурация читается из каталога `IMAGER_CONFIG_DIR` (по умолчанию текущий каталог).

## Укрепление контейнера (hardening)

| Мера | Реализация |
|------|------------|
| Non-root | Пользователь `imager` (uid 10001) в Dockerfile |
| Dropped capabilities | `cap_drop: ALL`, `cap_add: []` |
| no-new-privileges | `security_opt: no-new-privileges:true` |
| tmpfs | `/tmp`: `rw,noexec,nosuid,size=64m` |
| Права доступа | Бинарь `0755`, конфиг `0640`, каталоги данных `0750` |
| Pinned образы | `golang:1.27.0-alpine3.24` / `alpine:3.24`, pinned версии пакетов |
| Healthcheck | `wget http://127.0.0.1:8080/healthz` каждые 30s |

**`read_only: true` не используется**: при read-only rootfs Docker не может создать mountpoint для bind-mount `./models:/etc/imager/models` (каталог лежит в read-only слое). Writable-пути — bind-mounts `/data/result` (`:rw`), `/etc/imager/models` (`:rw`, сюда entrypoint скачивает модели) и tmpfs `/tmp`; `/data/source` и `/etc/imager/setting` монтируются `:ro`.

## Ресурсы

Compose-лимиты (`deploy.resources.limits`): `cpus: 2.0`, `memory: 512M`; reservations: `cpus: 0.25`, `memory: 128M`.

Подбирайте под нагрузку:

- `libvips.limits.concurrency` (рекомендуется 2–4) и `threads` (число логических ядер);
- `application.buffer-max-bytes` — бюджет памяти spillable-буферов;
- `http.max-concurrent-requests` — admission control при перегрузке.

## Graceful shutdown

По SIGINT/SIGTERM сервис:

1. прекращает принимать новые соединения;
2. дожидается активных запросов до `server.shutdown-timeout` (по умолчанию 15s);
3. дренирует очередь асинхронной публикации (см. [PROCESSING.md](PROCESSING.md#асинхронная-публикация)), закрывает хранилища, процессоры и пул буферов, останавливает janitor.

Compose использует `stop_signal: INT` и `stop_grace_period: 15s`.

## Health-check эндпоинты

| Эндпоинт | Назначение |
|----------|------------|
| `/healthz` | Liveness: `200 {"status":"alive"}`; `503` если процесс завершается |
| `/readyz` | Readiness: `200 {"status":"ready"}`; `503` при shutdown |
| `/metrics` | Метрики Prometheus exposition format |

Health/metrics остаются доступными при перегрузке asset-обработки (admission control применяется только к asset-запросам).

## nginx как фронт-прокси

Настройка описана в [NGINX.md](NGINX.md): раздача готовых файлов через `try_files`, проксирование генерации, проброс/скрытие эндпоинтов и выравнивание заголовков.

Ключевые моменты:

- **Раздача готовых файлов.** Ключ результата совпадает с путём в URL (без ведущего `/`), поэтому `try_files $uri @imager` с `root` на `result.path` отдаёт уже сгенерированные ассеты напрямую. Пути задаются в `source.path` / `result.path` (см. [STORAGE.md](STORAGE.md)).
- **Проксирование.** `proxy_pass` на адрес imager с пробросом `Host`, `X-Real-IP`, `X-Forwarded-For`, `X-Forwarded-Proto`; таймауты проксирования должны быть больше `http.generate-timeout` (по умолчанию 30s).
- **Эндпоинты.** В паблик пробрасываются asset URL (`/`), `/healthz`, `/readyz`. Служебные `/metrics` и `/admin/*` рекомендуется закрыть.

## Наблюдаемость

### Логи

Структурированные JSON-логи в stderr (`log/slog`). Каждый запрос получает request ID (заголовок `X-Request-Id` или сгенерированный), пробрасываемый в контекст и логи. URL/query/user input и секреты не логируются.

Уровень — `observability.log-level`: `debug`/`info`/`warn`/`error`; для production рекомендуется `info` или `warn`.

### Метрики

Реализация на stdlib `expvar`, отдаются через `/metrics` в Prometheus exposition format. Все метки — фиксированные enum-ы (bounded cardinality):

| Метрика | Описание |
|---------|----------|
| `imager_requests{class}` | Счётчик запросов по классу статуса (`2xx/3xx/4xx/5xx`) |
| `imager_request_duration_seconds` | Гистограмма длительности запросов |
| `imager_cache_hits` / `imager_cache_misses` | Стадии кэша |
| `imager_processor_success` / `imager_processor_errors` | Стадия процессора |
| `imager_processor_duration_seconds` | Гистограмма обработки |
| `imager_storage_ops{op}` | Операции хранилищ (`source_lookup/open`, `result_lookup/open/publish`) с исходами success/error |
| `imager_storage_duration_seconds_{op}` | Гистограммы длительности операций хранилищ |

### Защита /metrics

Опциональная защита `/metrics` по bearer-токену (`X-Metrics-Token`) и/или списку IP/CIDR настраивается в composition root. По умолчанию выключена; при публичном доступе ограничьте эндпоинт на уровне reverse-proxy или сети.

## Рекомендуемый production-профиль

**Три слоя конфигурации**, слияние и приоритеты — [CONFIGURATION.md](CONFIGURATION.md#загрузка-конфигурации). Секреты — только в `*-local.yaml` (не коммитятся).

`server-local.yaml` (фундамент; секреты не коммитятся):

```yaml
server:
  addr: ":8080"
  write-timeout: "120s"        # крупные медиа-ответы медленным клиентам

http:
  cache-control: "public, max-age=2592000"
  allowed-origins:
    - "https://cdn.example.com"
  max-concurrent-requests: 32

source:
  storage: s3
  bucket: "prod-images-source"
  prefix: "source/"
  endpoint: "https://storage.yandexcloud.net"
  region: "ru-central1"

result:
  storage: s3
  bucket: "prod-images-result"
  prefix: "gen/"

metadata:
  dir: "/var/cache/imager/meta"

libvips:
  limits:
    concurrency: 4
    threads: 4
    timeout: "30s"
    output-bytes: 10485760

application:
  buffer-max-bytes: 524288000
  limits:
    source-bytes: 10485760
    output-bytes: 10485760

observability:
  log-level: "warn"
```

`generate-local.yaml` (генерация ассетов — path-policies + application.limits):

```yaml
policy:
  presets:
    thumb:
      width: 200
      height: 200
      output-formats: [webp, avif]
      dpr: 1
  path-policies:
    "/":
      presets: ["thumb"]
      customs:
        x:
          output-formats: [webp]

application:
  limits:
    source-bytes: 10485760
    output-bytes: 10485760
```

Чек-лист перед запуском:

- [ ] `*-local.yaml` с секретами не коммитятся; секреты не в базовых `*.yaml`;
- [ ] настроены `policy.path-policies` (deny-by-default) и лимиты `application.limits`;
- [ ] `max-concurrent-requests` соответствует ресурсам контейнера;
- [ ] healthcheck балансировщика указывает на `/healthz` (liveness) и `/readyz` (readiness);
- [ ] `/metrics` закрыт от публичного доступа;
- [ ] `/data/source` смонтирован `:ro`, `/data/result` — на достаточный `:rw` volume; для fs-result работает janitor;
- [ ] TLS терминируется на reverse-proxy (сервис слушает plain HTTP).

## CI

Workflow: [`.gitverse/workflows/ci.yml`](../.gitverse/workflows/ci.yml).

### CI-образ (test/quality)

Джобы `test` и `quality` выполняются в предварительно собранном образе
[`gitverse.ru/pkg-ru/imager-ci`](../.gitverse/docker/imager-ci/README.md)
(Go 1.27 + libvips + ONNX Runtime + ffmpeg + gofmt + govulncheck +
предзагруженный `GOMODCACHE` + ONNX-модели + nodejs/git для
GitHub Actions-действий). Toolchain и зависимости **не устанавливаются
в каждом запуске** — это основное ускорение пайплайна.

Тег образа — фиксированный (immutable), не `latest`:
`gitverse.ru/pkg-ru/imager-ci:v<N>` (например `v1`). Обновление образа —
отдельное контролируемое изменение (см. README в каталоге образа).

### Публикация на Docker Hub (docker-release)

Docker-сборка и публикация `altrap/imager` выполняются **в GitHub Actions**
(workflow [`.github/workflows/docker-release.yml`](../.github/workflows/docker-release.yml)),
а не в GitVerse. Причина: раннеры GitVerse работают в контейнере без привилегий
(нет NET_ADMIN / mount / unshare), где Docker-сборка невозможна. GitHub Actions
использует полноценные VM, где `docker build` и `docker push` работают штатно.

GitVerse — основной репозиторий, GitHub — зеркало: refs (ветки и теги)
синхронизирует [`.gitverse/workflows/mirror.yml`](../.gitverse/workflows/mirror.yml),
поэтому push тега `vX.Y.Z` автоматически попадает в GitHub и запускает
публикацию (джоба `publish`). Ручной запуск — через `workflow_dispatch`
с input `imager_version` (тег или `latest`).

Джоба `build` собирает образ из исходников (target `from-source`, build tags
`libvips,onnx`) и сканирует его Trivy (HIGH, CRITICAL, `--ignore-unfixed`);
при найденных критических уязвимостях публикация блокируется (`needs: build`).

Секрет `DOCKERHUB_TOKEN` (учётка `altrap`, права Read & Write) задаётся в
настройках репозитория **GitHub** как environment secret окружения `Imager`
(Settings → Environments → Imager → Environment secrets); джоба `publish`
объявляет `environment: Imager`, чтобы получить к нему доступ.
