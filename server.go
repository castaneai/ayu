package ayu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/websocket"
)

const (
	authnTimeout         = 10 * time.Second
	roomOperationTimeout = 10 * time.Second
)

var (
	ErrUnauthenticated = errors.New("unauthenticated")
)

type clientProxy struct {
	roomID       RoomID
	clientID     ClientID
	connectionID ConnectionID
	conn         *websocket.Conn
	iceServers   []*ICEServer
}

type Server struct {
	logger      Logger
	opts        *serverOptions
	roomManager RoomManager
}

func NewServer(roomManager RoomManager, opts ...ServerOption) *Server {
	dopts := defaultServerOptions()
	for _, opt := range opts {
		opt.apply(dopts)
	}
	logger := dopts.logger
	if logger == nil {
		lg, err := newDefaultLogger()
		if err != nil {
			panic(err)
		}
		logger = lg
	}
	return &Server{
		logger:      logger,
		opts:        defaultServerOptions(),
		roomManager: roomManager,
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	websocket.Handler(s.handle).ServeHTTP(w, r)
}

func (s *Server) handle(conn *websocket.Conn) {
	defer s.shutdownConn(conn)

	client, err := s.authn(conn)
	if err != nil {
		s.logger.Errorf("failed to authenticate client: %+v", err)
		return
	}
	defer func() {
		s.logger.Infof("client closed (roomID: %s, clientID: %s, connectionID: %s)", client.roomID, client.clientID, client.connectionID)
	}()

	if err := s.joinRoom(client); err != nil {
		s.logger.Errorf("failed to join room: %+v", err)
		return
	}
	s.logger.Infof("client joined (roomID: %s, clientID: %s, connectionID: %s)", client.roomID, client.clientID, client.connectionID)
	defer func() {
		if err := s.leaveRoom(client); err != nil {
			s.logger.Errorf("failed to leave room: %+v", err)
		}
		s.logger.Infof("client left (roomID: %s, clientID: %s, connectionID: %s)", client.roomID, client.clientID, client.connectionID)
	}()
	recvForwardMessageCh := s.roomManager.SubscribeForwardMessage(client.roomID)
	leaveCh := s.roomManager.SubscribeLeaveRoom(client.roomID)
	pongCh, forwardCh, disconnectedCh := s.startReceive(client)
	pongTimeoutCh := s.startPingPong(client, pongCh)

	for {
		select {
		case <-disconnectedCh:
			return
		case <-pongTimeoutCh:
			return
		case <-leaveCh:
			return
		case msg := <-forwardCh:
			s.forwardCandidateToRoom(client, msg)
		case msg := <-recvForwardMessageCh:
			if msg.Sender == client.clientID {
				continue
			}
			s.forwardCandidateFromRoom(client, msg.Payload)
		}
	}
}

func (s *Server) joinRoom(client *clientProxy) error {
	ctx, cancel := context.WithTimeout(context.Background(), roomOperationTimeout)
	defer cancel()
	resp, err := s.roomManager.JoinRoom(ctx, client.roomID, client.clientID)
	if err != nil {
		if errors.Is(err, ErrRoomIsFull) {
			if err := s.writeJSON(client.conn, &RejectMessage{
				Type:   MessageTypeReject,
				Reason: "full",
			}); err != nil {
				return fmt.Errorf("failed to send reject message: %w", err)
			}
		}
		return fmt.Errorf("failed to join room: %w", err)
	}
	if err := s.writeJSON(client.conn, &AcceptMessage{
		Type:          MessageTypeAccept,
		IceServers:    client.iceServers,
		IsExistClient: resp.OtherClientExists,
		IsExistUser:   resp.OtherClientExists,
	}); err != nil {
		return fmt.Errorf("failed to send accept message: %w", err)
	}
	return nil
}

func (s *Server) leaveRoom(client *clientProxy) error {
	ctx, cancel := context.WithTimeout(context.Background(), roomOperationTimeout)
	defer cancel()
	if _, err := s.roomManager.LeaveRoom(ctx, client.roomID, client.clientID); err != nil {
		return err
	}
	return nil
}

func (s *Server) authn(conn *websocket.Conn) (*clientProxy, error) {
	regMsg, err := s.readRegisterMessage(conn)
	if err != nil {
		return nil, fmt.Errorf("failed to read register message: %w", err)
	}
	s.logger.Debugf("received register: %+v", regMsg)
	authn := s.opts.authn
	connID := NewRandomConnectionID()
	req := &AuthnRequest{
		RoomID:        regMsg.RoomID,
		ClientID:      regMsg.ClientID,
		ConnectionID:  connID,
		AuthnMetadata: regMsg.AuthnMetadata,
	}
	if req.ClientID == "" {
		req.ClientID = ClientID(connID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), authnTimeout)
	defer cancel()
	authnResponse, err := authn.Authenticate(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate: %w", err)
	}
	if !authnResponse.Allowed {
		if err := s.writeJSON(conn, &RejectMessage{
			Type:   MessageTypeReject,
			Reason: "InternalServerError",
		}); err != nil {
			return nil, fmt.Errorf("failed to send reject message: %w", err)
		}
		return nil, ErrUnauthenticated
	}
	s.logger.Debugf("authenticated: %+v", authnResponse)
	return &clientProxy{
		roomID:       req.RoomID,
		clientID:     req.ClientID,
		connectionID: req.ConnectionID,
		conn:         conn,
		iceServers:   authnResponse.ICEServers,
	}, nil
}

func (s *Server) readRegisterMessage(conn *websocket.Conn) (*RegisterMessage, error) {
	var msg RegisterMessage
	if err := s.readJSON(conn, &msg); err != nil {
		return nil, err
	}
	if msg.Type != MessageTypeRegister {
		return nil, fmt.Errorf("message type '%s' expected but '%s' found", MessageTypeRegister, msg.Type)
	}
	return &msg, nil
}

func (s *Server) readJSON(conn *websocket.Conn, v interface{}) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.opts.readTimeout)
	defer cancel()
	return readJSONMessage(ctx, conn, v)
}

