# Политика доступа (Policy Engine)

Политика Imager определяет, какие ассеты могут быть сгенерированы из каких исходников. Модель — **deny-by-default**: запрос разрешён, только если найдено подходящее правило (`path-policy`). Это ключевой механизм безопасности: без явного правила ни один ассет не будет обработан.

Связанные документы: [ARCHITECTURE.md](ARCHITECTURE.md), [API.md](API.md), [CONFIGURATION.md](CONFIGURATION.md), [SECURITY.md](SECURITY.md).

## Модель политики

Политика состоит из двух уровней:

1. **Пресеты** (`presets`) — именованные наборы параметров обработки: размер, качество, операции, переопределения кодеков.
2. **Path-policies** (`path-policies`) — правила, привязывающие пресеты к путям исходников с ограничениями.

### Пресеты

Пресет объявляется в секции `presets` конфигурации политики. Пример:

```yaml
policy:
  presets:
    thumb:
      width: 200
      height: 200
      output-formats: [webp, avif]
      quality: 80
      dpr: 1
    full:
      width: 4000
      height: 4000
      output-formats: [jpeg, webp]
      quality: 85
```

Параметры пресета:

| Параметр | Описание |
|----------|----------|
| `width`, `height` | Базовый (логический) размер; итоговый = `width × dpr`. `0` = не задан (вычисляется пропорционально). |
| `output-formats` | **Обязательный** whitelist выходных форматов (`jpeg\|png\|webp\|gif\|avif\|heif\|jxl`). |
| `quality` | Качество кодирования (0–100; `0` = `encoders.default-quality`). |
| `crop` | Операция: `""`=resize, `center`, `smart`, `face`, `object`, `face-fix`, `object-fix`. |
| `trim` | Независимый фильтр обрезки однотонных полей (bool). |
| `dpr` | Множитель плотности пикселей (см. правила dpr). |
| `frames` / `duration` | Лимиты анимации (0 = без ограничения). |
| `loop` | Зацикливание анимации (nil = `processing.default-loop`). |
| `watermark` | Имя ватермарки из секции `watermarks`. |
| `auto-orient` / `rotate` / `flip` | Ориентация (nil = глобальный дефолт). |
| `<формат>-<параметр>` | Плоские native-ключи кодеков (`webp-quality`, `jxl-effort` и т.д.). |

### Path-policies

Path-policy — **map**: ключ = префикс пути, значение = настройки. Выбор — longest-prefix match, `"/"` — fallback. Каждая path-policy перечисляет доступные `presets` (имена из `policy.presets`) и `customs` (размер-грамматика с настройками как у пресетов):

```yaml
policy:
  path-policies:
    "/":
      presets: [thumb, full]
      customs:
        x:
          output-formats: [webp]
        200x200:
          output-formats: [jpeg, webp]
    "/photos":
      presets: [thumb]
      customs:
        400x300:
          output-formats: [webp, avif]
```

| Параметр | Описание |
|----------|----------|
| *(ключ)* | Префикс пути (нормализуется в `/prefix`); `"/"` — fallback для всех путей. |
| `presets` | Список имён глобальных пресетов (`policy.presets`), доступных на пути. |
| `customs` | Map custom-размеров: ключ = размер-грамматика (`x`, `x200`, `200x`, `200x200`, опционально `@2`/`@3`), значение = настройки как у пресетов. |

## Сопоставление путей

Сопоставление пути исходника с path-policy — по **самому длинному совпадающему префиксу** (longest-prefix matching). Префиксы нормализуются (`normalizePath`): добавляется ведущий `/`, убирается завершающий `/` (кроме `"/"`). Совпадение — по сегментам: путь равен префиксу или начинается с `префикс/`. Регистр не нормализуется.

Пример: для пути `/photos/2024/01/img.jpg`:

- `/photos` — совпадает;
- `/photos/2024` — совпадает и длиннее;
- `/photos/2024/01` — совпадает и ещё длиннее.

Выбирается самое длинное совпадение. Если ни одно правило не совпало (и нет `"/"`-fallback) — запрос запрещён (`deny_by_default`).

## Алгоритм принятия решения

Решение принимается в `domain/policy/policy.go` (метод `Resolve`/`Authorize`). Алгоритм:

1. Если запрос пуст (`nil_request`) — запрет.
2. Найти path-policy по самому длинному префиксу. Не найдено (нет `"/"` и совпадений) — `path_not_allowed`.
3. Разрешить сегмент: точное совпадение `segment@dpr` в customs → базовое имя в customs (wildcard-dpr) → тот же алгоритм в presets пути. Не найдено — `segment_not_allowed`.
4. Проверить DPR: конфликт `@dpr` URL с фиксированным `@N` в имени или заданным `dpr` пресета/custom — `dpr_not_allowed`.
5. Проверить выходной формат: должен входить в whitelist `output-formats` пресета/custom — `format_not_allowed`.
6. Разрешено (`allowed`).

