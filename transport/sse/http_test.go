package sse

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPStreamHandler(t *testing.T) {
	s := NewServer(
		WithAddress(":8800"),
	)
	defer s.Stop(nil)

	mux := http.NewServeMux()
	mux.HandleFunc("/events", s.ServeHTTP)
	server := httptest.NewServer(mux)

	s.CreateStream("test")

	c := NewClient(server.URL + "/events")

	events := make(chan *Event)
	var cErr error
	go func() {
		cErr = c.Subscribe("test", func(msg *Event) {
			if msg.Data != nil {
				events <- msg
				return
			}
		})
	}()

	time.Sleep(time.Millisecond * 200)
	require.Nil(t, cErr)
	s.Publish("test", &Event{Data: []byte("test")})

	msg, err := wait(events, time.Millisecond*500)
	require.Nil(t, err)
	assert.Equal(t, []byte(`test`), msg)
}

func TestHTTPStreamHandlerExistingEvents(t *testing.T) {
	s := NewServer(
		WithAddress(":8800"),
	)
	defer s.Stop(nil)

	mux := http.NewServeMux()
	mux.HandleFunc("/events", s.ServeHTTP)
	server := httptest.NewServer(mux)

	s.CreateStream("test")

	s.Publish("test", &Event{Data: []byte("test 1")})
	s.Publish("test", &Event{Data: []byte("test 2")})
	s.Publish("test", &Event{Data: []byte("test 3")})

	time.Sleep(time.Millisecond * 100)

	c := NewClient(server.URL + "/events")

	events := make(chan *Event)
	var cErr error
	go func() {
		cErr = c.Subscribe("test", func(msg *Event) {
			if len(msg.Data) > 0 {
				events <- msg
			}
		})
	}()

	require.Nil(t, cErr)

	for i := 1; i <= 3; i++ {
		msg, err := wait(events, time.Millisecond*500)
		require.Nil(t, err)
		assert.Equal(t, []byte("test "+strconv.Itoa(i)), msg)
	}
}

func TestHTTPStreamHandlerEventID(t *testing.T) {
	s := NewServer(
		WithAddress(":8800"),
	)
	defer s.Stop(nil)

	mux := http.NewServeMux()
	mux.HandleFunc("/events", s.ServeHTTP)
	server := httptest.NewServer(mux)

	s.CreateStream("test")

	s.Publish("test", &Event{Data: []byte("test 1")})
	s.Publish("test", &Event{Data: []byte("test 2")})
	s.Publish("test", &Event{Data: []byte("test 3")})

	time.Sleep(time.Millisecond * 100)

	c := NewClient(server.URL + "/events")
	c.LastEventID.Store([]byte("2"))

	events := make(chan *Event)
	var cErr error
	go func() {
		cErr = c.Subscribe("test", func(msg *Event) {
			if len(msg.Data) > 0 {
				events <- msg
			}
		})
	}()

	require.Nil(t, cErr)

	msg, err := wait(events, time.Millisecond*500)
	require.Nil(t, err)
	assert.Equal(t, []byte("test 3"), msg)
}

func TestHTTPStreamHandlerEventTTL(t *testing.T) {
	s := NewServer(
		WithAddress(":8800"),
	)
	defer s.Stop(nil)

	s.eventTTL = time.Second * 1

	mux := http.NewServeMux()
	mux.HandleFunc("/events", s.ServeHTTP)
	server := httptest.NewServer(mux)

	s.CreateStream("test")

	s.Publish("test", &Event{Data: []byte("test 1")})
	s.Publish("test", &Event{Data: []byte("test 2")})
	time.Sleep(time.Second * 2)
	s.Publish("test", &Event{Data: []byte("test 3")})

	time.Sleep(time.Millisecond * 100)

	c := NewClient(server.URL + "/events")

	events := make(chan *Event)
	var cErr error
	go func() {
		cErr = c.Subscribe("test", func(msg *Event) {
			if len(msg.Data) > 0 {
				events <- msg
			}
		})
	}()

	require.Nil(t, cErr)

	msg, err := wait(events, time.Millisecond*500)
	require.Nil(t, err)
	assert.Equal(t, []byte("test 3"), msg)
}

func TestHTTPStreamHandlerHeaderFlushIfNoEvents(t *testing.T) {
	ctx := context.Background()

	//go func() {
	s := NewServer(
		WithAddress(":8800"),
	)
	defer s.Stop(ctx)

	s.HandleServeHTTP("/events")
	s.CreateStream("test")

	s.Start(ctx)
	//}()

	c := NewClient("localhost:8800/events")

	subscribed := make(chan struct{})
	events := make(chan *Event)
	go func() {
		assert.NoError(t, c.SubscribeChan("test", events))
		subscribed <- struct{}{}
	}()

	select {
	case <-subscribed:
	case <-time.After(1000 * time.Millisecond):
		assert.Fail(t, "Subscribe should returned in 100 milliseconds")
	}
}

func TestHTTPStreamHandlerAutoStream(t *testing.T) {
	t.Parallel()

	sseServer := NewServer()
	defer sseServer.Stop(nil)

	sseServer.autoReplay = false

	sseServer.autoStream = true

	mux := http.NewServeMux()
	mux.HandleFunc("/events", sseServer.ServeHTTP)
	server := httptest.NewServer(mux)

	c := NewClient(server.URL + "/events")

	events := make(chan *Event)

	cErr := make(chan error)

	go func() {
		cErr <- c.SubscribeChan("test", events)
	}()

	require.Nil(t, <-cErr)

	sseServer.Publish("test", &Event{Data: []byte("test")})

	msg, err := wait(events, 1*time.Second)

	require.Nil(t, err)

	assert.Equal(t, []byte(`test`), msg)

	c.Unsubscribe(events)

	_, _ = wait(events, 1*time.Second)

	assert.Equal(t, (*Stream)(nil), sseServer.streamMgr.Get("test"))
}
