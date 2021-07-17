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
	roomMaxMembers          = 2
	roomLockExpiration      = 30 * time.Second
	subscribeChannelBufSize = 50
)

type roomMessageType string

const (
	roomMessageTypeForward roomMessageType = "forward"
	roomMessageTypeJoin    roomMessageType = "join"
	roomMessageTypeLeave   roomMessageType = "leave"
)

var (
	// ErrRoomIsFull is returned when the room trying to join is full.
	ErrRoomIsFull = errors.New("room is full")
)

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
	client      *redis.Client
	roomID      RoomID
	lock        *redisMutex    // Locking by SETNX is used for transactional updating of the room member list.
	publishBuf  *publishBuffer // The forward message sent before the other client connects should be buffered.
	subscribers map[ClientID]chan *roomMessage
	mu          sync.RWMutex // to protect subscribers map
	stopSub     context.CancelFunc
	logger      Logger
	expiration  time.Duration
}

func newRedisRoom(client *redis.Client, roomID RoomID, logger Logger, expiration time.Duration) (*redisRoom, error) {
	ctx, cancel := context.WithCancel(context.Background())
	room := &redisRoom{
		client:      client,
		roomID:      roomID,
		lock:        newRedisMutex(client, redisRoomLockKey(roomID), roomLockExpiration, redisOperationTimeout),
		publishBuf:  newPublishBuffer(),
		subscribers: map[ClientID]chan *roomMessage{},
		mu:          sync.RWMutex{},
		stopSub:     cancel,
		logger:      logger,
		expiration:  expiration,
	}
	subscribeOpened := make(chan struct{})
	go func() {
		sub := client.Subscribe(ctx, redisRoomPubSubKey(roomID))
		go func() {
			<-ctx.Done()
			rctx, cancel := context.WithTimeout(context.Background(), redisOperationTimeout)
			defer cancel()
			if err := sub.Unsubscribe(rctx, redisRoomPubSubKey(roomID)); err != nil {
				logger.Errorf("failed to unsubscribe redis pub/sub (room: %s): %+v", roomID, err)
			}
			logger.Debugf("closing redis pub/sub subscription (room: %s)", roomID)
			if err := sub.Close(); err != nil {
				logger.Errorf("failed to close redis pub/sub (room: %s): %+v", roomID, err)
			}
		}()
		var once sync.Once
		for {
			recv, err := sub.Receive(ctx)
			if err != nil {
				if ctx.Err() == nil {
					room.logger.Errorf("failed to subscribe from redis (room: %s): %+v", roomID, err)
				}
				return
			}
			switch m := recv.(type) {
			case *redis.Subscription:
				once.Do(func() { close(subscribeOpened) })
			case *redis.Message:
				var rm roomMessage
				if err := json.Unmarshal([]byte(m.Payload), &rm); err != nil {
					logger.Errorf("failed to unmarshal subscribed room message (room: %s): %+v", roomID, err)
					continue
				}
				go room.handleRoomMessage(&rm)
			case *redis.Pong:
				// ignore
			default:
				logger.Warnf("unknown message received from pub/sub (room: %s): %+v", roomID, m)
			}
		}
	}()
	select {
	case <-subscribeOpened:
		return room, nil
	case <-time.After(redisOperationTimeout):
		return nil, fmt.Errorf("opening pub/sub subscription timeout(%v)", redisOperationTimeout)
	}
}

func (r *redisRoom) join(ctx context.Context, clientID ClientID, otherClientExists bool) error {
	key := redisRoomMembersKey(r.roomID)
	if _, err := r.client.SAdd(ctx, key, string(clientID)).Result(); err != nil {
		return fmt.Errorf("failed to exec SADD for %s: %w", key, err)
	}
	// If the other client is present in the room, we will notify them of our participation.
	if otherClientExists {
		if err := r.publishMessages(ctx, []*roomMessage{
			{Sender: clientID, Type: roomMessageTypeJoin},
		}, otherClientExists); err != nil {
			return err
		}
	}
	return nil
}

type roomLock struct {
	clientIDs []ClientID
	unlock    func()
}

func (t *roomLock) Unlock() {
	t.unlock()
}

