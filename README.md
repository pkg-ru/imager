# Полный скрипт `pve-schedule.sh`

Сохранить файл:

`/usr/local/sbin/pve-schedule.sh`

```bash
#!/usr/bin/env bash

set -Eeuo pipefail

CONFIG_FILE="/etc/pve/local/pve-schedule.conf"
STATE_DIR="/var/lib/pve-schedule"
LOCK_FILE="/run/pve-schedule.lock"

LOG_TAG="pve-schedule"

# Сколько секунд максимум ждём штатного выключения.
SHUTDOWN_TIMEOUT=120

# Интервал проверки состояния VM/CT во время ожидания.
POLL_INTERVAL=2


# -------------------------------------------------------------------
# Инициализация
# -------------------------------------------------------------------

mkdir -p "$STATE_DIR"

exec 9>"$LOCK_FILE"

if ! flock -n 9; then
    exit 0
fi


# -------------------------------------------------------------------
# Логирование
# -------------------------------------------------------------------

log() {
    logger -t "$LOG_TAG" -- "$*"
    echo "$*"
}

error() {
    logger -t "$LOG_TAG" -- "ERROR: $*"
    echo "ERROR: $*" >&2
}


# -------------------------------------------------------------------
# Справка
# -------------------------------------------------------------------

usage() {
    cat <<EOF

Использование:

  $0 run
      Выполнить проверку расписания.

  $0 validate
      Проверить конфигурационный файл.

  $0 show
      Показать конфигурацию.

  $0 status
      Показать состояние VM/CT и scheduler.

  $0 start TYPE ID
      Принудительно запустить VM/CT.

  $0 stop TYPE ID
      Принудительно остановить VM/CT.

  $0 help
      Показать эту справку.

Примеры:

  $0 validate
  $0 status
  $0 start vm 100
  $0 stop ct 102

EOF
}


# -------------------------------------------------------------------
# Валидация времени
# -------------------------------------------------------------------

validate_time() {
    local value="$1"

    [[ "$value" =~ ^([01][0-9]|2[0-3]):[0-5][0-9]$ ]]
}


# -------------------------------------------------------------------
# Файл состояния scheduler
#
# Файл существует только тогда, когда машину остановил
# именно scheduler.
# -------------------------------------------------------------------

state_file() {
    local type="$1"
    local id="$2"

    echo "$STATE_DIR/${type}-${id}.state"
}


# -------------------------------------------------------------------
# Проверка существования гостя
# -------------------------------------------------------------------

guest_exists() {
    local type="$1"
    local id="$2"

    case "$type" in
        vm)
            qm status "$id" >/dev/null 2>&1
            ;;

        ct)
            pct status "$id" >/dev/null 2>&1
            ;;

        *)
            return 1
            ;;
    esac
}


# -------------------------------------------------------------------
# Проверка состояния
# -------------------------------------------------------------------

is_running() {
    local type="$1"
    local id="$2"

    case "$type" in
        vm)
            qm status "$id" 2>/dev/null | grep -q '^status: running$'
            ;;

        ct)
            pct status "$id" 2>/dev/null | grep -q '^status: running$'
            ;;

        *)
            return 1
            ;;
    esac
}


is_stopped() {
    ! is_running "$1" "$2"
}


# -------------------------------------------------------------------
# Ожидание остановки
#
# Важный момент:
# после qm/pct shutdown состояние может оставаться running
# ещё несколько секунд.
#
# Поэтому не используем:
#
#   sleep 2
#   is_running
#
# Вместо этого ждём фактического перехода в stopped
# до SHUTDOWN_TIMEOUT секунд.
# -------------------------------------------------------------------

wait_for_stopped() {
    local type="$1"
    local id="$2"
    local timeout="${3:-$SHUTDOWN_TIMEOUT}"

    local elapsed=0

    while (( elapsed < timeout )); do

        if is_stopped "$type" "$id"; then
            return 0
        fi

        sleep "$POLL_INTERVAL"

        elapsed=$((elapsed + POLL_INTERVAL))
    done

    return 1
}


# -------------------------------------------------------------------
# Ожидание запуска
# -------------------------------------------------------------------

wait_for_running() {
    local type="$1"
    local id="$2"
    local timeout="${3:-60}"

    local elapsed=0

    while (( elapsed < timeout )); do

        if is_running "$type" "$id"; then
            return 0
        fi

        sleep "$POLL_INTERVAL"

        elapsed=$((elapsed + POLL_INTERVAL))
    done

    return 1
}


# -------------------------------------------------------------------
# Штатная остановка VM/CT
# -------------------------------------------------------------------

stop_guest() {
    local type="$1"
    local id="$2"

    if ! guest_exists "$type" "$id"; then
        error "$type $id: гостевая система не существует"
        return 1
    fi

    # Уже остановлена.
    if is_stopped "$type" "$id"; then
        log "$type $id: уже остановлена"
        return 0
    fi

    log "$type $id: выполняется штатное выключение"

    case "$type" in

        vm)
            if ! qm shutdown "$id" --timeout "$SHUTDOWN_TIMEOUT"; then
                log "$type $id: qm shutdown завершился с ошибкой"

                # Не спешим сразу делать stop.
                # Сначала самостоятельно ждём фактической остановки.
                if wait_for_stopped "$type" "$id" "$SHUTDOWN_TIMEOUT"; then
                    log "$type $id: остановлена после qm shutdown"
                else
                    log "$type $id: выполняется принудительная остановка"
                    qm stop "$id"
                fi
            fi
            ;;

        ct)
            if ! pct shutdown "$id" --timeout "$SHUTDOWN_TIMEOUT"; then
                log "$type $id: pct shutdown завершился с ошибкой"

                if wait_for_stopped "$type" "$id" "$SHUTDOWN_TIMEOUT"; then
                    log "$type $id: остановлен после pct shutdown"
                else
                    log "$type $id: выполняется принудительная остановка"
                    pct stop "$id"
                fi
            fi
            ;;

        *)
            error "$type $id: неизвестный тип"
            return 1
            ;;
    esac

    # После команды обязательно ждём фактической остановки.
    if wait_for_stopped "$type" "$id" "$SHUTDOWN_TIMEOUT"; then

        # Только теперь фиксируем:
        # "эту машину выключил scheduler".
        date +%s > "$(state_file "$type" "$id")"

        log "$type $id: успешно остановлена scheduler'ом"

        return 0
    fi

    error "$type $id: не удалось остановить за ${SHUTDOWN_TIMEOUT} сек."

    return 1
}


# -------------------------------------------------------------------
# Запуск VM/CT
# -------------------------------------------------------------------

start_guest() {
    local type="$1"
    local id="$2"

    if ! guest_exists "$type" "$id"; then
        error "$type $id: гостевая система не существует"
        return 1
    fi

    # Уже работает.
    if is_running "$type" "$id"; then

        log "$type $id: уже работает"

        # Если существовал marker, значит scheduler ранее
        # выключил машину, а теперь она снова работает.
        #
        # Это означает ручной запуск:
        # Web UI / qm / pct / API / любой другой способ.
        rm -f "$(state_file "$type" "$id")"

        log "$type $id: обнаружен ручной запуск, scheduler больше не считает её остановленной"

        return 0
    fi

    log "$type $id: запускается"

    case "$type" in
        vm)
            qm start "$id"
            ;;

        ct)
            pct start "$id"
            ;;

        *)
            error "$type $id: неизвестный тип"
            return 1
            ;;
    esac

    # Ждём фактического запуска.
    if wait_for_running "$type" "$id" 60; then

        # Marker больше не нужен.
        rm -f "$(state_file "$type" "$id")"

        log "$type $id: успешно запущена"

        return 0
    fi

    error "$type $id: запуск не подтверждён в течение 60 сек."

    return 1
}


# -------------------------------------------------------------------
# Проверка конфигурации
# -------------------------------------------------------------------

validate_config() {
    local line_no=0
    local errors=0

    if [[ ! -f "$CONFIG_FILE" ]]; then
        error "Конфигурационный файл не существует: $CONFIG_FILE"
        return 1
    fi

    while IFS= read -r line || [[ -n "$line" ]]; do

        line_no=$((line_no + 1))

        # Убираем комментарий.
        line="${line%%#*}"

        # Пропускаем пустые строки.
        [[ -z "${line//[[:space:]]/}" ]] && continue

        read -r type id start stop enabled extra <<< "$line"

        # Проверяем число полей.
        if [[ -n "${extra:-}" ]]; then
            error "Строка $line_no: слишком много параметров"
            errors=$((errors + 1))
            continue
        fi

        # TYPE.
        if [[ "$type" != "vm" && "$type" != "ct" ]]; then
            error "Строка $line_no: тип должен быть vm или ct"
            errors=$((errors + 1))
            continue
        fi

        # ID.
        if ! [[ "$id" =~ ^[0-9]+$ ]]; then
            error "Строка $line_no: некорректный ID: $id"
            errors=$((errors + 1))
        fi

        # START.
        if ! validate_time "$start"; then
            error "Строка $line_no: некорректное время старта: $start"
            errors=$((errors + 1))
        fi

        # STOP.
        if ! validate_time "$stop"; then
            error "Строка $line_no: некорректное время остановки: $stop"
            errors=$((errors + 1))
        fi

        # ENABLED.
        if [[ "$enabled" != "0" && "$enabled" != "1" ]]; then
            error "Строка $line_no: enabled должен быть 0 или 1"
            errors=$((errors + 1))
        fi

        # START == STOP.
        if [[ "$start" == "$stop" ]]; then
            error "Строка $line_no: start и stop не должны совпадать"
            errors=$((errors + 1))
        fi

        # Проверяем существование только активных гостей.
        if [[ "$enabled" == "1" ]] &&
           [[ "$id" =~ ^[0-9]+$ ]] &&
           ! guest_exists "$type" "$id"; then

            error "Строка $line_no: $type $id не существует"
            errors=$((errors + 1))
        fi

    done < "$CONFIG_FILE"

    if (( errors > 0 )); then
        error "Конфигурация содержит $errors ошибок"
        return 1
    fi

    log "Конфигурация корректна"

    return 0
}


# -------------------------------------------------------------------
# Показать конфигурацию
# -------------------------------------------------------------------

show_config() {
    if [[ ! -f "$CONFIG_FILE" ]]; then
        error "Конфигурационный файл не существует: $CONFIG_FILE"
        return 1
    fi

    echo
    echo "Конфигурация:"
    echo "------------------------------------------------------------"

    printf "%-5s %-6s %-8s %-8s %-8s\n" \
        "TYPE" "ID" "START" "STOP" "ENABLED"

    while IFS= read -r line || [[ -n "$line" ]]; do

        line="${line%%#*}"

        [[ -z "${line//[[:space:]]/}" ]] && continue

        read -r type id start stop enabled <<< "$line"

        printf "%-5s %-6s %-8s %-8s %-8s\n" \
            "$type" \
            "$id" \
            "$start" \
            "$stop" \
            "$enabled"

    done < "$CONFIG_FILE"

    echo "------------------------------------------------------------"
    echo
}


# -------------------------------------------------------------------
# Показать состояние
# -------------------------------------------------------------------

show_status() {
    if [[ ! -f "$CONFIG_FILE" ]]; then
        error "Конфигурационный файл не существует: $CONFIG_FILE"
        return 1
    fi

    echo
    echo "Состояние scheduler:"
    echo "---------------------------------------------------------------------"

    printf "%-5s %-6s %-8s %-8s %-12s %-18s\n" \
        "TYPE" \
        "ID" \
        "START" \
        "STOP" \
        "RUNNING" \
        "SCHEDULER"

    while IFS= read -r line || [[ -n "$line" ]]; do

        line="${line%%#*}"

        [[ -z "${line//[[:space:]]/}" ]] && continue

        read -r type id start stop enabled <<< "$line"

        [[ "$enabled" == "1" ]] || continue

        local_running="no"
        local_scheduler="-"

        if is_running "$type" "$id"; then
            local_running="yes"
        fi

        if [[ -f "$(state_file "$type" "$id")" ]]; then
            local_scheduler="stopped-by-scheduler"
        fi

        printf "%-5s %-6s %-8s %-8s %-12s %-18s\n" \
            "$type" \
            "$id" \
            "$start" \
            "$stop" \
            "$local_running" \
            "$local_scheduler"

    done < "$CONFIG_FILE"

    echo "---------------------------------------------------------------------"
    echo
}


# -------------------------------------------------------------------
# Основной scheduler
# -------------------------------------------------------------------

run_scheduler() {

    if ! validate_config >/dev/null; then
        error "Расписание не обработано из-за ошибки конфигурации"
        return 1
    fi

    local now
    now="$(date '+%H:%M')"

    log "Проверка расписания: $now"

    while IFS= read -r line || [[ -n "$line" ]]; do

        line="${line%%#*}"

        [[ -z "${line//[[:space:]]/}" ]] && continue

        read -r type id start stop enabled <<< "$line"

        [[ "$enabled" == "1" ]] || continue

        local_state="$(state_file "$type" "$id")"

        # ----------------------------------------------------------
        # Плановая остановка
        # ----------------------------------------------------------

        if [[ "$now" == "$stop" ]]; then

            if is_running "$type" "$id"; then

                log "$type $id: наступило время остановки ($stop)"

                if ! stop_guest "$type" "$id"; then
                    error "$type $id: плановая остановка завершилась ошибкой"
                fi

            else

                # Если машина уже выключена вручную,
                # scheduler ничего не делает.
                log "$type $id: наступило время остановки ($stop), но машина уже выключена"

            fi

            continue
        fi


        # ----------------------------------------------------------
        # Плановый запуск
        # ----------------------------------------------------------
        #
        # Запускаем машину только в случае, если её ранее
        # выключил scheduler.
        #
        # Если пользователь выключил машину вручную —
        # marker отсутствует и машина не запускается.
        # ----------------------------------------------------------

        if [[ "$now" == "$start" ]]; then

            if [[ -f "$local_state" ]]; then

                if is_running "$type" "$id"; then

                    # Машина была запущена вручную после
                    # планового выключения.
                    log "$type $id: уже работает к плановому старту, считаем запуск ручным"

                    rm -f "$local_state"

                else

                    log "$type $id: наступило время запуска ($start)"

                    if ! start_guest "$type" "$id"; then
                        error "$type $id: плановый запуск завершился ошибкой"
                    fi

                fi

            else

                log "$type $id: время запуска ($start), но scheduler ранее её не выключал — запуск пропущен"

            fi

            continue
        fi


        # ----------------------------------------------------------
        # Обнаружение ручного запуска
        #
        # Если scheduler ранее выключил машину и она появилась
        # running до планового времени старта, считаем это
        # ручным запуском и удаляем marker.
        # ----------------------------------------------------------

        if [[ -f "$local_state" ]] &&
           is_running "$type" "$id"; then

            log "$type $id: обнаружен ручной запуск после планового выключения"

            rm -f "$local_state"

        fi

    done < "$CONFIG_FILE"
}


# -------------------------------------------------------------------
# Ручная остановка
# -------------------------------------------------------------------

manual_stop() {
    local type="$1"
    local id="$2"

    stop_guest "$type" "$id"
}


# -------------------------------------------------------------------
# Ручной запуск
# -------------------------------------------------------------------

manual_start() {
    local type="$1"
    local id="$2"

    start_guest "$type" "$id"
}


# -------------------------------------------------------------------
# CLI
# -------------------------------------------------------------------

case "${1:-}" in

    run)
        run_scheduler
        ;;

    validate)
        validate_config
        ;;

    show)
        show_config
        ;;

    status)
        show_status
        ;;

    start)
        if [[ $# -ne 3 ]]; then
            error "Использование: $0 start vm 100"
            exit 1
        fi

        manual_start "$2" "$3"
        ;;

    stop)
        if [[ $# -ne 3 ]]; then
            error "Использование: $0 stop vm 100"
            exit 1
        fi

        manual_stop "$2" "$3"
        ;;

    help|--help|-h)
        usage
        ;;

    *)
        usage
        exit 1
        ;;

esac
```

