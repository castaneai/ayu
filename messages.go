package ayu

import (
	"encoding/json"

	"github.com/google/uuid"
)

// MessageType represents a type of messages in ayame protocol.
// See https://github.com/OpenAyame/ayame-spec for details
type MessageType string

const (
	MessageTypeAccept    MessageType = "accept"
	MessageTypeAnswer    MessageType = "answer"
	MessageTypeBye       MessageType = "bye"
	MessageTypeCandidate MessageType = "candidate"
	MessageTypeOffer     MessageType = "offer"
	MessageTypePing      MessageType = "ping"
	MessageTypePong      MessageType = "pong"
	MessageTypeRegister  MessageType = "register"
	MessageTypeReject    MessageType = "reject"
)

// RoomID represents a room ID
type RoomID string

// ClientID represents a client ID
type ClientID string

// connectionID represents a connection ID (internal-use only)
type connectionID string

func newRandomConnectionID() connectionID {
	return connectionID(uuid.Must(uuid.NewRandom()).String())
}

// ICEServer represents ICE server's information
type ICEServer struct {
	URLs       []string `json:"urls"`
	UserName   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

type message struct {
	Type    MessageType `json:"type"`
	Payload []byte
}

func (j *message) UnmarshalJSON(bytes []byte) error {
	var t struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(bytes, &t); err != nil {
		return err
	}
	j.Type = MessageType(t.Type)
	j.Payload = bytes
	return nil
}

// PingPongMessage represents a ping-pong message
// See https://github.com/OpenAyame/ayame-spec for details
type PingPongMessage struct {
	Type MessageType `json:"type"`
}

// RegisterMessage represents a register message
// See https://github.com/OpenAyame/ayame-spec for details
type RegisterMessage struct {
	Type          MessageType            `json:"type"`
	RoomID        RoomID                 `json:"roomId"`
	ClientID      ClientID               `json:"clientId"`
	AuthnMetadata map[string]interface{} `json:"authnMetadata,omitempty"`
}

// AcceptMessage represents a register message
// See https://github.com/OpenAyame/ayame-spec for details
type AcceptMessage struct {
	Type          MessageType  `json:"type"`
	IceServers    []*ICEServer `json:"iceServers"`
	IsExistClient bool         `json:"isExistClient"`
	IsExistUser   bool         `json:"isExistUser"` // for compatibility
}

// RejectMessage represents a reject message
// See https://github.com/OpenAyame/ayame-spec for details
type RejectMessage struct {
	Type   MessageType `json:"type"`
	Reason string      `json:"reason"`
}

// ByeMessage represents a bye message
// See https://github.com/OpenAyame/ayame-spec for details
type ByeMessage struct {
	Type MessageType `json:"type"`
}

// CandidateMessage represents a candidate message
// See https://github.com/OpenAyame/ayame-spec for details
type CandidateMessage struct {
	Type         MessageType       `json:"type"`
	ICECandidate *ICECandidateInit `json:"ice,omitempty"`
}

// ICECandidateInit is used to serialize ice candidates
type ICECandidateInit struct {
	Candidate        string  `json:"candidate"`
	SDPMid           *string `json:"sdpMid"`
	SDPMLineIndex    *uint16 `json:"sdpMLineIndex"`
	UsernameFragment *string `json:"usernameFragment"`
}
