package xml

import (
	"strings"
	"testing"
)

func TestParseEnvelope_Basic(t *testing.T) {
	input := `<mos><mosID>MOS_SERVER</mosID><ncsID>NCS_001</ncsID><messageID>msg-123</messageID><keepAlive/></mos>`

	env, msg, err := ParseEnvelope([]byte(input))
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

	env, msg, err := ParseEnvelope([]byte(input))
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

	env, msg, err := ParseEnvelope([]byte(input))
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
}

func TestParseEnvelope_MissingMosID(t *testing.T) {
	input := `<mos><ncsID>NCS</ncsID><messageID>1</messageID><keepAlive/></mos>`
	_, _, err := ParseEnvelope([]byte(input))
	if err == nil {
		t.Fatal("expected error for missing mosID")
	}
	if !strings.Contains(err.Error(), "mosID") {
		t.Errorf("error should mention mosID: %v", err)
	}
}

func TestParseEnvelope_MissingNcsID(t *testing.T) {
	input := `<mos><mosID>MOS</mosID><messageID>1</messageID><keepAlive/></mos>`
	_, _, err := ParseEnvelope([]byte(input))
	if err == nil {
		t.Fatal("expected error for missing ncsID")
	}
	if !strings.Contains(err.Error(), "ncsID") {
		t.Errorf("error should mention ncsID: %v", err)
	}
}

func TestParseEnvelope_MissingMessageID(t *testing.T) {
	input := `<mos><mosID>MOS</mosID><ncsID>NCS</ncsID><keepAlive/></mos>`
	_, _, err := ParseEnvelope([]byte(input))
	if err == nil {
		t.Fatal("expected error for missing messageID")
	}
	if !strings.Contains(err.Error(), "messageID") {
		t.Errorf("error should mention messageID: %v", err)
	}
}

func TestParseEnvelope_MalformedXML(t *testing.T) {
	input := `<mos><mosID>MOS</ncsID>`
	_, _, err := ParseEnvelope([]byte(input))
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
	env, msg, err := ParseEnvelope([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if env.MosID != "MOS" {
		t.Errorf("mosID = %q, want %q", env.MosID, "MOS")
	}
	if msg != nil {
		t.Errorf("expected nil message for empty body, got %T", msg)
	}
}
