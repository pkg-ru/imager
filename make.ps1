<#
.SYNOPSIS
    Imager — Windows-раннер (PowerShell-аналог Makefile).

.DESCRIPTION
    Единый раннер для локальной разработки и тестов на Windows.
    Реализует те же цели, что и Makefile, с учётом Windows-специфики:
      - libvips-теги на Windows НЕ используются (доступны только default/onnx,
        см. README.md); libvips/onnx-прогон — через Docker (CI-образ);
      - CGO_LDFLAGS="-no-pie" НЕ нужен: это исправление для Linux/musl (Alpine);
      - FEAT-зависимости (libvips, onnxruntime) устанавливаются скриптом
        docker/install-deps-windows.ps1 (DLL в %LOCALAPPDATA%\imager\bin).

.PARAMETER Target
    Цель для запуска (см. список ниже). По умолчанию help.

    Допустимые цели:
      help               список целей
      install            go mod download + go mod tidy
      build              сборка imager.exe (tags: $Tags)
      build-onnx         сборка с -tags onnx
      run                build + запуск IMAGER_CONFIG_DIR=./setting imager.exe
      stop               остановка imager.exe (taskkill /F /IM)
      restart            stop + run
      fmt                gofmt -l -w .
      fmt-check          проверка форматирования без изменений
      vet                go vet ./...
      tags-check         все комбинации build tags на Windows (default/onnx)
      test-onnx          тесты реального инференса (тег onnx)
      test-onnx-all      полный прогон с тегом onnx
      test               go test ./... -count=1
      race               go test -race ./... -count=1
      fuzz               fuzz smoke-тесты (FuzzParse, FuzzParseSize)
      check              fmt-check + vet + test + race
      docker-test        go test (libvips,onnx) в CI-образе (Docker Desktop)
      docker-test-race   go test -race (libvips,onnx) в CI-образе
      docker-vet         go vet ./... в CI-образе
      docker-tags-check  все комбинации tags в CI-образе
      docker-fmt-check   gofmt-check в CI-образе
      docker-govulncheck govulncheck ./... в CI-образе
      docker-check       полный прогон в CI-образе (без fuzz)
      docker-build       docker build -t imager:production .
      docker-build-from-source  docker build --target from-source ...
      docker-up          docker compose up -d --build
      docker-down        docker compose down

.PARAMETER Tags
    Build tags для build (по умолчанию пусто — default-сборка без libvips/onnx).

.PARAMETER CI_IMAGE
    Образ CI-контейнера (по умолчанию gitverse.ru/pkg-ru/imager-ci:v1).
    Можно задать через переменную окружения с тем же именем.

.EXAMPLE
    .\make.ps1 install
    .\make.ps1 test
    .\make.ps1 docker-test
    .\make.ps1 build -Tags onnx
#>
[CmdletBinding()]
param(
    [Parameter(Position = 0)]
    [ValidateSet('help', 'install', 'build', 'build-onnx', 'run', 'stop', 'restart', 'fmt', 'fmt-check', 'vet', 'tags-check', 'test-onnx', 'test-onnx-all', 'test', 'race', 'fuzz', 'check', 'docker-test', 'docker-test-race', 'docker-vet', 'docker-tags-check', 'docker-fmt-check', 'docker-govulncheck', 'docker-check', 'docker-build', 'docker-build-from-source', 'docker-up', 'docker-down')]
    [string]$Target = 'help',

    [ValidateSet('', 'onnx')]
    [string]$Tags = '',

    [switch]$Help
)

$ErrorActionPreference = 'Stop'
$Prefix = '[imager]'

function Log   { Write-Host "$Prefix $($args -join ' ')" }
function Die   { Write-Host "$Prefix ERROR: $($args -join ' ')" -ForegroundColor Red; exit 1 }

function Exec {
    param([scriptblock]$Script, [string]$What)
    Log $What
    & $Script
    if ($LASTEXITCODE -ne 0) { Die "'$What' failed (exit $LASTEXITCODE)" }
}

if ($Help) { return Get-Help $PSCommandPath }

# --- Параметры сборки -------------------------------------------------------
# Go-сборка: на Windows CGO_LDFLAGS="-no-pie" не нужен (это фикс Linux/musl),
# поэтому просто прокидываем TAGS.
function Invoke-GoBuild([string]$tags) {
    $args = @('build', '-trimpath', '-ldflags=-s -w', '-o', './imager.exe', './cmd/imager')
    if ($tags) { $args = @('build', '-tags', $tags, '-trimpath', '-ldflags=-s -w', '-o', './imager.exe', './cmd/imager') }
    Exec { & go @args } "go $($args -join ' ')"
}

# --- CI-образ (Docker) --------------------------------------------------------
$CI_IMAGE = if ($env:CI_IMAGE) { $env:CI_IMAGE } else { 'gitverse.ru/pkg-ru/imager-ci:v1' }

# Запуск команды в CI-образе: исходники монтируются в /src, GOMODCACHE —
# предзагруженный каталог образа, CGO_LDFLAGS="-no-pie" — фикс PIE для
# Alpine/musl (иначе segfault тестов cgo-пакетов).
function Exec-CI([string]$cmd, [string]$what) {
    # ВАЖНО: не называть переменную $args — это автоматическая переменная
    # PowerShell, которая внутри скриптблока Exec переопределяется (пустой
    # массив), и & docker @args развернулся бы в пустоту (usage без команд).
    $ciArgs = @('run', '--rm', '-v', "${PWD}:/src", '-w', '/src',
                '-e', 'CGO_ENABLED=1', '-e', 'GOMODCACHE=/gomodcache',
                '-e', 'CGO_LDFLAGS=-no-pie', $CI_IMAGE, 'sh', '-c', $cmd)
    Exec { & docker @ciArgs } $what
}

