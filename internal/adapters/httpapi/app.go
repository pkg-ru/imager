package httpapi

import (
	"context"
	"fmt"

	"github.com/pkg-ru/imager/internal/adapters/coordination/singleflight"
	"github.com/pkg-ru/imager/internal/adapters/storage/remote"
	"github.com/pkg-ru/imager/internal/application/generatev2"
	"github.com/pkg-ru/imager/internal/application/ports/processor"
	"github.com/pkg-ru/imager/internal/application/ports/storage"
	"github.com/pkg-ru/imager/internal/config"
)

// AppOptions — параметры сборки нового pipeline (composition root).
type AppOptions struct {
	// Config — typed конфигурация конвейера (policy/processing).
	Config *config.Config
	// HTTP — конфигурация HTTP-адаптера.
	HTTP Config

	// SourceDir — каталог исходников (используется при FS fallback).
	SourceDir string
	// ResultDir — каталог кэша результатов (используется при FS fallback).
	ResultDir string
	// SourceStorage — конфигурация удалённого source-хранилища (S3/SFTP/
	// FTP/FTPS). Пустой Kind = FS fallback на SourceDir.
	SourceStorage RemoteStorageConfig
	// ResultStorage — конфигурация удалённого result-хранилища (S3/SFTP/
	// FTPS). Пустой Kind = FS fallback на ResultDir.
	ResultStorage RemoteStorageConfig

	// Processor — абстрактный процессор. Если nil, используется
	// ImageMagick-адаптер (требует установленного binary).
	Processor processor.Processor
	// Sources — кастомный SourceStore (для тестов). Если задан, имеет
	// приоритет над SourceStorage/SourceDir.
	Sources storage.SourceStore
	// Results — кастомный ResultStore (для тестов). Если задан, имеет
	// приоритет над ResultStorage/ResultDir.
	Results storage.ResultStore

	// OutputLimit — максимальный размер выходного файла (0 = без лимита).
	OutputLimit int64
	// BufferMaxBytes — общий бюджет памяти процесса для spillable-буферов
	// (0 = без лимита). По умолчанию 500 МБ.
	BufferMaxBytes int64
}

// App — собранный pipeline.
type App struct {
	Handler *Handler
	Service *generatev2.Service
	Sources storage.SourceStore
	Results storage.ResultStore
	// Pool — общий бюджет памяти процесса для spillable-буферов. Может быть
	// закрыт при пересоздании приложения (доп. замечание).
	Pool *remote.BufferPool
}

// Build собирает новый pipeline. Fail-fast на invalid config.
func Build(ctx context.Context, opt AppOptions) (*App, error) {
	if opt.Config == nil {
		return nil, fmt.Errorf("httpapi: build: nil config")
	}
	compiled, err := opt.Config.Compile()
	if err != nil {
		return nil, fmt.Errorf("httpapi: build: compile config: %w", err)
	}

	// Общий бюджет памяти процесса для spillable-буферов (source+result).
	// Один пул на весь процесс; фабрика буферов передаётся и в use case, и в
	// remote-адаптеры (через RemoteStorageConfig.Pool).
	pool := remote.NewBufferPool(opt.BufferMaxBytes)
	buffers := remote.NewBufferFactory(pool, "")
	opt.SourceStorage.Pool = pool
	opt.ResultStorage.Pool = pool

	// Хранилища: кастомные (тесты) → remote-конфигурация → FS fallback.
	sources := opt.Sources
	results := opt.Results
	if sources == nil || results == nil {
		s, r, err := ensureFSStores(ctx, opt.SourceDir, opt.ResultDir, opt.SourceStorage, opt.ResultStorage)
		if err != nil {
			return nil, fmt.Errorf("httpapi: build: %w", err)
		}
		if sources == nil {
			sources = s
		}
		if results == nil {
			results = r
		}
	}

	// Процессор.
	proc := opt.Processor
	if proc == nil {
		return nil, fmt.Errorf("httpapi: build: processor is required (ImageMagick adapter or fake)")
	}

	// Координатор.
	coord := singleflight.New(singleflight.Options{})

	// Use case.
	svc, err := generatev2.New(generatev2.Deps{
		Sources:            sources,
		Results:            results,
		Coordinator:        coord,
		Processor:          proc,
		Policy:             compiled.Policy,
		Presets:            compiled.Presets,
		Buffers:            buffers,
		OutputLimit:        opt.OutputLimit,
		Quality:            compiled.DefaultQuality,
		DefaultWatermark:   compiled.DefaultWatermark,
		DefaultOrientation: compiled.DefaultOrientation,
		Logger:             opt.HTTP.Logger,
		Metrics:            opt.HTTP.Metrics,
	})
	if err != nil {
		return nil, fmt.Errorf("httpapi: build: generatev2: %w", err)
	}

	// HTTP handler.
	h, err := New(svc, opt.HTTP)
	if err != nil {
		return nil, fmt.Errorf("httpapi: build: handler: %w", err)
	}

	return &App{
		Handler: h,
		Service: svc,
		Sources: sources,
		Results: results,
		Pool:    pool,
	}, nil
}
