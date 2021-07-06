package ayu

import (
	"encoding/json"

	"github.com/google/uuid"
	"github.com/pion/webrtc/v3"
)

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

type RoomID string
type ClientID string
type ConnectionID string

func NewRandomConnectionID() ConnectionID {
	return ConnectionID(uuid.Must(uuid.NewRandom()).String())
}

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

type PingPongMessage struct {
	Type MessageType `json:"type"`
}

type RegisterMessage struct {
	Type          MessageType            `json:"type"`
	RoomID        RoomID                 `json:"roomId"`
	ClientID      ClientID               `json:"clientId"`
	AuthnMetadata map[string]interface{} `json:"authnMetadata,omitempty"`
}

type AcceptMessage struct {
	Type          MessageType  `json:"type"`
	IceServers    []*ICEServer `json:"iceServers"`
	IsExistClient bool         `json:"isExistClient"`
	IsExistUser   bool         `json:"isExistUser"` // for compatibility
}

type RejectMessage struct {
	Type   MessageType `json:"type"`
	Reason string      `json:"reason"`
}

type ByeMessage struct {
	Type MessageType `json:"type"`
}

type CandidateMessage struct {
	Type         MessageType              `json:"type"`
	ICECandidate *webrtc.ICECandidateInit `json:"ice,omitempty"`
}
