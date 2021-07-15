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

	onLeave1 := roomManager1.SubscribeLeaveRoom(roomID)
	onForward1 := roomManager1.SubscribeForwardMessage(roomID)
	onLeave2 := roomManager2.SubscribeLeaveRoom(roomID)
	onForward2 := roomManager2.SubscribeForwardMessage(roomID)

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

	for _, expected := range []struct {
		sender  ClientID
		payload string
	}{
		{sender: client1ID, payload: "hello"},
		{sender: client1ID, payload: "world"},
	} {
		fmsg := testutils.MustReceiveChan(t, onForward2, 3*time.Second).(*roomMessage)
		assert.Equal(t, expected.sender, fmsg.Sender)
		assert.Equal(t, roomMessageTypeForward, fmsg.Type)
		assert.Equal(t, expected.payload, fmsg.Payload)
	}

	assert.NoError(t, roomManager2.ForwardMessage(ctx, roomID, &roomMessage{
		Sender:  client2ID,
		Type:    roomMessageTypeForward,
		Payload: "foo",
	}))

	for _, expected := range []struct {
		sender  ClientID
		payload string
	}{
		{sender: client1ID, payload: "hello"},
		{sender: client1ID, payload: "world"},
		{sender: client2ID, payload: "foo"},
	} {
		fmsg := testutils.MustReceiveChan(t, onForward1, 3*time.Second).(*roomMessage)
		assert.Equal(t, expected.sender, fmsg.Sender)
		assert.Equal(t, roomMessageTypeForward, fmsg.Type)
		assert.Equal(t, expected.payload, fmsg.Payload)
	}

	leaveResp, err := roomManager1.LeaveRoom(ctx, roomID, client1ID)
	assert.NoError(t, err)
	assert.True(t, leaveResp.OtherClientExists)
	testutils.MustReceiveChan(t, onLeave2, 3*time.Second)

	leaveResp, err = roomManager2.LeaveRoom(ctx, roomID, client2ID)
	assert.NoError(t, err)
	assert.False(t, leaveResp.OtherClientExists)
	testutils.MustReceiveChan(t, onLeave1, 3*time.Second)
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
