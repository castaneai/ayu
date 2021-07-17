package ayu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"syscall"
	"time"

	"github.com/go-redis/redis/v8"
	"golang.org/x/net/websocket"
)

const (
	authnTimeout          = 10 * time.Second
	redisOperationTimeout = 10 * time.Second
)

var (
	// ErrUnauthenticated is returned when the authentication fails.
	ErrUnauthenticated = errors.New("unauthenticated")
)

type clientProxy struct {
	roomID       RoomID
	clientID     ClientID
	connectionID connectionID
	conn         *websocket.Conn
	iceServers   []*ICEServer
}

// ServerOption represents an option for Server.
type ServerOption interface {
	apply(opts *serverOptions)
}

type serverOptionFunc func(opts *serverOptions)

func (f serverOptionFunc) apply(opts *serverOptions) {
	f(opts)
}

// WithAuthenticator specifies the custom authentication for joining the room.
func WithAuthenticator(authn Authenticator) ServerOption {
	return serverOptionFunc(func(opts *serverOptions) {
		opts.authn = authn
	})
}

// WithLogger specifies the custom logger.
func WithLogger(logger Logger) ServerOption {
	return serverOptionFunc(func(opts *serverOptions) {
		opts.logger = logger
	})
}

// WithRoomExpiration specifies the custom room expiration in Redis.
func WithRoomExpiration(expiration time.Duration) ServerOption {
	return serverOptionFunc(func(opts *serverOptions) {
		opts.roomExpiration = expiration
	})
}

// WithPingInterval specifies the custom ping interval.
func WithPingInterval(interval time.Duration) ServerOption {
	return serverOptionFunc(func(opts *serverOptions) {
		opts.pingInterval = interval
	})
}

type serverOptions struct {
	readTimeout    time.Duration
	writeTimeout   time.Duration
	pingInterval   time.Duration
	pongTimeout    time.Duration
	logger         Logger
	authn          Authenticator
	roomExpiration time.Duration
}

func defaultServerOptions() *serverOptions {
	return &serverOptions{
		readTimeout:    90 * time.Second,
		writeTimeout:   90 * time.Second,
		pingInterval:   5 * time.Second,
		pongTimeout:    60 * time.Second,
		authn:          &insecureAuthenticator{},
		logger:         nil,
		roomExpiration: 24 * time.Hour,
	}
}

// Server is a server of ayu.
type Server struct {
	logger      Logger
	opts        *serverOptions
	roomManager *redisRoomManager
	conns       map[string]*websocket.Conn
	mu          sync.RWMutex
}

// NewServer creates a new ayu server.
func NewServer(redisClient *redis.Client, opts ...ServerOption) *Server {
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
		opts:        dopts,
		roomManager: newRedisRoomManager(redisClient, logger, dopts.roomExpiration),
		conns:       map[string]*websocket.Conn{},
		mu:          sync.RWMutex{},
	}
}

// ServeHTTP implements the http.Handler interface for a WebSocket.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	websocket.Handler(s.handle).ServeHTTP(w, r)
}