### DecisionReason

Результат решения — перечисление `DecisionReason`:

| Значение | Смысл |
|----------|-------|
| `allowed` | Запрос разрешён. |
| `deny_by_default` | Нет подходящего path-policy (deny-by-default). |
| `path_not_allowed` | Путь не покрыт ни одним правилом. |
| `segment_not_allowed` | Имя сегмента недопустимо. |
| `dpr_not_allowed` | DPR превышает ограничение. |
| `format_not_allowed` | Формат (входной или выходной) не разрешён. |
| `nil_request` | Пустой запрос. |

## Приоритет пресетов и кастомных размеров

При разрешении сегмента URL (в рамках выбранной path-policy):

1. Точное совпадение полного имени `segment@dpr` в `customs` пути.
2. Базовое имя в `customs` (wildcard-dpr: `@dpr` в URL разрешён, только если у custom `dpr` не задан).
3. Тот же алгоритм в `presets` пути (customs имеют приоритет над presets).
4. Ничего не найдено — deny (`segment_not_allowed`).

Сегмент URL — всегда имя пресета или custom-имя (размер-грамматика); «первый пресет из whitelist'а» не применяется — сегмент обязан быть явно указан в URL.

## Переопределения кодеков

Пресет/custom может содержать **плоские native-ключи** вида `<формат>-<параметр>` (имена параметров реестра `domain/encoding` с префиксом формата). Приоритет на каждом экспорте:

1. Preset override (плоские native-ключи) — самый высокий.
2. Секция `encoders` YAML-конфигурации.
3. Автоматическое маппинг-сопоставление (anchor mapping по качеству).
4. Дефолт реестра кодеков.

Пример переопределения:

```yaml
presets:
  webp-high:
    width: 1000
    quality: 90
    output-formats: [webp, avif]
    webp-reduction-effort: 6
    avif-speed: 4
```

## Компиляция политики

Политика компилируется при старте (`domain/policy/compile.go`):

1. `ValidateConfig` — валидация конфигурации (размеры, DPR, форматы, пресеты).
2. `Compile` — компиляция в эффективную структуру для быстрого сопоставления.
3. `encodingOverridesFromConfig` — извлечение переопределений кодеков из пресетов.
4. `sizeFromPreset` / `sizeFromCustom` — вычисление размеров из пресета или кастомного запроса.

Ошибки валидации конфигурации приводят к отказу старта сервиса (fail-fast).

## Полный пример конфигурации

```yaml
policy:
  presets:
    thumb:
      width: 200
      height: 200
      output-formats: [webp, avif]
      quality: 80
      dpr: 1
    full:
      width: 4000
      height: 4000
      output-formats: [jpeg, webp]
      quality: 85
      dpr: 1
  path-policies:
    "/":
      presets: [thumb, full]
      customs:
        x:
          output-formats: [webp]
        200x200:
          output-formats: [jpeg, webp]
    "/photos":
      presets: [thumb]
      customs:
        400x300:
          output-formats: [webp, avif]
```

## Объяснение человеческим языком

- **Deny-by-default**: если для пути нет правила — ассет не генерируется (HTTP 403).
- **Longest-prefix**: более специфичное правило перекрывает общее. `/photos/2024/` важнее `/photos/`.
- **Whitelist пресетов**: из пути можно запросить только те пресеты, которые явно перечислены.
- **Ограничения**: даже разрешённый путь не даёт права на любой размер, формат или DPR — всё проверяется по правилу.

## Learning-mode

При включённом learning-mode (`policy.learning-mode: true`) сервис генерирует и отдаёт ассеты, которые не разрешены текущими `path-policies`, но **не сохраняет** их в result-хранилище. Наблюдаемые пути накапливаются в `generate-local.yaml` (с комментарием `# added by learning-mode`). Это позволяет «обучить» политику на реальном трафике перед включением. Подробности — в [CONFIGURATION.md](CONFIGURATION.md#policylearning-mode).

## Связанные документы

- [ARCHITECTURE.md](ARCHITECTURE.md) — место политики в конвейере.
- [API.md](API.md) — как политика влияет на HTTP-ответы (403).
- [CONFIGURATION.md](CONFIGURATION.md) — секция `policy` конфигурации.
- [SECURITY.md](SECURITY.md) — политика как механизм безопасности.
- [FORMATS.md](FORMATS.md) — допустимые форматы.