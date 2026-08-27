package imager

import (
	"context"
	"net/http"

	"github.com/pkg-ru/imager/adapters/httpapi"
	"github.com/pkg-ru/imager/adapters/storage/remote"
	"github.com/pkg-ru/imager/app/adminsvc"
	"github.com/pkg-ru/imager/app/generatev2"
	"github.com/pkg-ru/imager/composition"
	"github.com/pkg-ru/imager/config"
	"github.com/pkg-ru/imager/ports/detector"
	"github.com/pkg-ru/imager/ports/processor"
	"github.com/pkg-ru/imager/ports/storage"
	"github.com/pkg-ru/imager/ports/videoframe"
)

// Options — параметры программной сборки pipeline без YAML (New).
//
// Пользователь сам подставляет порты: processor, storage, coordinator,
// detector, buffer, metadata. Все поля опциональны, но для рабочего pipeline
// обязательны Processor и (Sources|Results) — иначе Build вернёт ошибку.
type Options struct {
	// Config — typed конфигурация конвейера (policy/processing). Обязателен.
	Config *config.Config
	// HTTP — конфигурация HTTP-адаптера.
	HTTP httpapi.Config

	// SourceDir — каталог исходников (используется при FS fallback).
	SourceDir string
	// ResultDir — каталог кэша результатов (используется при FS fallback).
	ResultDir string
	// SourceStorage — конфигурация удалённого source-хранилища (S3/SFTP/
	// FTP/FTPS). Пустой Kind = FS fallback на SourceDir.
	SourceStorage composition.RemoteStorageConfig
	// ResultStorage — конфигурация удалённого result-хранилища (S3/SFTP/
	// FTPS). Пустой Kind = FS fallback на ResultDir.
	ResultStorage composition.RemoteStorageConfig

	// Processor — абстрактный процессор. Обязателен (или ImageMagick-адаптер).
	Processor processor.Processor
	// Sources — кастомный SourceStore. Если задан, имеет приоритет над
	// SourceStorage/SourceDir.
	Sources storage.SourceStore
	// Results — кастомный ResultStore. Если задан, имеет приоритет над
	// ResultStorage/ResultDir.
	Results storage.ResultStore

	// OutputLimit — максимальный размер выходного файла (0 = без лимита).
	OutputLimit int64
	// BufferMaxBytes — общий бюджет памяти процесса для spillable-буферов
	// (0 = без лимита). По умолчанию 500 МБ.
	BufferMaxBytes int64

	// MetadataEnabled — включить sidecar-кэш моделей и largest_ai_asset.
	MetadataEnabled bool
	// MetadataDir — КОРЕНЬ sidecar-хранилища метаданных (metadata.dir).
	MetadataDir string
	// Detector — порт ИИ-детекции на уровне приложения (nil = детекция
	// остаётся в процессоре).
	Detector detector.Detector
	// VideoExtractor — извлекатель кадра из видео (ffmpeg). nil = видео
	// не поддерживается (запрос ассета из видео вернёт понятную ошибку).
	VideoExtractor videoframe.Extractor
}

// App — собранный pipeline (обёртка над httpapi.App).
type App struct {
	// Handler — HTTP-обработчик asset URL.
	Handler http.Handler
	// Service — use case генерации ассета (для прямого вызова).
	Service *generatev2.Service
	// Sources — хранилище исходников.
	Sources storage.SourceStore
	// Results — хранилище результатов.
	Results storage.ResultStore
	// Pool — общий бюджет памяти процесса для spillable-буферов.
	Pool *remote.BufferPool
	// AdminSvc — admin-сервис (nil, если admin выключен).
	AdminSvc *adminsvc.Service
	// AdminHandler — HTTP-обработчик /admin/* (nil, если admin выключен).
	AdminHandler http.Handler
}

// New собирает pipeline программно (без YAML). Пользователь подставляет
// порты через Options. Возвращает App с Handler и Service.
func New(ctx context.Context, opts Options) (*App, error) {
	app, err := composition.Build(ctx, composition.AppOptions{
		Config:          opts.Config,
		HTTP:            opts.HTTP,
		SourceDir:       opts.SourceDir,
		ResultDir:       opts.ResultDir,
		SourceStorage:   opts.SourceStorage,
		ResultStorage:   opts.ResultStorage,
		Processor:       opts.Processor,
		Sources:         opts.Sources,
		Results:         opts.Results,
		OutputLimit:     opts.OutputLimit,
		BufferMaxBytes:  opts.BufferMaxBytes,
		MetadataEnabled: opts.MetadataEnabled,
		MetadataDir:     opts.MetadataDir,
		Detector:        opts.Detector,
		VideoExtractor:  opts.VideoExtractor,
	})
	if err != nil {
		return nil, err
	}
	return &App{
		Handler:      app.Handler,
		Service:      app.Service,
		Sources:      app.Sources,
		Results:      app.Results,
		Pool:         app.Pool,
		AdminSvc:     app.AdminSvc,
		AdminHandler: app.AdminHandler,
	}, nil
}
