package config

import (
	log "github.com/sirupsen/logrus"
)

func (v *Variables) GetAppName() (string, error) {
	return v.GetString(v.DefaultConfig.AppName)
}

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

func (v *Variables) GetAppListenType() (string, error) {
	return v.GetString(v.DefaultConfig.AppListenType)
}

func (v *Variables) GetAppListenAddress() (string, error) {
	return v.GetString(v.DefaultConfig.AppListenAddress)
}

func (v *Variables) GetAppPlatform() (string, error) {
	return v.GetString(v.DefaultConfig.AppPlatform)
}

func (v *Variables) GetAppRegion() (string, error) {
	return v.GetString(v.DefaultConfig.AppRegion)
}

func (v *Variables) GetAppFqdn() (string, error) {
	return v.GetString(v.DefaultConfig.AppFqdn)
}

func (v *Variables) GetAppLocation() (string, error) {
	return v.GetString(v.DefaultConfig.AppLocation)
}

func (v *Variables) GetAppUrl() (string, error) {
	return v.GetString(v.DefaultConfig.AppUrl)
}

func (v *Variables) GetAppTrustedProxies() ([]string, error) {
	return v.GetSliceString(v.DefaultConfig.AppTrustedProxies)
}

func (v *Variables) GetAppCorsAllowOrigins() ([]string, error) {
	return v.GetSliceString(v.DefaultConfig.AppCorsAllowOrigins)
}

func (v *Variables) GetAppCorsAllowMethods() ([]string, error) {
	return v.GetSliceString(v.DefaultConfig.AppCorsAllowMethods)
}

func (v *Variables) GetAppCorsAllowHeaders() ([]string, error) {
	return v.GetSliceString(v.DefaultConfig.AppCorsAllowHeaders)
}

func (v *Variables) GetAppCorsExposeHeaders() ([]string, error) {
	return v.GetSliceString(v.DefaultConfig.AppCorsExposeHeaders)

}

func (v *Variables) GetAppCorsAllowCredentials() (bool, error) {
	return v.GetBool(v.DefaultConfig.AppCorsAllowCredentials)
}

func (v *Variables) GetAppCorsMaxAge() (int64, error) {
	return v.GetInt64(v.DefaultConfig.AppCorsMaxAge)
}

type AppConfig struct {
	AppLogLevel             log.Level
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
