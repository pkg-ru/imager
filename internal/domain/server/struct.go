package server

import (
	"net/http"

	"github.com/pkg-ru/imager/internal/controller"
)

type Server struct {
	http.Server
	App *controller.Controler
}
