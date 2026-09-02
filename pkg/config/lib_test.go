package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- GetString

func TestGetString_assign_dotenv(t *testing.T) {
	t.Setenv("INFRAPI_CONFIG_DOTENV_FILE", "lib_test.env")
	c, err := NewConfig()
	require.NoError(t, err)

	v, err := c.GetString(&VariableOpts{Key: "TESTING_DOTENV_SIMPLE", Default: "default"})
	require.NoError(t, err)
	assert.Equal(t, "simple", v)
}

func TestGetString_assign_from_variables(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"TESTING_ENV_VAR_SIMPLE": "simple",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	v, err := c.GetString(&VariableOpts{Key: "TESTING_ENV_VAR_SIMPLE", Default: "default"})
	require.NoError(t, err)
	assert.Equal(t, "simple", v)
}

func TestGetString_default(t *testing.T) {
	c := &Variables{
		Values:        map[string]string{},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	v, err := c.GetString(&VariableOpts{Key: "TESTING_ENV_VAR_SIMPLE", Default: "default"})
	require.NoError(t, err)
	assert.Equal(t, "default", v)
}

func TestGetString_missing_no_default(t *testing.T) {
	c := &Variables{
		Values:        map[string]string{},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	_, err := c.GetString(&VariableOpts{Key: "TESTING_ENV_VAR_MISSING"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "opts.Default not provided")
}

func TestGetString_with_validator_success(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"TESTING_EMAIL": "test@example.com",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	v, err := c.GetString(&VariableOpts{Key: "TESTING_EMAIL", Validator: "email"})
	require.NoError(t, err)
	assert.Equal(t, "test@example.com", v)
}

func TestGetString_with_validator_failure(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"TESTING_EMAIL": "invalid-email",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	_, err := c.GetString(&VariableOpts{Key: "TESTING_EMAIL", Validator: "email"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not compliant with validator rule")
}

func TestGetString_with_validator_failure_sensible(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"TESTING_PASSWORD": "short",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	_, err := c.GetString(&VariableOpts{Key: "TESTING_PASSWORD", Validator: "min=10", Sensible: true})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "[masked]")
	assert.NotContains(t, err.Error(), "short")
}

// ---- GetBool

func TestGetBool_assign_dotenv(t *testing.T) {
	t.Setenv("INFRAPI_CONFIG_DOTENV_FILE", "lib_test.env")
	c, err := NewConfig()
	require.NoError(t, err)

	v, err := c.GetBool(&VariableOpts{Key: "TESTING_DOTENV_BOOL", Default: "false"})
	require.NoError(t, err)
	assert.Equal(t, true, v)
}

func TestGetBool_assign(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"TESTING_ENV_VAR_BOOL": "true",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	v, err := c.GetBool(&VariableOpts{Key: "TESTING_ENV_VAR_BOOL", Default: "false"})
	require.NoError(t, err)
	assert.Equal(t, true, v)
}

func TestGetBool_default(t *testing.T) {
	c := &Variables{
		Values:        map[string]string{},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	v, err := c.GetBool(&VariableOpts{Key: "TESTING_ENV_VAR_BOOL", Default: "true"})
	require.NoError(t, err)
	assert.Equal(t, true, v)
}

func TestGetBool_parse_error(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"TESTING_ENV_VAR_BOOL": "invalid",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	_, err := c.GetBool(&VariableOpts{Key: "TESTING_ENV_VAR_BOOL"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not parsable as bool")
}

// ---- GetInt

func TestGetInt_assign_dotenv(t *testing.T) {
	t.Setenv("INFRAPI_CONFIG_DOTENV_FILE", "lib_test.env")
	c, err := NewConfig()
	require.NoError(t, err)

	v, err := c.GetInt(&VariableOpts{Key: "TESTING_DOTENV_INT", Default: "50"})
	require.NoError(t, err)
	assert.Equal(t, 100, v)
}

func TestGetInt_assign(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"TESTING_ENV_VAR_INT": "100",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	v, err := c.GetInt(&VariableOpts{Key: "TESTING_ENV_VAR_INT", Default: "50"})
	require.NoError(t, err)
	assert.Equal(t, 100, v)
}

func TestGetInt_default(t *testing.T) {
	c := &Variables{
		Values:        map[string]string{},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	v, err := c.GetInt(&VariableOpts{Key: "TESTING_ENV_VAR_INT", Default: "100"})
	require.NoError(t, err)
	assert.Equal(t, 100, v)
}

func TestGetInt_parse_error(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"TESTING_ENV_VAR_INT": "invalid",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	_, err := c.GetInt(&VariableOpts{Key: "TESTING_ENV_VAR_INT"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not parsable as int")
}

// ---- GetInt64

func TestGetInt64_assign_dotenv(t *testing.T) {
	t.Setenv("INFRAPI_CONFIG_DOTENV_FILE", "lib_test.env")
	c, err := NewConfig()
	require.NoError(t, err)

	v, err := c.GetInt64(&VariableOpts{Key: "TESTING_DOTENV_INT64", Default: "50"})
	require.NoError(t, err)
	assert.Equal(t, int64(100), v)
}

func TestGetInt64_assign(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"TESTING_ENV_VAR_INT64": "100",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	v, err := c.GetInt64(&VariableOpts{Key: "TESTING_ENV_VAR_INT64", Default: "50"})
	require.NoError(t, err)
	assert.Equal(t, int64(100), v)
}

func TestGetInt64_default(t *testing.T) {
	c := &Variables{
		Values:        map[string]string{},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	v, err := c.GetInt64(&VariableOpts{Key: "TESTING_ENV_VAR_INT64", Default: "100"})
	require.NoError(t, err)
	assert.Equal(t, int64(100), v)
}

func TestGetInt64_parse_error(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"TESTING_ENV_VAR_INT64": "invalid",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	_, err := c.GetInt64(&VariableOpts{Key: "TESTING_ENV_VAR_INT64"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not parsable as int64")
}

// ---- GetSliceString

func TestGetSliceString_assign_dotenv(t *testing.T) {
	t.Setenv("INFRAPI_CONFIG_DOTENV_FILE", "lib_test.env")
	c, err := NewConfig()
	require.NoError(t, err)

	v, err := c.GetSliceString(&VariableOpts{Key: "TESTING_DOTENV_SLICE", Default: "[\"default\"]"})
	require.NoError(t, err)
	assert.Equal(t, []string{"simple"}, v)
}

func TestGetSliceString_assign(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"TESTING_ENV_VAR_SLICE": "[\"simple\"]",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	v, err := c.GetSliceString(&VariableOpts{Key: "TESTING_ENV_VAR_SLICE", Default: "[\"default\"]"})
	require.NoError(t, err)
	assert.Equal(t, []string{"simple"}, v)
}

func TestGetSliceString_default(t *testing.T) {
	c := &Variables{
		Values:        map[string]string{},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	v, err := c.GetSliceString(&VariableOpts{Key: "TESTING_ENV_VAR_SLICE", Default: "[\"default\"]"})
	require.NoError(t, err)
	assert.Equal(t, []string{"default"}, v)
}

func TestGetSliceString_parse_error(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"TESTING_ENV_VAR_SLICE": "invalid",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	_, err := c.GetSliceString(&VariableOpts{Key: "TESTING_ENV_VAR_SLICE"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not parsable as string slice")
}

// ---- GetDuration

func TestGetDuration_assign_dotenv(t *testing.T) {
	t.Setenv("INFRAPI_CONFIG_DOTENV_FILE", "lib_test.env")
	c, err := NewConfig()
	require.NoError(t, err)

	v, err := c.GetDuration(&VariableOpts{Key: "TESTING_DOTENV_DURATION", Default: "5s"})
	require.NoError(t, err)
	assert.Equal(t, 15*time.Second, v)
}

func TestGetDuration_assign(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"TESTING_ENV_VAR_DURATION": "15s",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	v, err := c.GetDuration(&VariableOpts{Key: "TESTING_ENV_VAR_DURATION", Default: "5s"})
	require.NoError(t, err)
	assert.Equal(t, 15*time.Second, v)
}

func TestGetDuration_default(t *testing.T) {
	c := &Variables{
		Values:        map[string]string{},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	v, err := c.GetDuration(&VariableOpts{Key: "TESTING_ENV_VAR_DURATION", Default: "5s"})
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, v)
}

func TestGetDuration_parse_error(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"TESTING_ENV_VAR_DURATION": "invalid",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	_, err := c.GetDuration(&VariableOpts{Key: "TESTING_ENV_VAR_DURATION"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not parsable as time.Duration")
}

// ---- NewConfig

func TestNewConfig_success(t *testing.T) {
	t.Setenv("INFRAPI_CONFIG_DOTENV_FILE", "lib_test.env")
	c, err := NewConfig()
	require.NoError(t, err)
	assert.NotNil(t, c)
	assert.NotNil(t, c.Values)
	assert.Equal(t, "lib_test.env", c.Path)
}

func TestNewConfig_file_not_found(t *testing.T) {
	t.Setenv("INFRAPI_CONFIG_DOTENV_FILE", "nonexistent.env")
	_, err := NewConfig()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unable to load dotenv")
}

func TestNewConfig_custom_dotenv_file(t *testing.T) {
	t.Setenv("INFRAPI_CONFIG_DOTENV_FILE", "lib_test.env")
	c, err := NewConfig()
	require.NoError(t, err)
	assert.NotNil(t, c)
	assert.Equal(t, "lib_test.env", c.Path)
}

// Test getDotEnv function coverage - default path
func TestGetDotEnv_default_path(t *testing.T) {
	// Don't set INFRAPI_CONFIG_DOTENV_FILE to test default ".env" path
	path := getDotEnv()
	// This will error because .env doesn't exist, but that's ok - we're testing the path
	assert.Equal(t, path, ".env")
}

// Additional GetString tests
func TestGetString_nil_opts(t *testing.T) {
	c := &Variables{
		Values:        map[string]string{},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	_, err := c.GetString(nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "opts cannot be nil")
}

func TestGetString_empty_value_with_default(t *testing.T) {
	c := &Variables{
		Values: map[string]string{
			"EMPTY_KEY": "",
		},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	v, err := c.GetString(&VariableOpts{Key: "EMPTY_KEY", Default: "default_value"})
	require.NoError(t, err)
	assert.Equal(t, "default_value", v)
}

// Additional parse error tests
func TestGetBool_missing_key_no_default(t *testing.T) {
	c := &Variables{
		Values:        map[string]string{},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	_, err := c.GetBool(&VariableOpts{Key: "MISSING_BOOL"})
	assert.Error(t, err)
}

func TestGetInt_missing_key_no_default(t *testing.T) {
	c := &Variables{
		Values:        map[string]string{},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	_, err := c.GetInt(&VariableOpts{Key: "MISSING_INT"})
	assert.Error(t, err)
}

func TestGetInt64_missing_key_no_default(t *testing.T) {
	c := &Variables{
		Values:        map[string]string{},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	_, err := c.GetInt64(&VariableOpts{Key: "MISSING_INT64"})
	assert.Error(t, err)
}

func TestGetSliceString_missing_key_no_default(t *testing.T) {
	c := &Variables{
		Values:        map[string]string{},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	_, err := c.GetSliceString(&VariableOpts{Key: "MISSING_SLICE"})
	assert.Error(t, err)
}

func TestGetDuration_missing_key_no_default(t *testing.T) {
	c := &Variables{
		Values:        map[string]string{},
		Path:          ".env",
		DefaultConfig: NewDefaultConfig(),
	}

	_, err := c.GetDuration(&VariableOpts{Key: "MISSING_DURATION"})
	assert.Error(t, err)
}
