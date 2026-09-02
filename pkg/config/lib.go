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

type Variables struct {
	Values        map[string]string `json:"variables"`
	Path          string            `json:"path"`
	DefaultConfig *DefaultConfig    `json:"-"`
}

type VariableOpts struct {
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

// GetString retrieves a string value from environment variables
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
