package imagerencode

import (
	"slices"
	"strings"
)

// инициализация
//
// thumb - настройки находятся в файле "setting.yaml" секция `thumb`
func NewImage(thumb ...string) *imageEncode {
	res := &imageEncode{
		loop: true,
	}
	if len(thumb) >= 1 {
		res.Thumb(thumb[0])
	}
	return res
}

// клонирование объекта
func (i *imageEncode) Clone() *imageEncode {
	s := *i
	return &(s)
}

// задаем ширину и высоту
// если один из размеров 0 - картинка масштабируется пропорционально по указанному размеру
func (i *imageEncode) Size(width, height uint16) (this *imageEncode) {
	i.width = width
	i.height = height
	return i
}

// качество сжатия
func (i *imageEncode) Quality(quality uint8) (this *imageEncode) {
	i.quality = quality
	return i
}

// Crop
func (i *imageEncode) Crop(crop bool) (this *imageEncode) {
	i.crop = crop
	return i
}

// цвет фона (при конвертации картинок с прозрачностью в формат без прозрачности)
func (i *imageEncode) Color(r, g, b uint8) (this *imageEncode) {
	i.color = [3]uint8{r, g, b}
	i._color = true
	return i
}

// повтор анимации
func (i *imageEncode) Loop(loop bool) (this *imageEncode) {
	i.loop = loop
	return i
}

// подключение настроек обработки картинок на сервере
//
// настройки находятся в файле "setting.yaml" секция `thumb`
func (i *imageEncode) Thumb(thumb string) (this *imageEncode) {
	i.thumb = thumb
	return i
}

// обрезать поля картинки по прозрачным пикселям, или по указанным цветам
//
// active - активность фильтра
//
// rate - погрешность при сравнении цветов
//
// colors - список цветов
func (i *imageEncode) Trim(active bool, rate uint8, colors [][3]uint8) (this *imageEncode) {
	i.trimActive = active
	i.trimRate = rate
	i.trimColor = colors
	return i
}

// получаем ссылку без изменения формата картинки
func (i *imageEncode) Get(file string) string {
	return i.GetConvert(file, "")
}

// получаем ссылку с конвертацией в новый формат
func (i *imageEncode) GetConvert(file, format string) string {
	lastIndex := strings.LastIndex(file, ".")
	i.format = file[lastIndex+1:]
	if !slices.Contains(FormatsList(), strings.ToLower(i.format)) {
		return file
	}

	if format == "" {
		format = strings.ToLower(i.format)
	}

	nf, is := getFormat(format)
	if is {
		i.formatTo = nf
		if i.format == format {
			// если запрашиваемый формат совпадает с текущим то не пишем в данные
			i.format = ""
		}
	} else {
		return file
	}

	if i.format != "" && i.format == strings.ToLower(i.format) {
		//если формат файла в нижнем регистре, пишем в данные только 1 байт
		nf, is := getFormat(i.format)
		if is {
			i.formatFrom = nf
			i.format = ""
		}
	}

	code := encode(i)
	if code == "" {
		return file
	}

	return file[0:lastIndex] + "/" + code + "." + format
}
