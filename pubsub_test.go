package ayu

import (
	"context"
	"testing"
	"time"

	"github.com/castaneai/ayu/internal/testutils"
	"github.com/stretchr/testify/assert"
)

func TestRedisPubSub(t *testing.T) {
	rd := newTestRedisClient()
	roomID := newRandomRoomID()
	logger := newDefaultLogger()
	p, err := newRedisPubSub(rd, roomID, logger)
	assert.NoError(t, err)

	assert.Equal(t, 0, p.CountSubscribers())
	client1ID := ClientID("client1")
	sub1 := p.Subscribe(client1ID)

	client2ID := ClientID("client2")
	sub2 := p.Subscribe(client2ID)

	ctx := context.Background()
	assert.NoError(t, p.Publish(ctx, &roomMessage{Sender: client1ID, Type: roomMessageTypeJoin}))
	msg := testutils.MustReceiveChan(t, sub2, 3*time.Second).(*roomMessage)
	assert.Equal(t, client1ID, msg.Sender)
	assert.Equal(t, roomMessageTypeJoin, msg.Type)

	assert.NoError(t, p.Publish(ctx, &roomMessage{Sender: client2ID, Type: roomMessageTypeLeave}))
	msg = testutils.MustReceiveChan(t, sub1, 3*time.Second).(*roomMessage)
	assert.Equal(t, client2ID, msg.Sender)
	assert.Equal(t, roomMessageTypeLeave, msg.Type)

	p.Unsubscribe(client1ID)
	p.Unsubscribe(client2ID)
	p.Close()
}
