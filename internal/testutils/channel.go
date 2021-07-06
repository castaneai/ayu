package testutils

import (
	"reflect"
	"testing"
	"time"
)

func MustSendChan(t *testing.T, channel, value interface{}, timeout time.Duration) {
	t.Helper()

	cases := []reflect.SelectCase{
		{Dir: reflect.SelectSend, Chan: reflect.ValueOf(channel), Send: reflect.ValueOf(value)},
		{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(time.After(timeout))},
	}
	chosen, _, _ := reflect.Select(cases)
	if chosen == 1 { // index 1: timeout
		t.Fatalf("channel send timed out")
	}
}

func MustReceiveChan(t *testing.T, channel interface{}, timeout time.Duration) interface{} {
	t.Helper()

	cases := []reflect.SelectCase{
		{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(channel)},
		{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(time.After(timeout))},
	}
	chosen, value, _ := reflect.Select(cases)
	switch chosen {
	case 0: // received from channel
		return value.Interface()
	case 1: // timeout
		t.Fatalf("channel receive timed out")
	}
	// channel closed
	return nil
}