// Shutdown shuts down the server.
// When Shutdown is called, all connections (WebSocket and Redis) will be forcibly disconnected.
func (s *Server) Shutdown() {
	s.logger.Infof("shutting down server...")
	ctx, cancel := context.WithTimeout(context.Background(), redisOperationTimeout)
	defer cancel()
	for _, rid := range s.roomManager.managingRooms() {
		if err := s.roomManager.DeleteRoom(ctx, rid); err != nil {
			s.logger.Errorf("failed to delete room(%s): %+v", rid, err)
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, conn := range s.conns {
		go s.shutdownConn(conn)
	}
}

func (s *Server) handle(conn *websocket.Conn) {
	defer s.shutdownConn(conn)
	s.mu.Lock()
	s.conns[conn.RemoteAddr().String()] = conn
	s.mu.Unlock()

	client, err := s.authn(conn)
	if err != nil {
		s.logger.Errorf("failed to authenticate client: %+v", err)
		return
	}
	onRoomMessage, err := s.joinRoom(client)
	if err != nil {
		s.logger.Errorf("failed to join room(room: %s, client: %s): %+v", client.roomID, client.clientID, err)
		return
	}
	defer s.leaveRoom(client)
	pongCh, forwardCh, disconnectedCh := s.startReceive(client)
	pongTimeoutCh := s.startPingPong(client, pongCh)

	for {
		select {
		case <-disconnectedCh:
			s.logger.Infof("client disconnected (room: %s, client: %s)", client.roomID, client.clientID)
			return
		case <-pongTimeoutCh:
			s.logger.Warnf("pong timeout (%v)", s.opts.pongTimeout)
			return
		case msg := <-forwardCh:
			s.forwardToRoom(client, msg)
		case msg := <-onRoomMessage:
			if msg.Sender == client.clientID {
				continue
			}
			switch msg.Type {
			case roomMessageTypeForward:
				s.forwardFromRoom(client, msg.Payload)
			case roomMessageTypeLeave:
				s.logger.Infof("one client left and ayu kicks the other client (room: %s, one: %s, other: %s)",
					client.roomID, msg.Sender, client.clientID)
				return
			}
		}
	}
}

func (s *Server) joinRoom(client *clientProxy) (<-chan *roomMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), redisOperationTimeout)
	defer cancel()

	lock, err := s.roomManager.BeginRoomLock(ctx, client.roomID)
	if err != nil {
		return nil, fmt.Errorf("failed to begin room lock: %w", err)
	}

	if len(lock.clientIDs) >= roomMaxMembers {
		lock.Unlock()
		if err := s.writeJSON(client.conn, &RejectMessage{
			Type:   MessageTypeReject,
			Reason: "full",
		}); err != nil {
			return nil, fmt.Errorf("failed to send reject message: %w", err)
		}
		return nil, ErrRoomIsFull
	}

	onRoomMessage, err := s.roomManager.SubscribeMessage(client.roomID, client.clientID)
	if err != nil {
		lock.Unlock()
		return nil, fmt.Errorf("failed to subscribe room: %w", err)
	}

	otherClientExists := len(lock.clientIDs) > 0
	if err := s.roomManager.JoinRoom(ctx, client.roomID, client.clientID, otherClientExists); err != nil {
		lock.Unlock()
		return nil, fmt.Errorf("failed to join room: %w", err)
	}
	lock.Unlock()
	if otherClientExists {
		s.logger.Infof("client-two joined (room: %s, client: %s)", client.roomID, client.clientID)
	} else {
		s.logger.Infof("client-one joined (room: %s, client: %s)", client.roomID, client.clientID)
	}

	if err := s.writeJSON(client.conn, &AcceptMessage{
		Type:          MessageTypeAccept,
		IceServers:    client.iceServers,
		IsExistClient: otherClientExists,
		IsExistUser:   otherClientExists,
	}); err != nil {
		return nil, fmt.Errorf("failed to send accept message: %w", err)
	}
	return onRoomMessage, nil
}

func (s *Server) leaveRoom(client *clientProxy) {
	ctx, cancel := context.WithTimeout(context.Background(), redisOperationTimeout)
	defer cancel()

	lock, err := s.roomManager.BeginRoomLock(ctx, client.roomID)
	if err != nil {
		s.logger.Errorf("failed to begin room lock (room: %s, client: %s): %+v", client.roomID, client.clientID, err)
		return
	}
	defer lock.Unlock()

	otherClientExists := len(lock.clientIDs) > 1
	if err := s.roomManager.LeaveRoom(ctx, client.roomID, client.clientID, otherClientExists); err != nil {
		s.logger.Errorf("failed to leave room (room: %s, client: %s): %+v", client.roomID, client.clientID, err)
	}
	if err := s.roomManager.UnsubscribeMessage(client.roomID, client.clientID); err != nil {
		s.logger.Errorf("failed to unsubscribe (room: %s, client: %s): %+v", client.roomID, client.clientID, err)
	}
	s.logger.Infof("client left (room: %s, client: %s)", client.roomID, client.clientID)
}

