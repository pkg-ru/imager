# Imager <sup><sub><sup>([Imager Client](https://github.com/pkg-ru/imager-client))</sub></sup></sub>
### Микро-сервис (горячей) генерации ассетов/превью картинок на лету

запуск

```bash
docker run -d -p 80:80 --volume ".:/app/example:rw" altrap/imager:v0.0.2
```

или `docker-compose.yaml`

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

> настройки микро-сервиса в файле `setting.yaml`
> 
> можно пробросить в контейнер или создать рядом файл `setting-local.yaml` - настройки будут переопределяться

## Один из примеров настройки микро-сервиса с nginx

```bash
docker run -d -p 8181:80 --volume ".:/app/example:rw" --restart=always altrap/imager:v0.0.2
```

примерный конфиг для nginx

файлы должны быть доступны для nginx, если файла нет - отправляем запрос на микро-сервис (который создает привьюху)

так-же папка с файлами должна быть проброшена в контейнер... (в нашем случае монтируем ее в /app/example)

```conf
server {
	# ....
	# картинки, если не существует - проксируем
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
	# ....
}

upstream imager {
    server http://127.0.0.1:8181;
}
```

> Imager можно использовать как самостоятельный сервер
> 
> для этого нужно установить зависимости: [ImageMagick](https://imagemagick.org/script/download.php) и [FFmpeg](https://ffmpeg.org/download.html)

---

#### Для формирования ссылок на миниатюры картинок, можно использовать следующие библиотеки:

#### [Golang](https://github.com/pkg-ru/imager-client/doc/GO-RU.md)

```bash
go get github.com/pkg-ru/imager-client
```

#### [PHP](https://github.com/pkg-ru/imager-client/doc/PHP-RU.md)

```bash
composer require pkg-ru/imager-client
```

#### [JavaScript (TS)](https://github.com/pkg-ru/imager-client/doc/TS-RU.md)

```bash
npm i imager-client
```

#### [Python3](https://github.com/pkg-ru/imager-client/doc/PY-RU.md)

```bash
pip install imager-client
```
