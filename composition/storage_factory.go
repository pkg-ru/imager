// Package composition — composition root приложения imager.
//
// Здесь сосредоточена вся сборка/DI: загрузка YAML-конфигурации (три слоя),
// typed runtime-config, фабрики storage-адаптеров (FS/S3/SFTP/FTP/FTPS/HTTP),
// создание app-сервисов (generatev2, adminsvc), координатора (singleflight),
// пул буферов и sidecar-метаданных. Транспортный адаптер (adapters/httpapi)
// получает готовые зависимости через конструкторы и зависит только от
// ports/domain — он ничего не собирает сам.
//
// Пакет может импортировать все слои (adapters, app, config, coordination,
// observability); никто не импортирует его, кроме публичного фасада
// package imager и bootstrap.
package composition

import (
	"context"
	"fmt"
	"net"
	stdhttp "net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	"gitverse.ru/pkg-ru/imager/adapters/storage/fs"
	"gitverse.ru/pkg-ru/imager/adapters/storage/ftp"
	httpadapter "gitverse.ru/pkg-ru/imager/adapters/storage/http"
	"gitverse.ru/pkg-ru/imager/adapters/storage/remote"
	s3adapter "gitverse.ru/pkg-ru/imager/adapters/storage/s3"
	"gitverse.ru/pkg-ru/imager/adapters/storage/sftp"
	"gitverse.ru/pkg-ru/imager/ports/storage"
)

// DefaultHTTPSpoolMaxBytes — безопасный дефолт лимита spool для HTTP source
// (512 МБ). Применяется, когда spool-max-bytes не задан (0 = неограниченный
// буфер — DoS-риск).
const DefaultHTTPSpoolMaxBytes int64 = 512 * 1024 * 1024

// StorageKind — тип удалённого хранилища.
type StorageKind string

const (
	StorageFS   StorageKind = "fs"
	StorageS3   StorageKind = "s3"
	StorageSFTP StorageKind = "sftp"
	StorageFTP  StorageKind = "ftp"
	StorageFTPS StorageKind = "ftps"
	// StorageHTTP — HTTP/HTTPS источник исходников (source-only).
	StorageHTTP StorageKind = "http"
)

// RemoteStorageConfig — конфигурация удалённого хранилища (source или result).
// Секреты задаются отдельными полями и не попадают в URI/логи.
type RemoteStorageConfig struct {
	// Kind — тип хранилища (s3, sftp, ftp, ftps, http). Пусто = fs.
	Kind StorageKind
	// Path — локальный каталог для FS-хранилища (storage: fs).
	Path string
	// BaseURL — базовый адрес исходников для HTTP/HTTPS source
	// (например "https://addr.site/path_to_image/").
	BaseURL string
	// Bucket — bucket для S3.
	Bucket string
	// Prefix — префикс ключей для S3.
	Prefix string
	// Endpoint — endpoint S3 (для S3-совместимых хранилищ; пусто = AWS).
	Endpoint string
	// Region — регион S3.
	Region string
	// AccessKey — access key S3.
	AccessKey string
	// SecretKey — secret key S3.
	SecretKey string
	// Addr — адрес "host:port" для SFTP/FTP/FTPS.
	Addr string
	// User — пользователь для SFTP/FTP/FTPS.
	User string
	// Password — пароль для SFTP/FTP/FTPS.
	Password string
	// PrivateKey — содержимое приватного ключа для SFTP.
	PrivateKey []byte
	// Root — корневой каталог для SFTP/FTP/FTPS.
	Root string
	// TLS — true для FTPS.
	TLS bool
	// TLSVerify — проверять ли TLS-сертификат для FTPS (default: true).
	TLSVerify bool
	// HostKeyFingerprint — ожидаемый SHA-256 fingerprint host key SFTP
	// (например "SHA256:..."). Пусто = не проверять (небезопасно).
	HostKeyFingerprint string
	// SpoolDir — каталог временных spool.
	SpoolDir string
	// SpoolMaxBytes — максимальный размер source spool (0 = без лимита).
	SpoolMaxBytes int64
	// Pool — общий бюджет памяти процесса для spillable-буферов.
	Pool *remote.BufferPool
	// Conn — общие параметры соединения для FTP/SFTP/FTPS, HTTP и S3:
	// DialTimeout, ReadTimeout, MaxAttempts, MaxIdleConns, MaxConns,
	// MaxIdleConnsPerHost, IdleConnTimeout. Дефолты задаёт remote.Default*
	// (единый источник; ранее дублировались здесь и в http-адаптере).
	Conn remote.ConnOptions
	// MetadataTTL — TTL кэша метаданных (S3; 0 = кэш отключён).
	MetadataTTL time.Duration
}

