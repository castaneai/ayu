package ayu

import (
	"context"
	"sync"
)

type forwarder struct {
	pubsubs *redisPubSubManager
	bufs    map[RoomID]*roomMessageBuffer
	mu      sync.RWMutex
	logger  Logger
}

func newForwarder(pubsubs *redisPubSubManager, logger Logger) *forwarder {
	return &forwarder{
		pubsubs: pubsubs,
		bufs:    map[RoomID]*roomMessageBuffer{},
		mu:      sync.RWMutex{},
		logger:  logger,
	}
}

func (f *forwarder) Forward(ctx context.Context, roomID RoomID, rm *roomMessage, otherClientExists bool) error {
	if !otherClientExists {
		if rm.Type == roomMessageTypeForward {
			f.getBuf(roomID).Add(rm)
			f.logger.Debugf("buffered message (room: %s): %+v", roomID, rm)
		}
		return nil
	}
	for _, rm := range append(f.getBuf(roomID).Flush(), rm) {
		if err := f.pubsubs.Publish(ctx, roomID, rm); err != nil {
			return err
		}
	}
	return nil
}

func (f *forwarder) ForwardBuffered(ctx context.Context, roomID RoomID, otherClientExists bool) error {
	if !otherClientExists {
		return nil
	}
	for _, rm := range f.getBuf(roomID).Flush() {
		if err := f.pubsubs.Publish(ctx, roomID, rm); err != nil {
			return err
		}
	}
	return nil
}

func (f *forwarder) getBuf(roomID RoomID) *roomMessageBuffer {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.bufs[roomID]
	if !ok {
		f.bufs[roomID] = newRoomMessageBuffer()
	}
	return f.bufs[roomID]
}

type roomMessageBuffer struct {
	buf []*roomMessage
	mu  sync.Mutex
}

func newRoomMessageBuffer() *roomMessageBuffer {
	return &roomMessageBuffer{
		buf: nil,
		mu:  sync.Mutex{},
	}
}

func (b *roomMessageBuffer) Add(rms ...*roomMessage) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, rms...)
}

func (b *roomMessageBuffer) Flush() []*roomMessage {
	b.mu.Lock()
	defer b.mu.Unlock()
	rms := b.buf
	b.buf = nil
	return rms
}
