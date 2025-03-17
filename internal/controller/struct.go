package controller

import (
	"net/http"

	"github.com/pkg-ru/imager/internal/domain/logx"
	"github.com/pkg-ru/imager/internal/domain/setting"
)

type Controler struct {
	LogPanic   logx.FieldLogger
	LogServer  logx.FieldLogger
	LogRequest logx.FieldLogger
	Setting    *setting.Setting
}

func (c *Controler) Panicf(format string, args ...any) {
	if c.LogServer != nil {
		c.LogServer.Panicf(format, args...)
	} else {
		c.LogPanic.Panicf(format, args...)
	}
}

func (c *Controler) LogSer(calback func(log logx.FieldLogger)) {
	if c.LogServer != nil {
		calback(c.LogServer)
	}
}

func (c *Controler) LogReq(calback func(log logx.FieldLogger)) {
	if c.LogRequest != nil {
		calback(c.LogRequest)
	}
}

func (c *Controler) Sett() *setting.Setting {
	return c.Setting
}

func (c *Controler) File(file string, w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, file)
}
