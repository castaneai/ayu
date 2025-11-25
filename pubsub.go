package ayu

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	subscriberChBufSize = 200
)

func redisRoomPubSubKey(roomID RoomID) string {
	return fmt.Sprintf("ayu:room:pubsub:%s", roomID)
}

type redisPubSub struct {
	redisClient *redis.Client
	roomID      RoomID
	subs        map[ClientID]chan *roomMessage
	mu          sync.RWMutex
	logger      Logger
	stop        context.CancelFunc
	stopped     chan struct{}
}

func newRedisPubSub(redisClient *redis.Client, roomID RoomID, logger Logger) (*redisPubSub, error) {
	ctx, stop := context.WithCancel(context.Background())
	p := &redisPubSub{
		redisClient: redisClient,
		roomID:      roomID,
		subs:        map[ClientID]chan *roomMessage{},
		mu:          sync.RWMutex{},
		logger:      logger,
		stop:        stop,
		stopped:     make(chan struct{}),
	}
	if err := p.startReceive(ctx); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *redisPubSub) Publish(ctx context.Context, rm *roomMessage) error {
	b, err := json.Marshal(rm)
	if err != nil {
		return fmt.Errorf("failed to marshal room message: %w", err)
	}
	if _, err := p.redisClient.Publish(ctx, redisRoomPubSubKey(p.roomID), string(b)).Result(); err != nil {
		return err
	}
	p.logger.Debugf("published message (room: %s): %+v", p.roomID, rm)
	return nil
}

func (p *redisPubSub) Subscribe(clientID ClientID) <-chan *roomMessage {
	subCh := make(chan *roomMessage, subscriberChBufSize)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.subs[clientID] = subCh
	p.logger.Debugf("subscribe (room: %s, client: %s)", p.roomID, clientID)
	return subCh
}

func (p *redisPubSub) Unsubscribe(clientID ClientID) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.subs, clientID)
	p.logger.Debugf("unsubscribe (room: %s, client: %s)", p.roomID, clientID)
}

func (p *redisPubSub) CountSubscribers() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.subs)
}

func (p *redisPubSub) Close() {
	p.stop()
	<-p.stopped
	p.logger.Debugf("pub/sub connection was closed (room: %s)", p.roomID)
}

func (p *redisPubSub) startReceive(ctx context.Context) error {
	subscribeOpened := make(chan struct{})
	go func() {
		sub := p.redisClient.Subscribe(ctx, redisRoomPubSubKey(p.roomID))
		go func() {
			<-ctx.Done()
			rctx, cancel := context.WithTimeout(context.Background(), redisOperationTimeout)
			defer cancel()
			if err := sub.Unsubscribe(rctx, redisRoomPubSubKey(p.roomID)); err != nil {
				p.logger.Errorf("failed to unsubscribe redis pub/sub (room: %s): %+v", p.roomID, err)
			}
			p.logger.Debugf("closing redis pub/sub subscription (room: %s)", p.roomID)
			if err := sub.Close(); err != nil {
				p.logger.Errorf("failed to close redis pub/sub (room: %s): %+v", p.roomID, err)
			}
			close(p.stopped)
		}()
		var once sync.Once
		for {
			recv, err := sub.Receive(ctx)
			if err != nil {
				if ctx.Err() == nil {
					p.logger.Errorf("failed to subscribe from redis (room: %s): %+v", p.roomID, err)
				}
				return
			}
			switch m := recv.(type) {
			case *redis.Subscription:
				once.Do(func() { close(subscribeOpened) })
			case *redis.Message:
				var rm roomMessage
				if err := json.Unmarshal([]byte(m.Payload), &rm); err != nil {
					p.logger.Errorf("failed to unmarshal subscribed room message (room: %s): %+v", p.roomID, err)
					continue
				}
				p.handleRoomMessage(&rm)
			case *redis.Pong:
				// ignore
			default:
				p.logger.Warnf("unknown message received from pub/sub (room: %s): %+v", p.roomID, m)
			}
		}
	}()
	select {
	case <-subscribeOpened:
		p.logger.Debugf("pub/sub connection is opened (room: %s)", p.roomID)
		return nil
	case <-time.After(redisOperationTimeout):
		return fmt.Errorf("opening pub/sub connection timeout(%v)", redisOperationTimeout)
	}
}

func (p *redisPubSub) handleRoomMessage(rm *roomMessage) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for target, subCh := range p.subs {
		if rm.Sender == target {
			continue
		}
		select {
		case subCh <- rm:
		default:
			p.logger.Warnf("failed to forward message from room because channel is full (room: %s, target: %s): %+v",
				p.roomID, target, rm)
		}
	}
}

type redisPubSubManager struct {
	redisClient *redis.Client
	pubsubs     map[RoomID]*redisPubSub
	mu          sync.RWMutex
	logger      Logger
}

func newRedisPubSubManager(redisClient *redis.Client, logger Logger) *redisPubSubManager {
	return &redisPubSubManager{
		redisClient: redisClient,
		pubsubs:     map[RoomID]*redisPubSub{},
		mu:          sync.RWMutex{},
		logger:      logger,
	}
}

func (m *redisPubSubManager) Publish(ctx context.Context, roomID RoomID, rm *roomMessage) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.pubsubs[roomID]
	if !ok {
		return fmt.Errorf("pub/sub not found (room: %s)", roomID)
	}
	if err := p.Publish(ctx, rm); err != nil {
		return err
	}
	return nil
}

func (m *redisPubSubManager) Subscribe(roomID RoomID, clientID ClientID) (<-chan *roomMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.pubsubs[roomID]
	if !ok {
		p, err := newRedisPubSub(m.redisClient, roomID, m.logger)
		if err != nil {
			return nil, fmt.Errorf("failed to open redis pub/sub (room: %s, client: %s): %+v", roomID, clientID, err)
		}
		m.pubsubs[roomID] = p
	}
	return m.pubsubs[roomID].Subscribe(clientID), nil
}

func (m *redisPubSubManager) Unsubscribe(roomID RoomID, clientID ClientID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.pubsubs[roomID]
	if !ok {
		return
	}
	p.Unsubscribe(clientID)
	if p.CountSubscribers() == 0 {
		p.Close()
		delete(m.pubsubs, roomID)
	}
}
