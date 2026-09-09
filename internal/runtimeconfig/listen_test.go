package runtimeconfig

import "testing"

func TestHTTPListenDefaultsByRuntimeMode(t *testing.T) {
	tests := []struct {
		name string
		mode RuntimeMode
		want string
	}{
		{name: "native", mode: ModeNative, want: NativeHTTPListen},
		{name: "docker", mode: ModeDocker, want: DockerHTTPListen},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := HTTPListen(tt.mode, "")
			if err != nil {
				t.Fatalf("HTTPListen() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("HTTPListen() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHTTPListenExplicitAddressWins(t *testing.T) {
	got, err := HTTPListen(ModeNative, "  10.0.0.8:15631  ")
	if err != nil {
		t.Fatalf("HTTPListen() error = %v", err)
	}
	if got != "10.0.0.8:15631" {
		t.Fatalf("HTTPListen() = %q, want explicit address", got)
	}
}

func TestHTTPListenRejectsUnknownMode(t *testing.T) {
	if _, err := HTTPListen(RuntimeMode("container-auto-detect"), ""); err == nil {
		t.Fatal("HTTPListen() accepted an unknown runtime mode")
	}
}
