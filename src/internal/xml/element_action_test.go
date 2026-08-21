package xml

import (
	"encoding/xml"
	"fmt"
	"testing"
)

func TestROElementActionUnmarshal(t *testing.T) {
	// Test INSERT with stories
	insertXML := `<roElementAction operation="INSERT">
		<roID>RO123</roID>
		<element_target>
			<storyID>STORY1</storyID>
		</element_target>
		<element_source>
			<story>
				<storyID>STORY_NEW</storyID>
				<storySlug>New Story</storySlug>
			</story>
		</element_source>
	</roElementAction>`

	var action ROElementAction
	err := xml.Unmarshal([]byte(insertXML), &action)
	if err != nil {
		t.Fatalf("INSERT unmarshal error: %v", err)
	}
	if action.Operation != "INSERT" {
		t.Errorf("expected operation INSERT, got %s", action.Operation)
	}
	if action.ROID != "RO123" {
		t.Errorf("expected roID RO123, got %s", action.ROID)
	}
	if action.Target == nil || action.Target.StoryID != "STORY1" {
		t.Errorf("expected target storyID STORY1")
	}
	if len(action.Source.Stories) != 1 {
		t.Errorf("expected 1 story in source, got %d", len(action.Source.Stories))
	}
	if action.Source.GetSourceType() != "story" {
		t.Errorf("expected source type story, got %s", action.Source.GetSourceType())
	}

	// Test DELETE with storyIDs
	deleteXML := `<roElementAction operation="DELETE">
		<roID>RO123</roID>
		<element_source>
			<storyID>STORY1</storyID>
			<storyID>STORY2</storyID>
		</element_source>
	</roElementAction>`

	var deleteAction ROElementAction
	err = xml.Unmarshal([]byte(deleteXML), &deleteAction)
	if err != nil {
		t.Fatalf("DELETE unmarshal error: %v", err)
	}
	if deleteAction.Operation != "DELETE" {
		t.Errorf("expected operation DELETE, got %s", deleteAction.Operation)
	}
	if len(deleteAction.Source.StoryIDs) != 2 {
		t.Errorf("expected 2 storyIDs, got %d", len(deleteAction.Source.StoryIDs))
	}
	if deleteAction.Source.GetSourceType() != "storyID" {
		t.Errorf("expected source type storyID, got %s", deleteAction.Source.GetSourceType())
	}

	// Test SWAP
	swapXML := `<roElementAction operation="SWAP">
		<roID>RO123</roID>
		<element_source>
			<storyID>STORY_A</storyID>
			<storyID>STORY_B</storyID>
		</element_source>
	</roElementAction>`

	var swapAction ROElementAction
	err = xml.Unmarshal([]byte(swapXML), &swapAction)
	if err != nil {
		t.Fatalf("SWAP unmarshal error: %v", err)
	}
	if swapAction.Operation != "SWAP" {
		t.Errorf("expected operation SWAP, got %s", swapAction.Operation)
	}
	if len(swapAction.Source.StoryIDs) != 2 {
		t.Errorf("expected 2 storyIDs for SWAP, got %d", len(swapAction.Source.StoryIDs))
	}

	// Test MOVE with itemIDs
	moveXML := `<roElementAction operation="MOVE">
		<roID>RO123</roID>
		<element_target>
			<storyID>STORY1</storyID>
			<itemID>ITEM_TARGET</itemID>
		</element_target>
		<element_source>
			<itemID>ITEM1</itemID>
			<itemID>ITEM2</itemID>
		</element_source>
	</roElementAction>`

	var moveAction ROElementAction
	err = xml.Unmarshal([]byte(moveXML), &moveAction)
	if err != nil {
		t.Fatalf("MOVE unmarshal error: %v", err)
	}
	if moveAction.Operation != "MOVE" {
		t.Errorf("expected operation MOVE, got %s", moveAction.Operation)
	}
	if moveAction.Target.ItemID != "ITEM_TARGET" {
		t.Errorf("expected target itemID ITEM_TARGET, got %s", moveAction.Target.ItemID)
	}
	if len(moveAction.Source.ItemIDs) != 2 {
		t.Errorf("expected 2 itemIDs, got %d", len(moveAction.Source.ItemIDs))
	}
	if moveAction.Source.GetSourceType() != "itemID" {
		t.Errorf("expected source type itemID, got %s", moveAction.Source.GetSourceType())
	}

	// Test REPLACE with items
	replaceXML := `<roElementAction operation="REPLACE">
		<roID>RO123</roID>
		<element_target>
			<storyID>STORY1</storyID>
			<itemID>ITEM_OLD</itemID>
		</element_target>
		<element_source>
			<item>
				<itemID>ITEM_NEW</itemID>
				<itemSlug>New Item</itemSlug>
				<objID>OBJ1</objID>
			</item>
		</element_source>
	</roElementAction>`

	var replaceAction ROElementAction
	err = xml.Unmarshal([]byte(replaceXML), &replaceAction)
	if err != nil {
		t.Fatalf("REPLACE unmarshal error: %v", err)
	}
	if replaceAction.Operation != "REPLACE" {
		t.Errorf("expected operation REPLACE, got %s", replaceAction.Operation)
	}
	if len(replaceAction.Source.Items) != 1 {
		t.Errorf("expected 1 item in source, got %d", len(replaceAction.Source.Items))
	}
	if replaceAction.Source.GetSourceType() != "item" {
		t.Errorf("expected source type item, got %s", replaceAction.Source.GetSourceType())
	}

	fmt.Println("All ROElementAction unmarshal tests passed!")
}

