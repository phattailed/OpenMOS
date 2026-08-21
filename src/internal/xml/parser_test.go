package xml

import (
	"testing"
)

// TestParserAllProfiles verifies that the parser correctly recognizes and parses
// representative messages from all MOS profiles (0-7)
func TestParserAllProfiles(t *testing.T) {
	tests := []struct {
		name         string
		xml          string
		expectedType string
		validate     func(t *testing.T, msg MOSMessage)
	}{
		// Profile 0: Basic Communication
		{
			name:         "Profile 0 - keepAlive",
			xml:          `<keepAlive/>`,
			expectedType: "keepAlive",
			validate: func(t *testing.T, msg MOSMessage) {
				_, ok := msg.(KeepAlive)
				if !ok {
					t.Error("expected KeepAlive type")
				}
			},
		},
		{
			name:         "Profile 0 - reqMachInfo",
			xml:          `<reqMachInfo/>`,
			expectedType: "reqMachInfo",
			validate: func(t *testing.T, msg MOSMessage) {
				_, ok := msg.(ReqMachInfo)
				if !ok {
					t.Error("expected ReqMachInfo type")
				}
			},
		},
		{
			name: "Profile 0 - listMachInfo",
			xml: `<listMachInfo>
				<manufacturer>OpenMOS</manufacturer>
				<model>Server</model>
				<hwRev>1.0</hwRev>
				<swRev>4.0.0</swRev>
				<DOM>2024-01-01</DOM>
				<SN>SN001</SN>
				<ID>mos.server.com</ID>
				<time>2024-01-01T00:00:00Z</time>
				<mosRev>4.0.0</mosRev>
				<supportedProfiles deviceType="MOS">
					<mosProfile number="0">true</mosProfile>
					<mosProfile number="1">true</mosProfile>
				</supportedProfiles>
			</listMachInfo>`,
			expectedType: "listMachInfo",
			validate: func(t *testing.T, msg MOSMessage) {
				lmi, ok := msg.(ListMachInfo)
				if !ok {
					t.Fatal("expected ListMachInfo type")
				}
				if lmi.Manufacturer != "OpenMOS" {
					t.Errorf("expected manufacturer OpenMOS, got %s", lmi.Manufacturer)
				}
				if lmi.MosRev != "4.0.0" {
					t.Errorf("expected mosRev 4.0.0, got %s", lmi.MosRev)
				}
				if lmi.SupportedProfiles.DeviceType != "MOS" {
					t.Errorf("expected deviceType MOS, got %s", lmi.SupportedProfiles.DeviceType)
				}
				if len(lmi.SupportedProfiles.Profiles) != 2 {
					t.Errorf("expected 2 profiles, got %d", len(lmi.SupportedProfiles.Profiles))
				}
			},
		},
		{
			name:         "Profile 0 - heartbeat",
			xml:          `<heartbeat timestamp="2024-01-01T12:00:00Z" source="mos.server.com"/>`,
			expectedType: "heartbeat",
			validate: func(t *testing.T, msg MOSMessage) {
				hb, ok := msg.(Heartbeat)
				if !ok {
					t.Fatal("expected Heartbeat type")
				}
				if hb.Source != "mos.server.com" {
					t.Errorf("expected source mos.server.com, got %s", hb.Source)
				}
			},
		},

		// Profile 1: Basic Object Based Workflow
		{
			name: "Profile 1 - mosObj",
			xml: `<mosObj>
				<objID>OBJ001</objID>
				<objSlug>Test Object</objSlug>
				<mosAbstract>A test MOS object</mosAbstract>
				<objType>VIDEO</objType>
				<objTB>50</objTB>
				<objDur>300</objDur>
				<status>READY</status>
				<createdBy>admin</createdBy>
				<created>2024-01-01T00:00:00Z</created>
			</mosObj>`,
			expectedType: "mosObj",
			validate: func(t *testing.T, msg MOSMessage) {
				obj, ok := msg.(MosObj)
				if !ok {
					t.Fatal("expected MosObj type")
				}
				if obj.ObjID != "OBJ001" {
					t.Errorf("expected objID OBJ001, got %s", obj.ObjID)
				}
				if obj.ObjSlug != "Test Object" {
					t.Errorf("expected objSlug 'Test Object', got %s", obj.ObjSlug)
				}
				if obj.ObjType != "VIDEO" {
					t.Errorf("expected objType VIDEO, got %s", obj.ObjType)
				}
				if obj.ObjTB != 50 {
					t.Errorf("expected objTB 50, got %d", obj.ObjTB)
				}
				if obj.ObjDur != 300 {
					t.Errorf("expected objDur 300, got %d", obj.ObjDur)
				}
			},
		},
		{
			name:         "Profile 1 - mosReqObj",
			xml:          `<mosReqObj><objID>OBJ001</objID></mosReqObj>`,
			expectedType: "mosReqObj",
			validate: func(t *testing.T, msg MOSMessage) {
				req, ok := msg.(MosReqObj)
				if !ok {
					t.Fatal("expected MosReqObj type")
				}
				if req.ObjID != "OBJ001" {
					t.Errorf("expected objID OBJ001, got %s", req.ObjID)
				}
			},
		},

		// Profile 2: Basic Running Order Workflow
		{
			name: "Profile 2 - roCreate",
			xml: `<roCreate>
				<roID>RO001</roID>
				<roSlug>Evening News</roSlug>
				<roChannel>CH1</roChannel>
				<story>
					<storyID>STORY001</storyID>
					<storySlug>Top Story</storySlug>
					<item>
						<itemID>ITEM001</itemID>
						<itemSlug>Lead Package</itemSlug>
						<objID>OBJ001</objID>
					</item>
				</story>
			</roCreate>`,
			expectedType: "roCreate",
			validate: func(t *testing.T, msg MOSMessage) {
				ro, ok := msg.(RunningOrderInfo)
				if !ok {
					t.Fatal("expected RunningOrderInfo type")
				}
				if ro.ID != "RO001" {
					t.Errorf("expected roID RO001, got %s", ro.ID)
				}
				if ro.Slug != "Evening News" {
					t.Errorf("expected roSlug 'Evening News', got %s", ro.Slug)
				}
				if len(ro.Stories) != 1 {
					t.Fatalf("expected 1 story, got %d", len(ro.Stories))
				}
				if ro.Stories[0].ID != "STORY001" {
					t.Errorf("expected storyID STORY001, got %s", ro.Stories[0].ID)
				}
				if len(ro.Stories[0].Items) != 1 {
					t.Fatalf("expected 1 item, got %d", len(ro.Stories[0].Items))
				}
				if ro.Stories[0].Items[0].ID != "ITEM001" {
					t.Errorf("expected itemID ITEM001, got %s", ro.Stories[0].Items[0].ID)
				}
			},
		},
		{
			name:         "Profile 2 - roDelete",
			xml:          `<roDelete><roID>RO001</roID></roDelete>`,
			expectedType: "roDelete",
			validate: func(t *testing.T, msg MOSMessage) {
				del, ok := msg.(RODelete)
				if !ok {
					t.Fatal("expected RODelete type")
				}
				if del.ID != "RO001" {
					t.Errorf("expected roID RO001, got %s", del.ID)
				}
			},
		},

		// Profile 3: Advanced Object Based Workflow
		{
			name: "Profile 3 - mosObjCreate",
			xml: `<mosObjCreate>
				<objSlug>New Object</objSlug>
				<objType>AUDIO</objType>
				<objTB>48000</objTB>
				<objDur>120</objDur>
				<createdBy>producer</createdBy>
			</mosObjCreate>`,
			expectedType: "mosObjCreate",
			validate: func(t *testing.T, msg MOSMessage) {
				obj, ok := msg.(MosObjCreate)
				if !ok {
					t.Fatal("expected MosObjCreate type")
				}
				if obj.ObjSlug != "New Object" {
					t.Errorf("expected objSlug 'New Object', got %s", obj.ObjSlug)
				}
				if obj.ObjType != "AUDIO" {
					t.Errorf("expected objType AUDIO, got %s", obj.ObjType)
				}
				if obj.ObjTB != 48000 {
					t.Errorf("expected objTB 48000, got %d", obj.ObjTB)
				}
			},
		},

		// Profile 4: Advanced RO/Content List Workflow
		{
			name: "Profile 4 - roElementAction INSERT",
			xml: `<roElementAction operation="INSERT">
				<roID>RO001</roID>
				<element_target>
					<storyID>STORY001</storyID>
				</element_target>
				<element_source>
					<story>
						<storyID>STORY_NEW</storyID>
						<storySlug>Inserted Story</storySlug>
					</story>
				</element_source>
			</roElementAction>`,
			expectedType: "roElementAction",
			validate: func(t *testing.T, msg MOSMessage) {
				action, ok := msg.(ROElementAction)
				if !ok {
					t.Fatal("expected ROElementAction type")
				}
				if action.Operation != "INSERT" {
					t.Errorf("expected operation INSERT, got %s", action.Operation)
				}
				if action.ROID != "RO001" {
					t.Errorf("expected roID RO001, got %s", action.ROID)
				}
				if action.Target == nil || action.Target.StoryID != "STORY001" {
					t.Error("expected target storyID STORY001")
				}
				if len(action.Source.Stories) != 1 {
					t.Fatalf("expected 1 story in source, got %d", len(action.Source.Stories))
				}
				if action.Source.Stories[0].ID != "STORY_NEW" {
					t.Errorf("expected source storyID STORY_NEW, got %s", action.Source.Stories[0].ID)
				}
			},
		},
		{
			name:         "Profile 4 - roReadyToAir",
			xml:          `<roReadyToAir><roID>RO001</roID><roAir>READY</roAir></roReadyToAir>`,
			expectedType: "roReadyToAir",
			validate: func(t *testing.T, msg MOSMessage) {
				rta, ok := msg.(ROReadyToAir)
				if !ok {
					t.Fatal("expected ROReadyToAir type")
				}
				if rta.ROID != "RO001" {
					t.Errorf("expected roID RO001, got %s", rta.ROID)
				}
				if rta.ROAir != "READY" {
					t.Errorf("expected roAir READY, got %s", rta.ROAir)
				}
			},
		},

		// Profile 5: Item Control
		{
			name: "Profile 5 - roCtrl EXECUTE",
			xml: `<roCtrl>
				<roID>RO001</roID>
				<storyID>STORY001</storyID>
				<itemID>ITEM001</itemID>
				<command>EXECUTE</command>
			</roCtrl>`,
			expectedType: "roCtrl",
			validate: func(t *testing.T, msg MOSMessage) {
				ctrl, ok := msg.(ROCtrl)
				if !ok {
					t.Fatal("expected ROCtrl type")
				}
				if ctrl.ROID != "RO001" {
					t.Errorf("expected roID RO001, got %s", ctrl.ROID)
				}
				if ctrl.StoryID != "STORY001" {
					t.Errorf("expected storyID STORY001, got %s", ctrl.StoryID)
				}
				if ctrl.ItemID != "ITEM001" {
					t.Errorf("expected itemID ITEM001, got %s", ctrl.ItemID)
				}
				if ctrl.Command != "EXECUTE" {
					t.Errorf("expected command EXECUTE, got %s", ctrl.Command)
				}
			},
		},
		{
			name: "Profile 5 - roCtrl with mosExternalMetadata",
			xml: `<roCtrl>
				<roID>RO002</roID>
				<storyID>STORY002</storyID>
				<itemID>ITEM002</itemID>
				<command>READY</command>
				<mosExternalMetadata>
					<mosSchema>http://example.com/schema</mosSchema>
					<mosPayload>payload data</mosPayload>
				</mosExternalMetadata>
			</roCtrl>`,
			expectedType: "roCtrl",
			validate: func(t *testing.T, msg MOSMessage) {
				ctrl, ok := msg.(ROCtrl)
				if !ok {
					t.Fatal("expected ROCtrl type")
				}
				if ctrl.Command != "READY" {
					t.Errorf("expected command READY, got %s", ctrl.Command)
				}
				if len(ctrl.MosExternalMetadata) != 1 {
					t.Fatalf("expected 1 mosExternalMetadata, got %d", len(ctrl.MosExternalMetadata))
				}
				if ctrl.MosExternalMetadata[0].MosSchema != "http://example.com/schema" {
					t.Errorf("expected mosSchema 'http://example.com/schema', got %s", ctrl.MosExternalMetadata[0].MosSchema)
				}
			},
		},
		{
			name: "Profile 5 - roItemCue",
			xml: `<roItemCue>
				<mosID>mos.server.com</mosID>
				<roID>RO001</roID>
				<storyID>STORY001</storyID>
				<itemID>ITEM001</itemID>
				<roEventType>CUE</roEventType>
				<roEventTime>2024-01-15T10:30:00Z</roEventTime>
			</roItemCue>`,
			expectedType: "roItemCue",
			validate: func(t *testing.T, msg MOSMessage) {
				cue, ok := msg.(ROItemCue)
				if !ok {
					t.Fatal("expected ROItemCue type")
				}
				if cue.MosID != "mos.server.com" {
					t.Errorf("expected mosID mos.server.com, got %s", cue.MosID)
				}
				if cue.ROID != "RO001" {
					t.Errorf("expected roID RO001, got %s", cue.ROID)
				}
				if cue.StoryID != "STORY001" {
					t.Errorf("expected storyID STORY001, got %s", cue.StoryID)
				}
				if cue.ItemID != "ITEM001" {
					t.Errorf("expected itemID ITEM001, got %s", cue.ItemID)
				}
				if cue.ROEventType != "CUE" {
					t.Errorf("expected roEventType CUE, got %s", cue.ROEventType)
				}
				if cue.ROEventTime != "2024-01-15T10:30:00Z" {
					t.Errorf("expected roEventTime 2024-01-15T10:30:00Z, got %s", cue.ROEventTime)
				}
			},
		},

		// Profile 6: MOS Redirection / Story Send
		{
			name: "Profile 6 - roStorySend standalone",
			xml: `<roStorySend>
				<roID>RO001</roID>
				<storyID>STORY001</storyID>
				<storySlug>Breaking News</storySlug>
				<storyNum>A1</storyNum>
				<storyBody>
					<p>This is the story content.</p>
				</storyBody>
			</roStorySend>`,
			expectedType: "roStorySend",
			validate: func(t *testing.T, msg MOSMessage) {
				ss, ok := msg.(ROStorySend)
				if !ok {
					t.Fatal("expected ROStorySend type")
				}
				if ss.ROID != "RO001" {
					t.Errorf("expected roID RO001, got %s", ss.ROID)
				}
				if ss.StoryID != "STORY001" {
					t.Errorf("expected storyID STORY001, got %s", ss.StoryID)
				}
				if ss.StorySlug != "Breaking News" {
					t.Errorf("expected storySlug 'Breaking News', got %s", ss.StorySlug)
				}
				if ss.StoryNum != "A1" {
					t.Errorf("expected storyNum A1, got %s", ss.StoryNum)
				}
				if len(ss.StoryBody.Paragraphs) != 1 {
					t.Fatalf("expected 1 paragraph, got %d", len(ss.StoryBody.Paragraphs))
				}
			},
		},
		{
			name: "Profile 6 - roReqStoryAction",
			xml: `<roReqStoryAction operation="REPLACE" leaseLock="LOCK123" username="producer1">
				<roStorySend>
					<roID>RO001</roID>
					<storyID>STORY001</storyID>
					<storySlug>Updated Story</storySlug>
					<storyBody>
						<p>Updated content here.</p>
					</storyBody>
				</roStorySend>
			</roReqStoryAction>`,
			expectedType: "roReqStoryAction",
			validate: func(t *testing.T, msg MOSMessage) {
				rsa, ok := msg.(ROReqStoryAction)
				if !ok {
					t.Fatal("expected ROReqStoryAction type")
				}
				if rsa.Operation != "REPLACE" {
					t.Errorf("expected operation REPLACE, got %s", rsa.Operation)
				}
				if rsa.LeaseLock != "LOCK123" {
					t.Errorf("expected leaseLock LOCK123, got %s", rsa.LeaseLock)
				}
				if rsa.Username != "producer1" {
					t.Errorf("expected username producer1, got %s", rsa.Username)
				}
				if rsa.ROStorySend.ROID != "RO001" {
					t.Errorf("expected roStorySend.roID RO001, got %s", rsa.ROStorySend.ROID)
				}
				if rsa.ROStorySend.StoryID != "STORY001" {
					t.Errorf("expected roStorySend.storyID STORY001, got %s", rsa.ROStorySend.StoryID)
				}
				if rsa.ROStorySend.StorySlug != "Updated Story" {
					t.Errorf("expected roStorySend.storySlug 'Updated Story', got %s", rsa.ROStorySend.StorySlug)
				}
			},
		},

		// Profile 7: MOS-initiated RO Modification (same roElementAction structure, reverse direction)
		{
			name: "Profile 7 - roElementAction DELETE (MOS-initiated)",
			xml: `<roElementAction operation="DELETE">
				<roID>RO001</roID>
				<element_source>
					<storyID>STORY_OLD</storyID>
					<storyID>STORY_OBSOLETE</storyID>
				</element_source>
			</roElementAction>`,
			expectedType: "roElementAction",
			validate: func(t *testing.T, msg MOSMessage) {
				action, ok := msg.(ROElementAction)
				if !ok {
					t.Fatal("expected ROElementAction type")
				}
				if action.Operation != "DELETE" {
					t.Errorf("expected operation DELETE, got %s", action.Operation)
				}
				if action.ROID != "RO001" {
					t.Errorf("expected roID RO001, got %s", action.ROID)
				}
				if len(action.Source.StoryIDs) != 2 {
					t.Fatalf("expected 2 storyIDs in source, got %d", len(action.Source.StoryIDs))
				}
				if action.Source.StoryIDs[0] != "STORY_OLD" {
					t.Errorf("expected first storyID STORY_OLD, got %s", action.Source.StoryIDs[0])
				}
				if action.Source.StoryIDs[1] != "STORY_OBSOLETE" {
					t.Errorf("expected second storyID STORY_OBSOLETE, got %s", action.Source.StoryIDs[1])
				}
			},
		},
		{
			name: "Profile 7 - roElementAction SWAP (MOS-initiated)",
			xml: `<roElementAction operation="SWAP">
				<roID>RO001</roID>
				<element_source>
					<storyID>STORY_A</storyID>
					<storyID>STORY_B</storyID>
				</element_source>
			</roElementAction>`,
			expectedType: "roElementAction",
			validate: func(t *testing.T, msg MOSMessage) {
				action, ok := msg.(ROElementAction)
				if !ok {
					t.Fatal("expected ROElementAction type")
				}
				if action.Operation != "SWAP" {
					t.Errorf("expected operation SWAP, got %s", action.Operation)
				}
				if len(action.Source.StoryIDs) != 2 {
					t.Fatalf("expected 2 storyIDs for SWAP, got %d", len(action.Source.StoryIDs))
				}
			},
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

			if tc.validate != nil {
				tc.validate(t, msg)
			}
		})
	}
}