switch ($Target) {
    'help' {
        Get-Help $PSCommandPath
        break
    }

    'install' {
        Exec { & go mod download } 'go mod download'
        Exec { & go mod tidy }     'go mod tidy'
        break
    }

    'build' {
        Invoke-GoBuild $Tags
        break
    }

    'build-onnx' {
        Invoke-GoBuild 'onnx'
        break
    }

    'run' {
        Invoke-GoBuild $Tags
        Log 'starting ./imager.exe (IMAGER_CONFIG_DIR=./setting)'
        $env:IMAGER_CONFIG_DIR = './setting'
        Exec { & './imager.exe' } 'run imager.exe'
        break
    }

    'stop' {
        Log 'Imager project stop'
        taskkill /F /IM imager.exe 2>$null | Out-Null
        break
    }

    'restart' {
        & $PSCommandPath stop
        & $PSCommandPath run
        break
    }

    'fmt' {
        Exec { & gofmt -l -w . } 'gofmt -l -w .'
        break
    }

    'fmt-check' {
        Exec {
            $unformatted = & gofmt -l .
            if ($unformatted) {
                Write-Host "Unformatted files:"
                Write-Host $unformatted
                exit 1
            }
        } 'gofmt check (no changes)'
        break
    }

    'vet' {
        Exec { & go vet ./... } 'go vet ./...'
        break
    }

    'tags-check' {
        # Windows: доступны default и onnx (libvips-теги только на Linux,
        # см. README.md). Прогон libvips,onnx — через docker-test* (CI-образ).
        Exec { & go build ./... }                    'go build ./...'
        Exec { & go vet ./... }                      'go vet ./...'
        Exec { & go build -tags onnx ./... }         'go build -tags onnx ./...'
        Exec { & go vet -tags onnx ./... }           'go vet -tags onnx ./...'
        break
    }

    'test-onnx' {
        # Требует onnxruntime.dll в PATH (docker/install-deps-windows.ps1)
        # и модели в ./models (IMAGER_MODELS_DIR).
        Exec { & go test -tags onnx ./adapters/processor/detection/... -count=1 } 'go test -tags onnx (detection)'
        break
    }

    'test-onnx-all' {
        # Полный прогон с onnx на Windows (libvips недоступен; его часть
        # покрывается docker-test через CI-образ).
        Exec { & go test -tags onnx ./... -count=1 } 'go test -tags onnx ./...'
        break
    }

    'test' {
        Exec { & go test ./... -count=1 } 'go test ./... -count=1'
        break
    }

    'race' {
        Exec { & go test -race ./... -count=1 } 'go test -race ./... -count=1'
        break
    }

    'fuzz' {
        Exec { & go test -run=^$ -fuzz=^FuzzParse$ -fuzztime=10s ./domain/asset }     'fuzz FuzzParse'
        Exec { & go test -run=^$ -fuzz=^FuzzParseSize$ -fuzztime=10s ./domain/asset } 'fuzz FuzzParseSize'
        break
    }

    'check' {
        & $PSCommandPath fmt-check
        & $PSCommandPath vet
        & $PSCommandPath test
        & $PSCommandPath race
        break
    }

    # --- CI-образ: тесты libvips+onnx в Docker (Docker Desktop) -------------
    # ВАЖНО: sh-команды НЕ должны содержать двойных кавычек — PowerShell 5.1
    # ломает их при передаче аргументов нативному docker.exe (sh получает
    # обрезанную команду). Поэтому: -tags=libvips,onnx (запятая, без кавычек),
    # а в fmt-check — одинарные кавычки sh (в PowerShell экранируются '' ).
    'docker-test' {
        Exec-CI 'go test -tags=libvips,onnx ./... -count=1' 'docker-test (libvips,onnx)'
        break
    }

    'docker-test-race' {
        Exec-CI 'go test -race -tags=libvips,onnx ./... -count=1' 'docker-test-race (libvips,onnx)'
        break
    }

    'docker-vet' {
        Exec-CI 'go vet ./...' 'docker-vet'
        break
    }

    'docker-tags-check' {
        Exec-CI 'go build ./... && go vet ./... && go build -tags=onnx ./... && go vet -tags=onnx ./... && go build -tags=libvips,onnx ./... && go vet -tags=libvips,onnx ./...' 'docker-tags-check'
        break
    }

    'docker-fmt-check' {
        # Без кавычек: gofmt|grep печатает неотформатированные файлы и
        # возвращает 1, если их нет (-> exit 0); есть файлы (-> exit 1).
        Exec-CI 'gofmt -l . | grep . && exit 1 || exit 0' 'docker-fmt-check'
        break
    }

    'docker-govulncheck' {
        Exec-CI 'govulncheck ./...' 'docker-govulncheck'
        break
    }

    'docker-check' {
        Exec-CI 'gofmt -l . | grep . && exit 1 || exit 0' 'docker-fmt-check'
        Exec-CI 'go test -tags=libvips,onnx ./... -count=1' 'docker-test (libvips,onnx)'
        Exec-CI 'go test -race -tags=libvips,onnx ./... -count=1' 'docker-test-race (libvips,onnx)'
        Exec-CI 'govulncheck ./...' 'docker-govulncheck'
        break
    }

    # --- Production / Docker -------------------------------------------------
    'docker-build' {
        Exec { & docker build -t imager:production . } 'docker build -t imager:production .'
        break
    }

    'docker-build-from-source' {
        Exec { & docker build --target from-source -t imager:from-source . } 'docker build --target from-source'
        break
    }

    'docker-up' {
        Exec { & docker compose up -d --build } 'docker compose up -d --build'
        break
    }

    'docker-down' {
        Exec { & docker compose down } 'docker compose down'
        break
    }
}