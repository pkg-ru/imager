package httpapi

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
	"github.com/pkg-ru/imager/internal/adapters/storage/fs"
	"github.com/pkg-ru/imager/internal/adapters/storage/ftp"
	httpadapter "github.com/pkg-ru/imager/internal/adapters/storage/http"
	"github.com/pkg-ru/imager/internal/adapters/storage/remote"
	s3adapter "github.com/pkg-ru/imager/internal/adapters/storage/s3"
	"github.com/pkg-ru/imager/internal/adapters/storage/sftp"
	"github.com/pkg-ru/imager/internal/application/ports/storage"
)

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
	// DialTimeout — таймаут соединения для FTP/SFTP/FTPS, HTTP и S3.
	DialTimeout time.Duration
	// ReadTimeout — таймаут операции для SFTP/FTP/FTPS, HTTP и S3
	// (0 = дефолт).
	ReadTimeout time.Duration
	// MaxAttempts — максимальное число попыток операции для
	// SFTP/FTP/FTPS, HTTP и S3 (0 = дефолт).
	MaxAttempts int
	// MaxIdleConns — максимальное число idle-соединений в пуле
	// (SFTP/FTP/FTPS, HTTP, S3; 0 = не держать соединение).
	MaxIdleConns int
	// MaxConns — максимальное число одновременных соединений в пуле
	// (SFTP/FTP/FTPS; 0 = 2).
	MaxConns int
	// MaxIdleConnsPerHost — максимальное число idle-соединений на хост
	// (HTTP, S3).
	MaxIdleConnsPerHost int
	// IdleConnTimeout — таймаут idle-соединений (SFTP/FTP/FTPS, HTTP, S3).
	IdleConnTimeout time.Duration
	// MetadataTTL — TTL кэша метаданных (S3; 0 = кэш отключён).
	MetadataTTL time.Duration
}

// BuildSourceStore создаёт SourceStore по конфигурации. При пустом Kind
// возвращается nil (вызывающий использует FS fallback).
func BuildSourceStore(ctx context.Context, cfg RemoteStorageConfig) (storage.SourceStore, error) {
	switch cfg.Kind {
	case "", StorageFS:
		return nil, nil
	case StorageS3:
		client, err := buildS3Client(ctx, cfg)
		if err != nil {
			return nil, err
		}
		return s3adapter.NewSourceStore(s3adapter.Options{
			Bucket:        cfg.Bucket,
			Prefix:        cfg.Prefix,
			Client:        client,
			SpoolDir:      cfg.SpoolDir,
			SpoolMaxBytes: cfg.SpoolMaxBytes,
			Pool:          cfg.Pool,
			MetadataTTL:   cfg.MetadataTTL,
		})
	case StorageSFTP:
		return sftp.NewSourceStore(sftp.Options{
			Addr:               cfg.Addr,
			User:               cfg.User,
			Password:           cfg.Password,
			PrivateKey:         cfg.PrivateKey,
			Root:               cfg.Root,
			SpoolDir:           cfg.SpoolDir,
			SpoolMaxBytes:      cfg.SpoolMaxBytes,
			Pool:               cfg.Pool,
			DialTimeout:        cfg.DialTimeout,
			ReadTimeout:        cfg.ReadTimeout,
			MaxAttempts:        cfg.MaxAttempts,
			MaxIdleConns:       cfg.MaxIdleConns,
			MaxConns:           cfg.MaxConns,
			IdleConnTimeout:    cfg.IdleConnTimeout,
			HostKeyFingerprint: cfg.HostKeyFingerprint,
		})
	case StorageFTP, StorageFTPS:
		return ftp.NewSourceStore(ftp.Options{
			Addr:            cfg.Addr,
			User:            cfg.User,
			Password:        cfg.Password,
			TLS:             cfg.Kind == StorageFTPS,
			TLSVerify:       cfg.TLSVerify,
			Root:            cfg.Root,
			SpoolDir:        cfg.SpoolDir,
			SpoolMaxBytes:   cfg.SpoolMaxBytes,
			Pool:            cfg.Pool,
			DialTimeout:     cfg.DialTimeout,
			ReadTimeout:     cfg.ReadTimeout,
			MaxAttempts:     cfg.MaxAttempts,
			MaxIdleConns:    cfg.MaxIdleConns,
			MaxConns:        cfg.MaxConns,
			IdleConnTimeout: cfg.IdleConnTimeout,
		})
	case StorageHTTP:
		return httpadapter.NewSourceStore(httpadapter.Options{
			BaseURL:             cfg.BaseURL,
			SpoolDir:            cfg.SpoolDir,
			SpoolMaxBytes:       cfg.SpoolMaxBytes,
			Pool:                cfg.Pool,
			DialTimeout:         cfg.DialTimeout,
			ReadTimeout:         cfg.ReadTimeout,
			MaxAttempts:         cfg.MaxAttempts,
			MaxIdleConns:        cfg.MaxIdleConns,
			MaxIdleConnsPerHost: cfg.MaxIdleConnsPerHost,
			IdleConnTimeout:     cfg.IdleConnTimeout,
		})
	default:
		return nil, fmt.Errorf("httpapi: unsupported source storage kind %q", cfg.Kind)
	}
}

