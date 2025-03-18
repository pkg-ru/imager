# Imager
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
> можно пробросить в контейнер или создать рядом файл `setting-local.yaml` - настройки будут переопределяться

## Один из примеров настройки микро-сервиса с nginx

```bash
docker run -d -p 80:8181 --volume ".:/app/example:rw" --restart=always altrap/imager:v0.0.2
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
> для этого нужно установить зависимости: [ImageMagick](https://imagemagick.org/script/download.php) и [FFmpeg](https://ffmpeg.org/download.html)

для формирования ссылок на миниатюры картинок, можно использовать следующие библиатеки:

> **ВНИМАНИЕ**
> нужно и можно передавать только те параметры,
> которые разрешены в настройках `thumb` сервиса Imager
> по умолчанию `thumb` = `default`

## Golang

```bash
go get github.com/pkg-ru/imager/pkg/imager/imager-encode
```

```go
package main

import (
	"fmt"
	imagerencode "github.com/pkg-ru/imager/pkg/imager/imager-encode"
)

func main() {
	img := imagerencode.NewImage().Quality(75).Size(150, 150).Trim(true, 10, nil)

	fmt.Println(img.GetConvert("my_path_image.png", "webp"))  // return: my_path_image/DqcECgCWSwoAlg.webp
	fmt.Println(img.GetConvert("my_path_image2.jpg", "webp")) // return: my_path_image2/DqcBCgCWSwoAlg.webp
	fmt.Println(img.GetConvert("my_path_image3.gif", "webp")) // return: my_path_image3/DqcDCgCWSwoAlg.webp
	fmt.Println(img.GetConvert("my_path_image3.png", "gif"))  // return: my_path_image3/DqcEAwCWSwoAlg.gif
}
```

[GIT](https://github.com/pkg-ru/imager/tree/master/pkg/imager/imager-encode) / [GO](https://pkg.go.dev/github.com/pkg-ru/imager/pkg/imager/imager-encode)

## PHP

```bash
php composer.phar require --prefer-dist ru-pkg/imager-php "*"
```

```php
<?php

use ruPkg\imagerPhp\NewImage;

$imager = new NewImage;
$imager->quality(75)->size(150, 150)->trim(true, 10);

echo $imager->getConvert("my_path_image.png", "webp"), "\n";  // return: my_path_image/DqcECgCWSwoAlg.webp
echo $imager->getConvert("my_path_image2.jpg", "webp"), "\n"; // return: my_path_image2/DqcBCgCWSwoAlg.webp
echo $imager->getConvert("my_path_image3.gif", "webp"), "\n"; // return: my_path_image3/DqcDCgCWSwoAlg.webp
echo $imager->getConvert("my_path_image3.png", "gif"), "\n";  // return: my_path_image3/DqcEAwCWSwoAlg.gif

?>
```

[GIT](https://github.com/pkg-ru/imager-php)

## JS/TS

> dev
