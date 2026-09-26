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

// GenerateEnvelope serializes a receive-side MOS acknowledgment frame.
func GenerateEnvelope(mosID, ncsID, messageID string, message MOSMessage) ([]byte, error) {
	envelope := Envelope{MosID: mosID, NcsID: ncsID, MessageID: messageID}
	switch value := message.(type) {
	case ROAck:
		envelope.ROAck = &value
	case Heartbeat:
		envelope.Heartbeat = &value
	case ListMachInfo:
		envelope.ListMachInfo = &value
	case KeepAlive:
		envelope.KeepAlive = &value
	case ROListAll:
		envelope.ROListAll = &value
	default:
		return nil, fmt.Errorf("unsupported enveloped message type %T", message)
	}

	data, err := xml.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal MOS envelope: %w", err)
	}
	return append([]byte(xml.Header), data...), nil
}

// CreateHeartbeat creates a heartbeat message
func CreateHeartbeat() Heartbeat {
	return Heartbeat{
		Time: Now(),
	}
}

// CreateHeartbeatResponse creates a heartbeat response message
func CreateHeartbeatResponse(requestID string) Heartbeat {
	return Heartbeat{
		RequestID: requestID,
		Time:      Now(),
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

// MOS protocol revisions advertised in listMachInfo. The value is a property of
// the transport answering the request, not of the process: the raw TCP transport
// speaks the 2.x family, the WebSocket transport speaks 4.0.
const (
	MosRev28 = "2.8.4"
	MosRev40 = "4.0.0"
)

// CreateListMachInfo creates a listMachInfo response message from config (Profile 0).
//
// mosRev must be supplied by the calling transport -- see MosRev28 / MosRev40.
func CreateListMachInfo(cfg *config.Config, mosRev string) ListMachInfo {
	profiles := make([]MosProfile, 8)
	for i := 0; i < 8; i++ {
		profiles[i] = MosProfile{
			Number: i,
			Value:  YesNo(i == 0), // Only Profile 0 is advertised.
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
		MosRev:       mosRev,
		SupportedProfiles: SupportedProfiles{
			DeviceType: "MOS",
			Profiles:   profiles,
		},
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
