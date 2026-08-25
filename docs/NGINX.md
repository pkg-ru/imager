# Nginx перед imager

## 1. Назначение

Nginx используется как внешний HTTP-сервер перед `imager` и решает четыре задачи:

1. Отдаёт уже существующие ассеты без обращения к `imager`.
2. При отсутствии ассета передаёт запрос в `imager` для генерации.
3. При необходимости отдаёт оригиналы напрямую из хранилища.
4. Завершает TLS и предоставляет HTTP/2; HTTP/3 подключается опционально.

Главный принцип:

```text
GET asset
    │
    ├── asset уже существует ──────► nginx / S3
    │
    └── asset отсутствует ─────────► imager
                                      │
                                      └── storage
```

URL ассета соответствует ключу result-хранилища. Для локального FS это позволяет использовать `try_files` без преобразования URL.

---

## 2. Выбор схемы хранения

Конфигурация выбирается в зависимости от `source.storage` и `result.storage`.

### Локальный FS

Рекомендуемая схема:

```text
client
  │
  ▼
 nginx
  │
  ├── result/source → filesystem
  │
  └── miss → imager
```

`proxy_cache` не используется.

Это наиболее быстрый вариант при наличии локального SSD/NVMe: nginx читает файл напрямую, а повторные обращения дополнительно обслуживаются page cache ОС.

### Публичный S3

Схема:

```text
client
  │
  ▼
 nginx
  │
  ├── object exists → public S3
  │
  └── 404 → imager → S3
```

Такой вариант подходит, если готовые объекты публичны. Yandex Object Storage позволяет отдельно разрешать публичное чтение объектов, а Selectel поддерживает public/private buckets.

Для public S3 nginx может выполнять роль единой точки доступа и fallback на `imager`.

### Приватный S3

Схема:

```text
client
  │
  ▼
 nginx
  │
  ▼
 imager
  │
  └── private S3
```

Nginx не должен самостоятельно получать приватные S3-объекты, если для этого требуется S3-аутентификация. В таком варианте `imager` остаётся владельцем доступа к S3, а nginx только проксирует запрос.

Для private S3 дополнительно можно включить `proxy_cache` на nginx.

---

# 3. Общая конфигурация nginx

Эти параметры общие для всех режимов.

```nginx
http {
    include /etc/nginx/mime.types;

    server_tokens off;

    # Не кэшируем POST/DELETE и другие методы.
    proxy_cache_methods GET HEAD;

    # Пример upstream.
    upstream imager {
        server imager:8080;
        keepalive 32;
    }

    server {
        listen 443 ssl;
        http2 on;

        server_name images.example.com;

        ssl_certificate     /etc/nginx/ssl/fullchain.pem;
        ssl_certificate_key /etc/nginx/ssl/privkey.pem;

        # ------------------------------------------------------------
        # Основной asset endpoint
        # ------------------------------------------------------------

        location / {
            # storage-specific configuration
        }

        # ------------------------------------------------------------
        # Health
        # ------------------------------------------------------------

        location = /healthz {
            proxy_pass http://imager;

            proxy_http_version 1.1;
            proxy_set_header Connection "";

            proxy_set_header Host              $host;
            proxy_set_header X-Real-IP         $remote_addr;
            proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto $scheme;

            proxy_connect_timeout 5s;
            proxy_read_timeout 5s;

            proxy_buffering off;
        }

        location = /readyz {
            proxy_pass http://imager;

            proxy_http_version 1.1;
            proxy_set_header Connection "";

            proxy_set_header Host              $host;
            proxy_set_header X-Real-IP         $remote_addr;
            proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto $scheme;

            proxy_connect_timeout 5s;
            proxy_read_timeout 5s;

            proxy_buffering off;
        }

        # ------------------------------------------------------------
        # Служебные endpoints
        # ------------------------------------------------------------

        location ~ ^/(metrics|admin)(/|$) {
            deny all;
        }

        # ------------------------------------------------------------
        # TLS / HTTP security
        # ------------------------------------------------------------

        add_header X-Content-Type-Options nosniff always;

        # Если это значение используется imager,
        # оно должно совпадать с его настройкой.
        add_header Referrer-Policy "no-referrer" always;
    }
}
```

