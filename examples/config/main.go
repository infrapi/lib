package main

import (
	"fmt"
	"log"
	"os"

	"github.com/infrapi/lib/pkg/config"
)

func main() {
	fmt.Println("=== InfraPi Config Package Examples ===")

	// Example 1: Basic usage with NewConfig
	example1()

	// Example 2: Get individual configuration values
	example2()

	// Example 3: Get complete AppConfig struct
	example3()

	// Example 4: Using default values
	example4()

	// Example 5: Error handling
	example5()

	// Example 6: Custom default config with modified validators
	example6()
}

// Example 1: Basic usage with NewConfig
func example1() {
	fmt.Println("--- Example 1: Basic Configuration Loading ---")

	// Set the path to the .env file (optional, defaults to .env)
	if err := os.Setenv("INFRAPI_CONFIG_DOTENV_FILE", "examples/config/example.env"); err != nil {
		log.Printf("Error setting envvar: %v\n", err)
	}

	// Load configuration from .env file
	cfg, err := config.NewConfig()
	if err != nil {
		log.Printf("Error loading config: %v\n", err)
		return
	}

	fmt.Printf("Configuration loaded from: %s\n", cfg.Path)
	fmt.Printf("Number of variables loaded: %d\n\n", len(cfg.Values))
}

// Example 2: Get individual configuration values
func example2() {
	fmt.Println("--- Example 2: Getting Individual Values ---")

	if err := os.Setenv("INFRAPI_CONFIG_DOTENV_FILE", "examples/config/example.env"); err != nil {
		log.Printf("Error setting envvar: %v\n", err)
	}

	cfg, err := config.NewConfig()
	if err != nil {
		log.Printf("Error loading config: %v\n", err)
		return
	}

	// Get application name
	appName, err := cfg.GetAppName()
	if err != nil {
		log.Printf("Error getting app name: %v\n", err)
	} else {
		fmt.Printf("App Name: %s\n", appName)
	}

	// Get log level
	logLevel, err := cfg.GetAppLogLevel()
	if err != nil {
		log.Printf("Error getting log level: %v\n", err)
	} else {
		fmt.Printf("Log Level: %s\n", logLevel)
	}

	// Get listen address
	listenAddr, err := cfg.GetAppListenAddress()
	if err != nil {
		log.Printf("Error getting listen address: %v\n", err)
	} else {
		fmt.Printf("Listen Address: %s\n", listenAddr)
	}

	// Get platform
	platform, err := cfg.GetAppPlatform()
	if err != nil {
		log.Printf("Error getting platform: %v\n", err)
	} else {
		fmt.Printf("Platform: %s\n", platform)
	}

	// Get region
	region, err := cfg.GetAppRegion()
	if err != nil {
		log.Printf("Error getting region: %v\n", err)
	} else {
		fmt.Printf("Region: %s\n", region)
	}

	// Get FQDN
	fqdn, err := cfg.GetAppFqdn()
	if err != nil {
		log.Printf("Error getting FQDN: %v\n", err)
	} else {
		fmt.Printf("FQDN: %s\n", fqdn)
	}

	// Get trusted proxies (slice)
	trustedProxies, err := cfg.GetAppTrustedProxies()
	if err != nil {
		log.Printf("Error getting trusted proxies: %v\n", err)
	} else {
		fmt.Printf("Trusted Proxies: %v\n", trustedProxies)
	}

	// Get CORS settings
	corsOrigins, err := cfg.GetAppCorsAllowOrigins()
	if err != nil {
		log.Printf("Error getting CORS origins: %v\n", err)
	} else {
		fmt.Printf("CORS Allow Origins: %v\n", corsOrigins)
	}

	corsCredentials, err := cfg.GetAppCorsAllowCredentials()
	if err != nil {
		log.Printf("Error getting CORS credentials: %v\n", err)
	} else {
		fmt.Printf("CORS Allow Credentials: %v\n", corsCredentials)
	}

	corsMaxAge, err := cfg.GetAppCorsMaxAge()
	if err != nil {
		log.Printf("Error getting CORS max age: %v\n", err)
	} else {
		fmt.Printf("CORS Max Age: %d hours\n\n", corsMaxAge)
	}
}

// Example 3: Get complete AppConfig struct
func example3() {
	fmt.Println("--- Example 3: Getting Complete AppConfig ---")

	if err := os.Setenv("INFRAPI_CONFIG_DOTENV_FILE", "examples/config/example.env"); err != nil {
		log.Printf("Error setting envvar: %v\n", err)
	}

	cfg, err := config.NewConfig()
	if err != nil {
		log.Printf("Error loading config: %v\n", err)
		return
	}

	// Get the complete application configuration
	appConfig, err := cfg.GetAppConfig()
	if err != nil {
		log.Printf("Error getting app config: %v\n", err)
		return
	}

	fmt.Printf("Complete Application Configuration:\n")
	fmt.Printf("  Name: %s\n", appConfig.AppName)
	fmt.Printf("  Log Level: %s\n", appConfig.AppLogLevel)
	fmt.Printf("  Listen Type: %s\n", appConfig.AppListenType)
	fmt.Printf("  Listen Address: %s\n", appConfig.AppListenAddress)
	fmt.Printf("  Platform: %s\n", appConfig.AppPlatform)
	fmt.Printf("  Region: %s\n", appConfig.AppRegion)
	fmt.Printf("  Location: %s\n", appConfig.AppLocation)
	fmt.Printf("  FQDN: %s\n", appConfig.AppFqdn)
	fmt.Printf("  URL: %s\n", appConfig.AppUrl)
	fmt.Printf("  Trusted Proxies: %v\n", appConfig.AppTrustedProxies)
	fmt.Printf("  CORS Allow Origins: %v\n", appConfig.AppCorsAllowOrigins)
	fmt.Printf("  CORS Allow Methods: %v\n", appConfig.AppCorsAllowMethods)
	fmt.Printf("  CORS Allow Headers: %v\n", appConfig.AppCorsAllowHeaders)
	fmt.Printf("  CORS Expose Headers: %v\n", appConfig.AppCorsExposeHeaders)
	fmt.Printf("  CORS Allow Credentials: %v\n", appConfig.AppCorsAllowCredentials)
	fmt.Printf("  CORS Max Age: %d\n\n", appConfig.AppCorsMaxAge)
}

