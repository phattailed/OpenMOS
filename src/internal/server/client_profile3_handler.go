package server

import (
	"context"
	"fmt"

	"airshift/openmos/internal/xml"
	"airshift/openmos/pkg/logger"

	"github.com/getsentry/sentry-go"
)

// handleMosObjCreate processes a mosObjCreate message (Profile 3)
// NCS tells MOS to create a new object; respond with mosAck containing the new objID
func (c *ClientConnection) handleMosObjCreate(ctx context.Context, mosObjCreate xml.MosObjCreate) error {
	span := sentry.StartSpan(ctx, "handle_mos_obj_create")
	span.SetTag("obj_slug", mosObjCreate.ObjSlug)
	defer span.Finish()

	logger.Infof("Received mosObjCreate from client %s: slug=%s type=%s",
		c.id, mosObjCreate.ObjSlug, mosObjCreate.ObjType)

	// Delegate to service layer
	objID, err := c.server.service.CreateObjectFromNCS(ctx, mosObjCreate)
	if err != nil {
		logger.Errorf("Failed to create MOS object from NCS: %v", err)
		ack := xml.CreateMosObjAck("", 0, "NACK", fmt.Sprintf("Failed to create object: %v", err))
		data, marshalErr := xml.GenerateMessage(ack)
		if marshalErr != nil {
			return marshalErr
		}
		return c.Write(data)
	}

	// Send success ack with the new object ID
	ack := xml.CreateMosObjAck(objID, 1, "ACK", "Object created successfully")
	data, err := xml.GenerateMessage(ack)
	if err != nil {
		return err
	}

	return c.Write(data)
}

// handleMosItemReplace processes a mosItemReplace message (Profile 3)
// Replaces a specific item within a story
func (c *ClientConnection) handleMosItemReplace(ctx context.Context, mosItemReplace xml.MosItemReplace) error {
	span := sentry.StartSpan(ctx, "handle_mos_item_replace")
	span.SetTag("ro_id", mosItemReplace.ROID)
	span.SetTag("story_id", mosItemReplace.StoryID)
	span.SetTag("item_id", mosItemReplace.Item.ItemID)
	defer span.Finish()

	logger.Infof("Received mosItemReplace from client %s: roID=%s storyID=%s itemID=%s",
		c.id, mosItemReplace.ROID, mosItemReplace.StoryID, mosItemReplace.Item.ItemID)

	// Delegate to service layer
	err := c.server.service.ReplaceItemInStory(ctx, mosItemReplace)
	if err != nil {
		logger.Errorf("Failed to replace item %s in story %s: %v",
			mosItemReplace.Item.ItemID, mosItemReplace.StoryID, err)
		ack := xml.CreateROAck(mosItemReplace.ROID, "NACK", nil)
		data, marshalErr := xml.GenerateMessage(ack)
		if marshalErr != nil {
			return marshalErr
		}
		return c.Write(data)
	}

	// Send success ack
	ack := xml.CreateROAck(mosItemReplace.ROID, "ACK", nil)
	data, err := xml.GenerateMessage(ack)
	if err != nil {
		return err
	}

	return c.Write(data)
}

// handleMosReqSearchableSchema processes a mosReqSearchableSchema message (Profile 3)
// Responds with mosListSearchableSchema containing the searchable schema
func (c *ClientConnection) handleMosReqSearchableSchema(ctx context.Context, req xml.MosReqSearchableSchema) error {
	span := sentry.StartSpan(ctx, "handle_mos_req_searchable_schema")
	defer span.Finish()

	logger.Infof("Received mosReqSearchableSchema from client %s (username=%s)", c.id, req.Username)

	// Create response with a basic schema placeholder
	// In a full implementation, this would return the actual searchable schema
	response := xml.CreateMosListSearchableSchema(req.Username, "")
	data, err := xml.GenerateMessage(response)
	if err != nil {
		return fmt.Errorf("failed to generate mosListSearchableSchema response: %w", err)
	}

	return c.Write(data)
}

// handleMosListSearchableSchema processes a received mosListSearchableSchema message (Profile 3)
// This is received when the remote MOS responds to our mosReqSearchableSchema
func (c *ClientConnection) handleMosListSearchableSchema(ctx context.Context, schema xml.MosListSearchableSchema) error {
	logger.Infof("Received mosListSearchableSchema from client %s (username=%s)", c.id, schema.Username)
	// Log receipt of the schema
	return nil
}