func (s *Server) authn(conn *websocket.Conn) (*clientProxy, error) {
	regMsg, err := s.readRegisterMessage(conn)
	if err != nil {
		return nil, fmt.Errorf("failed to read register message: %w", err)
	}
	authn := s.opts.authn
	connID := newRandomConnectionID()
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
	s.logger.Debugf("client authenticated (room: %s, client: %s)", req.RoomID, req.ClientID)
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
	return readJSONMessage(conn, v, s.opts.readTimeout)
}

func (s *Server) writeJSON(conn *websocket.Conn, v interface{}) error {
	return writeJSONMessage(conn, v, s.opts.writeTimeout)
}

func (s *Server) writeText(conn *websocket.Conn, v interface{}) error {
	return writeTextMessage(conn, v, s.opts.writeTimeout)
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
			s.logger.Errorf("failed to close websocket conn", err)
		}
	}
	s.mu.Lock()
	delete(s.conns, conn.RemoteAddr().String())
	s.mu.Unlock()
}

func isClosedError(err error) bool {
	return errors.Is(err, io.EOF) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNRESET)
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
						s.logger.Errorf("failed to send ping message (room: %s, client: %s): %+v", client.roomID, client.clientID, err)
					}
				}
			case <-pongCh:
				resetTimer(pongTimeoutTimer, s.opts.pongTimeout)
			case <-pongTimeoutTimer.C:
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
				if !isClosedError(err) {
					s.logger.Errorf("failed to read WebSocket message (room: %s, client: %s): %+v",
						client.roomID, client.clientID, err)
				}
				close(disconnectedCh)
				return
			}
			switch msg.Type {
			case MessageTypePong:
				var pong PingPongMessage
				if err := json.Unmarshal(msg.Payload, &pong); err != nil {
					s.logger.Errorf("failed to unmarshal pong message (room: %s, client: %s): %+v",
						client.roomID, client.clientID, err)
					continue
				}
				pongCh <- &pong
			case MessageTypeOffer, MessageTypeAnswer, MessageTypeCandidate:
				forwardCh <- &msg
			default:
				s.logger.Warnf("unknown message type received (room: %s, client: %s): %+v",
					client.roomID, client.clientID, msg)
			}
		}
	}()
	return pongCh, forwardCh, disconnectedCh
}

func (s *Server) forwardToRoom(sender *clientProxy, msg *message) {
	ctx, cancel := context.WithTimeout(context.Background(), redisOperationTimeout)
	defer cancel()
	if err := s.roomManager.PublishMessage(ctx, sender.roomID, &roomMessage{
		Sender:  sender.clientID,
		Type:    roomMessageTypeForward,
		Payload: string(msg.Payload),
	}); err != nil {
		s.logger.Errorf("failed to forward message to room (room: %s, client: %s): %+v",
			sender.roomID, sender.clientID, err)
	}
}

func (s *Server) forwardFromRoom(receiver *clientProxy, payload string) {
	if err := s.writeText(receiver.conn, payload); err != nil {
		s.logger.Errorf("failed to forward message from room (room: %s, client: %s): %+v",
			receiver.roomID, receiver.clientID, err)
	}
}

func readMessage(conn *websocket.Conn, v interface{}, codec websocket.Codec, timeout time.Duration) error {
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return fmt.Errorf("failed to set read deadline: %w", err)
	}
	return codec.Receive(conn, v)
}

func readJSONMessage(conn *websocket.Conn, v interface{}, timeout time.Duration) error {
	return readMessage(conn, v, websocket.JSON, timeout)
}

func writeMessage(conn *websocket.Conn, v interface{}, codec websocket.Codec, timeout time.Duration) error {
	if err := conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return fmt.Errorf("failed to set write deadline: %w", err)
	}
	return codec.Send(conn, v)
}

func writeJSONMessage(conn *websocket.Conn, v interface{}, timeout time.Duration) error {
	return writeMessage(conn, v, websocket.JSON, timeout)
}

func writeTextMessage(conn *websocket.Conn, v interface{}, timeout time.Duration) error {
	return writeMessage(conn, v, websocket.Message, timeout)
}
