package xml

import (
	"encoding/xml"
	"fmt"

	"airshift/openmos/internal/config"
)

// GenerateMessage serializes a MOS message to XML
func GenerateMessage(message MOSMessage) ([]byte, error) {
	data, err := xml.Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal XML: %w", err)
	}

	// Add XML declaration
	result := append([]byte(xml.Header), data...)
	return result, nil
}

// CreateHeartbeat creates a heartbeat message
func CreateHeartbeat(source string, requestID string) Heartbeat {
	return Heartbeat{
		RequestID: requestID,
		Timestamp: Now(),
		Source:    source,
	}
}

// CreateHeartbeatResponse creates a heartbeat response message
func CreateHeartbeatResponse(source string, requestID string) Heartbeat {
	return Heartbeat{
		RequestID: requestID,
		Timestamp: Now(),
		Source:    source,
	}
}

// CreateMOSAck creates an acknowledgment message
func CreateMOSAck(source string, requestID string, status string, description string) MOSAck {
	return MOSAck{
		RequestID:         requestID,
		Timestamp:         Now(),
		Source:            source,
		Status:            status,
		StatusDescription: description,
	}
}

// CreateRunningOrderList creates a running order list message
func CreateRunningOrderList(source string, requestID string, items []ROListItem) RunningOrderList {
	return RunningOrderList{
		RequestID:    requestID,
		Timestamp:    Now(),
		Source:       source,
		RunningOrder: items,
	}
}

// CreateRunningOrderInfo creates a full running order message
func CreateRunningOrderInfo(source string, requestID string, id string, slug string,
	channel string, editTime string, startTime string, duration string,
	stories []StoryInfo) RunningOrderInfo {

	return RunningOrderInfo{
		RequestID: requestID,
		Timestamp: Now(),
		Source:    source,
		ID:        id,
		Slug:      slug,
		Channel:   channel,
		EditTime:  editTime,
		StartTime: startTime,
		Duration:  duration,
		Stories:   stories,
	}
}

// CreateStoryResponse creates a response to a story creation request
func CreateStoryResponse(requestID, source, status, description string) ([]byte, error) {
	ack := MOSAck{
		RequestID:         requestID,
		Timestamp:         Now(),
		Source:            source,
		Status:            status,
		StatusDescription: description,
	}

	return GenerateMessage(ack)
}

// CreateKeepAlive creates a keepAlive message (Profile 0)
func CreateKeepAlive() KeepAlive {
	return KeepAlive{}
}

// CreateListMachInfo creates a listMachInfo response message from config (Profile 0)
func CreateListMachInfo(cfg *config.Config) ListMachInfo {
	profiles := make([]MosProfile, 8)
	for i := 0; i < 8; i++ {
		profiles[i] = MosProfile{
			Number: i,
			Value:  i == 0, // Only Profile 0 is actually implemented and tested
		}
	}

	return ListMachInfo{
		Manufacturer: cfg.MOS.Manufacturer,
		Model:        cfg.MOS.Model,
		HwRev:        cfg.MOS.HwRev,
		SwRev:        cfg.MOS.SwRev,
		DOM:          cfg.MOS.DOM,
		SN:           cfg.MOS.SN,
		ID:           cfg.MOS.ID,
		Time:         Now(),
		MosRev:       "4.0.0",
		SupportedProfiles: SupportedProfiles{
			DeviceType: "MOS",
			Profiles:   profiles,
		},
	}
}

// CreateMosObj creates a mosObj message from object data (Profile 1)
func CreateMosObj(objID, objSlug, mosAbstract, objGroup, objType string,
	objTB, objRev, objDur int, status string,
	createdBy, created, changedBy, changed, description string) MosObj {

	return MosObj{
		ObjID:       objID,
		ObjSlug:     objSlug,
		MosAbstract: mosAbstract,
		ObjGroup:    objGroup,
		ObjType:     objType,
		ObjTB:       objTB,
		ObjRev:      objRev,
		ObjDur:      objDur,
		Status:      status,
		CreatedBy:   createdBy,
		Created:     created,
		ChangedBy:   changedBy,
		Changed:     changed,
		Description: description,
	}
}

// CreateMosListAll creates a mosListAll response message (Profile 1)
func CreateMosListAll(objects []MosObj) MosListAll {
	return MosListAll{
		MosObjs: objects,
	}
}

// CreateMosObjAck creates an object acknowledgment message (Profile 1)
func CreateMosObjAck(objID string, objRev int, status, statusDescription string) MosObjAck {
	return MosObjAck{
		ObjID:             objID,
		ObjRev:            objRev,
		Status:            status,
		StatusDescription: statusDescription,
	}
}

// --- Profile 2 generators ---

// CreateROAck creates a running order acknowledgment message (Profile 2)
func CreateROAck(roID, roStatus string, stories []ROAckStory) ROAck {
	return ROAck{
		ID:      roID,
		Status:  roStatus,
		Stories: stories,
	}
}

// CreateROListAll creates a roListAll response message (Profile 2)
func CreateROListAll(items []ROListAllItem) ROListAll {
	return ROListAll{
		ROs: items,
	}
}

// --- Profile 3 generators ---

// CreateMosListSearchableSchema creates a mosListSearchableSchema response (Profile 3)
func CreateMosListSearchableSchema(username, mosSchema string) MosListSearchableSchema {
	return MosListSearchableSchema{
		Username:  username,
		MosSchema: mosSchema,
	}
}
