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
		// MOS 2.6 / 2.8.x: the DTD envelope is mos (mosID, ncsID, payload) with
		// no messageID element at all, so it must never be required.
		{"2.x roCreate", Gen2x, roCreate, false},
		{"2.x keepAlive", Gen2x, keepAlive, false},

		// MOS 3.x / 4.0 make messageID mandatory (MOS 4.0 §4.1.6)...
		{"3.x roCreate", Gen3x, roCreate, true},
		{"4.0 roCreate", Gen4x, roCreate, true},

		// ...except for keepAlive (MOS 4.0 §4.1.1).
		{"3.x keepAlive", Gen3x, keepAlive, false},
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

func TestParseMessageID(t *testing.T) {
	cases := []struct {
		raw     string
		want    int32
		wantErr bool
	}{
		{"1", 1, false},
		{"437", 437, false},
		{"2147483647", 2147483647, false},
		// MOS 4.0 §6: "Numeric values may be provided in decimal or hexadecimal
		// (when preceded by "0x", or "x")."
		{"0x12D", 301, false},
		{"0X12D", 301, false},
		{"x12D", 301, false},
		{"X12D", 301, false},
		// "a value larger than or equal to 1"
		{"0", 0, true},
		{"-1", 0, true},
		{"", 0, true},
		{"msg-123", 0, true},
		{"2147483648", 0, true}, // exceeds 32-bit signed
	}

	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := ParseMessageID(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseMessageID(%q) = %d, want error", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseMessageID(%q) unexpected error: %v", tc.raw, err)
			}
			if got != tc.want {
				t.Errorf("ParseMessageID(%q) = %d, want %d", tc.raw, got, tc.want)
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
		// The case reproduced live against a real ENPS: a spec-legal MOS 2.8.4
		// roCreate with no messageID must be accepted, not refused.
		{"2.x without messageID", withoutID, Gen2x, false},
		{"4.0 with messageID", withID, Gen4x, false},
		{"4.0 without messageID", withoutID, Gen4x, true},
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

	t.Run("malformed messageID is refused even when optional", func(t *testing.T) {
		env := base
		env.MessageID = "0"
		if _, err := ValidateEnvelope(env, Gen2x, base.MosID, ""); err == nil {
			t.Fatal("messageID 0 must be refused; the spec requires >= 1")
		}
	})
}