// BuildSourceStore создаёт SourceStore по конфигурации. При пустом Kind
// возвращается nil (вызывающий использует FS fallback).
func BuildSourceStore(ctx context.Context, cfg RemoteStorageConfig) (storage.SourceStore, error) {
	s, err := buildStore(ctx, cfg, false)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, nil
	}
	return s.(storage.SourceStore), nil
}

// BuildResultStore создаёт ResultStore по конфигурации. При пустом Kind
// возвращается nil (вызывающий использует FS fallback). Plain FTP не
// поддерживает ResultStore — возвращается ошибка capability.
func BuildResultStore(ctx context.Context, cfg RemoteStorageConfig) (storage.ResultStore, error) {
	s, err := buildStore(ctx, cfg, true)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, nil
	}
	return s.(storage.ResultStore), nil
}

// buildStore — общая фабрика Source/Result-хранилищ по конфигурации.
// isResult=true строит ResultStore, иначе SourceStore. При пустом Kind
// возвращается (nil, nil) — вызывающий использует FS fallback.
func buildStore(ctx context.Context, cfg RemoteStorageConfig, isResult bool) (any, error) {
	role := "source"
	if isResult {
		role = "result"
	}
	switch cfg.Kind {
	case "", StorageFS:
		return nil, nil
	case StorageS3:
		client, err := buildS3Client(ctx, cfg)
		if err != nil {
			return nil, err
		}
		opts := s3adapter.Options{
			Bucket:        cfg.Bucket,
			Prefix:        cfg.Prefix,
			Client:        client,
			SpoolDir:      cfg.SpoolDir,
			SpoolMaxBytes: cfg.SpoolMaxBytes,
			Pool:          cfg.Pool,
			MetadataTTL:   cfg.MetadataTTL,
		}
		if isResult {
			return s3adapter.NewResultStore(opts)
		}
		return s3adapter.NewSourceStore(opts)
	case StorageSFTP:
		opts := sftp.Options{
			Addr:               cfg.Addr,
			User:               cfg.User,
			Password:           cfg.Password,
			PrivateKey:         cfg.PrivateKey,
			Root:               cfg.Root,
			SpoolDir:           cfg.SpoolDir,
			SpoolMaxBytes:      cfg.SpoolMaxBytes,
			Pool:               cfg.Pool,
			ConnOptions:        cfg.Conn,
			HostKeyFingerprint: cfg.HostKeyFingerprint,
		}
		if isResult {
			return sftp.NewResultStore(opts)
		}
		return sftp.NewSourceStore(opts)
	case StorageFTP, StorageFTPS:
		opts := ftp.Options{
			Addr:          cfg.Addr,
			User:          cfg.User,
			Password:      cfg.Password,
			TLS:           cfg.Kind == StorageFTPS,
			TLSVerify:     cfg.TLSVerify,
			Root:          cfg.Root,
			SpoolDir:      cfg.SpoolDir,
			SpoolMaxBytes: cfg.SpoolMaxBytes,
			Pool:          cfg.Pool,
			ConnOptions:   cfg.Conn,
		}
		if isResult {
			return ftp.NewResultStore(opts)
		}
		return ftp.NewSourceStore(opts)
	case StorageHTTP:
		if isResult {
			return nil, fmt.Errorf("composition: http storage is source-only and cannot be used as result")
		}
		// SpoolMaxBytes == 0 означает неограниченный буфер (DoS-риск):
		// устанавливаем безопасный дефолт 512 МБ, если лимит не задан.
		spoolMax := cfg.SpoolMaxBytes
		if spoolMax == 0 {
			spoolMax = DefaultHTTPSpoolMaxBytes
		}
		return httpadapter.NewSourceStore(httpadapter.Options{
			BaseURL:       cfg.BaseURL,
			SpoolDir:      cfg.SpoolDir,
			SpoolMaxBytes: spoolMax,
			Pool:          cfg.Pool,
			ConnOptions:   cfg.Conn,
		})
	default:
		return nil, fmt.Errorf("composition: unsupported %s storage kind %q", role, cfg.Kind)
	}
}

