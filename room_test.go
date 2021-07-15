package ayu

import (
	"context"
	"testing"
	"time"

	"github.com/castaneai/ayu/internal/testutils"
	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestRedisRoom(t *testing.T) {
	rd1 := newTestRedisClient()
	rd2 := newTestRedisClient()
	logger, err := newDefaultLogger()
	assert.NoError(t, err)
	roomManager1 := newRedisRoomManager(rd1, logger)
	roomManager2 := newRedisRoomManager(rd2, logger)
	ctx := context.Background()
	roomID := newRandomRoomID()

	onForward1 := roomManager1.SubscribeForwardMessage(roomID)
	onLeave2 := roomManager1.SubscribeLeaveRoom(roomID)
	onForward2 := roomManager1.SubscribeForwardMessage(roomID)

	client1ID := newRandomClientID()
	joinResp, err := roomManager1.JoinRoom(ctx, roomID, client1ID)
	assert.NoError(t, err)
	assert.False(t, joinResp.OtherClientExists)

	assert.NoError(t, roomManager1.ForwardMessage(ctx, roomID, &roomMessage{
		Sender:  client1ID,
		Type:    roomMessageTypeForward,
		Payload: "hello",
	}))

	client2ID := newRandomClientID()
	joinResp, err = roomManager2.JoinRoom(ctx, roomID, client2ID)
	assert.NoError(t, err)
	assert.True(t, joinResp.OtherClientExists)

	client3ID := newRandomClientID()
	_, err = roomManager2.JoinRoom(ctx, roomID, client3ID)
	assert.Error(t, ErrRoomIsFull)

	assert.NoError(t, roomManager1.ForwardMessage(ctx, roomID, &roomMessage{
		Sender:  client1ID,
		Type:    roomMessageTypeForward,
		Payload: "world",
	}))

	for _, expected := range []string{"hello", "world"} {
		fmsg := testutils.MustReceiveChan(t, onForward2, 3*time.Second).(*roomMessage)
		assert.Equal(t, client1ID, fmsg.Sender)
		assert.Equal(t, roomMessageTypeForward, fmsg.Type)
		assert.Equal(t, expected, fmsg.Payload)
	}

	assert.NoError(t, roomManager2.ForwardMessage(ctx, roomID, &roomMessage{
		Sender:  client2ID,
		Type:    roomMessageTypeForward,
		Payload: "foo",
	}))
	fmsg := testutils.MustReceiveChan(t, onForward1, 3*time.Second).(*roomMessage)
	assert.Equal(t, client2ID, fmsg.Sender)
	assert.Equal(t, roomMessageTypeForward, fmsg.Type)
	assert.Equal(t, "foo", fmsg.Payload)

	leaveResp, err := roomManager1.LeaveRoom(ctx, roomID, client1ID)
	assert.NoError(t, err)
	assert.True(t, leaveResp.OtherClientExists)
	testutils.MustReceiveChan(t, onLeave2, 3*time.Second)
}

func newTestRedisClient() *redis.Client {
	return redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
}

func newRandomRoomID() RoomID {
	return RoomID(uuid.Must(uuid.NewRandom()).String())
}

func newRandomClientID() ClientID {
	return ClientID(uuid.Must(uuid.NewRandom()).String())
}