В текущем imager публичными являются asset URL, `/healthz` и `/readyz`; `/metrics` и `/admin/*` являются служебными endpoint'ами.
HTTP/2 включается директивой `http2 on;`; такой синтаксис актуален для современных версий nginx.

HTTP/3 можно добавить отдельно:

```nginx
server {
    listen 443 quic reuseport;
    listen 443 ssl;

    http3 on;

    ssl_certificate     /etc/nginx/ssl/fullchain.pem;
    ssl_certificate_key /etc/nginx/ssl/privkey.pem;

    add_header Alt-Svc 'h3=":443"; ma=86400' always;
}
```

HTTP/3 требует nginx с `ngx_http_v3_module`; модуль не включён в стандартную сборку автоматически.

---

# 4. Режим: локальный FS

Если `source.storage: fs` и/или `result.storage: fs`, nginx может работать без дополнительного HTTP-кэша.

Для result:

```nginx
location / {
    root /data/result;

    try_files $uri @source;
}
```

Для source:

```nginx
location @source {
    root /data/source;

    try_files $uri @imager;
}
```

Fallback в imager:

```nginx
location @imager {
    proxy_pass http://imager;

    proxy_http_version 1.1;
    proxy_set_header Connection "";

    proxy_set_header Host              $host;
    proxy_set_header X-Real-IP         $remote_addr;
    proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;

    proxy_connect_timeout 5s;

    # Должно быть больше максимального времени генерации imager.
    proxy_send_timeout 120s;
    proxy_read_timeout 120s;

    # Для изображений оставляем buffering.
    proxy_buffering on;
}
```

`try_files` сначала проверяет result, затем source и только после этого делает внутренний переход в `@imager`. Это штатный сценарий использования `try_files` с named location.

### Почему без proxy_cache

Не следует делать:

```text
filesystem
    ↓
nginx
    ↓
proxy_cache
    ↓
client
```

без необходимости.

Получается дополнительный слой хранения:

```text
filesystem → nginx cache → client
```

при том, что ОС уже кэширует недавно прочитанные страницы файлов. Для локального SSD это обычно только усложняет конфигурацию и расходует место на ещё один кэш.

---

# 5. Режим: публичный S3

Для публичного S3 nginx может сначала запросить объект непосредственно из object storage.

```nginx
upstream s3_public {
    server S3_PUBLIC_ENDPOINT;
    keepalive 32;
}
```

Пример:

```nginx
location / {
    proxy_intercept_errors on;

    proxy_pass https://s3_public;

    proxy_http_version 1.1;
    proxy_set_header Connection "";

    proxy_set_header Host S3_PUBLIC_HOST;

    proxy_ssl_server_name on;
    proxy_ssl_name S3_PUBLIC_HOST;

    proxy_connect_timeout 5s;
    proxy_send_timeout 60s;
    proxy_read_timeout 60s;

    proxy_buffering on;

    # Объект отсутствует → генерация через imager.
    error_page 404 = @imager;
}
```

Fallback:

```nginx
location @imager {
    proxy_pass http://imager;

    proxy_http_version 1.1;
    proxy_set_header Connection "";

    proxy_set_header Host              $host;
    proxy_set_header X-Real-IP         $remote_addr;
    proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;

    proxy_connect_timeout 5s;
    proxy_send_timeout 120s;
    proxy_read_timeout 120s;

    proxy_buffering on;
}
```

Таким образом:

```text
GET /photo-jpg/640x.webp
          │
          ▼
      public S3
       │     │
     200    404
      │      │
      ▼      ▼
   client  imager
```

Это особенно удобно для immutable assets: после первой генерации последующие запросы вообще не доходят до imager.

У конкретного S3-провайдера endpoint и способ формирования публичного URL отличаются. Например, Selectel поддерживает public bucket и отдельные публичные домены объектов, а Yandex Object Storage позволяет отдельно разрешать анонимное чтение объектов.

---

# 6. Режим: приватный S3

Если result хранится в приватном S3, основной endpoint nginx должен проксировать запрос в imager:

