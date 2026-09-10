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

func TestBridgeDefaultsAreSafe(t *testing.T) {
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	// The bridge must be off by default so OpenMOS behaves exactly as before
	// unless a deployment explicitly opts in.
	if cfg.Bridge.Enabled {
		t.Error("bridge should be disabled by default")
	}
	// Its HTTP port must not collide with any MOS transport port (10540-10542, 8080).
	switch cfg.Bridge.HTTPPort {
	case 10540, 10541, 10542, 8080:
		t.Errorf("bridge HTTP port %d collides with a MOS transport port", cfg.Bridge.HTTPPort)
	}
}

func TestBridgeEnabledViaEnv(t *testing.T) {
	t.Setenv("BRIDGE_ENABLED", "true")
	t.Setenv("BRIDGE_HTTP_PORT", "9099")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Bridge.Enabled {
		t.Error("BRIDGE_ENABLED=true should enable the bridge")
	}
	if cfg.Bridge.HTTPPort != 9099 {
		t.Errorf("BRIDGE_HTTP_PORT override = %d, want 9099", cfg.Bridge.HTTPPort)
	}
}
