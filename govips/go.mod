// Модуль govips — вендоренный форк github.com/davidbyttow/govips
// v2.18.0 (с заменой модульного пути: импортируется как
// github.com/davidbyttow/govips/v2 через replace-директиву главного go.mod).
//
// Отличия от upstream (минимальные, точечные):
//   - vips.HeifExportParams и vips.JxlExportParams получили поле StripMetadata;
//   - vipsSaveHEIFToBuffer / vipsSaveJxlToBuffer передают stripMetadata в C;
//   - set_heifsave_options / set_jxlsave_options (foreign.c) устанавливают
//     общий аргумент VipsForeignSave "strip". libvips 8.16 принимает его
//     (наследуется всеми saver'ами) и при strip=true не синтезирует свежий
//     EXIF-блок из заголовка (vips__exif_update) для HEIF/JXL выходников.
//
// Причина форка: upstream-биндинг не прокидывает strip для heifsave/jxlsave,
// из-за чего перекодированные HEIF/JXL содержали технический EXIF-блок
// (см. adapters/processor/libvips/metadata_strip_libvips_test.go).
//
// Пакет содержит только vips/ — без *_test.go и vips/mem_tests/,
// поэтому `go test` из главного модуля не подхватывает тесты govips.
//
// ВНИМАНИЕ (ручная синхронизация при обновлении): пакет является копией
// исходников модуля 'github.com/davidbyttow/govips/v2@v2.18.0/vips'. go.mod
// ниже совпадает с оригинальным go.mod upstream (модульный путь заменён).
module github.com/davidbyttow/govips/v2

go 1.27.0

require (
	golang.org/x/image v0.45.0
	golang.org/x/net v0.58.0
)

require golang.org/x/text v0.41.0 // indirect
