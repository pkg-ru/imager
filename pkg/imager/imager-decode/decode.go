package imagerdecode

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	imagerencode "github.com/pkg-ru/imager/pkg/imager/imager-encode"
)

// Декодируем ссылку, если не удалось декодировать - вернет nil
func Decode[T stringer](encodeFile T, setting map[string]ThumbSetting) (*decodeImage, error) {
	file := getFile(encodeFile)
	if file == "" {
		return nil, fmt.Errorf("не удалось получить путь до файла: %v", encodeFile)
	}

	base := filepath.Base(file)
	if len(base) >= 255 {
		return nil, fmt.Errorf("слишком много данных передано в запросе, такое большое название не поддерживается в файловой системой")
	}

	res := &decodeImage{}
	res.File = filepath.Dir(file)
	if res.File == "/" || res.File == "." {
		res.File = ""
	}

	lastIndex := strings.LastIndex(base, ".")
	format := base[lastIndex+1:]
	if !slices.Contains(imagerencode.FormatsList(), format) {
		return nil, fmt.Errorf(`формат файла "%s" не поддерживатся, или возможно в написании формата используется верхний регистр букв`, base)
	}

	res.Format = format

	// декодируем данные
	data, err := base64.RawURLEncoding.DecodeString(base[0:lastIndex])
	if err != nil {
		return nil, fmt.Errorf(`ошибка декодирования base64: %w`, err)
	}
	lenDat := len(data)
	if lenDat < 2 {
		return nil, fmt.Errorf(`не верные данные, отсутствует заголовок`)
	}

	offset := 2
	flagColumns := binary.BigEndian.Uint16(data[0:offset])

	errParseData := fmt.Errorf(`не верные данные, запрашивается больше - чем есть`)

	for _, f := range imagerencode.FlagsSorted {
		if offset > lenDat {
			return nil, errParseData
		}
		flag := imagerencode.Flags[f]
		if (flag & flagColumns) > 0 {
			// [2]uint8
			if f == "color" {
				if lenDat < offset+3 {
					return nil, errParseData
				}
				res.IsColor = true
				res.Color = [3]uint8{data[offset], data[offset+1], data[offset+2]}
				offset += 3
			} else
			// [][3]uint8
			if f == "trimColor" {
				if lenDat < offset+1 {
					return nil, errParseData
				}
				length := int(data[offset])
				offset += 1
				if lenDat < offset+length {
					return nil, errParseData
				}
				valData := data[offset : offset+length]
				offset += length
				lenData := len(valData)
				trimColor := [][3]uint8{}
				for i := 0; i < lenData; i += 3 {
					trimColor = append(trimColor, [3]uint8{valData[i], valData[i+1], valData[i+2]})
				}
				res.Trim.Color = trimColor
			} else
			// bool
			if slices.Contains([]string{"loop", "trimActive", "crop"}, f) {
				if lenDat < offset+1 {
					return nil, errParseData
				}
				switch f {
				case "loop":
					res.Loop = true
				case "crop":
					res.Crop = true
				case "trimActive":
					res.Trim.Active = true
				}
			} else
			// uint8
			if slices.Contains([]string{"formatTo", "formatFrom", "quality", "trimRate"}, f) {
				if lenDat < offset+1 {
					return nil, errParseData
				}
				val := uint8(data[offset])
				offset++
				switch f {
				case "formatTo":
					res.Format = imagerencode.FormatName(val)
				case "formatFrom":
					if res.FormatFrom != "" {
						return nil, fmt.Errorf(`не верные данные, передан 2 раза исходный формат`)
					}
					res.FormatFrom = imagerencode.FormatName(val)
				case "quality":
					res.Quality = val
				case "trimRate":
					res.Trim.Rate = val
				}
			}
			// uint16
			if slices.Contains([]string{"width", "height"}, f) {
				if lenDat < offset+2 {
					return nil, errParseData
				}
				val := binary.BigEndian.Uint16(data[offset : offset+2])
				offset += 2
				switch f {
				case "width":
					res.Width = val
				case "height":
					res.Height = val
				}
			}
			// string
			if slices.Contains([]string{"format", "thumb"}, f) {
				if lenDat < offset+1 {
					return nil, errParseData
				}
				length := int(data[offset])
				offset += 1
				if lenDat < offset+length {
					return nil, errParseData
				}
				val := string(data[offset : offset+length])
				offset += length
				switch f {
				case "format":
					if res.FormatFrom != "" {
						return nil, fmt.Errorf(`не верные данные, передан 2 раза исходный формат`)
					}
					res.FormatFrom = val
				case "thumb":
					res.Thumb = val
				}
			}
		}
	}

	if res.FormatFrom == "" {
		//если в данных формат не передан - значит он не меняется.
		res.FormatFrom = format
	}

	if format != res.Format {
		return nil, fmt.Errorf(`В данных указан формат "%s", запрашивается "%s"`, res.Format, format)
	}

	err = res.init(setting)
	if err != nil {
		return nil, err
	}

	res.File = res.File + "." + res.FormatFrom

	return res, nil
}

func getFile[T stringer](encodeFile T) string {
	switch any(encodeFile).(type) {
	case string:
		return any(encodeFile).(string)
	case []byte:
		return string(any(encodeFile).([]byte))
	}
	stringer, is := any(encodeFile).(fmt.Stringer)
	if is {
		return stringer.String()
	}
	return ""
}
