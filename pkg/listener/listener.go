// Package listener opens the socket a service accepts connections on, either a
// plain TCP socket or the one systemd hands over through socket activation.
package listener

import (
	"fmt"
	"net"
	"os"
	"strconv"

	"github.com/infrapi/lib/pkg/config"
)

// systemdListenFD is SD_LISTEN_FDS_START: systemd passes the first socket it
// opened for the service on file descriptor 3.
const systemdListenFD = 3

// Listen opens the listener described by cfg.AppListenType: a TCP socket bound
// to cfg.AppListenAddress, or the socket systemd handed over. Any other type is
// an error.
func Listen(cfg *config.AppConfig) (net.Listener, error) {
	switch cfg.AppListenType {
	case "tcp":
		return net.Listen(cfg.AppListenType, cfg.AppListenAddress)
	case "systemd":
		return listenSystemd(systemdListenFD, cfg.AppName)
	default:
		return nil, fmt.Errorf("bad value %q for listen type, systemd or tcp expected", cfg.AppListenType)
	}
}

// listenSystemd adopts the socket activation descriptor fd, named name in the
// process file table. Doing it by hand rather than through a systemd library
// keeps the dependency list short.
// https://www.freedesktop.org/software/systemd/man/sd_listen_fds.html
func listenSystemd(fd uintptr, name string) (net.Listener, error) {
	if os.Getenv("LISTEN_PID") != strconv.Itoa(os.Getpid()) {
		return nil, fmt.Errorf("systemd socket used but LISTEN_PID doesn't match current PID")
	}

	fds, err := strconv.Atoi(os.Getenv("LISTEN_FDS"))
	if err != nil {
		return nil, fmt.Errorf("LISTEN_FDS is not a number: %w", err)
	}

	if fds == 0 {
		return nil, fmt.Errorf("LISTEN_FDS equal 0 and shouldn't, systemd problem")
	}

	// net.FileListener duplicates the descriptor, so the one systemd passed is
	// ours to close on both paths.
	f := os.NewFile(fd, name)
	defer func() { _ = f.Close() }()

	return net.FileListener(f)
}
