package config

import "testing"

func TestDefaultServerPortIsMOSReceivePort(t *testing.T) {
	t.Setenv("SERVER_PORT", "")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Port != 10541 {
		t.Fatalf("default server port = %d, want 10541", cfg.Server.Port)
	}
}
