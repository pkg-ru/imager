package logx

import (
	"io"
	"os"

	"github.com/sirupsen/logrus"
)

type FieldLogger interface {
	Debugf(format string, args ...any)
	Infof(format string, args ...any)
	Printf(format string, args ...any)
	Warnf(format string, args ...any)
	Warningf(format string, args ...any)
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
	Panicf(format string, args ...any)

	Debug(args ...any)
	Info(args ...any)
	Print(args ...any)
	Warn(args ...any)
	Warning(args ...any)
	Error(args ...any)
	Fatal(args ...any)
	Panic(args ...any)

	Debugln(args ...any)
	Infoln(args ...any)
	Println(args ...any)
	Warnln(args ...any)
	Warningln(args ...any)
	Errorln(args ...any)
	Fatalln(args ...any)
	Panicln(args ...any)

	Writer() *io.PipeWriter
}

func Init(file string, devMode bool) FieldLogger {
	var Log = logrus.New()
	// установим форматирование логов в джейсоне
	Log.SetFormatter(&logrus.JSONFormatter{})
	// установим уровень логирования
	if devMode {
		Log.SetLevel(logrus.DebugLevel)
	} else {
		Log.SetLevel(logrus.ErrorLevel)
	}
	// установим вывод логов в файл
	fp, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err == nil {
		Log.SetOutput(fp)
	} else {
		Log.Info("Не удалось открыть файл логов, используется стандартный stderr")
	}

	Log.Writer()

	return Log
}
