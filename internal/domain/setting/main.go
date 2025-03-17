package setting

import (
	"github.com/pkg-ru/imager/internal/domain/logx"
	"github.com/pkg-ru/pkg/config"
	"github.com/pkg-ru/pkg/files"
)

func Get(file string, log logx.FieldLogger) *Setting {
	setting := &Setting{}
	err := config.Get(file, setting)
	if err != nil {
		log.Panicf("Error load configuration file: %s\n%#w", file, err)
	}

	if setting.Paths.Source == "" {
		setting.Paths.Source = files.GetPath("")
	} else {
		setting.Paths.Source = files.GetPath(setting.Paths.Source)
	}
	if setting.Paths.Result == "" {
		setting.Paths.Result = setting.Paths.Source
	} else {
		setting.Paths.Result = files.GetPath(setting.Paths.Result)
	}

	return setting
}
