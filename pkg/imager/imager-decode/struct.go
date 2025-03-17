package imagerdecode

import (
	"fmt"
	"slices"
)

type (
	decodeImageTrim struct {
		Color  [][3]uint8 // цвета
		Active bool       // активность
		Rate   uint8      // погрешность при сравнении цветов
	}
	decodeImage struct {
		Trim       decodeImageTrim // обрезать поля картинки по прозрачным пикселям, или по указанным цветам
		File       string          // исходный файл
		Format     string          // формат
		Thumb      string          // настройки
		FormatFrom string          // исходный формат файла
		Width      uint16          // ширина
		Height     uint16          // высота
		Color      [3]uint8        // цвет заливки
		Quality    uint8           // качество
		IsColor    bool            // установлен ли цвет заливки
		Loop       bool            // зацикливание анимации
		Crop       bool            // кроп
	}
)

type stringer interface {
	string | []byte | any
}

type ThumbSetting struct {
	Privacy []string `yaml:"privacy"` // разрешенные для установки/замены значения

	Color      []uint8    `yaml:"color"`       // цвет заливки RGB
	Width      uint16     `yaml:"width"`       // ширина
	Height     uint16     `yaml:"height"`      // высота
	Quality    uint8      `yaml:"quality"`     // качество
	Loop       bool       `yaml:"loop"`        // зацикливание анимации
	Crop       bool       `yaml:"crop"`        // кроп
	TrimActive bool       `yaml:"trim-active"` // активность
	TrimColor  [][3]uint8 `yaml:"trim-color"`  // цвета
	TrimRate   uint8      `yaml:"trim-rate"`   // погрешность при сравнении цветов
}

func (di *decodeImage) init(setting map[string]ThumbSetting) error {
	if di.Thumb == "default" {
		return fmt.Errorf(`в настройках установлен "%s" его не нужно передавать - используется по умолчанию`, di.Thumb)
	}
	if di.Thumb == "" {
		di.Thumb = "default"
	}

	set, is := setting[di.Thumb]
	if !is {
		return fmt.Errorf(`в файле "setting.yaml" нет настроек для thumb "%s"`, di.Thumb)
	}

	err := `в настройках для thumb "%s" нет разрешения на установку параметра "%s"`
	if di.Width == 0 {
		di.Width = set.Width
	} else if !slices.Contains(set.Privacy, "width") {
		return fmt.Errorf(err, di.Thumb, "width")
	}

	if di.Height == 0 {
		di.Height = set.Height
	} else if !slices.Contains(set.Privacy, "height") {
		return fmt.Errorf(err, di.Thumb, "height")
	}

	if !di.IsColor {
		if len(set.Color) == 3 {
			di.Color = [3]uint8{set.Color[0], set.Color[1], set.Color[2]}
			di.IsColor = true
		}
	} else if !slices.Contains(set.Privacy, "height") {
		return fmt.Errorf(err, di.Thumb, "height")
	}

	if di.Quality == 0 {
		di.Quality = set.Quality
	} else if !slices.Contains(set.Privacy, "quality") {
		return fmt.Errorf(err, di.Thumb, "quality")
	}

	if di.Loop { // default true
		di.Loop = set.Loop
	} else if !slices.Contains(set.Privacy, "loop") {
		return fmt.Errorf(err, di.Thumb, "loop")
	}

	if !di.Crop { // default false
		di.Crop = set.Crop
	} else if !slices.Contains(set.Privacy, "crop") {
		return fmt.Errorf(err, di.Thumb, "crop")
	}

	if !di.Trim.Active { // default false
		di.Trim.Active = set.TrimActive
	} else if !slices.Contains(set.Privacy, "trim-active") {
		return fmt.Errorf(err, di.Thumb, "trim-active")
	}

	if di.Trim.Rate == 0 {
		di.Trim.Rate = set.TrimRate
	} else if !slices.Contains(set.Privacy, "trim-rate") {
		return fmt.Errorf(err, di.Thumb, "trim-rate")
	}

	if len(di.Trim.Color) == 0 {
		di.Trim.Color = set.TrimColor
	} else if !slices.Contains(set.Privacy, "trim-color") {
		return fmt.Errorf(err, di.Thumb, "trim-color")
	}

	return nil
}