// Example 4: Using default values
func example4() {
	fmt.Println("--- Example 4: Using Default Values ---")

	if err := os.Setenv("INFRAPI_CONFIG_DOTENV_FILE", "examples/config/defaults.env"); err != nil {
		log.Printf("Error setting envvar: %v\n", err)
	}

	cfg, err := config.NewConfig()
	if err != nil {
		log.Printf("Error loading config: %v\n", err)
		return
	}

	// These values will use defaults if not set in the .env file
	logLevel, _ := cfg.GetAppLogLevel()        // defaults to "info"
	listenType, _ := cfg.GetAppListenType()    // defaults to "tcp"
	listenAddr, _ := cfg.GetAppListenAddress() // defaults to "127.0.0.1:8080"
	corsMaxAge, _ := cfg.GetAppCorsMaxAge()    // defaults to 12

	fmt.Printf("Log Level (default): %s\n", logLevel)
	fmt.Printf("Listen Type (default): %s\n", listenType)
	fmt.Printf("Listen Address (default): %s\n", listenAddr)
	fmt.Printf("CORS Max Age (default): %d\n\n", corsMaxAge)
}

// Example 5: Error handling
func example5() {
	fmt.Println("--- Example 5: Error Handling ---")

	if err := os.Setenv("INFRAPI_CONFIG_DOTENV_FILE", "examples/config/invalid.env"); err != nil {
		log.Printf("Error setting envvar: %v\n", err)
	}

	cfg, err := config.NewConfig()
	if err != nil {
		log.Printf("Expected error loading invalid config: %v\n", err)
		return
	}

	// Try to get app name (required field)
	_, err = cfg.GetAppName()
	if err != nil {
		fmt.Printf("Expected error - App name is required: %v\n", err)
	}

	// Try to get an invalid log level
	_, err = cfg.GetAppLogLevel()
	if err != nil {
		fmt.Printf("Expected error - Invalid log level: %v\n", err)
	}

	// Try to get an invalid platform
	_, err = cfg.GetAppPlatform()
	if err != nil {
		fmt.Printf("Expected error - Invalid platform: %v\n", err)
	}

	// GetAppConfig with nil Variables (will initialize automatically)
	var nilCfg *config.Variables
	if err := os.Setenv("INFRAPI_CONFIG_DOTENV_FILE", "examples/config/example.env"); err != nil {
		log.Printf("Error setting envvar: %v\n", err)
	}
	appConfig, err := nilCfg.GetAppConfig()
	if err != nil {
		fmt.Printf("Error with nil config: %v\n", err)
	} else {
		fmt.Printf("Successfully initialized config from nil, app name: %s\n", appConfig.AppName)
	}

	fmt.Println()
}

// Example 6: Custom default config with modified validators
func example6() {
	fmt.Println("--- Example 6: Custom DefaultConfig with Modified Validators ---")

	// Create a custom default configuration
	customDefaults := config.NewDefaultConfig()

	// Modify the region validator to accept "emea" and "apac" instead of specific AWS regions
	customDefaults.AppRegion.Validator = "oneof=emea apac"

	// You can also modify other validators as needed
	// For example, change platform options:
	customDefaults.AppPlatform.Validator = "oneof=development staging production"

	// Load the .env file manually
	if err := os.Setenv("INFRAPI_CONFIG_DOTENV_FILE", "examples/config/custom.env"); err != nil {
		log.Printf("Error setting envvar: %v\n", err)
	}

	// Create Variables with custom default config
	cfg, err := config.NewConfig()
	if err != nil {
		log.Printf("Error loading config: %v\n", err)
		return
	}

	// Override the default config with our custom one
	cfg.DefaultConfig = customDefaults

	fmt.Println("Custom validators applied:")
	fmt.Printf("  Region validator: %s\n", customDefaults.AppRegion.Validator)
	fmt.Printf("  Platform validator: %s\n\n", customDefaults.AppPlatform.Validator)

	// Now test with the custom validators
	region, err := cfg.GetAppRegion()
	if err != nil {
		log.Printf("Error getting region: %v\n", err)
	} else {
		fmt.Printf("Region (with custom validator): %s\n", region)
	}

	platform, err := cfg.GetAppPlatform()
	if err != nil {
		log.Printf("Error getting platform: %v\n", err)
	} else {
		fmt.Printf("Platform (with custom validator): %s\n", platform)
	}

	// Test that the custom validators work
	appName, _ := cfg.GetAppName()
	fmt.Printf("App Name: %s\n", appName)

	fmt.Println("\nDemonstrating validation with custom rules:")
	fmt.Printf("  'emea' is now valid for region (was eu-west-1, eu-west-2, au-southeast-1)\n")
	fmt.Printf("  'development' is now valid for platform (was sandbox, preprod, prod)\n")

	fmt.Println()
}
