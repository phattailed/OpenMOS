package xml

import (
	"testing"

	"airshift/openmos/internal/config"
)

func TestMachineInfoDoesNotClaimACompleteProfile(t *testing.T) {
	info := CreateListMachInfo(&config.Config{})
	if info.MosRev != "2.8.4" {
		t.Fatalf("mosRev = %q, want 2.8.4", info.MosRev)
	}
	for _, profile := range info.SupportedProfiles.Profiles {
		if profile.Value {
			t.Fatalf("incomplete profile %d was advertised as supported", profile.Number)
		}
	}
}
