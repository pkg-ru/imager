package imagerencode

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"slices"
)

func encode(img *imageEncode) string {
	var myFlag uint16 = 0

	if img.thumb == "default" {
		img.thumb = ""
	}

	f := []byte{0, 0}
	newData := [][]byte{f}
	for _, name := range FlagsSorted {
		flag := Flags[name]
		// [3]uint8
		if name == "color" && img._color {
			myFlag = myFlag | flag
			newData = append(newData, img.color[0:])
		} else
		// [][3]uint8
		if name == "trimColor" {
			l := len(img.trimColor)
			if l > 0 {
				val := [][]byte{{0}}
				for i := range l {
					val = append(val, img.trimColor[i][0:])
				}
				val[0][0] = uint8(l * 3)
				myFlag = myFlag | flag
				newData = append(newData, bytes.Join(val, []byte{}))
			}
		} else
		// bool
		if slices.Contains([]string{"loop", "trimActive", "crop"}, name) {
			switch name {
			case "loop":
				if img.loop {
					myFlag = myFlag | flag
				}
			case "crop":
				if img.crop {
					myFlag = myFlag | flag
				}
			case "trimActive":
				if img.trimActive {
					myFlag = myFlag | flag
				}
			}
		} else
		// uint8
		if slices.Contains([]string{"formatTo", "formatFrom", "quality", "trimRate"}, name) {
			val := uint8(0)
			switch name {
			case "formatTo":
				val = uint8(img.formatTo)
			case "formatFrom":
				val = uint8(img.formatFrom)
			case "quality":
				val = img.quality
			case "trimRate":
				val = img.trimRate
			}
			if val > 0 {
				myFlag = myFlag | flag
				newData = append(newData, []byte{val})
			}
		}
		// uint16
		if slices.Contains([]string{"width", "height"}, name) {
			val := uint16(0)
			switch name {
			case "width":
				val = img.width
			case "height":
				val = img.height
			}
			if val > 0 {
				myFlag = myFlag | flag
				dat := []byte{0, 0}
				binary.BigEndian.PutUint16(dat, val)
				newData = append(newData, dat)
			}
		}
		// string
		if slices.Contains([]string{"format", "thumb"}, name) {
			val := []byte{}
			switch name {
			case "format":
				val = []byte(img.format)
			case "thumb":
				val = []byte(img.thumb)
			}
			if len(val) > 0 && len(val) < 255 {
				myFlag = myFlag | flag
				newData = append(newData, bytes.Join([][]byte{{uint8(len(val))}, val}, []byte{}))
			}
		}
	}

	if myFlag == 0 {
		return ""
	}

	binary.BigEndian.PutUint16(f, myFlag)
	return base64.RawURLEncoding.EncodeToString(bytes.Join(newData, []byte{}))
}
