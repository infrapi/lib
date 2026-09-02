// Package config loads InfraPI service configuration from a dotenv file and
// exposes it as typed, validated values.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/joho/godotenv"
)

// Variables holds the content of a dotenv file together with the rule set used
// by the INFRAPI_APP_* getters.
type Variables struct {
	Values        map[string]string `json:"variables"`
	Path          string            `json:"path"`
	DefaultConfig *DefaultConfig    `json:"-"`
}

// VariableOpts describes how a single dotenv key is read and validated.
type VariableOpts struct {
	// Dotenv key to read
	Key string

	// Default value in string format
	Default string

	// go-playground validator rule
	Validator string

	// Hide from error and log messages
	Sensible bool
}

func getDotEnv() string {
	if v := os.Getenv("INFRAPI_CONFIG_DOTENV_FILE"); v != "" {
		return v
	}
	return ".env"
}

// NewConfig reads the dotenv file named by INFRAPI_CONFIG_DOTENV_FILE, or .env
// when that variable is unset. The process environment is not used as a
// fallback for the values themselves.
func NewConfig() (*Variables, error) {
	// load .env file
	path := getDotEnv()
	values, err := godotenv.Read(path)
	if err != nil {
		return nil, fmt.Errorf("unable to load dotenv located at %s: %w", path, err)
	}

	return &Variables{
		Values:        values,
		Path:          path,
		DefaultConfig: NewDefaultConfig(),
	}, nil
}

// GetString returns the value of opts.Key. An absent or empty key falls back to
// opts.Default, and is an error when no default is set. Only values coming from
// the dotenv file are checked against opts.Validator; defaults are trusted.
func (v *Variables) GetString(opts *VariableOpts) (string, error) {
	if opts == nil {
		return "", fmt.Errorf("opts cannot be nil")
	}

	value, ok := v.Values[opts.Key]
	if !ok || value == "" {
		if opts.Default == "" {
			return "", fmt.Errorf("key %s value is empty and opts.Default not provided, dotenv located at %s", opts.Key, v.Path)
		}
		return opts.Default, nil
	}

	vld := validator.New()

	if opts != nil && opts.Validator != "" {
		if err := vld.Var(value, opts.Validator); err != nil {
			if opts.Sensible {
				return "", fmt.Errorf("key %s value is not compliant with validator rule '%s', value [masked], dotenv located at %s", opts.Key, opts.Validator, v.Path)
			}
			return "", fmt.Errorf("key %s value is not compliant with validator rule '%s', value '%s', dotenv located at %s", opts.Key, opts.Validator, value, v.Path)
		}
	}

	return value, nil
}

// GetBool returns the value of opts.Key parsed with strconv.ParseBool.
func (v *Variables) GetBool(opts *VariableOpts) (bool, error) {
	value, err := v.GetString(opts)
	if err != nil {
		return false, err
	}
	r, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("key %s value is not parsable as bool, dotenv located at %s", opts.Key, v.Path)
	}
	return r, nil
}

// GetInt returns the value of opts.Key parsed with strconv.Atoi.
func (v *Variables) GetInt(opts *VariableOpts) (int, error) {
	value, err := v.GetString(opts)
	if err != nil {
		return 0, err
	}
	r, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("key %s value is not parsable as int, dotenv located at %s", opts.Key, v.Path)
	}
	return r, nil
}

// GetInt64 returns the value of opts.Key parsed as a base 10 64 bits integer.
func (v *Variables) GetInt64(opts *VariableOpts) (int64, error) {
	value, err := v.GetString(opts)
	if err != nil {
		return 0, err
	}
	r, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("key %s value is not parsable as int64, dotenv located at %s", opts.Key, v.Path)
	}
	return r, nil
}

// GetSliceString returns the value of opts.Key decoded as a JSON array of
// strings, so the dotenv value must be written as ["a", "b"].
func (v *Variables) GetSliceString(opts *VariableOpts) ([]string, error) {
	r := []string{}
	value, err := v.GetString(opts)
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal([]byte(value), &r); err != nil {
		return nil, fmt.Errorf("key %s value is not parsable as string slice, dotenv located at %s", opts.Key, v.Path)
	}
	return r, nil
}

// GetDuration returns the value of opts.Key parsed with time.ParseDuration.
func (v *Variables) GetDuration(opts *VariableOpts) (time.Duration, error) {
	value, err := v.GetString(opts)
	if err != nil {
		return 0, err
	}
	r, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("key %s value is not parsable as time.Duration, dotenv located at %s", opts.Key, v.Path)
	}
	return r, nil
}
