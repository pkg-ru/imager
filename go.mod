module gitverse.ru/pkg-ru/imager

go 1.27.0

// Вендоренный форк govips v2.18.0 (govips) с доработкой:
// поддержка аргумента "strip" (VipsForeignSave) для heifsave/jxlsave —
// чтобы перекодированные HEIF/JXL не получали синтезированный EXIF-блок
// из заголовка (vips__exif_update). Подробности — в govips/go.mod.

replace github.com/davidbyttow/govips/v2 => ./govips

require (
	github.com/aws/aws-sdk-go-v2 v1.45.1
	github.com/aws/aws-sdk-go-v2/config v1.33.1
	github.com/aws/aws-sdk-go-v2/service/s3 v1.109.1
	github.com/aws/smithy-go v1.28.1
	github.com/davidbyttow/govips/v2 v2.18.0
	github.com/jlaffaye/ftp v0.2.4
	github.com/pkg-ru/dynamic v1.0.0
	github.com/pkg/sftp v1.13.11
	github.com/yalue/onnxruntime_go v1.35.0
	golang.org/x/crypto v0.55.0
	golang.org/x/sys v0.47.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.20 // indirect
	github.com/aws/aws-sdk-go-v2/credentials v1.20.1 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.19.1 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.1 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.1 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.5.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.19 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.11.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.14.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.20.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.7.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.35.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.40.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.47.1 // indirect
	github.com/kr/fs v0.1.0 // indirect
	golang.org/x/image v0.45.0 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/text v0.41.0 // indirect
)