// TestParserROCtrlAllCommands verifies roCtrl parses all valid command values
func TestParserROCtrlAllCommands(t *testing.T) {
	commands := []string{"READY", "EXECUTE", "PAUSE", "STOP", "SIGNAL"}

	for _, cmd := range commands {
		t.Run("command_"+cmd, func(t *testing.T) {
			xmlData := `<roCtrl><roID>RO1</roID><storyID>S1</storyID><itemID>I1</itemID><command>` + cmd + `</command></roCtrl>`
			parser := NewMessageParser()
			parser.AppendData([]byte(xmlData))

			msg, _, err := parser.Parse()
			if err != nil {
				t.Fatalf("Parser error for command %s: %v", cmd, err)
			}

			ctrl, ok := msg.(ROCtrl)
			if !ok {
				t.Fatal("expected ROCtrl type")
			}
			if ctrl.Command != cmd {
				t.Errorf("expected command %s, got %s", cmd, ctrl.Command)
			}
		})
	}
}

// TestParserROItemCueWithMetadata verifies roItemCue parsing with external metadata
func TestParserROItemCueWithMetadata(t *testing.T) {
	xmlData := `<roItemCue>
		<mosID>mos.server.com</mosID>
		<roID>RO001</roID>
		<storyID>STORY001</storyID>
		<itemID>ITEM001</itemID>
		<roEventType>TAKE</roEventType>
		<roEventTime>2024-06-15T14:30:00Z</roEventTime>
		<mosExternalMetadata>
			<mosSchema>http://example.com/cue-schema</mosSchema>
			<mosPayload>cue metadata payload</mosPayload>
		</mosExternalMetadata>
	</roItemCue>`

	parser := NewMessageParser()
	parser.AppendData([]byte(xmlData))

	msg, _, err := parser.Parse()
	if err != nil {
		t.Fatalf("Parser error: %v", err)
	}

	cue, ok := msg.(ROItemCue)
	if !ok {
		t.Fatal("expected ROItemCue type")
	}

	if cue.ROEventType != "TAKE" {
		t.Errorf("expected roEventType TAKE, got %s", cue.ROEventType)
	}

	if len(cue.MosExternalMetadata) != 1 {
		t.Fatalf("expected 1 mosExternalMetadata, got %d", len(cue.MosExternalMetadata))
	}

	if cue.MosExternalMetadata[0].MosSchema != "http://example.com/cue-schema" {
		t.Errorf("expected mosSchema, got %s", cue.MosExternalMetadata[0].MosSchema)
	}
	if cue.MosExternalMetadata[0].MosPayload != "cue metadata payload" {
		t.Errorf("expected mosPayload, got %s", cue.MosExternalMetadata[0].MosPayload)
	}
}

