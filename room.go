package ayu

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
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
	errRoomIsFull = errors.New("room is full")
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

type roomLock struct {
	roomID RoomID
	mu     *redisMutex
	logger Logger
}

func newRoomLock(mu *redisMutex, roomID RoomID, logger Logger) *roomLock {
	return &roomLock{
		roomID: roomID,
		mu:     mu,
		logger: logger,
	}
}

func (l *roomLock) Unlock() {
	if err := l.mu.Unlock(); err != nil {
		l.logger.Errorf("failed to unlock room (room: %s): %+v", l.roomID, err)
	}
}

type redisRoomManager struct {
	client         *redis.Client
	logger         Logger
	roomExpiration time.Duration
}

func newRedisRoomManager(client *redis.Client, logger Logger, roomExpiration time.Duration) *redisRoomManager {
	return &redisRoomManager{
		client:         client,
		logger:         logger,
		roomExpiration: roomExpiration,
	}
}

func (m *redisRoomManager) JoinRoom(ctx context.Context, roomID RoomID, clientID ClientID) (bool, error) {
	numClients, err := m.CountClients(ctx, roomID)
	if err != nil {
		return false, err
	}
	if numClients >= roomMaxMembers {
		return false, errRoomIsFull
	}
	otherClientExists := numClients > 0

	key := redisRoomMembersKey(roomID)
	if _, err := m.client.SAdd(ctx, key, string(clientID)).Result(); err != nil {
		return false, fmt.Errorf("failed to exec SADD for %s: %w", key, err)
	}
	if _, err := m.client.Expire(ctx, key, m.roomExpiration).Result(); err != nil {
		return false, fmt.Errorf("failed to exec EXPIRE for %s: %w", key, err)
	}
	return otherClientExists, nil
}

func (m *redisRoomManager) LeaveRoom(ctx context.Context, roomID RoomID, clientID ClientID) (bool, error) {
	key := redisRoomMembersKey(roomID)
	reply, err := m.client.Exists(ctx, key).Result()
	if err != nil {
		return false, err
	}
	roomExists := reply == 1
	if !roomExists {
		return false, nil
	}
	if _, err := m.client.SRem(ctx, key, string(clientID)).Result(); err != nil {
		return false, fmt.Errorf("failed to exec SREM for %s: %w", key, err)
	}
	numClients, err := m.CountClients(ctx, roomID)
	if err != nil {
		return false, err
	}
	otherClientExists := numClients > 0
	// If one client leaves the room, the room will be deleted.
	if err := m.DeleteRoom(ctx, roomID); err != nil {
		return false, fmt.Errorf("failed to delete room: %w", err)
	}
	return otherClientExists, nil
}

func (m *redisRoomManager) DeleteRoom(ctx context.Context, roomID RoomID) error {
	if _, err := m.client.Del(ctx, redisRoomMembersKey(roomID)).Result(); err != nil {
		return err
	}
	m.logger.Infof("room deleted (room: %s)", roomID)
	return nil
}

func (m *redisRoomManager) BeginRoomLock(roomID RoomID) (*roomLock, error) {
	mu := newRedisMutex(m.client, redisRoomLockKey(roomID), roomLockExpiration, redisOperationTimeout)
	if err := mu.Lock(); err != nil {
		return nil, err
	}
	return newRoomLock(mu, roomID, m.logger), nil
}

func (m *redisRoomManager) CountClients(ctx context.Context, roomID RoomID) (int, error) {
	key := redisRoomMembersKey(roomID)
	members, err := m.client.SMembers(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("failed to exec SMEMBERS for %s: %w", key, err)
	}
	return len(members), nil
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