После сохранения:

```bash
chmod +x /usr/local/sbin/pve-schedule.sh
```

Проверка:

```bash
/usr/local/sbin/pve-schedule.sh validate
```

Проверка состояния:

```bash
/usr/local/sbin/pve-schedule.sh status
```

Тест ручной остановки:

```bash
/usr/local/sbin/pve-schedule.sh stop ct 102
```

Лог:

```bash
journalctl -t pve-schedule -f
```

Для твоего случая теперь ожидаемый результат такой:

```text
01:20:03 ct 102: выполняется штатное выключение
01:20:18 ct 102: успешно остановлен scheduler'ом
```

а **ложной ошибки через 2 секунды больше не будет**: скрипт ждёт реального перехода CT в остановленное состояние до 120 секунд.

Конфигурационный файл остаётся таким:

```text
/etc/pve/local/pve-schedule.conf
```

а состояние scheduler:

```text
/var/lib/pve-schedule/
```

Systemd timer при этом менять не нужно — он по-прежнему запускает `pve-schedule.sh run` раз в минуту.










# Imager <sup><sup><sub>([Imager Client](https://github.com/pkg-ru/imager-client))</sub></sup></sup>

**Imager** — HTTP-микросервис для генерации и компрессии изображений на лету (Go + libvips, ImageMagick — опциональный fallback).

