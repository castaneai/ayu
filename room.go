package ayu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
)

const (
	roomMaxMembers     = 2
	roomLockExpiration = 30 * time.Second
)

type roomMessageType string

const (
	roomMessageTypeForward roomMessageType = "forward"
	roomMessageTypeJoin    roomMessageType = "join"
	roomMessageTypeLeave   roomMessageType = "leave"
)

var (
	ErrRoomIsFull = errors.New("room is full")
)

type RoomManager interface {
	JoinRoom(ctx context.Context, roomID RoomID, clientID ClientID) (*joinRoomResponse, error)
	LeaveRoom(ctx context.Context, roomID RoomID, clientID ClientID) (*leaveRoomResponse, error)
	ForwardMessage(ctx context.Context, roomID RoomID, rm *roomMessage) error
	SubscribeForwardMessage(roomID RoomID) <-chan *roomMessage
	SubscribeLeaveRoom(roomID RoomID) <-chan struct{}
}

type joinRoomResponse struct {
	OtherClientExists bool
}

type leaveRoomResponse struct {
	OtherClientExists bool
}

type roomMessage struct {
	Sender  ClientID        `json:"sender"`
	Type    roomMessageType `json:"type"`
	Payload string          `json:"payload"`
}

func redisRoomMembersKey(roomID RoomID) string {
	return fmt.Sprintf("ayu:room:members:%s", roomID)
}

func redisRoomLockKey(roomID RoomID) string {
	return fmt.Sprintf("ayu:room:lock:%s", roomID)
}

func redisRoomPubSubKey(roomID RoomID) string {
	return fmt.Sprintf("ayu:room:pubsub:%s", roomID)
}

type redisRoom struct {
	client         *redis.Client
	roomID         RoomID
	lock           *redisMutex
	mu             sync.Mutex
	full           bool
	forwardSendBuf []*roomMessage
	recvForwardCh  chan *roomMessage
	leaveCh        chan struct{}
	stopSub        context.CancelFunc
	logger         Logger
}

func newRedisRoom(client *redis.Client, roomID RoomID, logger Logger) *redisRoom {
	forwardCh := make(chan *roomMessage)
	leaveCh := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	room := &redisRoom{
		client:        client,
		roomID:        roomID,
		lock:          newRedisMutex(client, redisRoomLockKey(roomID), roomLockExpiration, roomOperationTimeout),
		mu:            sync.Mutex{},
		full:          false,
		recvForwardCh: forwardCh,
		leaveCh:       leaveCh,
		stopSub:       cancel,
		logger:        logger,
	}
	go func() {
		subCh := client.Subscribe(ctx, redisRoomPubSubKey(roomID)).Channel()
		for {
			select {
			case <-ctx.Done():
				return
			case m := <-subCh:
				var rm roomMessage
				if err := json.Unmarshal([]byte(m.Payload), &rm); err != nil {
					logger.Errorf("failed to unmarshal subscribed room message: %+v", err)
					return
				}
				room.handleRoomMessage(&rm, forwardCh, leaveCh)
			}
		}
	}()
	return room
}

func (r *redisRoom) join(ctx context.Context, clientID ClientID) (*joinRoomResponse, error) {
	if err := r.lock.Lock(); err != nil {
		return nil, err
	}
	defer func() { _ = r.lock.Unlock() }()

	clientIDs, err := r.client.SMembers(ctx, redisRoomMembersKey(r.roomID)).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to exec SMEMBERS for %s: %w", redisRoomMembersKey(r.roomID), err)
	}
	if len(clientIDs) >= roomMaxMembers {
		return nil, ErrRoomIsFull
	}
	otherClientExists := len(clientIDs) > 0

	if _, err := r.client.SAdd(ctx, redisRoomMembersKey(r.roomID), string(clientID)).Result(); err != nil {
		return nil, err
	}
	if otherClientExists {
		r.mu.Lock()
		r.full = true
		r.mu.Unlock()
	}
	if err := r.publishRoomMessage(ctx, &roomMessage{
		Sender: clientID,
		Type:   roomMessageTypeJoin,
	}); err != nil {
		return nil, err
	}
	return &joinRoomResponse{OtherClientExists: otherClientExists}, nil
}

func (r *redisRoom) publishRoomMessage(ctx context.Context, rm *roomMessage) error {
	r.mu.Lock()
	full := r.full
	r.mu.Unlock()

	publish := func(rm *roomMessage) error {
		b, err := json.Marshal(rm)
		if err != nil {
			return fmt.Errorf("failed to marshal room message: %w", err)
		}
		if _, err := r.client.Publish(ctx, redisRoomPubSubKey(r.roomID), string(b)).Result(); err != nil {
			return fmt.Errorf("failed to publish room message: %w", err)
		}
		return nil
	}
	if full {
		for _, rm := range r.forwardSendBuf {
			if err := publish(rm); err != nil {
				return err
			}
		}
		r.forwardSendBuf = nil
		return publish(rm)
	}
	if rm.Type == roomMessageTypeForward {
		r.forwardSendBuf = append(r.forwardSendBuf, rm)
	}
	return nil
}

