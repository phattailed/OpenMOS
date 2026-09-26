package xml

import (
	"strings"
	"testing"
)

func TestRequiresMessageID(t *testing.T) {
	roCreate := RunningOrderInfo{}
	keepAlive := KeepAlive{}

	cases := []struct {
		name    string
		gen     Generation
		payload MOSMessage
		want    bool
	}{
		// MOS 2.8.4 defines messageID; inbound absence is a compatibility seam.
		{"2.x roCreate", Gen2x, roCreate, true},
		{"2.x keepAlive", Gen2x, keepAlive, false},

		// MOS 4.0 makes messageID mandatory (MOS 4.0 §4.1.6)...
		{"4.0 roCreate", Gen4x, roCreate, true},

		// ...except for keepAlive (MOS 4.0 §4.1.1).
		{"4.0 keepAlive", Gen4x, keepAlive, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RequiresMessageID(tc.gen, tc.payload); got != tc.want {
				t.Errorf("RequiresMessageID(%v, %T) = %v, want %v", tc.gen, tc.payload, got, tc.want)
			}
		})
	}
}

func TestValidateEnvelope_MessageIDByGeneration(t *testing.T) {
	const (
		mosID = "openmos.test.mos"
		ncsID = "ncs.test.mos"
	)

	withID := Envelope{
		MosID:     mosID,
		NcsID:     ncsID,
		MessageID: "41",
		ROCreate:  &RunningOrderInfo{},
	}
	withoutID := Envelope{
		MosID:    mosID,
		NcsID:    ncsID,
		ROCreate: &RunningOrderInfo{},
	}

	cases := []struct {
		name    string
		env     Envelope
		gen     Generation
		wantErr bool
	}{
		{"2.x with messageID", withID, Gen2x, false},
		// Accept a missing ID on TCP as an intentional inbound exception.
		{"2.x without messageID", withoutID, Gen2x, false},
		{"4.0 with messageID", withID, Gen4x, false},
		{"4.0 without messageID", withoutID, Gen4x, true},
		{"2.x zero messageID", Envelope{MosID: mosID, NcsID: ncsID, MessageID: "0", ROCreate: &RunningOrderInfo{}}, Gen2x, true},
		{"4.0 zero messageID", Envelope{MosID: mosID, NcsID: ncsID, MessageID: "0", ROCreate: &RunningOrderInfo{}}, Gen4x, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidateEnvelope(tc.env, tc.gen, mosID, ncsID)
			if tc.wantErr && err == nil {
				t.Fatal("expected an error, got none")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateEnvelope_Identity(t *testing.T) {
	base := Envelope{
		MosID:     "openmos.test.mos",
		NcsID:     "ncs.test.mos",
		MessageID: "41",
		ROCreate:  &RunningOrderInfo{},
	}

	t.Run("wrong mosID is refused", func(t *testing.T) {
		env := base
		env.MosID = "someone.else.mos"
		if _, err := ValidateEnvelope(env, Gen2x, base.MosID, ""); err == nil {
			t.Fatal("expected refusal for mismatched mosID")
		}
	})

	t.Run("wrong ncsID is refused when one is configured", func(t *testing.T) {
		env := base
		env.NcsID = "other.ncs.mos"
		if _, err := ValidateEnvelope(env, Gen2x, base.MosID, base.NcsID); err == nil {
			t.Fatal("expected refusal for mismatched ncsID")
		}
	})

	t.Run("any ncsID accepted when none configured", func(t *testing.T) {
		env := base
		env.NcsID = "anything.at.all"
		if _, err := ValidateEnvelope(env, Gen2x, base.MosID, ""); err != nil {
			t.Fatalf("empty expectation should accept any ncsID: %v", err)
		}
	})

	t.Run("missing mosID is refused", func(t *testing.T) {
		env := base
		env.MosID = ""
		_, err := ValidateEnvelope(env, Gen2x, base.MosID, "")
		if err == nil || !strings.Contains(err.Error(), "mosID") {
			t.Fatalf("want a mosID error, got %v", err)
		}
	})

	t.Run("missing ncsID is refused", func(t *testing.T) {
		env := base
		env.NcsID = ""
		_, err := ValidateEnvelope(env, Gen2x, base.MosID, "")
		if err == nil || !strings.Contains(err.Error(), "ncsID") {
			t.Fatalf("want an ncsID error, got %v", err)
		}
	})

	t.Run("peer messageID is echoed even when unusual", func(t *testing.T) {
		env := base
		env.MessageID = "x2A"
		if _, err := ValidateEnvelope(env, Gen2x, base.MosID, ""); err != nil {
			t.Fatalf("inbound messageID should be accepted: %v", err)
		}
	})
}
