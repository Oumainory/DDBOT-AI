// Package runtimeconfig contains process-level defaults that must remain
// independent of the rest of the DDBOT-AI domain model.
package runtimeconfig

import (
	"errors"
	"strings"
)

const (
	// NativeHTTPListen is the safe default for a binary started directly on a
	// host. A reverse proxy can still reach it through the host loopback.
	NativeHTTPListen = "127.0.0.1:15631"

	// DockerHTTPListen is set explicitly by the container image. It allows a
	// reverse proxy in the same container network to reach the service while
	// Docker Compose controls whether the host publishes the port.
	DockerHTTPListen = "0.0.0.0:15631"
)

type RuntimeMode string

const (
	ModeNative RuntimeMode = "native"
	ModeDocker RuntimeMode = "docker"
)

func (m RuntimeMode) valid() bool {
	return m == ModeNative || m == ModeDocker
}

// HTTPListen returns an explicit address when provided, otherwise the
// default for the selected runtime mode. The application deliberately does
// not detect containers implicitly; the image or orchestrator must opt into
// Docker mode explicitly.
func HTTPListen(mode RuntimeMode, explicit string) (string, error) {
	if !mode.valid() {
		return "", errors.New("runtimeconfig: unsupported runtime mode")
	}

	if address := strings.TrimSpace(explicit); address != "" {
		return address, nil
	}

	if mode == ModeDocker {
		return DockerHTTPListen, nil
	}
	return NativeHTTPListen, nil
}
