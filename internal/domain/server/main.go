package server

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/pkg-ru/imager/internal/controller"
	"github.com/pkg-ru/imager/internal/domain/logx"
	"golang.org/x/sync/errgroup"
)

func New(app *controller.Controler) *Server {
	server := &Server{
		App: app,
		Server: http.Server{
			Handler:           app,
			ReadTimeout:       10 * time.Second,
			ReadHeaderTimeout: 10 * time.Second,
			WriteTimeout:      5 * time.Second,
			IdleTimeout:       15 * time.Second,
		},
	}

	app.LogReq(func(l logx.FieldLogger) {
		server.ErrorLog = log.New(l.Writer(), "", 0)
	})

	return server
}

func (s *Server) StartUnix(ctx context.Context) {
	s.runner(ctx,
		"Unix socket %s stop", "Error stopped Unix socket %s\n%#w", s.App.Setting.Unix,
		func() error {
			var err error
			s.App.LogSer(func(log logx.FieldLogger) {
				log.Debugf("Unix socket %s start", s.App.Setting.Unix)
			})
			if err := s.Serve(s.getSocket()); !errors.Is(err, http.ErrServerClosed) {
				s.App.Panicf("Unix socket: %s server error: %#w", s.App.Setting.Unix, err)
			}
			return err
		})
}

func (s *Server) StartHttp(ctx context.Context) {
	s.Server.Addr = s.App.Setting.Http
	s.runner(ctx,
		"HTTP %s stop", "Error stopped HTTP %s\n%#w", s.App.Setting.Http,
		func() error {
			var err error
			s.App.LogSer(func(log logx.FieldLogger) {
				log.Debugf("HTTP %s start", s.App.Setting.Http)
			})
			if err = s.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				s.App.Panicf("HTTP: %s server error: %#w", s.Addr, err)
			}
			return err
		})
}

func (s *Server) StartHttps(ctx context.Context) {
	s.Server.Addr = s.App.Setting.Https
	s.runner(ctx,
		"HTTPS %s stop", "Error stopped HTTPS %s\n%#w", s.App.Setting.Https,
		func() error {
			var err error
			s.App.LogSer(func(log logx.FieldLogger) {
				log.Debugf("HTTPS %s start", s.App.Setting.Https)
			})
			if err := s.ListenAndServeTLS(s.App.Setting.Tls.CertFile, s.App.Setting.Tls.KeyFile); !errors.Is(err, http.ErrServerClosed) {
				s.App.Panicf("HTTPS: %s server error: %v", s.Addr, err)
			}
			return err
		})
}

func (s *Server) runner(ctx context.Context, msgSucces, msgError, val string, serv func() error) {
	g, gCtx := errgroup.WithContext(ctx)
	g.Go(serv)
	g.Go(func() error {
		<-gCtx.Done()
		err := s.Shutdown(context.Background())
		if err == nil {
			s.App.LogSer(func(log logx.FieldLogger) {
				log.Debugf(msgSucces, val)
			})
		} else {
			s.App.LogSer(func(log logx.FieldLogger) {
				log.Errorf(msgError, val, err)
			})
		}
		// os.Remove(s.App.Setting.Unix)
		return err
	})

	g.Wait()
}

func (s *Server) getSocket() net.Listener {
	// os.Remove(s.App.Setting.Unix)

	unixSocket, err := net.Listen("unix", s.App.Setting.Unix)
	if err != nil {
		s.App.Panicf("Unix socket create: %s error: %#w", s.App.Setting.Unix, err)
	}

	os.Chmod(s.App.Setting.Unix, 0777)

	return unixSocket
}
