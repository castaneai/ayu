package ayu

import (
	"encoding/json"
	"log"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/castaneai/ayu/internal/testutils"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"golang.org/x/net/websocket"
)

func TestServer(t *testing.T) {
	singleDialer := &singleInstanceDialer{sv: newTestServer(t)}
	multiDialer := &multiInstanceDialer{sv1: newTestServer(t), sv2: newTestServer(t)}

	testCases := []struct {
		name   string
		dialer dialer
	}{
		// Connect to a single room with a single ayu instance
		{name: "single instance", dialer: singleDialer},

		// Connect to a single room with different ayu instances
		{name: "multi instance", dialer: multiDialer},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("register one accepted", func(t *testing.T) {
				roomID := newRandomRoomID()
				conn1, recv1 := tc.dialer.DialClient1(t)
				assert.NoError(t, websocket.JSON.Send(conn1, &RegisterMessage{
					Type:     MessageTypeRegister,
					RoomID:   roomID,
					ClientID: "client1",
				}))
				msg := testutils.MustReceiveChan(t, recv1, 3*time.Second).(*message)
				assert.Equal(t, MessageTypeAccept, msg.Type)
				var accepted AcceptMessage
				mustUnpackMessage(t, msg, &accepted)
				assert.Equal(t, false, accepted.IsExistClient)
			})

			t.Run("register two accepted", func(t *testing.T) {
				roomID := newRandomRoomID()
				conn1, recv1 := tc.dialer.DialClient1(t)
				assert.NoError(t, websocket.JSON.Send(conn1, &RegisterMessage{
					Type:     MessageTypeRegister,
					RoomID:   roomID,
					ClientID: "client1",
				}))
				assert.Equal(t, MessageTypeAccept, testutils.MustReceiveChan(t, recv1, 3*time.Second).(*message).Type)

				conn2, recv2 := tc.dialer.DialClient2(t)
				assert.NoError(t, websocket.JSON.Send(conn2, &RegisterMessage{
					Type:     MessageTypeRegister,
					RoomID:   roomID,
					ClientID: "client2",
				}))
				assert.Equal(t, MessageTypeAccept, testutils.MustReceiveChan(t, recv2, 3*time.Second).(*message).Type)
			})

			t.Run("register rejected due to full", func(t *testing.T) {
				roomID := newRandomRoomID()
				conn1, recv1 := tc.dialer.DialClient1(t)
				assert.NoError(t, websocket.JSON.Send(conn1, &RegisterMessage{
					Type:     MessageTypeRegister,
					RoomID:   roomID,
					ClientID: "client1",
				}))
				assert.Equal(t, MessageTypeAccept, testutils.MustReceiveChan(t, recv1, 3*time.Second).(*message).Type)

				conn2, recv2 := tc.dialer.DialClient2(t)
				assert.NoError(t, websocket.JSON.Send(conn2, &RegisterMessage{
					Type:     MessageTypeRegister,
					RoomID:   roomID,
					ClientID: "client2",
				}))
				assert.Equal(t, MessageTypeAccept, testutils.MustReceiveChan(t, recv2, 3*time.Second).(*message).Type)

				conn3, recv3 := tc.dialer.DialClient1(t)
				assert.NoError(t, websocket.JSON.Send(conn3, &RegisterMessage{
					Type:     MessageTypeRegister,
					RoomID:   roomID,
					ClientID: "client3",
				}))
				msg := testutils.MustReceiveChan(t, recv3, 3*time.Second).(*message)
				assert.Equal(t, MessageTypeReject, msg.Type)
				var rj RejectMessage
				mustUnpackMessage(t, msg, &rj)
				assert.Equal(t, "full", rj.Reason)
			})

			t.Run("signaling", func(t *testing.T) {
				roomID := newRandomRoomID()
				conn1, recv1 := tc.dialer.DialClient1(t)
				assert.NoError(t, websocket.JSON.Send(conn1, &RegisterMessage{
					Type:     MessageTypeRegister,
					RoomID:   roomID,
					ClientID: "client1",
				}))
				assert.Equal(t, MessageTypeAccept, testutils.MustReceiveChan(t, recv1, 3*time.Second).(*message).Type)
				// Possibly send candidate, before the other client joins.
				assert.NoError(t, websocket.JSON.Send(conn1, &CandidateMessage{
					Type:         MessageTypeCandidate,
					ICECandidate: &ICECandidateInit{Candidate: "test-candidate"},
				}))

				conn2, recv2 := tc.dialer.DialClient2(t)
				assert.NoError(t, websocket.JSON.Send(conn2, &RegisterMessage{
					Type:     MessageTypeRegister,
					RoomID:   roomID,
					ClientID: "client2",
				}))
				assert.Equal(t, MessageTypeAccept, testutils.MustReceiveChan(t, recv2, 3*time.Second).(*message).Type)

				assert.NoError(t, websocket.JSON.Send(conn2, &sdp{
					Type: "offer",
					SDP:  "test",
				}))
				assert.Equal(t, MessageTypeCandidate, testutils.MustReceiveChan(t, recv2, 3*time.Second).(*message).Type)
				assert.Equal(t, MessageTypeOffer, testutils.MustReceiveChan(t, recv1, 3*time.Second).(*message).Type)
				assert.NoError(t, websocket.JSON.Send(conn1, &sdp{
					Type: "answer",
					SDP:  "test",
				}))
				assert.Equal(t, MessageTypeAnswer, testutils.MustReceiveChan(t, recv2, 3*time.Second).(*message).Type)
			})

			t.Run("active close", func(t *testing.T) {
				roomID := newRandomRoomID()
				conn1, recv1 := tc.dialer.DialClient1(t)
				assert.NoError(t, websocket.JSON.Send(conn1, &RegisterMessage{
					Type:     MessageTypeRegister,
					RoomID:   roomID,
					ClientID: "client1",
				}))
				assert.Equal(t, MessageTypeAccept, testutils.MustReceiveChan(t, recv1, 3*time.Second).(*message).Type)

				conn2, recv2 := tc.dialer.DialClient2(t)
				assert.NoError(t, websocket.JSON.Send(conn2, &RegisterMessage{
					Type:     MessageTypeRegister,
					RoomID:   roomID,
					ClientID: "client2",
				}))
				assert.Equal(t, MessageTypeAccept, testutils.MustReceiveChan(t, recv2, 3*time.Second).(*message).Type)

				// active close by client1
				assert.NoError(t, conn1.Close())

				// When the other client unregistered, the room is destroyed and bye is received.
				assert.Equal(t, MessageTypeBye, testutils.MustReceiveChan(t, recv2, 3*time.Second).(*message).Type)
			})

			t.Run("re-joining to the same room", func(t *testing.T) {
				roomID := newRandomRoomID()
				conn1, recv1 := tc.dialer.DialClient1(t)
				assert.NoError(t, websocket.JSON.Send(conn1, &RegisterMessage{
					Type:     MessageTypeRegister,
					RoomID:   roomID,
					ClientID: "client1",
				}))
				assert.Equal(t, MessageTypeAccept, testutils.MustReceiveChan(t, recv1, 3*time.Second).(*message).Type)

				conn2, recv2 := tc.dialer.DialClient2(t)
				assert.NoError(t, websocket.JSON.Send(conn2, &RegisterMessage{
					Type:     MessageTypeRegister,
					RoomID:   roomID,
					ClientID: "client2",
				}))
				assert.Equal(t, MessageTypeAccept, testutils.MustReceiveChan(t, recv2, 3*time.Second).(*message).Type)

				assert.NoError(t, conn2.Close())
				assert.Equal(t, MessageTypeBye, testutils.MustReceiveChan(t, recv1, 3*time.Second).(*message).Type)

				conn1, recv1 = tc.dialer.DialClient1(t)
				assert.NoError(t, websocket.JSON.Send(conn1, &RegisterMessage{
					Type:     MessageTypeRegister,
					RoomID:   roomID,
					ClientID: "client1",
				}))
				assert.Equal(t, MessageTypeAccept, testutils.MustReceiveChan(t, recv1, 3*time.Second).(*message).Type)
				assert.NoError(t, websocket.JSON.Send(conn1, &CandidateMessage{
					Type:         MessageTypeCandidate,
					ICECandidate: &ICECandidateInit{Candidate: "test-candidate"},
				}))

				conn2, recv2 = tc.dialer.DialClient2(t)
				assert.NoError(t, websocket.JSON.Send(conn2, &RegisterMessage{
					Type:     MessageTypeRegister,
					RoomID:   roomID,
					ClientID: "client2",
				}))
				assert.Equal(t, MessageTypeAccept, testutils.MustReceiveChan(t, recv2, 3*time.Second).(*message).Type)

				assert.NoError(t, websocket.JSON.Send(conn2, &sdp{
					Type: "offer",
					SDP:  "test",
				}))
				assert.Equal(t, MessageTypeCandidate, testutils.MustReceiveChan(t, recv2, 3*time.Second).(*message).Type)
				assert.Equal(t, MessageTypeOffer, testutils.MustReceiveChan(t, recv1, 3*time.Second).(*message).Type)
				assert.NoError(t, websocket.JSON.Send(conn1, &sdp{
					Type: "answer",
					SDP:  "test",
				}))
				assert.Equal(t, MessageTypeAnswer, testutils.MustReceiveChan(t, recv2, 3*time.Second).(*message).Type)
			})

			t.Run("multi rooms", func(t *testing.T) {
				type roomCase struct {
					roomID    RoomID
					client1ID ClientID
					client2ID ClientID
				}
				roomCases := []roomCase{
					{roomID: newRandomRoomID(), client1ID: "client1-1", client2ID: "client1-2"},
					{roomID: newRandomRoomID(), client1ID: "client2-1", client2ID: "client2-2"},
				}
				var wg sync.WaitGroup
				for _, rc := range roomCases {
					wg.Add(1)
					go func(rc roomCase) {
						defer wg.Done()
						conn1, recv1 := tc.dialer.DialClient1(t)
						assert.NoError(t, websocket.JSON.Send(conn1, &RegisterMessage{
							Type:     MessageTypeRegister,
							RoomID:   rc.roomID,
							ClientID: rc.client1ID,
						}))
						assert.Equal(t, MessageTypeAccept, testutils.MustReceiveChan(t, recv1, 3*time.Second).(*message).Type)
						assert.NoError(t, websocket.JSON.Send(conn1, &CandidateMessage{
							Type:         MessageTypeCandidate,
							ICECandidate: &ICECandidateInit{Candidate: string(rc.roomID)},
						}))

						conn2, recv2 := tc.dialer.DialClient2(t)
						assert.NoError(t, websocket.JSON.Send(conn2, &RegisterMessage{
							Type:     MessageTypeRegister,
							RoomID:   rc.roomID,
							ClientID: rc.client2ID,
						}))
						assert.Equal(t, MessageTypeAccept, testutils.MustReceiveChan(t, recv2, 3*time.Second).(*message).Type)

						assert.NoError(t, websocket.JSON.Send(conn2, &sdp{
							Type: "offer",
							SDP:  string(rc.roomID),
						}))
						msg := testutils.MustReceiveChan(t, recv2, 3*time.Second).(*message)
						assert.Equal(t, MessageTypeCandidate, msg.Type)
						var cand CandidateMessage
						mustUnpackMessage(t, msg, &cand)
						assert.Equal(t, string(rc.roomID), cand.ICECandidate.Candidate)

						msg = testutils.MustReceiveChan(t, recv1, 3*time.Second).(*message)
						assert.Equal(t, MessageTypeOffer, msg.Type)
						var offer sdp
						mustUnpackMessage(t, msg, &offer)
						assert.Equal(t, string(rc.roomID), offer.SDP)

						assert.NoError(t, websocket.JSON.Send(conn1, &sdp{
							Type: "answer",
							SDP:  string(rc.roomID),
						}))
						msg = testutils.MustReceiveChan(t, recv2, 3*time.Second).(*message)
						assert.Equal(t, MessageTypeAnswer, msg.Type)
						var answer sdp
						mustUnpackMessage(t, msg, &answer)
						assert.Equal(t, string(rc.roomID), answer.SDP)
					}(rc)
				}
				wg.Wait()
			})
		})
	}
}

