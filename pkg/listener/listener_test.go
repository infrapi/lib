package listener

import (
	"net"
	"os"
	"strconv"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/infrapi/lib/pkg/config"
)

const testEnvContent = `
INFRAPI_APP_NAME=test-listener
INFRAPI_APP_PLATFORM=sandbox
INFRAPI_APP_REGION=eu-west-1
INFRAPI_APP_LOCATION=dc1
INFRAPI_APP_FQDN=test.example.com
INFRAPI_APP_URL=http://test.example.com
INFRAPI_APP_LISTEN_TYPE=tcp
INFRAPI_APP_LISTEN_ADDRESS=127.0.0.1:8080
`

func testAppConfig(t *testing.T) *config.AppConfig {
	t.Helper()

	f, err := os.CreateTemp(t.TempDir(), "*.env")
	require.NoError(t, err)
	_, err = f.WriteString(testEnvContent)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	t.Setenv("INFRAPI_CONFIG_DOTENV_FILE", f.Name())

	cfg, err := config.NewConfig()
	require.NoError(t, err)
	appConfig, err := cfg.GetAppConfig()
	require.NoError(t, err)
	return appConfig
}

// dupFD hands over a descriptor the caller no longer owns, the way systemd
// passes its socket: listenSystemd closes what it is given.
func dupFD(t *testing.T, f *os.File) uintptr {
	t.Helper()

	fd, err := syscall.Dup(int(f.Fd()))
	require.NoError(t, err)
	return uintptr(fd)
}

func TestListen_tcp(t *testing.T) {
	cfg := testAppConfig(t)
	// the hostname_port validator rejects port 0, so bind an ephemeral port here
	cfg.AppListenAddress = "127.0.0.1:0"

	ln, err := Listen(cfg)
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	assert.Equal(t, "tcp", ln.Addr().Network())
}

func TestListen_tcp_badAddress(t *testing.T) {
	cfg := testAppConfig(t)
	cfg.AppListenAddress = "127.0.0.1:99999"

	_, err := Listen(cfg)
	assert.Error(t, err)
}

func TestListen_badType(t *testing.T) {
	cfg := testAppConfig(t)
	cfg.AppListenType = "udp"

	_, err := Listen(cfg)
	assert.ErrorContains(t, err, "systemd or tcp expected")
}

func TestListen_systemd_wrongPID(t *testing.T) {
	cfg := testAppConfig(t)
	cfg.AppListenType = "systemd"
	t.Setenv("LISTEN_PID", "1")

	_, err := Listen(cfg)
	assert.ErrorContains(t, err, "LISTEN_PID")
}

func TestListen_systemd_fdsNotANumber(t *testing.T) {
	cfg := testAppConfig(t)
	cfg.AppListenType = "systemd"
	t.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()))
	t.Setenv("LISTEN_FDS", "many")

	_, err := Listen(cfg)
	assert.ErrorContains(t, err, "LISTEN_FDS is not a number")
}

func TestListen_systemd_noFDS(t *testing.T) {
	cfg := testAppConfig(t)
	cfg.AppListenType = "systemd"
	t.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()))
	t.Setenv("LISTEN_FDS", "0")

	_, err := Listen(cfg)
	assert.ErrorContains(t, err, "LISTEN_FDS equal 0")
}

// The activated socket is handed to listenSystemd directly: descriptor 3 of a
// test binary is its own log file, and adopting it would break the test run.
func TestListenSystemd_activatedSocket(t *testing.T) {
	t.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()))
	t.Setenv("LISTEN_FDS", "1")

	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = tcp.Close() }()

	f, err := tcp.(*net.TCPListener).File()
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	ln, err := listenSystemd(dupFD(t, f), "test-listener")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()

	assert.Equal(t, tcp.Addr().String(), ln.Addr().String())

	conn, err := net.Dial("tcp", ln.Addr().String())
	require.NoError(t, err)
	require.NoError(t, conn.Close())
}

func TestListenSystemd_notASocket(t *testing.T) {
	t.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()))
	t.Setenv("LISTEN_FDS", "1")

	f, err := os.CreateTemp(t.TempDir(), "not-a-socket")
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	_, err = listenSystemd(dupFD(t, f), "test-listener")
	assert.Error(t, err)
}
