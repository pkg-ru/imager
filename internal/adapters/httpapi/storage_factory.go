package httpapi

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
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
	// DialTimeout — таймаут соединения для FTP/SFTP/FTPS.
	DialTimeout time.Duration
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
			HostKeyFingerprint: cfg.HostKeyFingerprint,
		})
	case StorageFTP, StorageFTPS:
		return ftp.NewSourceStore(ftp.Options{
			Addr:          cfg.Addr,
			User:          cfg.User,
			Password:      cfg.Password,
			TLS:           cfg.Kind == StorageFTPS,
			TLSVerify:     cfg.TLSVerify,
			Root:          cfg.Root,
			SpoolDir:      cfg.SpoolDir,
			SpoolMaxBytes: cfg.SpoolMaxBytes,
			Pool:          cfg.Pool,
			DialTimeout:   cfg.DialTimeout,
		})
	case StorageHTTP:
		return httpadapter.NewSourceStore(httpadapter.Options{
			BaseURL:       cfg.BaseURL,
			SpoolDir:      cfg.SpoolDir,
			SpoolMaxBytes: cfg.SpoolMaxBytes,
			Pool:          cfg.Pool,
			Timeout:       cfg.DialTimeout,
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
			HostKeyFingerprint: cfg.HostKeyFingerprint,
		})
	case StorageFTP, StorageFTPS:
		return ftp.NewResultStore(ftp.Options{
			Addr:          cfg.Addr,
			User:          cfg.User,
			Password:      cfg.Password,
			TLS:           cfg.Kind == StorageFTPS,
			TLSVerify:     cfg.TLSVerify,
			Root:          cfg.Root,
			SpoolDir:      cfg.SpoolDir,
			SpoolMaxBytes: cfg.SpoolMaxBytes,
			Pool:          cfg.Pool,
			DialTimeout:   cfg.DialTimeout,
		})
	case StorageHTTP:
		return nil, fmt.Errorf("httpapi: http storage is source-only and cannot be used as result")
	default:
		return nil, fmt.Errorf("httpapi: unsupported result storage kind %q", cfg.Kind)
	}
}

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
