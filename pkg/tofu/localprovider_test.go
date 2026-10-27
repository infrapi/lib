package tofu_test

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// localProviderVersion is a hashicorp/terraform-provider-local release: a
// small, dependency-free provider (create/read/update/delete a local file)
// that makes a realistic, real provider plugin to test tofu against,
// without needing any credentials or external API.
const localProviderVersion = "2.5.2"

// randomProviderVersion is a hashicorp/terraform-provider-random release,
// built on the plugin framework, whose resources support import: what
// local_file does not.
const randomProviderVersion = "3.4.3"

func localProviderExe(t *testing.T) string {
	t.Helper()
	return providerExe(t, "local", localProviderVersion)
}

func randomProviderExe(t *testing.T) string {
	t.Helper()
	return providerExe(t, "random", randomProviderVersion)
}

// providerExe returns the path to a cached hashicorp/terraform-provider-<name>
// binary for the current platform, downloading and caching it under the OS
// temp directory on first use. Tests that need it skip, rather than fail,
// when no cached copy exists and it cannot be downloaded: that keeps this
// package's tests real (a genuine provider plugin process, not a mock)
// without requiring network access to run them at all.
func providerExe(t *testing.T, name, version string) string {
	t.Helper()

	cacheDir := filepath.Join(os.TempDir(), "infrapi-lib-tofu-testcache")
	exe := filepath.Join(cacheDir, fmt.Sprintf("terraform-provider-%s_%s_%s_%s", name, version, runtime.GOOS, runtime.GOARCH))

	if _, err := os.Stat(exe); err == nil {
		return exe
	}

	require.NoError(t, os.MkdirAll(cacheDir, 0o755))

	url := fmt.Sprintf(
		"https://releases.hashicorp.com/terraform-provider-%[1]s/%[2]s/terraform-provider-%[1]s_%[2]s_%[3]s_%[4]s.zip",
		name, version, runtime.GOOS, runtime.GOARCH,
	)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url) // #nosec G107 -- fixed HashiCorp releases domain, not user input
	if err != nil {
		t.Skipf("terraform-provider-%s: no network access to download it: %v", name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Skipf("terraform-provider-%s: download failed with status %s", name, resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	require.NoError(t, err)

	var binName string
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "terraform-provider-"+name) {
			binName = f.Name
			break
		}
	}
	require.NotEmptyf(t, binName, "terraform-provider-%s zip has no provider binary: %v", name, zr.File)

	rc, err := zr.Open(binName)
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()

	tmp := exe + fmt.Sprintf(".tmp-%d", os.Getpid())
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	require.NoError(t, err)
	_, copyErr := io.Copy(out, rc) //nolint:gosec // fixed-size official release archive, not user input
	require.NoError(t, out.Close())
	require.NoError(t, copyErr)
	require.NoError(t, os.Rename(tmp, exe))

	return exe
}