func (r *redisRoom) handleRoomMessage(rm *roomMessage, forwardCh chan<- *roomMessage, leaveCh chan<- struct{}) {
	switch rm.Type {
	case roomMessageTypeJoin:
		r.mu.Lock()
		r.full = true
		r.mu.Unlock()
	case roomMessageTypeLeave:
		r.mu.Lock()
		r.full = false
		r.mu.Unlock()
		close(leaveCh)
	case roomMessageTypeForward:
		forwardCh <- rm
	default:
		r.logger.Warnf("unknown forward message type received: '%s'", rm.Type)
	}
}

func (r *redisRoom) leave(ctx context.Context, clientID ClientID) (*leaveRoomResponse, error) {
	if err := r.lock.Lock(); err != nil {
		return nil, err
	}
	defer func() { _ = r.lock.Unlock() }()

	if _, err := r.client.SRem(ctx, redisRoomMembersKey(r.roomID), string(clientID)).Result(); err != nil {
		return nil, fmt.Errorf("failed to exec SREM for %s: %w", redisRoomMembersKey(r.roomID), err)
	}

	clientIDs, err := r.client.SMembers(ctx, redisRoomMembersKey(r.roomID)).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to exec SMEMBERS for %s: %w", redisRoomMembersKey(r.roomID), err)
	}
	otherClientExists := len(clientIDs) > 0
	if err := r.publishRoomMessage(ctx, &roomMessage{
		Sender: clientID,
		Type:   roomMessageTypeLeave,
	}); err != nil {
		return nil, err
	}
	return &leaveRoomResponse{OtherClientExists: otherClientExists}, nil
}

type RedisRoomManager struct {
	client *redis.Client
	rooms  map[RoomID]*redisRoom
	mu     sync.Mutex
	logger Logger
}

func NewRedisRoomManager(client *redis.Client) *RedisRoomManager {
	logger, err := newDefaultLogger()
	if err != nil {
		panic(err)
	}
	return &RedisRoomManager{
		client: client,
		rooms:  map[RoomID]*redisRoom{},
		mu:     sync.Mutex{},
		logger: logger,
	}
}

func (m *RedisRoomManager) JoinRoom(ctx context.Context, roomID RoomID, clientID ClientID) (*joinRoomResponse, error) {
	return m.getRoom(roomID).join(ctx, clientID)
}

func (m *RedisRoomManager) LeaveRoom(ctx context.Context, roomID RoomID, clientID ClientID) (*leaveRoomResponse, error) {
	return m.getRoom(roomID).leave(ctx, clientID)
}

func (m *RedisRoomManager) ForwardMessage(ctx context.Context, roomID RoomID, rm *roomMessage) error {
	return m.getRoom(roomID).publishRoomMessage(ctx, rm)
}

func (m *RedisRoomManager) SubscribeForwardMessage(roomID RoomID) <-chan *roomMessage {
	return m.getRoom(roomID).recvForwardCh
}

func (m *RedisRoomManager) SubscribeLeaveRoom(roomID RoomID) <-chan struct{} {
	return m.getRoom(roomID).leaveCh
}

func (m *RedisRoomManager) getRoom(roomID RoomID) *redisRoom {
	m.mu.Lock()
	defer m.mu.Unlock()
	room, ok := m.rooms[roomID]
	if ok {
		return room
	}
	m.rooms[roomID] = newRedisRoom(m.client, roomID, m.logger)
	return m.rooms[roomID]
}

type redisMutex struct {
	client           *redis.Client
	key              string
	expiration       time.Duration
	operationTimeout time.Duration
}

func newRedisMutex(client *redis.Client, key string, expiration, operationTimeout time.Duration) *redisMutex {
	return &redisMutex{
		client:           client,
		key:              key,
		expiration:       expiration,
		operationTimeout: operationTimeout,
	}
}

func (m *redisMutex) Lock() error {
	ctx, cancel := context.WithTimeout(context.Background(), m.operationTimeout)
	defer cancel()
	for {
		acquired, err := m.client.SetNX(ctx, m.key, "1", m.expiration).Result()
		if err != nil {
			return err
		}
		if acquired {
			return nil
		}
	}
}

func (m *redisMutex) Unlock() error {
	ctx, cancel := context.WithTimeout(context.Background(), m.operationTimeout)
	defer cancel()
	if _, err := m.client.Del(ctx, m.key).Result(); err != nil {
		return err
	}
	return nil
}
