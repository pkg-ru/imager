package controller

import (
	"net/http"
	"strconv"

	"github.com/pkg-ru/imager/internal/domain/logx"
)

func (c *Controler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Methods", "GET,HEAD,OPTIONS")
	if c.Setting.AccessControll.AllowOrigin != "" {
		w.Header().Set("Access-Control-Allow-Origin", c.Setting.AccessControll.AllowOrigin)
	}
	if c.Setting.AccessControll.AllowHeaders != "" {
		w.Header().Set("Access-Control-Allow-Headers", c.Setting.AccessControll.AllowHeaders)
	}
	if c.Setting.AccessControll.MaxAge > 0 {
		w.Header().Set("Access-Control-Max-Age", strconv.Itoa(c.Setting.AccessControll.MaxAge))
	}
	if r.Method == http.MethodOptions {
		return
	}

	if c.process(w, r) {
		return
	}

	c.LogReq(func(log logx.FieldLogger) {
		log.Errorf("GET %s Not Found", r.RequestURI)
	})

	if c.Setting.NotFound.Image != "" {
		c.File(c.Setting.NotFound.Image, w, r)
		return
	}
	if c.Setting.NotFound.Page != "" {
		c.File(c.Setting.NotFound.Page, w, r)
		return
	}
	if c.Setting.NotFound.Redirect != "" {
		http.Redirect(w, r, c.Setting.NotFound.Redirect, http.StatusMovedPermanently)
		return
	}

	http.NotFound(w, r)
}
