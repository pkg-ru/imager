module github.com/pkg-ru/imager

go 1.23.7

require (
	github.com/pkg-ru/imager/pkg/imager/imager-encode v0.0.0-00010101000000-000000000000
	github.com/pkg-ru/pkg v0.0.11
	github.com/sirupsen/logrus v1.9.3
	golang.org/x/sync v0.12.0
)

require (
	golang.org/x/sys v0.31.0 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
)

replace github.com/pkg-ru/imager/pkg/imager/imager-encode => ./pkg/imager/imager-encode
