package setting

import imagerdecode "github.com/pkg-ru/imager/pkg/imager/imager-decode"

type SettingTsl struct {
	CertFile string `yaml:"cert-file"` // Путь до сертификата
	KeyFile  string `yaml:"key-file"`  // Путь до ключа
}

type SettingNotFound struct {
	Redirect string `yaml:"redirect"` // Перенаправлять с кодом 303
	Page     string `yaml:"page"`     // Выводить страницу с кодом 404
	Image    string `yaml:"image"`    // Выводить картинку с кодом 404
	Pixel    bool   `yaml:"pixel"`    // Генерить прозрачный пиксель с кодом 404
}

type SettingCache struct {
	CacheControl string `yaml:"cache-control"` // Директива ответа кэша
	MaxAge       int    `yaml:"max-age"`       // Время жизни ресурса в секундах.
	SMaxage      int    `yaml:"s-maxage"`      // Немного похоже на предыдущую, однако s здесь означает shared cache, и нужна для CDN. Эта директива имеет преимущество над max-age, когда речь идёт о CDN-серверах.
	LastModified bool   `yaml:"last-modified"` // Генерировать заголовок  Last-Modified
	ETag         bool   `yaml:"etag"`          // Генерировать заголовок  ETag
}

type SettingPaths struct {
	Source string `yaml:"source"` // Путь где храняться исходники
	Result string `yaml:"result"` // Путь куда сораняем результат
}

type SettingAccessControll struct {
	AllowOrigin  string `yaml:"allow-origin"`  // Access-Controll-Allow-Origin
	AllowHeaders string `yaml:"allow-headers"` // Access-Controll-Allow-Headers
	MaxAge       int    `yaml:"max-age"`       // Access-Controll-Max-Age
}

type Setting struct {
	NotFound       SettingNotFound                      `yaml:"not-found"`       // Действие при отсутствии исходника
	AccessControll SettingAccessControll                `yaml:"access-controll"` // Access-Controll setting
	Cache          SettingCache                         `yaml:"cache"`           // Настройки заголовков кеша
	Tls            SettingTsl                           `yaml:"tls"`             // Настройка сертификатов
	Paths          SettingPaths                         `yaml:"paths"`           // Пути до исходников и результата
	Http           string                               `yaml:"http"`            // Не безопастное подключение
	Unix           string                               `yaml:"unix"`            // Доменный сокет
	Https          string                               `yaml:"https"`           // Безопастное подключение (необходима настройка сертификатов)
	LogServer      string                               `yaml:"log-server"`      // Лог сервера
	LogRequest     string                               `yaml:"log-request"`     // Лог запросов
	Thumbs         map[string]imagerdecode.ThumbSetting `yaml:"thumbs"`          // Настройки ресемплинга
	Development    bool                                 `yaml:"development"`     // Режим разработки
}
