package imagerencode

import "sort"

// uint8 uint16 string bool [3]uint8 [][3]uint8 [2]uint8
// если тип у новых полей будет отличаться - нужно добавить сценарий в упаковщик
type imageEncode struct {
	trimColor  [][3]uint8
	format     string
	thumb      string
	color      [3]uint8
	width      uint16
	height     uint16
	formatTo   format
	formatFrom format
	quality    uint8
	trimRate   uint8
	loop       bool
	trimActive bool
	crop       bool
	_color     bool
}

// Карта параметров с флагом (флаг суммирется при заполнении параметра)
var Flags map[string]uint16 = map[string]uint16{
	"width":      1 << 0,
	"height":     1 << 1,
	"quality":    1 << 2,
	"format":     1 << 3,
	"color":      1 << 4,
	"loop":       1 << 5,
	"thumb":      1 << 6,
	"trimActive": 1 << 7,
	"trimColor":  1 << 8,
	"trimRate":   1 << 9,
	"formatTo":   1 << 10,
	"formatFrom": 1 << 11,
	"crop":       1 << 12,
}

// Сортированный список параметров (устанавливает порядок упаковки параметров)
var FlagsSorted []string

func init() {
	FlagsSorted = []string{}
	for k := range Flags {
		FlagsSorted = append(FlagsSorted, k)
	}
	sort.Strings(FlagsSorted)
}
