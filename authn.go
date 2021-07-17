package ayu

import (
	"context"
)

// Authenticator is a authentication logic for joining the room.
type Authenticator interface {
	// Authenticate authenticates a client joining the room.
	Authenticate(ctx context.Context, req *AuthnRequest) (*AuthnResponse, error)
}

// AuthnRequest is a request for Authenticator.
type AuthnRequest struct {
	RoomID        RoomID
	ClientID      ClientID
	ConnectionID  connectionID
	AuthnMetadata map[string]interface{}
}

// AuthnResponse is a response for Authenticator.
type AuthnResponse struct {
	Allowed       bool
	Reason        string
	ICEServers    []*ICEServer
	AuthzMetadata map[string]interface{}
}

type insecureAuthenticator struct{}

func (a *insecureAuthenticator) Authenticate(ctx context.Context, req *AuthnRequest) (*AuthnResponse, error) {
	return &AuthnResponse{
		Allowed: true,
		ICEServers: []*ICEServer{
			{
				URLs: []string{
					"stun:stun.l.google.com:19302",
				},
			},
		},
	}, nil
}
