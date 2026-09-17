package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSourceCatalogueConfiguration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_FILE", path)
	t.Setenv("SOURCE_CATALOGUE_STATE_DIR", "/synthetic/catalogue")
	t.Setenv("SOURCE_ADDITIONAL_RUNDOWNS", `[{"rundownId":"opaque;folder\\show;id","stateDir":"/synthetic/second"}]`)
	cfg, err := LoadConfig()
	if err != nil || cfg.Source.CatalogueStateDir != "/synthetic/catalogue" || len(cfg.Source.Additional) != 1 || cfg.Source.Additional[0].RundownID != `opaque;folder\show;id` || cfg.Source.Additional[0].StateDir != "/synthetic/second" {
		t.Fatalf("additional source configuration did not preserve the supplied identity or directories: %v", err)
	}
	for _, raw := range []string{`null`, `{}`, `[] []`, `[{"rundownId":"other","unexpected":true}]`} {
		t.Setenv("SOURCE_ADDITIONAL_RUNDOWNS", raw)
		if _, err := LoadConfig(); err == nil {
			t.Fatalf("invalid source configuration accepted: %s", raw)
		}
	}
}
