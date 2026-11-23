// INFRAPI_APP_* getters, the configuration every InfraPI service shares.

package config

import (
	"fmt"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// GetAppName returns INFRAPI_APP_NAME.
func (v *Variables) GetAppName() (string, error) {
	return v.GetString(v.DefaultConfig.AppName)
}

// GetAppLogLevel returns INFRAPI_APP_LOG_LEVEL parsed as a logrus level.
// On error the returned level is logrus.InfoLevel.
func (v *Variables) GetAppLogLevel() (log.Level, error) {
	val, err := v.GetString(v.DefaultConfig.AppLogLevel)
	if err != nil {
		return log.InfoLevel, err
	}
	level, err := log.ParseLevel(val)
	if err != nil {
		return log.InfoLevel, err
	}
	return level, nil
}

// GetAppLogFormat returns INFRAPI_APP_LOG_FORMAT, either text or json.
func (v *Variables) GetAppLogFormat() (string, error) {
	return v.GetString(v.DefaultConfig.AppLogFormat)
}

// GetAppListenType returns INFRAPI_APP_LISTEN_TYPE, either tcp or systemd.
func (v *Variables) GetAppListenType() (string, error) {
	return v.GetString(v.DefaultConfig.AppListenType)
}

// GetAppListenAddress returns INFRAPI_APP_LISTEN_ADDRESS as host:port.
func (v *Variables) GetAppListenAddress() (string, error) {
	return v.GetString(v.DefaultConfig.AppListenAddress)
}

// GetAppPlatform returns INFRAPI_APP_PLATFORM.
func (v *Variables) GetAppPlatform() (string, error) {
	return v.GetString(v.DefaultConfig.AppPlatform)
}

// GetAppRegion returns INFRAPI_APP_REGION.
func (v *Variables) GetAppRegion() (string, error) {
	return v.GetString(v.DefaultConfig.AppRegion)
}

// GetAppFqdn returns INFRAPI_APP_FQDN, defaulting to the host name.
func (v *Variables) GetAppFqdn() (string, error) {
	return v.GetString(v.DefaultConfig.AppFqdn)
}

// GetAppLocation returns INFRAPI_APP_LOCATION.
func (v *Variables) GetAppLocation() (string, error) {
	return v.GetString(v.DefaultConfig.AppLocation)
}

// GetAppUrl returns INFRAPI_APP_URL.
func (v *Variables) GetAppUrl() (string, error) {
	return v.GetString(v.DefaultConfig.AppUrl)
}

// GetAppTrustedProxies returns INFRAPI_APP_TRUSTED_PROXIES.
func (v *Variables) GetAppTrustedProxies() ([]string, error) {
	return v.GetSliceString(v.DefaultConfig.AppTrustedProxies)
}

// GetAppCorsAllowOrigins returns INFRAPI_APP_CORS_ALLOW_ORIGINS.
func (v *Variables) GetAppCorsAllowOrigins() ([]string, error) {
	return v.GetSliceString(v.DefaultConfig.AppCorsAllowOrigins)
}

// GetAppCorsAllowMethods returns INFRAPI_APP_CORS_ALLOW_METHODS.
func (v *Variables) GetAppCorsAllowMethods() ([]string, error) {
	return v.GetSliceString(v.DefaultConfig.AppCorsAllowMethods)
}

// GetAppCorsAllowHeaders returns INFRAPI_APP_CORS_ALLOW_HEADERS.
func (v *Variables) GetAppCorsAllowHeaders() ([]string, error) {
	return v.GetSliceString(v.DefaultConfig.AppCorsAllowHeaders)
}

// GetAppCorsExposeHeaders returns INFRAPI_APP_CORS_EXPOSE_HEADERS.
func (v *Variables) GetAppCorsExposeHeaders() ([]string, error) {
	return v.GetSliceString(v.DefaultConfig.AppCorsExposeHeaders)

}

// GetAppCorsAllowCredentials returns INFRAPI_APP_CORS_ALLOW_CREDENTIALS.
func (v *Variables) GetAppCorsAllowCredentials() (bool, error) {
	return v.GetBool(v.DefaultConfig.AppCorsAllowCredentials)
}

// GetAppCorsMaxAge returns INFRAPI_APP_CORS_MAX_AGE, a number of hours.
func (v *Variables) GetAppCorsMaxAge() (int64, error) {
	return v.GetInt64(v.DefaultConfig.AppCorsMaxAge)
}

// AppConfig is the resolved INFRAPI_APP_* configuration of a service.
type AppConfig struct {
	AppLogLevel             log.Level
	AppLogFormat            string
	AppListenType           string
	AppListenAddress        string
	AppPlatform             string
	AppRegion               string
	AppFqdn                 string
	AppLocation             string
	AppUrl                  string
	AppName                 string
	AppTrustedProxies       []string
	AppCorsAllowOrigins     []string
	AppCorsAllowMethods     []string
	AppCorsAllowHeaders     []string
	AppCorsExposeHeaders    []string
	AppCorsAllowCredentials bool
	AppCorsMaxAge           int64
}

// GetAppConfig resolves every INFRAPI_APP_* key and returns them as a single
// struct, stopping on the first key that is missing or fails its validator.
// It may be called on a nil *Variables, in which case NewConfig is used to load
// the dotenv file first.
func (v *Variables) GetAppConfig() (*AppConfig, error) {
	if v == nil {
		var err error
		v, err = NewConfig()
		if err != nil {
			return nil, err
		}
	}

	appName, err := v.GetAppName()
	if err != nil {
		return nil, err
	}

	appPlatform, err := v.GetAppPlatform()
	if err != nil {
		return nil, err
	}

	appRegion, err := v.GetAppRegion()
	if err != nil {
		return nil, err
	}

	appFqdn, err := v.GetAppFqdn()
	if err != nil {
		return nil, err
	}

	appLocation, err := v.GetAppLocation()
	if err != nil {
		return nil, err
	}

	appUrl, err := v.GetAppUrl()
	if err != nil {
		return nil, err
	}

	appLogLevel, err := v.GetAppLogLevel()
	if err != nil {
		return nil, err
	}

	appLogFormat, err := v.GetAppLogFormat()
	if err != nil {
		return nil, err
	}

	appListenType, err := v.GetAppListenType()
	if err != nil {
		return nil, err
	}

	appListenAddress, err := v.GetAppListenAddress()
	if err != nil {
		return nil, err
	}

	appTrustedProxies, err := v.GetAppTrustedProxies()
	if err != nil {
		return nil, err
	}

	appCorsAllowOrigins, err := v.GetAppCorsAllowOrigins()
	if err != nil {
		return nil, err
	}

	appCorsAllowMethods, err := v.GetAppCorsAllowMethods()
	if err != nil {
		return nil, err
	}

	appCorsAllowHeaders, err := v.GetAppCorsAllowHeaders()
	if err != nil {
		return nil, err
	}

	appCorsExposeHeaders, err := v.GetAppCorsExposeHeaders()
	if err != nil {
		return nil, err
	}

	appCorsAllowCredentials, err := v.GetAppCorsAllowCredentials()
	if err != nil {
		return nil, err
	}

	appCorsMaxAge, err := v.GetAppCorsMaxAge()
	if err != nil {
		return nil, err
	}

	return &AppConfig{
		AppName:                 appName,
		AppPlatform:             appPlatform,
		AppRegion:               appRegion,
		AppFqdn:                 appFqdn,
		AppLocation:             appLocation,
		AppUrl:                  appUrl,
		AppLogLevel:             appLogLevel,
		AppLogFormat:            appLogFormat,
		AppListenType:           appListenType,
		AppListenAddress:        appListenAddress,
		AppTrustedProxies:       appTrustedProxies,
		AppCorsAllowOrigins:     appCorsAllowOrigins,
		AppCorsAllowMethods:     appCorsAllowMethods,
		AppCorsAllowHeaders:     appCorsAllowHeaders,
		AppCorsExposeHeaders:    appCorsExposeHeaders,
		AppCorsAllowCredentials: appCorsAllowCredentials,
		AppCorsMaxAge:           appCorsMaxAge,
	}, nil
}

// Identity is the service's own identity at Kayra (pkg/identity), both
// optional: INFRAPI_IDENTITY_KEY_PATH, its keys' PEM files separated by
// commas, the first signing, and INFRAPI_IDENTITY_TOKEN_PATH, a platform's
// identity token instead (a projected Kubernetes service account token).
type Identity struct {
	KeyPaths  []string
	TokenPath string
}

// GetIdentity returns INFRAPI_IDENTITY_KEY_PATH and
// INFRAPI_IDENTITY_TOKEN_PATH. Neither set, the service has no identity.
func (v *Variables) GetIdentity() (Identity, error) {
	keys, err := v.GetString(&VariableOpts{Key: "INFRAPI_IDENTITY_KEY_PATH", Optional: true})
	if err != nil {
		return Identity{}, err
	}
	token, err := v.GetString(&VariableOpts{Key: "INFRAPI_IDENTITY_TOKEN_PATH", Optional: true})
	if err != nil {
		return Identity{}, err
	}
	id := Identity{TokenPath: token}
	for p := range strings.SplitSeq(keys, ",") {
		if p = strings.TrimSpace(p); p != "" {
			id.KeyPaths = append(id.KeyPaths, p)
		}
	}
	if len(id.KeyPaths) > 0 && id.TokenPath != "" {
		return Identity{}, fmt.Errorf("INFRAPI_IDENTITY_KEY_PATH and INFRAPI_IDENTITY_TOKEN_PATH are both set: the service proves its identity one way, dotenv located at %s", v.Path)
	}
	return id, nil
}

// Events is how the service records its events (pkg/events),
// INFRAPI_EVENTS_*: Enabled sends them to Kayra (the log otherwise), the
// rest tunes the recorder, its defaults when zero.
type Events struct {
	Enabled      bool
	Queue        int
	Batch        int
	SeriesWindow time.Duration
	SpamBurst    int
	SpamRefill   time.Duration
}

// GetEvents returns INFRAPI_EVENTS_ENABLED (false), INFRAPI_EVENTS_QUEUE
// (1000), INFRAPI_EVENTS_BATCH (100), INFRAPI_EVENTS_SERIES_WINDOW (10m),
// INFRAPI_EVENTS_SPAM_BURST (25) and INFRAPI_EVENTS_SPAM_REFILL (5m).
func (v *Variables) GetEvents() (Events, error) {
	var e Events
	var err error
	if e.Enabled, err = v.GetBool(&VariableOpts{Key: "INFRAPI_EVENTS_ENABLED", Default: "false"}); err != nil {
		return e, err
	}
	for _, n := range []struct {
		key, def string
		into     *int
	}{{"INFRAPI_EVENTS_QUEUE", "1000", &e.Queue}, {"INFRAPI_EVENTS_BATCH", "100", &e.Batch}, {"INFRAPI_EVENTS_SPAM_BURST", "25", &e.SpamBurst}} {
		if *n.into, err = v.GetInt(&VariableOpts{Key: n.key, Default: n.def, Validator: "min=1"}); err != nil {
			return e, err
		}
	}
	if e.SeriesWindow, err = v.GetDuration(&VariableOpts{Key: "INFRAPI_EVENTS_SERIES_WINDOW", Default: "10m"}); err != nil {
		return e, err
	}
	if e.SpamRefill, err = v.GetDuration(&VariableOpts{Key: "INFRAPI_EVENTS_SPAM_REFILL", Default: "5m"}); err != nil {
		return e, err
	}
	return e, nil
}