// BuildResultStore создаёт ResultStore по конфигурации. При пустом Kind
// возвращается nil (вызывающий использует FS fallback). Plain FTP не
// поддерживает ResultStore — возвращается ошибка capability.
func BuildResultStore(ctx context.Context, cfg RemoteStorageConfig) (storage.ResultStore, error) {
	switch cfg.Kind {
	case "", StorageFS:
		return nil, nil
	case StorageS3:
		client, err := buildS3Client(ctx, cfg)
		if err != nil {
			return nil, err
		}
		return s3adapter.NewResultStore(s3adapter.Options{
			Bucket:        cfg.Bucket,
			Prefix:        cfg.Prefix,
			Client:        client,
			SpoolDir:      cfg.SpoolDir,
			SpoolMaxBytes: cfg.SpoolMaxBytes,
			Pool:          cfg.Pool,
			MetadataTTL:   cfg.MetadataTTL,
		})
	case StorageSFTP:
		return sftp.NewResultStore(sftp.Options{
			Addr:               cfg.Addr,
			User:               cfg.User,
			Password:           cfg.Password,
			PrivateKey:         cfg.PrivateKey,
			Root:               cfg.Root,
			SpoolDir:           cfg.SpoolDir,
			SpoolMaxBytes:      cfg.SpoolMaxBytes,
			Pool:               cfg.Pool,
			DialTimeout:        cfg.DialTimeout,
			ReadTimeout:        cfg.ReadTimeout,
			MaxAttempts:        cfg.MaxAttempts,
			MaxIdleConns:       cfg.MaxIdleConns,
			MaxConns:           cfg.MaxConns,
			IdleConnTimeout:    cfg.IdleConnTimeout,
			HostKeyFingerprint: cfg.HostKeyFingerprint,
		})
	case StorageFTP, StorageFTPS:
		return ftp.NewResultStore(ftp.Options{
			Addr:            cfg.Addr,
			User:            cfg.User,
			Password:        cfg.Password,
			TLS:             cfg.Kind == StorageFTPS,
			TLSVerify:       cfg.TLSVerify,
			Root:            cfg.Root,
			SpoolDir:        cfg.SpoolDir,
			SpoolMaxBytes:   cfg.SpoolMaxBytes,
			Pool:            cfg.Pool,
			DialTimeout:     cfg.DialTimeout,
			ReadTimeout:     cfg.ReadTimeout,
			MaxAttempts:     cfg.MaxAttempts,
			MaxIdleConns:    cfg.MaxIdleConns,
			MaxConns:        cfg.MaxConns,
			IdleConnTimeout: cfg.IdleConnTimeout,
		})
	case StorageHTTP:
		return nil, fmt.Errorf("httpapi: http storage is source-only and cannot be used as result")
	default:
		return nil, fmt.Errorf("httpapi: unsupported result storage kind %q", cfg.Kind)
	}
}