func (s *Server) writeJSON(conn *websocket.Conn, v interface{}) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.opts.writeTimeout)
	defer cancel()
	return writeJSONMessage(ctx, conn, v)
}

func (s *Server) writeText(conn *websocket.Conn, v interface{}) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.opts.writeTimeout)
	defer cancel()
	return writeTextMessage(ctx, conn, v)
}

func (s *Server) shutdownConn(conn *websocket.Conn) {
	if err := s.writeJSON(conn, &ByeMessage{Type: MessageTypeBye}); err != nil {
		if !isClosedError(err) {
			s.logger.Errorf("failed to send bye message: %+v", err)
		}
	}
	// close code 1000: Normal Closure (RFC 6455)
	if err := conn.WriteClose(1000); err != nil {
		if !isClosedError(err) {
			s.logger.Errorf("failed to send close code: %+v", err)
		}
	}
}

func isClosedError(err error) bool {
	return errors.Is(err, io.EOF) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.EPIPE) ||
		strings.Contains(err.Error(), "connection reset by peer")
}

func (s *Server) startPingPong(client *clientProxy, pongCh <-chan *PingPongMessage) <-chan struct{} {
	pongTimeoutCh := make(chan struct{})
	go func() {
		pongTimeoutTimer := time.NewTimer(s.opts.pongTimeout)
		defer pongTimeoutTimer.Stop()
		ticker := time.NewTicker(s.opts.pingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := s.writeJSON(client.conn, &PingPongMessage{MessageTypePing}); err != nil {
					if isClosedError(err) {
						return
					} else {
						s.logger.Errorf("failed to send ping message: %+v", err)
					}
				}
			case <-pongCh:
				resetTimer(pongTimeoutTimer, s.opts.pongTimeout)
			case <-pongTimeoutTimer.C:
				s.logger.Warnf("pong timeout(%v)", s.opts.pongTimeout)
				close(pongTimeoutCh)
				return
			}
		}
	}()
	return pongTimeoutCh
}

func resetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}

func (s *Server) startReceive(client *clientProxy) (<-chan *PingPongMessage, <-chan *message, <-chan struct{}) {
	pongCh := make(chan *PingPongMessage)
	forwardCh := make(chan *message)
	disconnectedCh := make(chan struct{})
	go func() {
		for {
			var msg message
			if err := s.readJSON(client.conn, &msg); err != nil {
				if isClosedError(err) {
					close(disconnectedCh)
					return
				} else {
					s.logger.Errorf("failed to read message: %+v", err)
				}
			}
			switch msg.Type {
			case MessageTypePong:
				var pong PingPongMessage
				if err := json.Unmarshal(msg.Payload, &pong); err != nil {
					s.logger.Errorf("failed to unmarshal pong message: %+v", err)
					continue
				}
				pongCh <- &pong
			case MessageTypeOffer, MessageTypeAnswer, MessageTypeCandidate:
				forwardCh <- &msg
			default:
				s.logger.Warnf("unknown message type received: %s", msg.Type)
			}
		}
	}()
	return pongCh, forwardCh, disconnectedCh
}

func (s *Server) forwardCandidateToRoom(sender *clientProxy, msg *message) {
	ctx, cancel := context.WithTimeout(context.Background(), roomOperationTimeout)
	defer cancel()
	if err := s.roomManager.ForwardMessage(ctx, sender.roomID, &roomMessage{
		Sender:  sender.clientID,
		Type:    roomMessageTypeForward,
		Payload: string(msg.Payload),
	}); err != nil {
		s.logger.Errorf("failed to forward candidate message to room: %+v", err)
		return
	}
	s.logger.Infof("forwarded message: %+v", string(msg.Payload))
}

func (s *Server) forwardCandidateFromRoom(receiver *clientProxy, payload string) {
	if err := s.writeText(receiver.conn, payload); err != nil {
		s.logger.Errorf("failed to forward candidate message from room: %+v", err)
	}
}