// TestParserROStorySendWithExternalMeta verifies roStorySend with mosExternalMetadata
func TestParserROStorySendWithExternalMeta(t *testing.T) {
	xmlData := `<roStorySend>
		<roID>RO001</roID>
		<storyID>STORY001</storyID>
		<storySlug>Test Story</storySlug>
		<storyBody>
			<p>First paragraph.</p>
			<p>Second paragraph.</p>
		</storyBody>
		<mosExternalMetadata>
			<mosSchema>http://example.com/story-schema</mosSchema>
			<mosPayload>story metadata</mosPayload>
		</mosExternalMetadata>
	</roStorySend>`

	parser := NewMessageParser()
	parser.AppendData([]byte(xmlData))

	msg, _, err := parser.Parse()
	if err != nil {
		t.Fatalf("Parser error: %v", err)
	}

	ss, ok := msg.(ROStorySend)
	if !ok {
		t.Fatal("expected ROStorySend type")
	}

	if ss.ROID != "RO001" {
		t.Errorf("expected roID RO001, got %s", ss.ROID)
	}
	if ss.StoryID != "STORY001" {
		t.Errorf("expected storyID STORY001, got %s", ss.StoryID)
	}
	if len(ss.StoryBody.Paragraphs) != 2 {
		t.Errorf("expected 2 paragraphs, got %d", len(ss.StoryBody.Paragraphs))
	}
	if len(ss.ExternalMeta) != 1 {
		t.Fatalf("expected 1 mosExternalMetadata, got %d", len(ss.ExternalMeta))
	}
	if ss.ExternalMeta[0].MosPayload != "story metadata" {
		t.Errorf("expected mosPayload 'story metadata', got %s", ss.ExternalMeta[0].MosPayload)
	}
}

