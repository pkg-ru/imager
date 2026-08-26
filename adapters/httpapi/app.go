package httpapi

import (
	"context"
	"fmt"
	"net/http"

	"github.com/pkg-ru/imager/adapters/storage/fs"
	"github.com/pkg-ru/imager/adapters/storage/remote"
	"github.com/pkg-ru/imager/app/adminsvc"
	"github.com/pkg-ru/imager/app/generatev2"
	"github.com/pkg-ru/imager/config"
	"github.com/pkg-ru/imager/coordination/singleflight"
	"github.com/pkg-ru/imager/ports/detector"
	"github.com/pkg-ru/imager/ports/metadata"
	"github.com/pkg-ru/imager/ports/processor"
	"github.com/pkg-ru/imager/ports/storage"
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

	// MetadataEnabled — включить sidecar-кэш моделей и largest_ai_asset
	MetadataEnabled bool
	// MetadataDir — КОРЕНЬ sidecar-хранилища метаданных (metadata.dir):
	// явный ЛОКАЛЬНЫЙ путь файловой системы, НЕЗАВИСИМЫЙ от хранилищ
	// source/result. Пусто = дефолт `<эффективный локальный
	// result-каталог>`. Применяется, только если MetadataEnabled и Detector задан.
	MetadataDir string
	// Detector — порт ИИ-детекции на уровне приложения (nil = детекция остаётся в процессоре).
	Detector detector.Detector
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

	// AdminSvc — admin-сервис (nil, если admin выключен). Требует Start()
	// перед использованием и Stop()/Close() при shutdown.
	AdminSvc *adminsvc.Service
	// AdminHandler — HTTP-обработчик /admin/* (nil, если admin выключен).
	AdminHandler http.Handler
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

	// Sidecar-кэш метаданных
	//
	// metaRoot задаётся metadata.dir (ЯВНЫЙ локальный путь, независимый от
	// хранилищ source/result); если не задан — дефолт
	// `<эффективный локальный result-каталог>` (без подкаталога .meta).
	// Эффективный локальный result-каталог = result.path, иначе ResultDir.
	// Отключён, если metadata выключено, детектор не задан или локальный
	// result-каталог не определён (best-effort: ошибки не ломают генерацию).
	var metaStore metadata.Store
	if opt.MetadataEnabled && opt.Detector != nil {
		metaRoot := opt.MetadataDir
		if metaRoot == "" {
			localResultDir := opt.ResultStorage.Path
			if localResultDir == "" {
				localResultDir = opt.ResultDir
			}
			if localResultDir != "" {
				metaRoot = localResultDir
			}
		}
		if metaRoot != "" {
			ms, err := fs.NewMetadataStore(metaRoot)
			if err != nil {
				return nil, fmt.Errorf("httpapi: build: metadata store: %w", err)
			}
			metaStore = ms
		}
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
		Quality:            int(compiled.DefaultQuality),
		DefaultWatermark:   compiled.DefaultWatermark,
		DefaultOrientation: compiled.DefaultOrientation,
		DefaultTrim:        compiled.DefaultTrim,
		Logger:             opt.HTTP.Logger,
		Metrics:            opt.HTTP.Metrics,
		Metadata:           metaStore,
		Detector:           opt.Detector,
	})
	if err != nil {
		return nil, fmt.Errorf("httpapi: build: generatev2: %w", err)
	}

	// HTTP handler. Пробрасываем хранилище исходников в конфиг для source
	// fallback (nil = фича недоступна).
	opt.HTTP.Sources = sources
	h, err := New(svc, opt.HTTP)
	if err != nil {
		return nil, fmt.Errorf("httpapi: build: handler: %w", err)
	}

	// Admin-сервис и handler (только если admin включён). Валидация
	// admin.enabled + token выполняется в Config.Validate (fail-fast).
	var adminSvc *adminsvc.Service
	var adminHandler http.Handler
	if opt.HTTP.Admin.Enabled {
		adminSvc, err = adminsvc.New(adminsvc.Deps{
			Gen:      svc,
			Sources:  sources,
			Results:  results,
			Presets:  compiled.Presets,
			Policy:   compiled.Policy,
			Metadata: metaStore,
			Logger:   opt.HTTP.Logger,
		}, adminsvc.Config{
			Workers:     opt.HTTP.Admin.Workers,
			QueueSize:   opt.HTTP.Admin.QueueSize,
			WaitTimeout: opt.HTTP.Admin.WaitTimeout,
		})
		if err != nil {
			return nil, fmt.Errorf("httpapi: build: adminsvc: %w", err)
		}
		adminHandler = NewAdminHandler(adminSvc, opt.HTTP.Admin, opt.HTTP.Logger)
	}

	return &App{
		Handler:      h,
		Service:      svc,
		Sources:      sources,
		Results:      results,
		Pool:         pool,
		AdminSvc:     adminSvc,
		AdminHandler: adminHandler,
	}, nil
}