func mustUnpackMessage(t *testing.T, msg *message, v interface{}) {
	if err := json.Unmarshal(msg.Payload, v); err != nil {
		t.Fatalf("failed to unmashal %s: %+v", string(msg.Payload), err)
	}
}

type dialer interface {
	DialClient1(t *testing.T) (*websocket.Conn, <-chan *message)
	DialClient2(t *testing.T) (*websocket.Conn, <-chan *message)
}

type singleInstanceDialer struct {
	sv *testServer
}

func (d *singleInstanceDialer) DialClient1(t *testing.T) (*websocket.Conn, <-chan *message) {
	return d.sv.Dial(t)
}

func (d *singleInstanceDialer) DialClient2(t *testing.T) (*websocket.Conn, <-chan *message) {
	return d.sv.Dial(t)
}

type multiInstanceDialer struct {
	sv1 *testServer
	sv2 *testServer
}

func (d *multiInstanceDialer) DialClient1(t *testing.T) (*websocket.Conn, <-chan *message) {
	return d.sv1.Dial(t)
}

func (d *multiInstanceDialer) DialClient2(t *testing.T) (*websocket.Conn, <-chan *message) {
	return d.sv2.Dial(t)
}

type testServer struct {
	*httptest.Server
}

func (ts *testServer) Dial(t *testing.T) (*websocket.Conn, <-chan *message) {
	wsURL := strings.Replace(ts.URL, "http://", "ws://", 1)
	conn, err := websocket.Dial(wsURL, "", wsURL)
	if err != nil {
		t.Fatalf("failed to dial to %s: %+v", wsURL, err)
	}
	onForward := make(chan *message)
	go func() {
		for {
			var msg message
			if err := websocket.JSON.Receive(conn, &msg); err != nil {
				if !isClosedError(err) {
					log.Printf("failed to receive message: %+v", err)
				}
				return
			}
			switch msg.Type {
			case MessageTypePing:
				_ = websocket.JSON.Send(conn, &PingPongMessage{Type: MessageTypePong})
			default:
				onForward <- &msg
			}
		}
	}()
	return conn, onForward
}

func newTestServer(t *testing.T) *testServer {
	sv := NewServer(newTestRedisClient())
	t.Cleanup(sv.Shutdown)
	hs := httptest.NewServer(sv)
	t.Cleanup(hs.Close)
	return &testServer{Server: hs}
}

func newTestRedisClient() *redis.Client {
	return redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
}

func newRandomRoomID() RoomID {
	return RoomID(uuid.Must(uuid.NewRandom()).String())
}

type sdp struct {
	Type string `json:"type"`
	SDP  string `json:"sdp"`
}