```nginx
location / {
    proxy_pass http://imager;

    proxy_http_version 1.1;
    proxy_set_header Connection "";

    proxy_set_header Host              $host;
    proxy_set_header X-Real-IP         $remote_addr;
    proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;

    proxy_connect_timeout 5s;
    proxy_send_timeout 120s;
    proxy_read_timeout 120s;

    proxy_buffering on;
}
```

В этом варианте:

```text
client
   │
   ▼
 nginx
   │
   ▼
 imager
   │
   ▼
 private S3
```

Это наиболее простой вариант с точки зрения безопасности: S3 credentials не появляются в nginx.

---

# 7. Proxy cache для S3

`proxy_cache` рекомендуется прежде всего для S3/HTTP-origin.

```nginx
proxy_cache_path /var/cache/nginx/imager
    levels=1:2
    keys_zone=imager_cache:32m
    max_size=20g
    inactive=30d
    use_temp_path=off;
```

`max_size` и размер `keys_zone` должны подбираться под инфраструктуру клиента.

`proxy_cache_path` хранит тело ответа на диске, а метаданные активных элементов — в shared memory. nginx автоматически удаляет наименее используемые элементы при достижении `max_size`.

Подключение:

```nginx
location / {
    proxy_cache imager_cache;

    proxy_cache_key "$scheme://$host$uri";

    proxy_cache_lock on;
    proxy_cache_lock_timeout 30s;

    proxy_cache_use_stale
        error
        timeout
        updating
        http_500
        http_502
        http_503
        http_504;

    proxy_pass http://imager;

    proxy_http_version 1.1;
    proxy_set_header Connection "";

    proxy_set_header Host              $host;
    proxy_set_header X-Real-IP         $remote_addr;
    proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;

    proxy_connect_timeout 5s;
    proxy_send_timeout 120s;
    proxy_read_timeout 120s;

    proxy_buffering on;
}
```

`proxy_cache_lock` позволяет только одному запросу заполнять новый cache entry; остальные запросы этого же URL ждут результат либо истечения lock timeout.

В текущем imager уже есть защита от одновременной генерации одного ассета, поэтому nginx cache lock является дополнительной защитой, а не единственным механизмом предотвращения stampede.

---

# 8. Cache key

Для immutable asset URL query string обычно не является частью идентичности объекта.

Поэтому можно использовать:

```nginx
proxy_cache_key "$scheme://$host$uri";
```

Вместо стандартного ключа nginx, включающего `$request_uri`. Сам nginx по умолчанию использует ключ, близкий к `$scheme$proxy_host$request_uri`.

Если query-параметры в конкретной конфигурации imager влияют на результат, ключ необходимо изменить.

---

# 9. CORS

CORS должен оставаться настроенным в imager.

Если nginx начинает напрямую отдавать FS/S3-файлы, он должен отдавать совместимый набор CORS-заголовков.

Если политика:

```text
Access-Control-Allow-Origin: *
```

то nginx может использовать:

```nginx
add_header Access-Control-Allow-Origin "*" always;
```

Если origin динамический, необходимо учитывать `Origin` и добавлять:

```nginx
add_header Vary Origin always;
```

При использовании `proxy_cache` и динамического CORS cache key также должен учитывать `Origin`, иначе один origin может получить закэшированный ответ с CORS-заголовками другого origin.

Сам imager уже использует `Vary: Origin` при соответствующих запросах.

---

# 10. Cache-Control

Главное правило:

**не задавать nginx отдельную политику кэширования, противоречащую imager.**

Asset URL неизменяемы, поэтому для generated assets подходит длительное кэширование:

```http
Cache-Control: public, max-age=31536000, immutable
```

Но конкретное значение должно быть единым с `http.cache-control` imager.

Для FS nginx можно задать его напрямую:

```nginx
add_header Cache-Control "public, max-age=31536000, immutable" always;
```

Для proxy-ответа `Cache-Control` следует передавать от upstream.

Если используется `proxy_cache`, не стоит без необходимости вручную задавать многочисленные `proxy_cache_valid`. nginx умеет учитывать cache headers upstream, а `proxy_cache_valid` нужен как явная политика, когда upstream не предоставляет подходящую.

---

# 11. ETag и Last-Modified

