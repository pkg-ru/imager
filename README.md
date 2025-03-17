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

настройки в файле `setting.yaml`

для формирования ссылок на миниатюры картинок, можно использовать следующие библиатеки:
# Golang
```go
package main

import (
	"fmt"
	imagerencode "github.com/pkg-ru/imager/pkg/imager/imager-encode"
)

func main() {
	img := imagerencode.NewImage().Quality(75).Size(150, 150).Trim(true, 10, nil);

	img.GetConvert("my_path_image.png", "webp") // return uri image to webp format
	img.GetConvert("my_path_image2.jpg", "webp") // return uri image to webp format
	img.GetConvert("my_path_image3.gif", "webp") // return uri image to webp format
	img.GetConvert("my_path_image3.png", "gif") // return uri image to gif format
}
```
[GIT](https://github.com/pkg-ru/imager/tree/master/pkg/imager/imager-encode) / [GO](https://pkg.go.dev/github.com/pkg-ru/imager/pkg/imager/imager-encode)
# PHP
> dev
# JS/TS
> dev
