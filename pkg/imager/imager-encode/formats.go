package imagerencode

import "strings"

// список форматов
func (f formatList) List() []string {
	return local.list
}

// список форматов
func FormatsList() []string {
	return local.list
}

// получаем формат из указателя
func (f formatList) Name(format format) string {
	value, is := f[format]
	if is {
		return value
	}
	return ""
}

// получаем формат из указателя
func (format format) Name() string {
	return formats.Name(format)
}

// получаем указатель формата
func getFormat(format string) (res format, is bool) {
	format = strings.ToLower(format)
	res, is = local.lists[format]
	return
}

func FormatName(f uint8) string {
	return format(f).Name()
}