// s3Defaults — разумные умолчания для S3-клиента.
const (
	s3DefaultDialTimeout      = 30 * time.Second
	s3DefaultReadTimeout      = 60 * time.Second
	s3DefaultMaxAttempts      = 3
	s3DefaultMaxIdleConns     = 100
	s3DefaultMaxIdleConnsHost = 10
	s3DefaultIdleConnTimeout  = 90 * time.Second
	s3DefaultKeepAlive        = 30 * time.Second
	s3DefaultTLSHandshake     = 10 * time.Second
	s3DefaultExpectContinue   = 1 * time.Second
	s3DefaultMaxConnsPerHost  = 2048
)

// buildS3Client создаёт S3-клиент с явной retry-политикой, таймаутами и
// пулом соединений. Без этих настроек SDK использует дефолты, которые не
// гарантируют bounded connect/read таймауты и достаточный пул для
// параллельных ReadStream/Open.
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

	dialTimeout := cfg.DialTimeout
	if dialTimeout <= 0 {
		dialTimeout = s3DefaultDialTimeout
	}
	readTimeout := cfg.ReadTimeout
	if readTimeout <= 0 {
		readTimeout = s3DefaultReadTimeout
	}
	maxAttempts := cfg.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = s3DefaultMaxAttempts
	}
	maxIdleConns := cfg.MaxIdleConns
	if maxIdleConns <= 0 {
		maxIdleConns = s3DefaultMaxIdleConns
	}
	maxIdleConnsPerHost := cfg.MaxIdleConnsPerHost
	if maxIdleConnsPerHost <= 0 {
		maxIdleConnsPerHost = s3DefaultMaxIdleConnsHost
	}
	idleConnTimeout := cfg.IdleConnTimeout
	if idleConnTimeout <= 0 {
		idleConnTimeout = s3DefaultIdleConnTimeout
	}

	// Явный HTTP-клиент с пулом соединений, keep-alive и bounded
	// connect/read таймаутами.
	dialer := &net.Dialer{
		Timeout:   dialTimeout,
		KeepAlive: s3DefaultKeepAlive,
		DualStack: true,
	}
	transport := &stdhttp.Transport{
		Proxy:                 stdhttp.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		MaxIdleConns:          maxIdleConns,
		MaxIdleConnsPerHost:   maxIdleConnsPerHost,
		MaxConnsPerHost:       s3DefaultMaxConnsPerHost,
		IdleConnTimeout:       idleConnTimeout,
		TLSHandshakeTimeout:   s3DefaultTLSHandshake,
		ExpectContinueTimeout: s3DefaultExpectContinue,
		ForceAttemptHTTP2:     true,
	}
	httpClient := &stdhttp.Client{
		Transport: transport,
		Timeout:   readTimeout,
	}

	// Retry-политика: standard mode с явным MaxAttempts и bounded backoff.
	retryer := func() aws.Retryer {
		return retry.NewStandard(func(o *retry.StandardOptions) {
			o.MaxAttempts = maxAttempts
			o.MaxBackoff = 20 * time.Second
		})
	}

	opts = append(opts,
		awsconfig.WithRetryMode(aws.RetryModeStandard),
		awsconfig.WithRetryMaxAttempts(maxAttempts),
		awsconfig.WithHTTPClient(httpClient),
		awsconfig.WithRetryer(retryer),
	)

	awscfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("httpapi: s3 config: %w", err)
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
			return nil, nil, fmt.Errorf("httpapi: source dir is empty")
		}
		s, err := fs.NewSourceStore(dir)
		if err != nil {
			return nil, nil, fmt.Errorf("httpapi: source store: %w", err)
		}
		sources = s
	}
	if results == nil {
		dir := resCfg.Path
		if dir == "" {
			dir = resultDir
		}
		if dir == "" {
			return nil, nil, fmt.Errorf("httpapi: result dir is empty")
		}
		r, err := fs.NewResultStore(dir)
		if err != nil {
			return nil, nil, fmt.Errorf("httpapi: result store: %w", err)
		}
		results = r
	}
	return sources, results, nil
}