func (r *redisRoom) beginLock(ctx context.Context) (*roomLock, error) {
	if err := r.lock.Lock(); err != nil {
		return nil, err
	}
	key := redisRoomMembersKey(r.roomID)
	members, err := r.client.SMembers(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to exec SMEMBERS for %s: %w", key, err)
	}
	if _, err := r.client.Expire(ctx, key, r.expiration).Result(); err != nil {
		return nil, fmt.Errorf("failed to exec EXPIRE for %s: %w", key, err)
	}
	var clientIDs []ClientID
	for _, m := range members {
		clientIDs = append(clientIDs, ClientID(m))
	}
	return &roomLock{
		clientIDs: clientIDs,
		unlock: func() {
			if err := r.lock.Unlock(); err != nil {
				r.logger.Errorf("failed to unlock room (room: %s): %+v", r.roomID, err)
			}
		},
	}, nil
}

func (r *redisRoom) publishMessages(ctx context.Context, rms []*roomMessage, otherClientExists bool) error {
	if !otherClientExists {
		for _, rm := range rms {
			if rm.Type == roomMessageTypeForward {
				r.publishBuf.Add(rm)
				r.logger.Debugf("buffered message (room: %s): %+v", r.roomID, rm)
			}
		}
		return nil
	}

	rms = append(r.publishBuf.Flush(), rms...)
	for _, rm := range rms {
		b, err := json.Marshal(rm)
		if err != nil {
			return fmt.Errorf("failed to marshal room message: %w", err)
		}
		if _, err := r.client.Publish(ctx, redisRoomPubSubKey(r.roomID), string(b)).Result(); err != nil {
			return fmt.Errorf("failed to publish room message: %w", err)
		}
		r.logger.Debugf("published message (room: %s): %+v", r.roomID, rm)
	}
	return nil
}

func (r *redisRoom) handleRoomMessage(rm *roomMessage) {
	switch rm.Type {
	case roomMessageTypeJoin:
		ctx, cancel := context.WithTimeout(context.Background(), redisOperationTimeout)
		defer cancel()
		lock, err := r.beginLock(ctx)
		if err != nil {
			r.logger.Errorf("failed to begin room lock (room: %s): %+v", r.roomID, err)
			return
		}
		defer lock.Unlock()
		otherClientExists := len(lock.clientIDs) > 1
		if err := r.publishMessages(ctx, nil, otherClientExists); err != nil {
			r.logger.Errorf("failed to publish buffered messages (room: %s): %+v", r.roomID, err)
		}
	case roomMessageTypeLeave:
	case roomMessageTypeForward:
	default:
		r.logger.Warnf("unknown room message received (room: %s): %+v", r.roomID, rm)
		return
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for target, sub := range r.subscribers {
		select {
		case sub <- rm:
		default:
			r.logger.Warnf("failed to forward message from room because channel is full (room: %s, target: %s): %+v",
				r.roomID, target, rm)
		}
	}
}

func (r *redisRoom) leave(ctx context.Context, clientID ClientID, otherClientExists bool) error {
	if otherClientExists {
		if err := r.publishMessages(ctx, []*roomMessage{
			{Sender: clientID, Type: roomMessageTypeLeave},
		}, otherClientExists); err != nil {
			return err
		}
	}
	if _, err := r.client.SRem(ctx, redisRoomMembersKey(r.roomID), string(clientID)).Result(); err != nil {
		return fmt.Errorf("failed to exec SREM for %s: %w", redisRoomMembersKey(r.roomID), err)
	}
	return nil
}

func (r *redisRoom) subscribeRoomMessage(clientID ClientID) <-chan *roomMessage {
	subCh := make(chan *roomMessage, subscribeChannelBufSize)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.subscribers[clientID] = subCh
	r.logger.Debugf("subscribe room pub/sub (room: %s, client: %s)", r.roomID, clientID)
	return subCh
}

func (r *redisRoom) unsubscribeRoomMessage(clientID ClientID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.subscribers, clientID)
	r.logger.Debugf("unsubscribe room pub/sub (room: %s, client: %s)", r.roomID, clientID)
}

func (r *redisRoom) delete(ctx context.Context) error {
	r.stopSub()
	if _, err := r.client.Del(ctx, redisRoomMembersKey(r.roomID)).Result(); err != nil {
		return err
	}
	r.logger.Infof("room deleted (room: %s)", r.roomID)
	return nil
}

