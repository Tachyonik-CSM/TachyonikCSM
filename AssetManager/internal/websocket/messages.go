// AssetManager
// SPDX-FileCopyrightText: 2026 Tachyonik GmbH
// SPDX-License-Identifier: AGPL-3.0-or-later

// The message types broadcast to clients, and their payloads — the wire
// vocabulary an open UI reacts to.

package websocket

import "encoding/json"

// MessageType represents the type of WebSocket message
type MessageType string

const (
	TypeAssetCreated         MessageType = "ASSET_CREATED"
	TypeAssetUpdated         MessageType = "ASSET_UPDATED"
	TypeAssetDeleted         MessageType = "ASSET_DELETED"
	TypeVulnerabilityCreated MessageType = "VULNERABILITY_CREATED"
	TypeVulnerabilityUpdated MessageType = "VULNERABILITY_UPDATED"
	TypeVulnerabilityDeleted MessageType = "VULNERABILITY_DELETED"
	TypeDetectionCreated     MessageType = "DETECTION_CREATED"
	TypeDetectionUpdated     MessageType = "DETECTION_UPDATED"
	TypeDetectionDeleted     MessageType = "DETECTION_DELETED"
	TypePing                 MessageType = "PING"
	TypePong                 MessageType = "PONG"
)

// Message represents a WebSocket message
type Message struct {
	Type    MessageType     `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`

	// OwnerUserIDs scopes delivery: the hub sends this message to the
	// services and to the clients whose visible-user set intersects it.
	// Empty reaches the services only (AM-SEC-19). json:"-" so it never
	// reaches the wire. (SECURITY: CRIT-3)
	OwnerUserIDs []int64 `json:"-"`
}

// NewMessage creates a new message with the given type and payload
func NewMessage(msgType MessageType, payload interface{}) (*Message, error) {
	var rawPayload json.RawMessage
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		rawPayload = data
	}

	return &Message{
		Type:    msgType,
		Payload: rawPayload,
	}, nil
}

// AssetDeletedPayload represents the payload for asset deleted messages
type AssetDeletedPayload struct {
	ID int64 `json:"id"`
}

// AssetUpdatedPayload represents the payload for asset updated messages
type AssetUpdatedPayload struct {
	ID int64 `json:"id"`
}