// buildS3Client создаёт S3-клиент с явной retry-политикой, таймаутами и
// пулом соединений. Без этих настроек SDK использует дефолты, которые не
// гарантируют bounded connect/read таймауты и достаточный пул для
// параллельных ReadStream/Open. Дефолты берутся из remote.Default*
// (единый источник с HTTP-адаптером; прежде константы дублировались здесь).
func buildS3Client(ctx context.Context, cfg RemoteStorageConfig) (*awss3.Client, error) {
	opts := []func(*awsconfig.LoadOptions) error{}
	if cfg.Region != "" {
		opts = append(opts, awsconfig.WithRegion(cfg.Region))
	}
	if cfg.AccessKey != "" && cfg.SecretKey != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			aws.NewCredentialsCache(staticCredentials{access: cfg.AccessKey, secret: cfg.SecretKey}),
		))
	}

	conn := cfg.Conn

	// Явный HTTP-клиент с пулом соединений, keep-alive и bounded
	// connect/read таймаутами.
	dialer := &net.Dialer{
		Timeout:   conn.DialTimeoutOrDefault(),
		KeepAlive: remote.DefaultKeepAlive,
		DualStack: true,
	}
	transport := &stdhttp.Transport{
		Proxy:                 stdhttp.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		MaxIdleConns:          conn.MaxIdleConnsOrDefault(),
		MaxIdleConnsPerHost:   conn.MaxIdleConnsPerHostOrDefault(),
		MaxConnsPerHost:       remote.DefaultMaxConnsPerHost,
		IdleConnTimeout:       conn.IdleConnTimeoutOrDefault(),
		TLSHandshakeTimeout:   remote.DefaultTLSHandshake,
		ExpectContinueTimeout: remote.DefaultExpectContinue,
		ForceAttemptHTTP2:     true,
	}
	httpClient := &stdhttp.Client{
		Transport: transport,
		Timeout:   conn.ReadTimeoutOrDefault(),
	}

	// Retry-политика: standard mode с явным MaxAttempts и bounded backoff.
	retryer := func() aws.Retryer {
		return retry.NewStandard(func(o *retry.StandardOptions) {
			o.MaxAttempts = conn.MaxAttemptsOrDefault()
			o.MaxBackoff = 20 * time.Second
		})
	}

	opts = append(opts,
		awsconfig.WithRetryMode(aws.RetryModeStandard),
		awsconfig.WithRetryMaxAttempts(conn.MaxAttemptsOrDefault()),
		awsconfig.WithHTTPClient(httpClient),
		awsconfig.WithRetryer(retryer),
	)

	awscfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("composition: s3 config: %w", err)
	}
	if cfg.Endpoint != "" {
		return awss3.NewFromConfig(awscfg, func(o *awss3.Options) {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}), nil
	}
	return awss3.NewFromConfig(awscfg), nil
}

// staticCredentials — статический провайдер учётных данных S3.
type staticCredentials struct {
	access, secret string
}

func (c staticCredentials) Retrieve(ctx context.Context) (aws.Credentials, error) {
	return aws.Credentials{
		AccessKeyID:     c.access,
		SecretAccessKey: c.secret,
		Source:          "imager-static",
	}, nil
}

// ensureFSStores создаёт FS-хранилища, если remote-адаптеры не заданы или
// Kind == fs. Для FS-конфигурации используется cfg.Path (если задан), иначе
// dir. Возвращает source/result, используя remote при наличии, иначе FS.
func ensureFSStores(ctx context.Context, sourceDir, resultDir string, srcCfg, resCfg RemoteStorageConfig) (storage.SourceStore, storage.ResultStore, error) {
	sources, err := BuildSourceStore(ctx, srcCfg)
	if err != nil {
		return nil, nil, err
	}
	results, err := BuildResultStore(ctx, resCfg)
	if err != nil {
		return nil, nil, err
	}
	if sources == nil {
		dir := srcCfg.Path
		if dir == "" {
			dir = sourceDir
		}
		if dir == "" {
			return nil, nil, fmt.Errorf("composition: source dir is empty")
		}
		s, err := fs.NewSourceStore(dir)
		if err != nil {
			return nil, nil, fmt.Errorf("composition: source store: %w", err)
		}
		sources = s
	}
	if results == nil {
		dir := resCfg.Path
		if dir == "" {
			dir = resultDir
		}
		if dir == "" {
			return nil, nil, fmt.Errorf("composition: result dir is empty")
		}
		r, err := fs.NewResultStore(dir)
		if err != nil {
			return nil, nil, fmt.Errorf("composition: result store: %w", err)
		}
		results = r
	}
	return sources, results, nil
}
