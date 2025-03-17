package files

import (
	"os"
	"path/filepath"

	"github.com/pkg-ru/pkg/files"
)

func GetPath(file string) string {
	var path = os.Args[0]
	path, err := filepath.Abs(path)
	if err == nil {
		return files.GetPath(file)
	}
	return filepath.Join(path, file)
}
