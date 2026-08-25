package xml

import (
	"strings"
	"testing"
)

func TestParseEnvelope_Basic(t *testing.T) {
	input := `<mos><mosID>MOS_SERVER</mosID><ncsID>NCS_001</ncsID><messageID>msg-123</messageID><keepAlive/></mos>`

	env, msg, _, err := ParseEnvelope([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if env.MosID != "MOS_SERVER" {
		t.Errorf("mosID = %q, want %q", env.MosID, "MOS_SERVER")
	}
	if env.NcsID != "NCS_001" {
		t.Errorf("ncsID = %q, want %q", env.NcsID, "NCS_001")
	}
	if env.MessageID != "msg-123" {
		t.Errorf("messageID = %q, want %q", env.MessageID, "msg-123")
	}
	if msg == nil {
		t.Fatal("expected a message, got nil")
	}
	if msg.GetMessageType() != "keepAlive" {
		t.Errorf("message type = %q, want %q", msg.GetMessageType(), "keepAlive")
	}
}

func TestParseEnvelope_ReqMachInfo(t *testing.T) {
	input := `<mos><mosID>MOS_SERVER</mosID><ncsID>NCS_001</ncsID><messageID>msg-456</messageID><reqMachInfo/></mos>`

	env, msg, _, err := ParseEnvelope([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if env.MessageID != "msg-456" {
		t.Errorf("messageID = %q, want %q", env.MessageID, "msg-456")
	}
	if msg == nil {
		t.Fatal("expected a message, got nil")
	}
	if msg.GetMessageType() != "reqMachInfo" {
		t.Errorf("message type = %q, want %q", msg.GetMessageType(), "reqMachInfo")
	}
}

func TestParseEnvelope_RoCreate(t *testing.T) {
	input := `<mos>
  <mosID>MOS_SERVER</mosID>
  <ncsID>NCS_001</ncsID>
  <messageID>msg-789</messageID>
  <roCreate>
    <roID>RO_001</roID>
    <roSlug>Evening News</roSlug>
    <roChannel>A</roChannel>
    <story>
      <storyID>STORY_001</storyID>
      <storySlug>Lead Story</storySlug>
    </story>
  </roCreate>
</mos>`

	env, msg, innerOpXML, err := ParseEnvelope([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if env.MosID != "MOS_SERVER" {
		t.Errorf("mosID = %q, want %q", env.MosID, "MOS_SERVER")
	}
	if msg == nil {
		t.Fatal("expected a message, got nil")
	}
	roCreate, ok := msg.(RunningOrderInfo)
	if !ok {
		t.Fatalf("expected RunningOrderInfo, got %T", msg)
	}
	if roCreate.ID != "RO_001" {
		t.Errorf("roID = %q, want %q", roCreate.ID, "RO_001")
	}
	if roCreate.Slug != "Evening News" {
		t.Errorf("roSlug = %q, want %q", roCreate.Slug, "Evening News")
	}
	if len(roCreate.Stories) != 1 {
		t.Fatalf("expected 1 story, got %d", len(roCreate.Stories))
	}
	if roCreate.Stories[0].ID != "STORY_001" {
		t.Errorf("storyID = %q, want %q", roCreate.Stories[0].ID, "STORY_001")
	}

	// Verify innerOpXML contains the operation element, not envelope metadata
	if innerOpXML == nil {
		t.Fatal("expected non-nil innerOpXML")
	}
	s := string(innerOpXML)
	if !strings.Contains(s, "<roCreate>") {
		t.Errorf("innerOpXML should contain <roCreate>, got: %s", s)
	}
	if strings.Contains(s, "<mosID>") {
		t.Errorf("innerOpXML should NOT contain <mosID>, got: %s", s)
	}
	if strings.Contains(s, "<messageID>") {
		t.Errorf("innerOpXML should NOT contain <messageID>, got: %s", s)
	}
}

func TestParseEnvelope_MissingMosID(t *testing.T) {
	input := `<mos><ncsID>NCS</ncsID><messageID>1</messageID><keepAlive/></mos>`
	_, _, _, err := ParseEnvelope([]byte(input))
	if err == nil {
		t.Fatal("expected error for missing mosID")
	}
	if !strings.Contains(err.Error(), "mosID") {
		t.Errorf("error should mention mosID: %v", err)
	}
}

func TestParseEnvelope_MissingNcsID(t *testing.T) {
	input := `<mos><mosID>MOS</mosID><messageID>1</messageID><keepAlive/></mos>`
	_, _, _, err := ParseEnvelope([]byte(input))
	if err == nil {
		t.Fatal("expected error for missing ncsID")
	}
	if !strings.Contains(err.Error(), "ncsID") {
		t.Errorf("error should mention ncsID: %v", err)
	}
}

func TestParseEnvelope_MissingMessageID(t *testing.T) {
	// A message that expects a reply must carry a messageID.
	input := `<mos><mosID>MOS</mosID><ncsID>NCS</ncsID><reqMachInfo/></mos>`
	_, _, _, err := ParseEnvelope([]byte(input))
	if err == nil {
		t.Fatal("expected error for missing messageID")
	}
	if !strings.Contains(err.Error(), "messageID") {
		t.Errorf("error should mention messageID: %v", err)
	}
}

// keepAlive is exempt from messageID in every generation. MOS 4.0 §4.1.1:
// "Since a reply is not required and therefore not sequenced, the messageID
// field is not required for this message." The spec's own keepAlive example
// carries no messageID.
func TestParseEnvelope_KeepAliveWithoutMessageID(t *testing.T) {
	input := `<mos><mosID>MOS</mosID><ncsID>NCS</ncsID><keepAlive/></mos>`
	env, msg, _, err := ParseEnvelope([]byte(input))
	if err != nil {
		t.Fatalf("keepAlive without messageID must be accepted: %v", err)
	}
	if env.MessageID != "" {
		t.Errorf("messageID = %q, want empty", env.MessageID)
	}
	if _, ok := msg.(KeepAlive); !ok {
		t.Errorf("payload = %T, want KeepAlive", msg)
	}
}

func TestParseEnvelope_KeepAliveWithMessageIDStillAccepted(t *testing.T) {
	input := `<mos><mosID>MOS</mosID><ncsID>NCS</ncsID><messageID>7</messageID><keepAlive/></mos>`
	if _, _, _, err := ParseEnvelope([]byte(input)); err != nil {
		t.Fatalf("keepAlive with messageID must also be accepted: %v", err)
	}
}

func TestParseEnvelope_MalformedXML(t *testing.T) {
	input := `<mos><mosID>MOS</ncsID>`
	_, _, _, err := ParseEnvelope([]byte(input))
	if err == nil {
		t.Fatal("expected error for malformed XML")
	}
}

func TestWrapEnvelope_Basic(t *testing.T) {
	inner := []byte(`<keepAlive/>`)
	result := WrapEnvelope("MOS_SERVER", "NCS_001", "msg-001", inner)

	s := string(result)
	if !strings.Contains(s, "<mos>") {
		t.Error("result should contain <mos>")
	}
	if !strings.Contains(s, "<mosID>MOS_SERVER</mosID>") {
		t.Error("result should contain mosID")
	}
	if !strings.Contains(s, "<ncsID>NCS_001</ncsID>") {
		t.Error("result should contain ncsID")
	}
	if !strings.Contains(s, "<messageID>msg-001</messageID>") {
		t.Error("result should contain messageID")
	}
	if !strings.Contains(s, "<keepAlive/>") {
		t.Error("result should contain the inner XML")
	}
	if !strings.HasSuffix(s, "</mos>") {
		t.Error("result should end with </mos>")
	}
}

func TestWrapEnvelope_EscapesSpecialChars(t *testing.T) {
	result := WrapEnvelope("A&B", "<NCS>", "msg>1", []byte(`<keepAlive/>`))
	s := string(result)
	if !strings.Contains(s, "<mosID>A&amp;B</mosID>") {
		t.Errorf("mosID should be escaped, got: %s", s)
	}
	if !strings.Contains(s, "<ncsID>&lt;NCS&gt;</ncsID>") {
		t.Errorf("ncsID should be escaped, got: %s", s)
	}
	if !strings.Contains(s, "<messageID>msg&gt;1</messageID>") {
		t.Errorf("messageID should be escaped, got: %s", s)
	}
}

func TestParseEnvelope_EmptyBody(t *testing.T) {
	// An envelope with no operation body is valid
	input := `<mos><mosID>MOS</mosID><ncsID>NCS</ncsID><messageID>1</messageID></mos>`
	env, msg, innerOpXML, err := ParseEnvelope([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if env.MosID != "MOS" {
		t.Errorf("mosID = %q, want %q", env.MosID, "MOS")
	}
	if msg != nil {
		t.Errorf("expected nil message for empty body, got %T", msg)
	}
	if innerOpXML != nil {
		t.Errorf("expected nil innerOpXML for empty body, got %v", innerOpXML)
	}
}