func TestROReadyToAirUnmarshal(t *testing.T) {
	readyXML := `<roReadyToAir><roID>RO123</roID><roAir>READY</roAir></roReadyToAir>`
	var ready ROReadyToAir
	err := xml.Unmarshal([]byte(readyXML), &ready)
	if err != nil {
		t.Fatalf("READY unmarshal error: %v", err)
	}
	if ready.ROID != "RO123" {
		t.Errorf("expected roID RO123, got %s", ready.ROID)
	}
	if ready.ROAir != "READY" {
		t.Errorf("expected roAir READY, got %s", ready.ROAir)
	}
	if ready.GetMessageType() != "roReadyToAir" {
		t.Errorf("expected message type roReadyToAir, got %s", ready.GetMessageType())
	}
}

func TestROElementStatUnmarshal(t *testing.T) {
	statXML := `<roElementStat><roID>RO123</roID><storyID>STORY1</storyID><itemID>ITEM1</itemID><objID>OBJ1</objID><itemChannel>CH1</itemChannel><status>PLAY</status><time>2024-01-01T00:00:00Z</time></roElementStat>`
	var stat ROElementStat
	err := xml.Unmarshal([]byte(statXML), &stat)
	if err != nil {
		t.Fatalf("STAT unmarshal error: %v", err)
	}
	if stat.ROID != "RO123" {
		t.Errorf("expected roID RO123, got %s", stat.ROID)
	}
	if stat.StoryID != "STORY1" {
		t.Errorf("expected storyID STORY1, got %s", stat.StoryID)
	}
	if stat.ItemID != "ITEM1" {
		t.Errorf("expected itemID ITEM1, got %s", stat.ItemID)
	}
	if stat.ObjID != "OBJ1" {
		t.Errorf("expected objID OBJ1, got %s", stat.ObjID)
	}
	if stat.ItemChannel != "CH1" {
		t.Errorf("expected itemChannel CH1, got %s", stat.ItemChannel)
	}
	if stat.Status != "PLAY" {
		t.Errorf("expected status PLAY, got %s", stat.Status)
	}
	if stat.Time != "2024-01-01T00:00:00Z" {
		t.Errorf("expected time 2024-01-01T00:00:00Z, got %s", stat.Time)
	}
	if stat.GetMessageType() != "roElementStat" {
		t.Errorf("expected message type roElementStat, got %s", stat.GetMessageType())
	}
}

func TestParserRecognizesProfile4Messages(t *testing.T) {
	tests := []struct {
		name        string
		xml         string
		expectedType string
	}{
		{
			name: "roElementAction INSERT",
			xml: `<roElementAction operation="INSERT"><roID>RO1</roID><element_source><storyID>S1</storyID></element_source></roElementAction>`,
			expectedType: "roElementAction",
		},
		{
			name: "roElementAction REPLACE",
			xml: `<roElementAction operation="REPLACE"><roID>RO1</roID><element_target><storyID>S1</storyID></element_target><element_source><story><storyID>S2</storyID><storySlug>Replacement</storySlug></story></element_source></roElementAction>`,
			expectedType: "roElementAction",
		},
		{
			name: "roElementAction MOVE",
			xml: `<roElementAction operation="MOVE"><roID>RO1</roID><element_target><storyID>S1</storyID></element_target><element_source><storyID>S2</storyID></element_source></roElementAction>`,
			expectedType: "roElementAction",
		},
		{
			name: "roElementAction DELETE",
			xml: `<roElementAction operation="DELETE"><roID>RO1</roID><element_source><storyID>S1</storyID></element_source></roElementAction>`,
			expectedType: "roElementAction",
		},
		{
			name: "roElementAction SWAP",
			xml: `<roElementAction operation="SWAP"><roID>RO1</roID><element_source><storyID>S1</storyID><storyID>S2</storyID></element_source></roElementAction>`,
			expectedType: "roElementAction",
		},
		{
			name: "roReadyToAir",
			xml: `<roReadyToAir><roID>RO1</roID><roAir>READY</roAir></roReadyToAir>`,
			expectedType: "roReadyToAir",
		},
		{
			name: "roElementStat",
			xml: `<roElementStat><roID>RO1</roID><itemID>ITEM1</itemID><status>PLAY</status><time>2024-01-01T00:00:00Z</time></roElementStat>`,
			expectedType: "roElementStat",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			parser := NewMessageParser()
			parser.AppendData([]byte(tc.xml))
			msg, _, err := parser.Parse()
			if err != nil {
				t.Fatalf("Parser error: %v", err)
			}
			if msg.GetMessageType() != tc.expectedType {
				t.Errorf("expected message type %s, got %s", tc.expectedType, msg.GetMessageType())
			}
		})
	}
}
