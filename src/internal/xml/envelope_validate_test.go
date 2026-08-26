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

	t.Run("malformed messageID is accepted inbound", func(t *testing.T) {
		// Deliberate policy, see AcceptInboundMessageID. messageID 0 violates
		// §4.1.6, but we understood the message and the identifier is one we only
		// ever echo, so rejecting it would lose the payload for no benefit.
		env := base
		env.MessageID = "0"
		if _, err := ValidateEnvelope(env, Gen2x, base.MosID, ""); err != nil {
			t.Fatalf("malformed inbound messageID must be tolerated, got %v", err)
		}
	})

	t.Run("non-numeric messageID is accepted inbound on both generations", func(t *testing.T) {
		for _, gen := range []Generation{Gen2x, Gen4x} {
			env := base
			env.MessageID = "msg-123"
			if _, err := ValidateEnvelope(env, gen, base.MosID, ""); err != nil {
				t.Fatalf("%s: non-numeric inbound messageID must be tolerated, got %v", gen, err)
			}
		}
	})

	t.Run("outbound origination stays strict", func(t *testing.T) {
		// The other half of the policy: leniency is inbound only.
		for _, bad := range []string{"0", "-1", "msg-123", "1.5", "abc"} {
			if err := ValidateOutboundMessageID(bad); err == nil {
				t.Errorf("originating messageID %q must be refused; §4.1.6 requires a 32-bit signed integer >= 1", bad)
			}
		}
		for _, good := range []string{"1", "9001", "0x1F", "2147483647"} {
			if err := ValidateOutboundMessageID(good); err != nil {
				t.Errorf("originating messageID %q must be allowed: %v", good, err)
			}
		}
		// Empty is legitimate: keepAlive carries none.
		if err := ValidateOutboundMessageID(""); err != nil {
			t.Errorf("empty messageID must be allowed for messages that carry none: %v", err)
		}
	})

	t.Run("FormatMessageID never emits below the spec floor", func(t *testing.T) {
		for _, n := range []int64{-5, 0, 1} {
			if err := ValidateOutboundMessageID(FormatMessageID(n)); err != nil {
				t.Errorf("FormatMessageID(%d) produced a spec-invalid identifier: %v", n, err)
			}
		}
		if got := FormatMessageID(42); got != "42" {
			t.Errorf("FormatMessageID(42) = %q, want \"42\"", got)
		}
	})
}