// TestParserROReqStoryActionOperations verifies different operation values
func TestParserROReqStoryActionOperations(t *testing.T) {
	operations := []string{"INSERT", "REPLACE", "DELETE", "SWAP", "MOVE"}

	for _, op := range operations {
		t.Run("operation_"+op, func(t *testing.T) {
			xmlData := `<roReqStoryAction operation="` + op + `"><roStorySend><roID>RO1</roID><storyID>S1</storyID><storyBody><p>content</p></storyBody></roStorySend></roReqStoryAction>`
			parser := NewMessageParser()
			parser.AppendData([]byte(xmlData))

			msg, _, err := parser.Parse()
			if err != nil {
				t.Fatalf("Parser error for operation %s: %v", op, err)
			}

			rsa, ok := msg.(ROReqStoryAction)
			if !ok {
				t.Fatal("expected ROReqStoryAction type")
			}
			if rsa.Operation != op {
				t.Errorf("expected operation %s, got %s", op, rsa.Operation)
			}
		})
	}
}

// TestParseMessageHelper verifies the convenience ParseMessage function
func TestParseMessageHelper(t *testing.T) {
	tests := []struct {
		name     string
		xml      string
		expected string
	}{
		{"keepAlive", `<keepAlive/>`, "keepAlive"},
		{"roCtrl", `<roCtrl><roID>RO1</roID><storyID>S1</storyID><itemID>I1</itemID><command>STOP</command></roCtrl>`, "roCtrl"},
		{"roItemCue", `<roItemCue><mosID>MOS1</mosID><roID>RO1</roID><storyID>S1</storyID><itemID>I1</itemID><roEventType>CUE</roEventType><roEventTime>2024-01-01T00:00:00Z</roEventTime></roItemCue>`, "roItemCue"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msg, err := ParseMessage(tc.xml)
			if err != nil {
				t.Fatalf("ParseMessage error: %v", err)
			}
			if msg.GetMessageType() != tc.expected {
				t.Errorf("expected type %s, got %s", tc.expected, msg.GetMessageType())
			}
		})
	}
}
