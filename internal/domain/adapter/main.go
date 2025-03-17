package adapter

import (
	"net/http"
	"os"
	"os/exec"
	"path"
	"slices"
	"strconv"

	"github.com/pkg-ru/imager/internal/domain/logx"
	"github.com/pkg-ru/imager/internal/domain/setting"
	imagerdecode "github.com/pkg-ru/imager/pkg/imager/imager-decode"
	"github.com/pkg-ru/pkg/files"
)

type Controller interface {
	Sett() *setting.Setting
	LogSer(func(logx.FieldLogger))
	LogReq(func(logx.FieldLogger))
	File(string, http.ResponseWriter, *http.Request)
}

func Get(file string, c Controller) bool {
	set, err := imagerdecode.Decode(file, c.Sett().Thumbs)
	if err == nil {
		filePathSource := c.Sett().Paths.Source + "/" + set.File
		filePathResult := c.Sett().Paths.Result + "/" + file
		if files.Exists(filePathSource) {
			set_r, err_r := imagerdecode.Decode(set.File, c.Sett().Thumbs)
			if err_r != nil || set_r == nil {
				sourceFile := filePathSource
				isOutAnimate := slices.Contains([]string{"gif", "webp", "apng", "heif"}, set.Format)
				if !isOutAnimate {
					sourceFile += "[0]"
				}

				os.MkdirAll(path.Dir(filePathResult), 0777)
				comand := []string{
					"-quiet",
					sourceFile,
					"-strip",
					"-filter", "Triangle",
					"-define", "filter:support=2",
					"-unsharp", "0.25x0.08+8.3+0.045",
					"-sampling-factor", "4:2:0",
					"-dither", "None",
					"-posterize", "136",
					"-define", "filter-strength=40",
					"-define", "webp:thread-level=1",
					"-define", "webp:alpha-compression=1",
					"-define", "webp:alpha-filtering=2",
					"-define", "webp:auto-filter=true",
					"-define", "webp:method=6",
					"-define", "jpeg:fancy-upsampling=off",
					"-define", "png:compression-filter=5",
					"-define", "png:compression-level=9",
					"-define", "png:compression-strategy=1",
					"-define", "png:exclude-chunk=all",
					"-interlace", "none",
					"-gravity", "center",
					"-colorspace", "sRGB",
					"-layers", "OptimizePlus",
					"-coalesce",
				}

				//зацикливание анимации
				if set.Loop {
					comand = append(comand, "-loop", "0")
				} else {
					comand = append(comand, "-loop", "1")
				}

				//цвет фона
				if set.IsColor {
					comand = append(comand, "-background", "rgb("+strconv.Itoa(int(set.Color[0]))+","+strconv.Itoa(int(set.Color[1]))+","+strconv.Itoa(int(set.Color[2]))+")")
				}

				//trim
				if set.Trim.Active {
					if len(set.Trim.Color) > 0 {
						for _, color := range set.Trim.Color {
							comand = append(comand, "-bordercolor", "rgb("+strconv.Itoa(int(color[0]))+","+strconv.Itoa(int(color[1]))+","+strconv.Itoa(int(color[2]))+")")
							comand = append(comand, "-border", "1")
							if set.Trim.Rate > 0 {
								comand = append(comand, "-fuzz", strconv.Itoa(int(set.Trim.Rate))+"%")
							}
							comand = append(comand, "-trim")
						}
					} else {
						if set.Trim.Rate > 0 {
							comand = append(comand, "-fuzz", strconv.Itoa(int(set.Trim.Rate))+"%")
						}
						comand = append(comand, "-trim")
					}
					comand = append(comand, "-layers", "trim-bounds")
				}

				//размер
				if set.Width > 0 || set.Height > 0 {
					resize := ""
					if set.Width > 0 {
						resize = strconv.Itoa(int(set.Width))
					}
					resize += "x"
					if set.Height > 0 {
						resize += strconv.Itoa(int(set.Height))
					}
					if set.Crop {
						resize += "^"
					}
					comand = append(comand, "-thumbnail", resize)
					comand = append(comand, "-extent", resize)
				}

				//компрессия
				if set.Quality > 0 {
					comand = append(comand, "-quality", strconv.Itoa(int(set.Quality)))
				}

				comand = append(comand, filePathResult)

				cmd := exec.Command("magick", comand...)
				_, err := cmd.CombinedOutput()
				if err != nil {
					c.LogSer(func(fl logx.FieldLogger) {
						fl.Errorln(`ошибка выполнения команды`, comand, err)
					})
					return false
				}
				os.Chmod(filePathResult, 0666)

				return true
			} else {
				c.LogSer(func(fl logx.FieldLogger) {
					fl.Infof(`Рекурсия файла "%s"`, set.File)
				})
			}
		} else {
			c.LogSer(func(fl logx.FieldLogger) {
				fl.Infof(`Исходный файл "%s" не существует`, set.File)
			})
		}
	} else {
		c.LogSer(func(fl logx.FieldLogger) {
			fl.Info("Ошибка декодирования параметров картинки: ", err)
		})
	}
	return false
}