Сервис принимает запрос на миниатюру (ассет) — генерирует её из исходного файла через **libvips** (in-process, govips) и сохраняет в хранилище результатов. При повторных запросах того же ассета сервис отдаёт ранее созданный файл (кэш по каноническому идентификатору). **ImageMagick** остаётся опциональным fallback для форматов, не поддерживаемых libvips (APNG).

> Например, вы хотите сжать и уменьшить картинку. Для этого нужно сформировать [каноническую ссылку](https://github.com/pkg-ru/imager-client) (ассет) на картинку — при запросе которой сервис **Imager** на лету создаст сжатую миниатюру и вернёт её пользователю.
>
> Исходник: `example.com/my_image.jpg` (не обязательно должен быть доступен из web)\
> Ассет на миниатюру: `example.com/photos/my_image-jpg/c-120x80@2.webp`\
> Ассет по пресету: `example.com/photos/my_image-jpg/thumb.webp`

---

## Возможности

- **Обработка изображений на лету**: resize, crop (центрированная обрезка), trim (обрезка краёв), crop+trim.
- **Движки**: libvips (основной, in-process через govips, cgo) + ImageMagick (опциональный fallback только для APNG). Маршрутизация — `internal/adapters/processor/routing`.
- **Форматы**: входные — `jpeg`, `png`, `webp`, `gif`, `avif`, `heif`, `jxl`, `tiff`, `bmp` (+ PDF/PSD/RAW в будущем через libvips); выходные — `jpeg`, `png`, `webp`, `gif`, `avif`, `heif`, `jxl`. **APNG** — только через ImageMagick (если не установлен, запрос с APNG возвращает понятную ошибку).
- **Пресеты**: именованные конфигурации обработки, вызываемые коротким URL.
- **Политики**: deny-by-default авторизация (`safe`/`unsafe`), whitelist пресетов, правила размеров, path-политики (longest prefix match).
- **Лимиты**: на всех уровнях — политика запроса, libvips/ImageMagick (resource limits + application-level), прикладные лимиты сервиса.
- **Хранилища**: `fs`, `s3`, `sftp`, `ftp`, `ftps`, `http` (source-only) — независимо для source и result.
- **Безопасность**: deny-by-default `policy.xml` ImageMagick (для fallback), CORS deny-by-default, security-заголовки, bounded URL/body/header.
- **Observability**: структурированные JSON-логи (`log/slog`), метрики Prometheus (`/metrics`), expvar (`/debug/vars`), health-эндпоинты.

---

## Формат asset URL

Сервис принимает только **канонические** и **preset** URL. Byte-based кодирование не используется — URL читается напрямую.

### Канонический URL

```
/{path}/{source_name}-{source_format}/{transform}-{size}@{dpr}.{output_format}
```

- `path` — произвольный префикс пути (используется для path-политик), до 512 символов.
- `source_name` — имя исходного файла (без расширения), до 128 символов.
- `source_format` — формат исходного файла, до 16 символов.
- `transform` — код операции:
  - `c` — crop (обрезка по центру);
  - `t` — trim (обрезка краёв);
  - `ct` — trim, затем crop (последовательно);
  - `sc` — smart-crop (умная обрезка по значимой области, attention libvips);
  - `fc` — face-crop (обрезка по обнаруженным лицам, ONNX YuNet);
  - `oc` — object-crop (обрезка по обнаруженным объектам, ONNX SSD/YOLO);
  - `sct` — trim, затем smart-crop (сначала trim, потом attention);
  - `fct` — trim, затем face-crop (сначала trim, потом детекция лиц);
  - `oct` — trim, затем object-crop (сначала trim, потом детекция объектов);
  - отсутствует — resize (масштабирование).
  `sc`/`fc`/`oc` и их trim-варианты `sct`/`fct`/`oct` требуют сборки с
  `-tags libvips`; `fc`/`oc`/`fct`/`oct` дополнительно требуют моделей ONNX
  и сборки с `-tags onnx` (см. ниже, секция detection). Trim-варианты
  выполняют trim ДО кропа: детекция/attention применяются к уже подрезанному
  изображению. Любые другие коды (включая `tc`) недопустимы.
- `size` — размер миниатюры: `120x80`, `x50`, `180x`, `x` (сохранить исходный размер).
- `dpr` — целочисленный множитель (device pixel ratio): отсутствие суффикса = `1`, явно допустимы только `2` или `3`.
- `output_format` — выходной формат: `jpeg`, `png`, `webp`, `gif`, `avif`, `heif`, `apng`, `jxl` (JPEG XL).

Пример: `photos/my-photo-jpg-c-120x80@2.webp` создаст ассет `120x80*2` из `my-photo.jpg`.

### Preset URL

```
/{path}/{source_name}-{source_format}/{preset_name}@{dpr}.{output_format}
```

Пример: `photos/my-photo-jpg/thumb.webp` применит пресет `thumb` к исходнику `my-photo.jpg` (source name — `my-photo`, source format — `jpg`).

Пресеты определяются в конфигурации (секция `policy.presets`), не содержат `source-format` (исходный формат определяется URL) и раскрываются в канонический запрос с параметрами пресета. `output-format` пресета обязан совпадать с расширением в URL. Имя пресета может содержать фиксированный суффикс `@dpr` (например `thumb@2`), который форсирует `dpr=2` при разрешении.

---

## Быстрый старт

### Требования

- Go 1.25+ (для сборки из исходников).
- **libvips** (`vips-dev` / `libvips-dev`) + C-компилятор (gcc/clang) для сборки с тэком `-tags libvips` (основной движок).
- [ImageMagick](https://imagemagick.org/script/download.php) (`magick` для версии 7, `convert` для версии 6) — **опционально**, только для APNG.
- Docker (опционально, для запуска в контейнере).

### Сборка и запуск локально

Сборка с libvips (основной движок):

```bash
go build -tags libvips -trimpath -ldflags="-s -w" -o imager ./cmd/imager
IMAGER_CONFIG_DIR=. ./imager
```

Сборка без libvips (ImageMagick как primary; APNG работает, остальные форматы — через ImageMagick):

```bash
go build -trimpath -ldflags="-s -w" -o imager ./cmd/imager
IMAGER_CONFIG_DIR=. ./imager
```

Конфигурация читается из каталога, указанного в `IMAGER_CONFIG_DIR` (по умолчанию `.` — корень репозитория, где лежат `setting.yaml`/`setting-local.yaml`).

> **Windows**: имя `magick` может не разрешаться через `PATH` процесса (например, при запуске из IDE или службы). Укажите абсолютный путь к `magick.exe` в `setting-local.yaml` (используйте прямые слэши):
>
> ```yaml
> imagemagick:
>   binary: "D:/OSPanel/addons/ImageMagick-vs17/magick.exe"
> ```

### Запуск с Docker

```bash
docker build -t imager:production .
docker run -d \
  --read-only \
  --tmpfs /tmp:rw,noexec,nosuid,size=64m \
  --security-opt no-new-privileges:true \
  --cap-drop ALL \
  -p 8080:8080 \
  -v /host/config:/etc/imager:ro \
  -v imager_source:/data/source \
  -v imager_result:/data/result \
  -e IMAGER_CONFIG_DIR=/etc/imager \
  imager:production
```

### Запуск с `docker-compose`

В репозитории есть готовый [`docker-compose.yaml`](docker-compose.yaml) с production hardening (non-root, read-only root fs, dropped capabilities, no-new-privileges, tmpfs, resource limits, healthcheck):

```bash
docker compose up -d --build
```

Compose-файл монтирует каталог `./config` в `/etc/imager` (read-only) и задаёт `IMAGER_CONFIG_DIR=/etc/imager`. Внутри каталога лежат `setting.yaml` (обязательный) и опциональный `setting-local.yaml`.

### Проверка

```bash
curl -i http://127.0.0.1:8080/healthz
curl -i http://127.0.0.1:8080/test-jpg/c-120x80@2.webp
```

---

## Конфигурация

### Загрузка конфигурации

**Все** настройки приложения задаются исключительно в YAML. Прикладных CLI-флагов нет. Единственная env-переменная — `IMAGER_CONFIG_DIR` — путь к каталогу, где лежат:

- `setting.yaml` — **обязательный** базовый конфиг (отсутствие или невалидность — ошибка запуска);
- `setting-local.yaml` — **опциональный** локальный конфиг, который **глубоко переопределяет** базовый.

Механизм загрузки (`internal/adapters/httpapi/configloader.go`):

1. Читается обязательный базовый файл `setting.yaml`.
2. Если рядом есть `setting-local.yaml` — его настройки **глубоко мержатся** поверх базового:
   - вложенные `map` объединяются рекурсивно (ключи, не указанные в local, сохраняются);
   - скаляры заменяются значением из local;
   - **списки заменяются ЦЕЛИКОМ** (не дополняются) — например `allowed-origins` или `disabled-coders` нельзя «дополнить» в local.
3. Результат строго декодируется (`yaml.UnmarshalStrict`): **любой ключ, отсутствующий в схеме, считается ошибкой** и валит старт (fail-fast).

> **Важно**: добавляйте только ключи из документации ниже. Полный самодокументированный пример актуальной схемы — в [`config/setting.yaml`](config/setting.yaml).

### Переменные окружения

| Переменная | По умолчанию | Описание |
|------------|--------------|----------|
| `IMAGER_CONFIG_DIR` | `.` | Каталог, где лежат `setting.yaml` и `setting-local.yaml`. |
| `IMAGER_S3_ACCESS_KEY` | — | Access key для S3-хранилищ (если не задан в YAML). Значение из YAML имеет приоритет. |
| `IMAGER_S3_SECRET_KEY` | — | Secret key для S3-хранилищ (если не задан в YAML). Значение из YAML имеет приоритет. |

> Legacy-переменная `IMAGER_CONFIG_NAME` в коде **не существует** — не используйте её.

### Схема верхнего уровня

```yaml
version: "1"          # версия схемы (обязательна; другое значение — ошибка старта)
server:               # HTTP/TCP сервер: addr, таймауты, лимиты
http:                 # HTTP-адаптер: CORS, cache-control, not-found и т.д.
watermarks:           # именованные декларации ватермарок (применяются по имени)
policy:               # политика авторизации запросов (deny-by-default)
processing:           # умолчания обработки (default-quality, default-loop,
                      # default-watermark, default-auto-orient,
                      # default-rotate, default-flip)
source:               # source-хранилище (storage, path, параметры backend)
result:               # result-хранилище (storage, path, параметры backend)
imagemagick:          # binary, policy.xml, resource limits
application:          # прикладные лимиты (output-limit, buffer-max-bytes)
observability:        # log-level
```

### `server` — HTTP/TCP сервер

| Ключ | Тип | Дефолт | Описание |
|------|-----|--------|----------|
| `addr` | string | `":8080"` | Адрес прослушивания TCP (`host:port`). Пусто = все интерфейсы на порту 8080. |
| `read-header-timeout` | duration | `"5s"` | Таймаут чтения заголовков запроса (защита от slowloris). |
| `read-timeout` | duration | `"15s"` | Таймаут чтения тела запроса. |
| `write-timeout` | duration | `"30s"` | Таймаут записи ответа клиенту. |
| `idle-timeout` | duration | `"60s"` | Таймаут простаивания keep-alive соединения. |
| `shutdown-timeout` | duration | `"15s"` | Максимальное время ожидания активных запросов при graceful shutdown. |
| `max-header-bytes` | int | `32768` | Максимальный суммарный размер заголовков (превышение → HTTP 431). |
| `max-body-bytes` | int | `4096` | Жёсткий лимит тела запроса. Сервис **не принимает тела запросов**, поэтому лимит мал (защита от slow-body/DoS). `0` = без лимита. |

Таймауты задаются строками Go duration (`"5s"`, `"1m30s"`, `"250ms"`). Отрицательные значения запрещены.

### `http` — HTTP-адаптер

| Ключ | Тип | Дефолт | Описание |
|------|-----|--------|----------|
| `allowed-origins` | list[string] | пусто | CORS allowlist (deny-by-default). Каждый элемент — схема+хост (`"https://cdn.example.com"`). Пустой список = никакие cross-origin запросы не получают CORS-заголовков. |
| `allow-credentials` | bool | `false` | Разрешать `Access-Control-Allow-Credentials`. **Несовместим** с wildcard `"*"` в `allowed-origins` (ошибка валидации на старте). |
| `cache-control` | string | `"public, max-age=31536000, immutable"` | `Cache-Control` для успешно сгенерированных канонических ассетов (immutable). Пусто = заголовок не выставляется. |
| `not-found-cache-control` | string | `"no-store"` | `Cache-Control` для fallback-ответов (404 и т.п.). Пусто = не выставляется. |
| `referrer-policy` | string | `"no-referrer"` | Значение заголовка `Referrer-Policy`. |
| `csp` | string | пусто | Значение `Content-Security-Policy` (выставляется для fallback-страниц). Пусто = не выставляется. |
| `max-url-len` | int | `1024` | Максимальная длина asset URL (`0` → 1024). Превышение → HTTP 414. |
| `generate-timeout` | duration | `"30s"` | Таймаут генерации ассета (context deadline). Превышение → HTTP 504. |
| `not-found.pixel` | bool | `false` | Отдавать прозрачный 1×1 пиксель в **запрошенном** формате при not-found. |
| `not-found.image` | string | пусто | Путь к статическому файлу-картинке, отдаваемому с HTTP 404. |
| `not-found.page` | string | пусто | Путь к статическому HTML-файлу, отдаваемому с HTTP 404. |
| `not-found.redirect` | string | пусто | URL для 301-редиректа при not-found. |

Поля `not-found` взаимоисключающие по приоритету: **pixel > redirect > image > page**.

### `policy` — политика авторизации и лимитов

Политика применяется к каноническим и preset URL. **Всё запрещено по умолчанию**; разрешается только то, что явно покрыто правилами.

#### `policy.global`

| Ключ | Тип | Дефолт | Описание |
|------|-----|--------|----------|
| `authorization` | string | `"safe"` | Режим авторизации: `"safe"` — разрешены только явно покрытые случаи (пресеты из `allowed-presets`, размеры из `size-rules`); `"unsafe"` — любые произвольные параметры разрешены (лимиты при этом не отключаются). |
| `allowed-presets` | list[string] | пусто | Whitelist имён пресетов, доступных в URL. Имена указываются **полностью** (включая `@dpr`-суффикс): чтобы разрешить пресет `thumb@2`, указываем именно `thumb@2`. В режиме `safe` пресет вне списка → 404. Игнорируется при `authorization: "unsafe"`. |
| `size-rules` | list[string] | пусто | Правила допустимых размеров для канонических запросов. Формат `"minW-maxWxminH-maxH"`; измерение может быть диапазоном (`"0-2000"`) или отсутствовать (пусто = «любая»). `"500x"` = точная ширина 500 при любой высоте. В режиме `safe` запрос отклоняется, если ни одно правило не совпало. Пустой список в режиме `safe` = все канонические запросы отклоняются. |
| `limits` | — | — | Лимиты обрабатываемого запроса (применяются к **любому** режиму; `0` = без ограничения). |

#### `policy.global.limits`

| Ключ | Тип | Описание |
|------|-----|----------|
| `source-bytes` | int64 | Максимум размера исходного файла (байт). |
| `width` | int | Максимум ширины запрошенного изображения (px). |
| `height` | int | Максимум высоты запрошенного изображения (px). |
| `pixels` | int64 | Максимум пикселей запрошенного изображения (w×h). |
| `dpr` | int | Максимум значения dpr в запросе. |
| `frames` | int | Максимум кадров анимации (GIF/WebP). |
| `duration` | int64 | Максимум длительности анимации (мс). |
| `output-bytes` | int64 | Максимум размера выходного файла (байт). |
| `concurrency` | int | Максимум одновременных операций обработки от одного клиента. |

#### `policy.path-policies[]`

Политики по префиксам пути канонических URL. Применяются **только** к каноническим URL (не к пресетам) и лишь **ужесточают** глобальную политику (не расширяют права). Выбор — **longest prefix match** (самый длинный совпадающий префикс побеждает). `"/"` — fallback, применяется ко всем путям без более специфичного совпадения.

| Ключ | Тип | Описание |
|------|-----|----------|
| `path` | string | Префикс пути. `"basket/products"` нормализуется в `"/basket/products"`. |
| `dpr` | string | Диапазон допустимых DPR (`"0-1"`, `"2-3"`; пусто = без ограничения). |
| `crop` | bool / string / list[string] / nil | Правило допустимых crop-режимов: `nil` = неважно; `true` = crop обязателен (c/ct); `false` = crop запрещён (c и ct); строка-режим — разрешён только он (`center` → c/ct, `smart` → sc/sct, `face` → fc/fct, `object` → oc/oct, `none` = любой crop запрещён); список (`[smart, face]`) — разрешены только перечисленные режимы. Trim-варианты покрыты автоматически. Неизвестное значение — ошибка старта. |
| `trim` | bool/nil | `nil` = неважно; `true` = trim обязан быть в URL (t/ct/sct/fct/oct); `false` = trim запрещён. |
| `watermark` | string | Имя ватермарки из секции `watermarks`: накладывается на канонические запросы префикса пути. Приоритет ниже пресета, выше `processing.default-watermark`. Пусто = не задана. |

#### `policy.presets[]`

Именованные конфигурации обработки. Имена должны быть **уникальными**, ≤ 64 символов, без дефисов `-` (допустимы буквы, цифры, `_`, `.`, `@` для суффикса `@dpr`).

| Ключ | Тип | Описание |
|------|-----|----------|
| `name` | string | Имя пресета (обязательно, ≤ 64 символов). |
| `crop` | bool | `false` (дефолт). |
| `trim` | bool | `false` (дефолт). |
| `size` | string | `"WxH"`; одно из измерений может быть пустым (`"x400"`); `"x"` = оригинал. |
| `output-format` | string | `jpeg` \| `png` \| `webp` \| `gif` \| `avif` \| `heif` \| `apng` \| `jxl`. |
| `quality` | int | `0`–`100` (`0` = `default-quality` из `processing`). |
| `dpr` | int | `0`/`1`/`2`/`3` (`0` = не задан; при не заданном dpr берётся из `@dpr`-суффикса имени). |
| `frames` | int | Макс. число кадров анимации (`0` = без ограничения). |
| `duration` | int | Макс. длительность анимации в мс (`0` = без ограничения). |
| `loop` | bool/nil | `nil` = `default-loop` из `processing`; `true` = бесконечный loop; `false` = однопроходная анимация. |
| `watermark` | string | Имя ватермарки из секции `watermarks`. Приоритет выше path-policy и `processing.default-watermark`. Пусто = не задана. |
| `auto-orient` | bool/nil | `nil` = `default-auto-orient` из `processing`; `true`/`false` — явно включить/выключить автоматический поворот по EXIF Orientation. |
| `rotate` | string | Фиксированный поворот по часовой стрелке: `""` = `default-rotate` из `processing`; `"none"` = явно отключить (перекрыть глобальный); `"90"` \| `"180"` \| `"270"`. |
| `flip` | string | Отражение: `""` = `default-flip` из `processing`; `"none"` = явно отключить (перекрыть глобальный); `"horizontal"` \| `"vertical"`. |

Комбинации `crop`/`trim` формируют трансформацию:

| crop | trim | Трансформация |
|------|------|---------------|
| `false` | `false` | resize |
| `true` | `false` | crop (центрированная обрезка) |
| `false` | `true` | trim (обрезка краёв) |
| `true` | `true` | crop-trim (trim затем crop) |

### `processing` — умолчания обработки

| Ключ | Тип | Дефолт | Описание |
|------|-----|--------|----------|
| `default-quality` | int | `85` | Качество сжатия по умолчанию (`0`–`100`; вне диапазона — ошибка валидации). Применяется к lossy-форматам (jpeg/webp). На PNG влияет `png-compression-level`. |
| `default-loop` | bool/nil | `true` | Зацикливание анимаций по умолчанию (GIF/WebP/APNG/HEIF), если в пресете не задан `loop`. |
| `default-watermark` | string | пусто | Ватермарка по умолчанию (имя из `watermarks`). Применяется к запросам без ватермарки в пресете и path-policy. Неизвестное имя — ошибка старта. |
| `default-auto-orient` | bool/nil | `true` | Автоматический поворот по EXIF Orientation по умолчанию. `true` = при загрузке изображение поворачивается согласно тегу EXIF Orientation; `false` = тег игнорируется. Пресеты перекрывают полем `auto-orient`. |
| `default-rotate` | string | `""` | Фиксированный поворот по умолчанию (по часовой стрелке): `""`/`"none"` = без поворота; `"90"` \| `"180"` \| `"270"`. Применяется после auto-orient и до resize/crop/trim. Пресеты перекрывают полем `rotate`. |
| `default-flip` | string | `""` | Отражение по умолчанию: `""`/`"none"` = без отражения; `"horizontal"` \| `"vertical"`. Применяется после rotate и до resize/crop/trim. Пресеты перекрывают полем `flip`. |

### `watermarks` — декларации ватермарок

Ватермарка описывается один раз и применяется **по имени** в пресетах (`policy.presets[].watermark`), path-policies (`policy.path-policies[].watermark`) или через дефолт (`processing.default-watermark`).

Приоритет применения: **пресет -> path-policy (longest prefix match) -> default-watermark**; нигде не задана — не накладывается.

```yaml
watermarks:
  - name: water-test
    path: "/etc/imager/watermarks/test.png"  # файл должен существовать на старте (fail-fast)
    position: center   # top | bottom | left | right | center (CSS background-position)
    repeat: no-repeat  # no-repeat | repeat | repeat-x | repeat-y | round | space (CSS background-repeat)
    size: contain      # contain | cover | "200px 50px" (CSS background-size)
```

| Ключ | Тип | Дефолт | Описание |
|------|-----|--------|----------|
| `name` | string | — (обязателен) | Уникальное имя для ссылок из пресетов/path-policies/дефолта. |
| `path` | string | — (обязателен) | Путь к файлу изображения ватермарки на диске. Отсутствующий файл — ошибка старта. |
| `position` | string | `center` | Позиция одиночной копии (CSS-подобно; вторая ось — центр). |
| `repeat` | string | `no-repeat` | Заполнение холста копиями (CSS-подобно). `round` масштабирует копии до целого числа по осям; `space` распределяет с равными промежутками. |
| `size` | string | `contain` | Размер копии относительно целевого холста: `contain`, `cover` или фиксированный `"200px 50px"`. |

Ограничения движков:

- **libvips** (основной движок) реализует `position`/`repeat`/`size` полностью; для анимированных выходов (GIF/WebP/HEIF) ватермарка накладывается на **каждый кадр**.
- **ImageMagick** (fallback для APNG): точный размер только в px-форме; `contain`/`cover` рендерятся в натуральном размере файла; все режимы `repeat`, кроме `no-repeat`, — как сплошная плитка.

### `source` / `result` — хранилища

Source и result настраиваются **независимо** секциями `source:` и `result:`. Тип задаётся ключом `storage` (`fs`, `s3`, `sftp`, `ftp`, `ftps`, `http`). `fs` (или пустое значение) — локальный filesystem на `path`.

Общая схема одной секции:

```yaml
source:            # или result:
  storage: fs      # fs | s3 | sftp | ftp | ftps | http
  path: "./data/source"   # локальный каталог при storage: fs
  # + параметры backend (см. ниже)
```

Общие ключи (применимы к обеим секциям):

| Ключ | Дефолт | Описание |
|------|--------|----------|
| `storage` | `fs` | Тип хранилища: `fs`, `s3`, `sftp`, `ftp`, `ftps`, `http`. |
| `path` | `./data/source` / `./data/result` | Локальный каталог для `fs`. |
| `spool-dir` | `os.TempDir()` | Каталог временных spool при чтении remote-объектов. |
| `spool-max-bytes` | `0` (нет) | Лимит размера spool при чтении (превышение → quota error). |
| `dial-timeout` | `30s` | Таймаут соединения для SFTP/FTP/FTPS, HTTP и S3. |
| `read-timeout` | `60s` (S3/HTTP) | Таймаут выполнения операции для S3/HTTP. |
| `max-attempts` | `3` | Число попыток операции (S3/HTTP). |
| `max-idle-conns` | `100` | Макс. idle-соединений в пуле (S3/HTTP). |
| `max-idle-conns-per-host` | `10` | Макс. idle-соединений на хост (S3/HTTP). |
| `idle-conn-timeout` | `90s` | Время жизни idle-соединения (S3/HTTP). |
| `metadata-ttl` | `30s` | TTL кэша метаданных S3 (`0` = кэш отключён). |

> **Важно**: и FTP, и FTPS поддерживают и source, и result. Публикация выполняется через temp-upload + rename и требует от сервера команд `STOR`, `RNFR`/`RNTO` и `DELE` (базовый RFC 959). Если сервер не поддерживает эти команды, `Publish` вернёт ошибку `ErrUnavailable`.

#### S3 (`storage: s3`)

| Ключ | Дефолт | Описание |
|------|--------|----------|
| `bucket` | — | Имя bucket (**обязательно**). |
| `prefix` | — | Префикс ключей внутри bucket (опционально). |
| `endpoint` | AWS | Endpoint для S3-совместимых хранилищ (MinIO, Yandex Object Storage и т.п.). Пусто = AWS. |
| `region` | — | Регион. |
| `access-key` | — | Access key. |
| `secret-key` | — | Secret key. |

Требования валидации:

- `bucket` обязателен;
- `access-key` и `secret-key` задаются **только парой** (один без другого — ошибка);
- если ключи не заданы в YAML, используются env `IMAGER_S3_ACCESS_KEY` / `IMAGER_S3_SECRET_KEY` (значение из YAML имеет приоритет).

S3 поддерживает и source, и result. `NoOverwrite` реализуется через conditional PUT (`If-None-Match: "*"`).

#### SFTP (`storage: sftp`)

| Ключ | Дефолт | Описание |
|------|--------|----------|
| `addr` | — | Адрес `host:port` (**обязательно**). |
| `user` | — | Пользователь (**обязательно**). |
| `password` | — | Пароль (password auth). |
| `private-key-file` | — | Путь к файлу приватного ключа (key auth). |
| `root` | — | Корневой каталог внутри SFTP (пусто = домашний каталог). |
| `host-key-fingerprint` | — | SHA-256 fingerprint host key (**обязательно**, например `SHA256:...`). |

Требования валидации:

- `addr`, `user` и `host-key-fingerprint` обязательны;
- требуется хотя бы один метод аутентификации: `password` или `private-key-file`.

SFTP поддерживает и source, и result. Result публикуется через temp-upload + rename (атомарно); `NoOverwrite` — через эксклюзивное создание (`O_EXCL`).

> **SSH host key**: для SFTP **обязательно** задать `host-key-fingerprint` (SHA-256). Без него конфигурация отклоняется на этапе валидации. Fingerprint можно получить командой `ssh-keyscan -t ed25519 host | ssh-keygen -lf -`.

#### FTP (`storage: ftp`) и FTPS (`storage: ftps`)

| Ключ | Дефолт | Описание |
|------|--------|----------|
| `addr` | — | Адрес `host:port` (**обязательно**). |
| `user` | — | Пользователь. |
| `password` | — | Пароль. |
| `root` | — | Корневой каталог (пусто = корень). |
| `tls` | `false` | Для `ftps` всегда `true` (explicit TLS, AUTH TLS). |
| `tls-verify` | `true` | Проверять TLS-сертификат. Для `ftps` значение `false` **запрещено** (ошибка валидации). |

- **FTPS** (`ftps`): поддерживает и source, и result. Result публикуется через temp-upload + rename; `NoOverwrite` — best-effort проверка существования перед rename (не атомарно).
- **FTP** (`ftp`): поддерживает и source, и result (аналогично FTPS, но без TLS). Публикация требует команд `STOR`, `RNFR`/`RNTO` и `DELE`; при их отсутствии `Publish` вернёт `ErrUnavailable`.

> **TLS**: FTPS проверяет сертификат по умолчанию (`tls-verify: true`). Отключение проверки (`tls-verify: false`) **запрещено** на этапе валидации. Для самоподписанных сертификатов настройте доверенные CA в системе.

#### HTTP/HTTPS (`storage: http`)

HTTP/HTTPS — **source-only** backend: реализует только чтение исходников и **не может** использоваться как result (ошибка старта).

| Ключ | Дефолт | Описание |
|------|--------|----------|
| `base-url` | — | Базовый адрес исходников (**обязательно**). Не должен содержать query-параметры или fragment. |

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
- `404`/`410` → `ErrNotFound`; `401`/`403`, `408`, `429`, `5xx` и прочие non-2xx → `ErrUnavailable`.
- Размер ограничивается `spool-max-bytes` (превышение → `ErrQuota`); при наличии `Content-Length` объект отклоняется до скачивания.
- Метаданные — из `Content-Length`, `Last-Modified`, `Content-Type`, `ETag`.
- Таймаут запроса — `dial-timeout` (по умолчанию `30s`).

### `detection` — детектор лиц/объектов

Секция настраивает операции `fc` (face-crop) и `oc` (object-crop) на libvips. Детекция исполняет ONNX-модели (YuNet для лиц, SSD/YOLO-подобную для объектов) внутри процесса; для этого бинарь должен быть собран с тэком `-tags onnx` и иметь C-библиотеку ONNX Runtime (`libonnxruntime`) в системе.

| Ключ | Тип | Дефолт | Описание |
|------|-----|--------|----------|
| `face-model` | string | пусто | Путь к ONNX-модели YuNet для `fc`. Пусто = face-crop отключён. |
| `object-model` | string | пусто | Путь к ONNX-модели (SSD/YOLO) для `oc`. Пусто = object-crop отключён. |
| `confidence-threshold` | float | `0.5` | Порог уверенности в `[0,1]`. Боксы ниже отбрасываются до NMS. |
| `max-objects` | int | `5` | Максимальное число объектов после NMS (первый N самых уверенных). Должно быть `> 0`. |
| `margin` | float | `0.1` | Отступ к найденной области как доля её размера в `[0,1]` (по половине с каждой стороны). |

Секция не имеет флага `enabled` — включение задаётся непустыми путями к моделям. Модели загружаются лениво (при первом запросе `fc`/`oc`) и кэшируются на время жизни процесса; отсутствующий файл модели — ошибка при первом обращении (не при старте). Без тэка `onnx` любые `fc`/`oc` возвращают понятную ошибку.

### `imagemagick` — процессор ImageMagick

Процессор запускает бинарник ImageMagick как subprocess для каждой операции. Лимиты применяются **тремя слоями**:

1. `-limit` аргументы командной строки (`limits` ниже);
2. сгенерированный `policy.xml` (`policy` ниже) через `MAGICK_CONFIGURE_PATH`;
3. application-level ограничения: bounded writer (`output-bytes`) и context deadline (`timeout`) — не полагаются только на policy.

| Ключ | Тип | Дефолт | Описание |
|------|-----|--------|----------|
| `binary` | string | `"magick"` | Имя или путь к исполняемому файлу ImageMagick. Для версии 6 укажите `"convert"`. |

#### `imagemagick.policy` — deny-by-default policy.xml

При `enabled: true` генерируется политика:

- запрет всех coders/delegates по умолчанию, разрешён только безопасный whitelist (JPEG/JPG/PNG/WEBP/GIF/AVIF/HEIC/HEIF/APNG/JXL/MIFF/PPM/PGM/PBM/PNM/TIFF/BMP/ICO);
- явный запрет network- и scripting-coders (URL/HTTPS/HTTP/FTP/MSL/MVG/LABEL/TEXT/PLASMA/WPG/PS/PDF/SVG и др.) и delegates (curl, wget, ssh, rsvg, inkscape...);
- resource limits (`0` = не задавать соответствующую директиву).

| Ключ | Тип | Дефолт | Описание |
|------|-----|--------|----------|
| `enabled` | bool | `true` | Включать генерацию policy.xml. `false` = полагаться на системную policy. |
| `dir` | string | пусто | Каталог, куда пишется policy.xml. Пусто = временный каталог (удаляется при закрытии). Файл пишется атомарно (права 0600, каталог 0700). |
| `max-memory-bytes` | int64 | `0` | Resource policy: память (байт). |
| `max-map-bytes` | int64 | `0` | Resource policy: виртуальная память (байт). |
| `max-disk-bytes` | int64 | `0` | Resource policy: дисковый кэш (байт). |
| `max-threads` | int | `0` | Resource policy: потоки. |
| `max-time-seconds` | int | `0` | Resource policy: время выполнения (сек). |
| `max-width` | int64 | `0` | Resource policy: ширина (px). |
| `max-height` | int64 | `0` | Resource policy: высота (px). |
| `max-pixels` | int64 | `0` | Resource policy: площадь (px, защита от decompression bomb). |
| `max-frames` | int | `0` | Resource policy: кадры анимации. |
| `disable-network` | bool | `true` | Отключать network-capable delegates (URL, HTTPS, FTP, MSL, MVG...) в policy.xml. **Не отключайте в production** (риск SSRF). |
| `disabled-coders` | list[string] | пусто | Дополнительные coders для явного запрета (помимо встроенного опасного списка). |
| `disabled-delegates` | list[string] | пусто | Дополнительные delegates для явного запрета. |

#### `imagemagick.limits` — resource limits subprocess

| Ключ | Тип | Дефолт | Описание |
|------|-----|--------|----------|
| `timeout` | duration | — | Application-level context deadline на один subprocess. Превышение → убийство процесса и `LimitError`. |
| `output-bytes` | int64 | `0` | Application-level лимит размера выходного файла (bounded writer на stdout; при превышении подпроцесс отменяется). |
| `memory-bytes` | int64 | `0` | `-limit memory` (байт). |
| `map-bytes` | int64 | `0` | `-limit map` (байт). |
| `disk-bytes` | int64 | `0` | `-limit disk` (байт). |
| `threads` | int | `0` | `-limit threads` (`0` = авто). |
| `time-seconds` | int | `0` | `-limit time` (сек). |
| `width` | int64 | `0` | `-limit width` (px). |
| `height` | int64 | `0` | `-limit height` (px). |
| `pixels` | int64 | `0` | `-limit area` (px = w×h; защита от decompression bomb). |
| `frames` | int | `0` | Лимит кадров анимации (list-length в policy). |
| `concurrency` | int | `16` | Максимум одновременно работающих ImageMagick subprocess (реальная защита CPU/RAM). `0` = в коде применяется `16`. Рекомендуется задавать явное разумное значение (например 2–4). |
| `webp-method` | int | `0` | Метод сжатия WebP (`0`–`6`; `0` = умолчание ImageMagick, `4` = баланс, `6` = максимальное сжатие). |
| `png-compression-level` | int | `0` | Уровень сжатия PNG (`0`–`9`; `0` = умолчание ImageMagick, обычно `6`). |

### `application` — прикладные лимиты

| Ключ | Тип | Дефолт | Описание |
|------|-----|--------|----------|
| `output-limit` | int64 | `0` | Максимальный размер выходного файла, который будет сохранён (`0` = без лимита). При превышении генерация прерывается, результат не сохраняется. |
| `buffer-max-bytes` | int64 | `524288000` | Общий бюджет памяти для spillable-буфера (source и result вместе). При исчерпании — спул на диск. `0` = без лимита (не рекомендуется). |

### `observability` — логирование

| Ключ | Тип | Дефолт | Описание |
|------|-----|--------|----------|
| `log-level` | string | `"info"` | Уровень логирования: `debug`, `info`, `warn`, `error` (регистронезависимо). |

---

## Политики и лимиты (сводка)

Лимиты применяются на нескольких уровнях:

1. **`policy.global.limits`** — лимиты запроса (размер исходника, размеры, пиксели, dpr, кадры, длительность, размер выхода, concurrency). Применяются к любому режиму авторизации.
2. **`imagemagick.policy`** — resource policies в сгенерированном `policy.xml` (память, диск, потоки, время, размеры, пиксели, кадры). Применяются ImageMagick на уровне policy **до** `-limit`.
3. **`imagemagick.limits`** — `-limit` аргументы subprocess (memory/map/disk/threads/time/width/height/area/frames) + application-level (`timeout`, `output-bytes`, `concurrency`).
4. **`application`** — `output-limit` (размер сохраняемого файла) и `buffer-max-bytes` (бюджет памяти буферов).

---

## Безопасность

- **Deny-by-default `policy.xml`**: запрещает все coders/delegates, разрешает только безопасный whitelist; network- и scripting-coders отключены (`imagemagick.policy.enabled`, `disable-network`).
- **Resource limits**: ImageMagick subprocess ограничен по памяти/времени/размеру выхода; application-level bounded writer и context deadline не полагаются только на policy.
- **CORS deny-by-default**: пустой `allowed-origins` = никакие cross-origin запросы не получают CORS-заголовков; комбинация `"*"` + `allow-credentials: true` запрещена.
- **HTTP hardening**: security-заголовки (`Referrer-Policy`, опционально `CSP`), bounded URL length (414), `max-header-bytes` (431), `max-body-bytes` (сервис не принимает тела запросов), таймауты сервера.
- **Not-found fallback**: при отсутствии ассета — пиксель/редирект/картинка/страница (приоритет: pixel > redirect > image > page), с `Cache-Control: no-store`.
- **SFTP**: обязательная проверка host key (`host-key-fingerprint`).
- **FTPS**: обязательная проверка TLS-сертификата (`tls-verify: false` запрещён).
- **HTTP source**: redirects запрещены (защита от SSRF через редиректы).
- **Секреты**: не логируются, не попадают в метрики; задаются в `setting-local.yaml` (не коммитится) или через env `IMAGER_S3_ACCESS_KEY` / `IMAGER_S3_SECRET_KEY`.

---

## Эндпоинты

| Endpoint | Назначение |
|----------|------------|
| `/*` | Asset URL (канонический или preset) — обрабатывается через fallback-семантику. |
| `/healthz` | Liveness. `200` пока процесс жив; `503` при shutdown. |
| `/readyz` | Readiness. `200` пока сервис принимает запросы (включая проверку зависимостей); `503` при shutdown или недоступности зависимостей. |
| `/metrics` | Метрики в Prometheus exposition format (bounded cardinality). |
| `/debug/vars` | Сырые expvar-переменные (тот же источник, что `/metrics`). |
| всё прочее | 404 через fallback-семантику (not-found). |

Healthcheck в Dockerfile/compose использует `/healthz`.

### Observability

- **Логи**: структурированные JSON-логи в **stderr** через `log/slog`. Каждый запрос получает `request_id` (заголовок `X-Request-Id` или сгенерированный).
- **Приватность**: URL/query/raw user input и секреты **не** логируются и не попадают в метрики. Логируются только bounded-события (статус-классы, ошибки по категориям, длительности).
- **Метрики** (stdlib `expvar`, без внешних зависимостей; все label-ы — фиксированные enum-ы):
  - `imager_requests{class}` — счётчик запросов по классу статуса (`2xx/3xx/4xx/5xx`);
  - `imager_request_duration_seconds` — гистограмма длительности запросов;
  - `imager_cache_hits` / `imager_cache_misses` — кэш-стадии;
  - `imager_processor_success` / `imager_processor_errors` — стадия процессора;
  - `imager_processor_duration_seconds` — гистограмма обработки;
  - `imager_storage_ops{op}` — операции хранилища (`source_lookup`, `source_open`, `result_lookup`, `result_open`, `result_publish`) с суффиксом `_success`/`_error`;
  - `imager_storage_duration_seconds_{op}` — гистограммы длительности storage ops.

---

## Примеры конфигураций

### FS → FS (минимальный)

```yaml
version: "1"

server:
  addr: ":8080"

http:
  allowed-origins:
    - "https://cdn.example.com"
  allow-credentials: false
  cache-control: "public, max-age=31536000, immutable"
  not-found:
    pixel: true

policy:
  global:
    authorization: "safe"
    allowed-presets: ["thumb"]
    size-rules: ["0-2000x0-2000"]
    limits:
      source-bytes: 10485760        # 10 MiB
      output-bytes: 10485760        # 10 MiB
  presets:
    - name: "thumb"
      size: "200x200"
      output-format: webp
      quality: 85
      dpr: 1
      auto-orient: true
      rotate: "90"
      flip: horizontal

processing:
  default-quality: 85
  default-auto-orient: true
  default-rotate: ""
  default-flip: ""

source:
  storage: fs
  path: "./data/source"

result:
  storage: fs
  path: "./data/result"

imagemagick:
  binary: "magick"
  policy:
    enabled: true
  limits:
    timeout: "30s"
    output-bytes: 10485760
    concurrency: 2

application:
  output-limit: 10485760
  buffer-max-bytes: 524288000

observability:
  log-level: "info"
```

### S3 → S3

```yaml
source:
  storage: s3
  bucket: "my-images-source"
  prefix: "source/"
  endpoint: "https://storage.yandexcloud.net"
  region: "ru-central1"
  access-key: "AKIA..."            # или env IMAGER_S3_ACCESS_KEY
  secret-key: "..."                # или env IMAGER_S3_SECRET_KEY
  dial-timeout: "30s"
  read-timeout: "60s"
  max-attempts: 3
  max-idle-conns: 100
  max-idle-conns-per-host: 10
  idle-conn-timeout: "90s"
  metadata-ttl: "30s"

result:
  storage: s3
  bucket: "my-result-bucket"
  prefix: "gen"
  endpoint: "https://storage.yandexcloud.net"
  region: "ru-central1"
  access-key: "AKIA..."
  secret-key: "..."
```

### SFTP → S3

```yaml
source:
  storage: sftp
  addr: "sftp.example.com:22"
  user: "imager"
  private-key-file: "/etc/imager/id_ed25519"
  root: "/srv/images"
  host-key-fingerprint: "SHA256:AbCd..."   # обязательно
  dial-timeout: "30s"

result:
  storage: s3
  bucket: "imager-cache"
  prefix: "thumbs"
```

### HTTP → FS

```yaml
source:
  storage: http
  base-url: "https://cdn.example.com/images"
  spool-max-bytes: 10485760
  dial-timeout: "30s"
  read-timeout: "60s"
  max-attempts: 3

result:
  storage: fs
  path: "./data/result"
```

---

## Пример настройки с Nginx

Если вы хотите использовать Nginx для проксирования запросов: если файл не существует, запрос перенаправляется на микросервис, который создаст превью изображения.

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
    server http://127.0.0.1:8080;
}
```

---

## Production

Полное руководство по production-запуску, env/config, security assumptions, resource limits, health endpoints и storage roadmap — в [`docs/PRODUCTION.md`](docs/PRODUCTION.md).

---

## Используйте библиотеки **Imager Client** в своих проектах для формирования ссылок на миниатюры

Вы можете использовать различные языки программирования для работы с Imager.

### [Golang](https://github.com/pkg-ru/imager-client/blob/master/doc/GO-RU.md)

```bash
go get github.com/pkg-ru/imager-client
```

### [PHP](https://github.com/pkg-ru/imager-client/blob/master/doc/PHP-RU.md)

```bash
composer require pkg-ru/imager-client
```

### [JavaScript (TypeScript)](https://github.com/pkg-ru/imager-client/blob/master/doc/TS-RU.md)

```bash
npm i imager-client
```

### [Python3](https://github.com/pkg-ru/imager-client/blob/master/doc/PY-RU.md)

```bash
pip install imager-client
