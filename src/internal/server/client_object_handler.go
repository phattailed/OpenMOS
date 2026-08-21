package server

import (
	"context"
	"fmt"

	"airshift/openmos/internal/xml"
	"airshift/openmos/pkg/logger"

	"github.com/getsentry/sentry-go"
)

// handleMosObj processes a mosObj message (Profile 1)
// Stores or updates the received object via the service layer
func (c *ClientConnection) handleMosObj(ctx context.Context, mosObj xml.MosObj) error {
	span := sentry.StartSpan(ctx, "handle_mos_obj")
	span.SetTag("obj_id", mosObj.ObjID)
	defer span.Finish()

	logger.Infof("Received mosObj from client %s: objID=%s slug=%s", c.id, mosObj.ObjID, mosObj.ObjSlug)

	// Store the object via service
	err := c.server.service.StoreObject(ctx, mosObj)
	if err != nil {
		logger.Errorf("Failed to store MOS object %s: %v", mosObj.ObjID, err)

		// Send error ack
		ack := xml.CreateMosObjAck(mosObj.ObjID, mosObj.ObjRev, "NACK", fmt.Sprintf("Failed to store object: %v", err))
		data, marshalErr := xml.GenerateMessage(ack)
		if marshalErr != nil {
			return marshalErr
		}
		return c.Write(data)
	}

	// Send success ack
	ack := xml.CreateMosObjAck(mosObj.ObjID, mosObj.ObjRev, "ACK", "Object stored successfully")
	data, err := xml.GenerateMessage(ack)
	if err != nil {
		return err
	}

	return c.Write(data)
}

// handleMosReqObj processes a mosReqObj request (Profile 1)
// Retrieves the requested object and responds with mosObj
func (c *ClientConnection) handleMosReqObj(ctx context.Context, req xml.MosReqObj) error {
	span := sentry.StartSpan(ctx, "handle_mos_req_obj")
	span.SetTag("obj_id", req.ObjID)
	defer span.Finish()

	logger.Infof("Received mosReqObj from client %s: objID=%s", c.id, req.ObjID)

	// Retrieve the object via service
	mosObj, err := c.server.service.GetObject(ctx, req.ObjID)
	if err != nil {
		logger.Errorf("Failed to get MOS object %s: %v", req.ObjID, err)
		return c.sendErrorAck("", "NACK", fmt.Sprintf("Object not found: %s", req.ObjID))
	}

	// Send the object back
	data, err := xml.GenerateMessage(*mosObj)
	if err != nil {
		return fmt.Errorf("failed to generate mosObj response: %w", err)
	}

	return c.Write(data)
}

// handleMosReqAll processes a mosReqAll request (Profile 1)
// Retrieves all objects and responds with mosListAll
func (c *ClientConnection) handleMosReqAll(ctx context.Context, req xml.MosReqAll) error {
	span := sentry.StartSpan(ctx, "handle_mos_req_all")
	defer span.Finish()

	logger.Infof("Received mosReqAll from client %s (pause=%d)", c.id, req.Pause)

	// Retrieve all objects via service
	objects, err := c.server.service.GetAllObjects(ctx)
	if err != nil {
		logger.Errorf("Failed to get all MOS objects: %v", err)
		return c.sendErrorAck("", "NACK", fmt.Sprintf("Failed to retrieve objects: %v", err))
	}

	// Create mosListAll response
	response := xml.CreateMosListAll(objects)
	data, err := xml.GenerateMessage(response)
	if err != nil {
		return fmt.Errorf("failed to generate mosListAll response: %w", err)
	}

	return c.Write(data)
}

// handleMosListAll processes a received mosListAll message (Profile 1)
// This is received when the remote device responds to our mosReqAll
func (c *ClientConnection) handleMosListAll(ctx context.Context, list xml.MosListAll) error {
	logger.Infof("Received mosListAll from client %s: %d objects", c.id, len(list.MosObjs))

	// Store all received objects
	for _, obj := range list.MosObjs {
		err := c.server.service.StoreObject(ctx, obj)
		if err != nil {
			logger.Warningf("Failed to store object %s from mosListAll: %v", obj.ObjID, err)
		}
	}

	return nil
}
