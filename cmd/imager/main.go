// Command imager — тонкая обёртка над публичным фасадом package imager.
//
// Вся логика (загрузка YAML-конфига из трёх слоёв — server/generate/failback,
// сборка pipeline, запуск HTTP-сервера, graceful shutdown) вынесена в
// библиотечный фасад gitverse.ru/pkg-ru/imager.
// Здесь остаётся только чтение env-переменной IMAGER_CONFIG_DIR и вызов
// imager.NewServer + imager.Server.Run.
package main

import (
	"context"
	"os"

	"gitverse.ru/pkg-ru/imager"
)

func main() {
	// Единственная env-переменная: каталог с настройками.
	configDir := os.Getenv(imager.ConfigDirEnv)
	if configDir == "" {
		configDir = imager.DefaultConfigDir
	}

	// Сборка сервера из YAML-конфига (fail-fast на invalid config).
	srv, err := imager.NewServer(configDir)
	if err != nil {
		// Логгер ещё не создан — печатаем в stderr и завершаем процесс.
		imager.Fatal("imager: %v", err)
	}

	// Запуск сервера: блокирует до сигнала завершения или отказа.
	if err := srv.Run(context.Background()); err != nil {
		imager.Fatal("imager: %v", err)
	}
}
