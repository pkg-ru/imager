package composition

import (
	"context"
	"fmt"
	"net/http"

	"github.com/pkg-ru/imager/adapters/httpapi"
	"github.com/pkg-ru/imager/adapters/storage/fs"
	"github.com/pkg-ru/imager/adapters/storage/remote"
	"github.com/pkg-ru/imager/app/adminsvc"
	"github.com/pkg-ru/imager/app/generatev2"
	"github.com/pkg-ru/imager/app/learning"
	"github.com/pkg-ru/imager/config"
	"github.com/pkg-ru/imager/coordination/singleflight"
	"github.com/pkg-ru/imager/ports/detector"
	"github.com/pkg-ru/imager/ports/metadata"
	"github.com/pkg-ru/imager/ports/processor"
	"github.com/pkg-ru/imager/ports/storage"
	"github.com/pkg-ru/imager/ports/videoframe"
)

// AppOptions — параметры сборки нового pipeline (composition root).
type AppOptions struct {
	// Config — typed конфигурация конвейера (policy/processing).
	Config *config.Config
	// HTTP — конфигурация HTTP-адаптера.
	HTTP httpapi.Config
	// ConfigDir — каталог конфигурации (для learning-mode Recorder:
	// generate-local.yaml пишется в этот каталог). Пусто = learning-mode
	// Recorder не создаётся (флаг всё равно работает из конфига).
	ConfigDir string

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

	// Processor — абстрактный процессор (обязательный).
	Processor processor.Processor
	// Sources — кастомный SourceStore (для тестов). Если задан, имеет
	// приоритет над SourceStorage/SourceDir.
	Sources storage.SourceStore
	// Results — кастомный ResultStore (для тестов). Если задан, имеет
	// приоритет над ResultStorage/ResultDir.
	Results storage.ResultStore

	// Limits — application-level лимиты генерации ассетов (application.limits).
	// Нулевые поля = без ограничения.
	Limits generatev2.Limits
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
	// VideoExtractor — извлекатель кадра из видео (ffmpeg). nil = видео
	// не поддерживается (запрос ассета из видео вернёт понятную ошибку).
	VideoExtractor videoframe.Extractor

	// AsyncPublish — флаг асинхронной публикации результата (S1). true
	// (умолчание для production) = публикация в кэш выполняется фоновыми
	// воркерами после ответа клиенту; false = публикация синхронная (прежнее
	// поведение, кэш готов сразу после Generate) — используется в тестах,
	// которые проверяют состояние кэша сразу после запроса.
	AsyncPublish bool
}

// App — собранный pipeline.
type App struct {
	Handler *httpapi.Handler
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

	// Learning — фасад learning-mode (nil, если learning-mode недоступен:
	// ConfigDir не задан). Требует Stop() при shutdown (drain + финальная
	// запись generate-local.yaml).
	Learning *learning.Service
}

// Build собирает новый pipeline. Fail-fast на invalid config.
func Build(ctx context.Context, opt AppOptions) (*App, error) {
	if opt.Config == nil {
		return nil, fmt.Errorf("composition: build: nil config")
	}
	compiled, err := opt.Config.Compile()
	if err != nil {
		return nil, fmt.Errorf("composition: build: compile config: %w", err)
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
			return nil, fmt.Errorf("composition: build: %w", err)
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
		return nil, fmt.Errorf("composition: build: processor is required")
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
				return nil, fmt.Errorf("composition: build: metadata store: %w", err)
			}
			metaStore = ms
		}
	}

	// Координатор.
	coord := singleflight.New(singleflight.Options{})

	// Learning-mode: runtime-флаг (Controller) + сборщик наблюдений
	// (Recorder). Controller создаётся всегда (generatev2 bypass); Recorder
	// — только если задан каталог конфигурации (generate-local.yaml пишется
	// в него). Начальное состояние флага — policy.learning-mode из
	// конфигурации.
	learningCtrl := learning.NewController()
	if compiled.LearningMode {
		learningCtrl.Enable()
	}
	var learningRec *learning.Recorder
	if opt.ConfigDir != "" {
		learningRec, err = learning.NewRecorder(learning.Deps{
			ConfigDir: opt.ConfigDir,
			Initial:   opt.Config.Policy,
			Logger:    opt.HTTP.Logger,
		})
		if err != nil {
			return nil, fmt.Errorf("composition: build: learning recorder: %w", err)
		}
	}
	learningSvc := learning.NewService(learningCtrl, learningRec)

	// Use case.
	// S1: асинхронная публикация результата в кэш. В production включена по
	// умолчанию (AsyncPublish=true): публикация выполняется bounded-очередью
	// фоновых воркеров, ответ клиенту не ждёт записи в remote. Тесты, которым
	// нужен готовый кэш сразу после Generate, отключают её (AsyncPublish=false
	// → synchroncore поведение).
	var publishQueue *generatev2.PublishQueueConfig
	if opt.AsyncPublish {
		publishQueue = &generatev2.PublishQueueConfig{}
	}
	svc, err := generatev2.New(generatev2.Deps{
		Sources:                  sources,
		Results:                  results,
		Coordinator:              coord,
		Processor:                proc,
		Policy:                   compiled.Policy,
		Presets:                  compiled.Presets,
		Buffers:                  buffers,
		Limits:                   &opt.Limits,
		Quality:                  int(compiled.DefaultQuality),
		DefaultWatermark:         compiled.DefaultWatermark,
		DefaultOrientation:       compiled.DefaultOrientation,
		DefaultTrim:              compiled.DefaultTrim,
		Logger:                   opt.HTTP.Logger,
		Metrics:                  opt.HTTP.Metrics,
		Metadata:                 metaStore,
		Detector:                 opt.Detector,
		VideoExtractor:           opt.VideoExtractor,
		DefaultVideoFramePercent: compiled.DefaultVideoFramePercent,
		DefaultVideoMinContrast:  compiled.DefaultVideoMinContrast,
		DefaultVideoFrameStep:    compiled.DefaultVideoFrameStep,
		DefaultVideoAttempts:     compiled.DefaultVideoAttempts,
		Learning:                 learningCtrl,
		PublishQueue:             publishQueue,
	})
	if err != nil {
		return nil, fmt.Errorf("composition: build: generatev2: %w", err)
	}

	// HTTP handler. Пробрасываем хранилище исходников в конфиг для source
	// fallback (nil = фича недоступна), а learning-mode — для Observe.
	opt.HTTP.Sources = sources
	opt.HTTP.PolicyRecorder = learningSvc
	h, err := httpapi.New(svc, opt.HTTP)
	if err != nil {
		return nil, fmt.Errorf("composition: build: handler: %w", err)
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
			return nil, fmt.Errorf("composition: build: adminsvc: %w", err)
		}
		adminHandler = httpapi.NewAdminHandler(adminSvc, opt.HTTP.Admin, opt.HTTP.Logger)
	}

	return &App{
		Handler:      h,
		Service:      svc,
		Sources:      sources,
		Results:      results,
		Pool:         pool,
		AdminSvc:     adminSvc,
		AdminHandler: adminHandler,
		Learning:     learningSvc,
	}, nil
}