type redisRoomManager struct {
	client         *redis.Client
	rooms          map[RoomID]*redisRoom
	mu             sync.Mutex
	logger         Logger
	roomExpiration time.Duration
}

func newRedisRoomManager(client *redis.Client, logger Logger, roomExpiration time.Duration) *redisRoomManager {
	return &redisRoomManager{
		client:         client,
		rooms:          map[RoomID]*redisRoom{},
		mu:             sync.Mutex{},
		logger:         logger,
		roomExpiration: roomExpiration,
	}
}

func (m *redisRoomManager) JoinRoom(ctx context.Context, roomID RoomID, clientID ClientID, otherClientExists bool) error {
	room, err := m.getRoom(roomID)
	if err != nil {
		return err
	}
	return room.join(ctx, clientID, otherClientExists)
}

func (m *redisRoomManager) LeaveRoom(ctx context.Context, roomID RoomID, clientID ClientID, otherClientExists bool) error {
	room, err := m.getRoom(roomID)
	if err != nil {
		return err
	}
	if err := room.leave(ctx, clientID, otherClientExists); err != nil {
		return err
	}
	if !otherClientExists {
		if err := m.DeleteRoom(ctx, roomID); err != nil {
			return fmt.Errorf("failed to delete room: %w", err)
		}
	}
	return nil
}

func (m *redisRoomManager) DeleteRoom(ctx context.Context, roomID RoomID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	room, ok := m.rooms[roomID]
	if !ok {
		return nil
	}
	defer delete(m.rooms, roomID)
	if err := room.delete(ctx); err != nil {
		return err
	}
	return nil
}

func (m *redisRoomManager) managingRooms() []RoomID {
	m.mu.Lock()
	defer m.mu.Unlock()
	var roomIDs []RoomID
	for rid := range m.rooms {
		roomIDs = append(roomIDs, rid)
	}
	return roomIDs
}

func (m *redisRoomManager) PublishMessage(ctx context.Context, roomID RoomID, rm *roomMessage) error {
	room, err := m.getRoom(roomID)
	if err != nil {
		return err
	}
	lock, err := room.beginLock(ctx)
	if err != nil {
		return err
	}
	defer lock.Unlock()
	otherClientExists := len(lock.clientIDs) > 1
	return room.publishMessages(ctx, []*roomMessage{rm}, otherClientExists)
}

func (m *redisRoomManager) SubscribeMessage(roomID RoomID, clientID ClientID) (<-chan *roomMessage, error) {
	room, err := m.getRoom(roomID)
	if err != nil {
		return nil, err
	}
	return room.subscribeRoomMessage(clientID), nil
}

func (m *redisRoomManager) UnsubscribeMessage(roomID RoomID, clientID ClientID) error {
	room, err := m.getRoom(roomID)
	if err != nil {
		return err
	}
	room.unsubscribeRoomMessage(clientID)
	return nil
}

func (m *redisRoomManager) BeginRoomLock(ctx context.Context, roomID RoomID) (*roomLock, error) {
	room, err := m.getRoom(roomID)
	if err != nil {
		return nil, err
	}
	return room.beginLock(ctx)
}

func (m *redisRoomManager) getRoom(roomID RoomID) (*redisRoom, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	room, ok := m.rooms[roomID]
	if ok {
		return room, nil
	}
	room, err := newRedisRoom(m.client, roomID, m.logger, m.roomExpiration)
	if err != nil {
		return nil, err
	}
	m.rooms[roomID] = room
	return room, nil
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
		time.Sleep(50 * time.Millisecond)
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

type publishBuffer struct {
	buf []*roomMessage
	mu  sync.Mutex
}

func newPublishBuffer() *publishBuffer {
	return &publishBuffer{
		buf: nil,
		mu:  sync.Mutex{},
	}
}

func (b *publishBuffer) Add(rms ...*roomMessage) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, rms...)
}

func (b *publishBuffer) Flush() []*roomMessage {
	b.mu.Lock()
	defer b.mu.Unlock()
	rms := b.buf
	b.buf = nil
	return rms
}
