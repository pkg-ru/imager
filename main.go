package main

import (
	"context"

	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/pkg-ru/imager/internal/controller"
	"github.com/pkg-ru/imager/internal/domain/logx"
	"github.com/pkg-ru/imager/internal/domain/server"
	"github.com/pkg-ru/imager/internal/domain/setting"
	"github.com/pkg-ru/imager/pkg/files"
)

func main() {
	app := &controller.Controler{
		LogPanic:   logx.Init(files.GetPath("panic.log"), true),
		LogServer:  nil,
		LogRequest: nil,
	}

	app.Setting = setting.Get(files.GetPath("setting"), app.LogPanic)

	if app.Setting.LogServer != "" {
		app.LogServer = logx.Init(files.GetPath(app.Setting.LogServer), app.Setting.Development)
	}
	if app.Setting.LogRequest != "" {
		app.LogRequest = logx.Init(files.GetPath(app.Setting.LogRequest), app.Setting.Development)
	}

	var wg sync.WaitGroup

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		exit := make(chan os.Signal, 1)
		signal.Notify(exit, os.Interrupt, syscall.SIGINT, syscall.SIGTERM, syscall.SIGTERM, syscall.SIGKILL)
		defer signal.Stop(exit)

		select {
		case <-ctx.Done():
		case <-exit:
			cancel()
		}
	}()

	if app.Setting.Http != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			server.New(app).StartHttp(ctx)
		}()
	}

	if app.Setting.Https != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			server.New(app).StartHttps(ctx)
		}()
	}

	if app.Setting.Unix != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			server.New(app).StartUnix(ctx)
		}()
	}

	wg.Wait()
}
