package ayu

import (
	"context"

	"golang.org/x/net/websocket"
)

func readJSONMessage(ctx context.Context, conn *websocket.Conn, v interface{}) error {
	result := make(chan error)
	go func() {
		result <- websocket.JSON.Receive(conn, v)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-result:
		if err != nil {
			return err
		}
		return nil
	}
}

func writeMessage(ctx context.Context, conn *websocket.Conn, v interface{}, codec websocket.Codec) error {
	result := make(chan error)
	go func() {
		result <- codec.Send(conn, v)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-result:
		if err != nil {
			return err
		}
		return nil
	}
}

func writeJSONMessage(ctx context.Context, conn *websocket.Conn, v interface{}) error {
	return writeMessage(ctx, conn, v, websocket.JSON)
}

func writeTextMessage(ctx context.Context, conn *websocket.Conn, v interface{}) error {
	return writeMessage(ctx, conn, v, websocket.Message)
}
