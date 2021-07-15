package ayu

import (
	"context"
)

type Authenticator interface {
	Authenticate(ctx context.Context, req *AuthnRequest) (*AuthnResponse, error)
}

type AuthnRequest struct {
	RoomID        RoomID
	ClientID      ClientID
	ConnectionID  ConnectionID
	AuthnMetadata map[string]interface{}
}

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
