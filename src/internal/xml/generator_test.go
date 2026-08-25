package xml

import (
	"testing"

	"airshift/openmos/internal/config"
)

// listMachInfo must not claim a profile that is not implemented. Only Profile 0
// is, and it must be claimed, since "Vendors wishing to claim MOS compatibility
// must fully support, at a minimum, Profile 0 and at least one other Profile."
// (MOS 4.0 §2) -- so an honest advertisement today is Profile 0 alone.
func TestMachineInfoAdvertisesOnlyImplementedProfiles(t *testing.T) {
	info := CreateListMachInfo(&config.Config{}, MosRev28)

	for _, profile := range info.SupportedProfiles.Profiles {
		want := profile.Number == 0
		if profile.Value != want {
			t.Errorf("profile %d advertised as %v, want %v", profile.Number, profile.Value, want)
		}
	}

	if info.SupportedProfiles.DeviceType != "MOS" {
		t.Errorf("deviceType = %q, want MOS", info.SupportedProfiles.DeviceType)
	}
}

// The advertised revision belongs to the transport answering the request, not to
// the process: the raw TCP transport speaks the 2.x family and the WebSocket
// transport speaks 4.0.
func TestMachineInfoRevisionFollowsTheTransport(t *testing.T) {
	cases := map[string]string{
		MosRev28: "2.8.4",
		MosRev40: "4.0.0",
	}
	for rev, want := range cases {
		info := CreateListMachInfo(&config.Config{}, rev)
		if info.MosRev != want {
			t.Errorf("CreateListMachInfo(_, %q) mosRev = %q, want %q", rev, info.MosRev, want)
		}
	}
}
