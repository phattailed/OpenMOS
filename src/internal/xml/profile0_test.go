package xml

import (
	"strings"
	"testing"
)

// Profile 0 is mandatory: "Vendors wishing to claim MOS compatibility must fully
// support, at a minimum, Profile 0 and at least one other Profile." (MOS 4.0 §2)
//
// Before this, xml.Envelope carried only running-order payloads, so none of these
// messages could be parsed at all and the handlers for them were unreachable.
func TestEnvelopeCarriesProfile0Messages(t *testing.T) {
	cases := []struct {
		name  string
		input string
		check func(t *testing.T, msg MOSMessage)
	}{
		{
			name:  "heartbeat",
			input: `<mos><mosID>M</mosID><ncsID>N</ncsID><messageID>1</messageID><heartbeat><time>2026-08-25T00:00:00</time></heartbeat></mos>`,
			check: func(t *testing.T, msg MOSMessage) {
				hb, ok := msg.(Heartbeat)
				if !ok {
					t.Fatalf("got %T, want Heartbeat", msg)
				}
				if hb.Time != "2026-08-25T00:00:00" {
					t.Errorf("time = %q, want the parsed value", hb.Time)
				}
			},
		},
		{
			name:  "reqMachInfo",
			input: `<mos><mosID>M</mosID><ncsID>N</ncsID><messageID>2</messageID><reqMachInfo/></mos>`,
			check: func(t *testing.T, msg MOSMessage) {
				if _, ok := msg.(ReqMachInfo); !ok {
					t.Fatalf("got %T, want ReqMachInfo", msg)
				}
			},
		},
		{
			name:  "keepAlive",
			input: `<mos><mosID>M</mosID><ncsID>N</ncsID><keepAlive/></mos>`,
			check: func(t *testing.T, msg MOSMessage) {
				if _, ok := msg.(KeepAlive); !ok {
					t.Fatalf("got %T, want KeepAlive", msg)
				}
			},
		},
		{
			name:  "listMachInfo",
			input: `<mos><mosID>M</mosID><ncsID>N</ncsID><messageID>3</messageID><listMachInfo><manufacturer>Acme</manufacturer><model>X1</model><mosRev>2.8.4</mosRev></listMachInfo></mos>`,
			check: func(t *testing.T, msg MOSMessage) {
				info, ok := msg.(ListMachInfo)
				if !ok {
					t.Fatalf("got %T, want ListMachInfo", msg)
				}
				if info.Manufacturer != "Acme" || info.MosRev != "2.8.4" {
					t.Errorf("unexpected content: %+v", info)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, err := ParseMessage(tc.input)
			if err != nil {
				t.Fatalf("parse failed: %v", err)
			}
			env, ok := msg.(Envelope)
			if !ok {
				t.Fatalf("got %T, want Envelope", msg)
			}
			inner, err := env.Message()
			if err != nil {
				t.Fatalf("envelope carried no recognised payload: %v", err)
			}
			tc.check(t, inner)
		})
	}
}

// Replies must be wrapped in the <mos> envelope. "Each MOS message begins with
// the root tag ("mos"), followed by the MOS and NCS ID."
func TestGenerateEnvelopeWrapsProfile0Replies(t *testing.T) {
	cases := []struct {
		name    string
		message MOSMessage
		want    string
	}{
		{"heartbeat", CreateHeartbeatResponse("mos.test", ""), "<heartbeat"},
		{"listMachInfo", ListMachInfo{Manufacturer: "Acme"}, "<listMachInfo>"},
		{"keepAlive", KeepAlive{}, "<keepAlive>"},
		{"roAck", ROAck{ID: "RO-1", Status: "OK"}, "<roAck>"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := GenerateEnvelope("mos.test", "ncs.test", "7", tc.message)
			if err != nil {
				t.Fatalf("GenerateEnvelope failed: %v", err)
			}
			out := string(data)
			for _, want := range []string{"<mos>", "<mosID>mos.test</mosID>", "<ncsID>ncs.test</ncsID>", "<messageID>7</messageID>", tc.want} {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q:\n%s", want, out)
				}
			}
		})
	}
}

// A heartbeat must carry a time element: <!ELEMENT heartbeat (time)>.
func TestHeartbeatCarriesTimeElement(t *testing.T) {
	for name, hb := range map[string]Heartbeat{
		"request":  CreateHeartbeat("mos.test", ""),
		"response": CreateHeartbeatResponse("mos.test", ""),
	} {
		if hb.Time == "" {
			t.Errorf("%s heartbeat has an empty time element", name)
		}
	}
}
