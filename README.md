# Imager <sub><sup><sub>([Imager Client](https://github.com/pkg-ru/imager-client))</sub></sup></sub>
### WEB Микро сервис для генерации и компрессии миниатюр к картинкам на лету

Сервис принимает запрос на миниатюру (ассет) — генерирует его из исходного файла и сохраняет на диск в указанное место.

При повторных запросах того же ассета сервис отдает ранее созданный, сжатый файл.

> Например, вы хотите сжать и уменьшить картинку. Для этого нужно [сгенерировать ссылку](https://github.com/pkg-ru/imager-client) (ассет) на картинку — при запросе которой сервис **Imager** на лету создаст сжатую миниатюру и вернет пользователю.
> 
> <sub>
> Исходник: example.com/my_image.gif (не обязательно должен быть доступен из web)
>
> Ассет на миниатюру: example.com/my_image/DqcDCgCWSwoAlg.webp
> </sub>
>

---

## Запуск

Для запуска Imager можно использовать Docker. Воспользуйтесь следующими командами:

### Запуск с Docker

```bash
docker run -d -p 80:80 --volume ".:/app/example:rw" altrap/imager:v0.0.2
```

### Запуск с использованием `docker-compose`

```yaml
services:
  imager:
    image: altrap/imager:v0.0.2
    restart: always
    stop_signal: INT
    stop_grace_period: 5s
    ports:
      - 80:80
      - 443:443
    volumes:
      - ./:/app/example:rw
    working_dir: /app
    networks:
      - default
```

> **Примечание**: Настройки микро-сервиса можно указать в файле `setting.yaml`. Вы можете переопределить настройки, создав файл `setting-local.yaml` рядом с основным файлом конфигурации.

---

## Пример настройки микро-сервиса с Nginx

Если вы хотите использовать Nginx для проксирования запросов, выполните следующие шаги.

### Запуск с Docker

```bash
docker run -d -p 8181:80 --volume ".:/app/example:rw" --restart=always altrap/imager:v0.0.2
```

### Конфигурация Nginx

Файлы должны быть доступны для Nginx. Если файл не существует, запрос будет перенаправлен на микро-сервис, который создаст превью изображения.

**Пример конфигурации для Nginx**:

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
    server http://127.0.0.1:8181;
}
```

> **Примечание**: Imager можно использовать как самостоятельный сервер. Для этого необходимо установить зависимости: 
> - [ImageMagick](https://imagemagick.org/script/download.php)
> - [FFmpeg](https://ffmpeg.org/download.html)

---

## Используйте библиотеки **Imager Client** в своих проектах для формирования ссылок на миниатюры

Вы можете использовать различные языки программирования для работы с Imager.

### [Golang](https://github.com/pkg-ru/imager-client/blob/master/doc/GO-RU.md)

Для установки клиента Golang:

```bash
go get github.com/pkg-ru/imager-client
```

### [PHP](https://github.com/pkg-ru/imager-client/blob/master/doc/PHP-RU.md)

Для установки клиента PHP:

```bash
composer require pkg-ru/imager-client
```

### [JavaScript (TypeScript)](https://github.com/pkg-ru/imager-client/blob/master/doc/TS-RU.md)

Для установки клиента JavaScript (или TypeScript):

```bash
npm i imager-client
```

### [Python3](https://github.com/pkg-ru/imager-client/blob/master/doc/PY-RU.md)

Для установки клиента Python:

```bash
pip install imager-client
```