Не следует пытаться сделать HTTP-ответ nginx и прямой ответ imager побайтно идентичными по служебным заголовкам.

При статической отдаче nginx самостоятельно формирует `ETag`, `Last-Modified` и поддерживает range-запросы. При отдаче через imager используются его собственные правила формирования заголовков. Это уже приводит к различиям.

Для immutable assets это не является проблемой.

Главное:

```text
URL immutable
+
Cache-Control max-age/immutable
=
клиенту не требуется постоянная revalidation
```

Поэтому не следует усложнять конфигурацию ради искусственного совпадения `ETag`.

---

# 12. Buffering

Для imager/S3:

```nginx
proxy_buffering on;
```

оставляется включённым.

При включённом buffering nginx забирает ответ от upstream максимально быстро и при необходимости использует временный файл на диске. Это позволяет не удерживать соединение с imager/S3 из-за медленного клиента.

Не нужно без измерений задавать:

```nginx
proxy_buffers 8 16k;
proxy_buffer_size 16k;
```

как обязательные параметры.

Стандартные значения nginx являются нормальной стартовой точкой; менять их следует после измерений.

---

# 13. Таймауты

Для обращения к imager:

```nginx
proxy_connect_timeout 5s;
proxy_send_timeout 120s;
proxy_read_timeout 120s;
```

`proxy_read_timeout` должен быть больше максимально допустимого времени генерации.

Например, если:

```yaml
http:
  generate-timeout: 30s
```

то `120s` даёт достаточный запас.

---

# 14. Ограничение нагрузки

В production можно дополнительно включить:

```nginx
# Ограничение числа запросов.
# limit_req_zone $binary_remote_addr zone=assets:10m rate=50r/s;

# Ограничение числа одновременных соединений.
# limit_conn_zone $binary_remote_addr zone=addr:10m;
```

И в `server`/`location`:

```nginx
# limit_req zone=assets burst=100 nodelay;
# limit_conn addr 100;
```

Эти ограничения должны настраиваться под реальную инфраструктуру. В базовый конфиг они не включаются.

---

# 15. Что не нужно делать

Не следует одновременно использовать все механизмы:

```text
FS
+
try_files
+
proxy_cache
+
S3 cache
+
imager cache
```

без причины.

Нужно выбирать цепочку согласно origin:

```text
FS:
nginx → filesystem → imager

Public S3:
nginx → S3 → imager

Private S3:
nginx → imager → S3

Private S3 + nginx cache:
nginx → cache → imager → S3
```

Каждый дополнительный слой должен решать конкретную проблему.

---

# 16. Рекомендуемый выбор

### Если есть быстрый локальный SSD

Использовать:

```text
result/source = FS
nginx = try_files
proxy_cache = OFF
```

Это самый простой и быстрый вариант.

### Если result/source находятся в public S3

Использовать:

```text
nginx → public S3
           ↓ 404
         imager
```

`proxy_cache` — опционально.

При этом само object storage может уже иметь собственный edge/cache-механизм. Например, Selectel отдельно кэширует объекты public bucket и позволяет задавать поведение через `Cache-Control`.

### Если S3 приватный

Использовать:

```text
nginx → imager → private S3
```

и при необходимости:

```text
nginx
  ↓
proxy_cache
  ↓ miss
imager
  ↓
S3
```

Это наиболее универсальный production-вариант для приватного объектного хранилища.

---

# 17. Итоговая структура конфигурации

Рекомендуется разделить конфигурацию на:

```text
/etc/nginx/
├── nginx.conf
├── mime.types
├── snippets/
│   └── imager-proxy.conf
└── conf.d/
    └── imager.conf
```

Общие proxy-заголовки и таймауты должны находиться в одном snippet, чтобы они не копировались по нескольким `location`.

Не нужно делать отдельные огромные конфигурации для FS/S3. Отличаться должен только механизм получения существующего объекта:

```text
FS       → try_files
Public S3 → proxy_pass S3 + error_page 404 = @imager
Private  → proxy_pass imager
```

Всё остальное — TLS, HTTP/2, health endpoints, security, proxy headers, timeouts, buffering и опциональный cache — задаётся один раз.
