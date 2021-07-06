package ayu

import "time"

type ServerOption interface {
	apply(opts *serverOptions)
}

type serverOptions struct {
	readTimeout  time.Duration
	writeTimeout time.Duration
	pingInterval time.Duration
	pongTimeout  time.Duration
	logger       Logger
	authn        Authenticator
}

func defaultServerOptions() *serverOptions {
	return &serverOptions{
		readTimeout:  90 * time.Second,
		writeTimeout: 90 * time.Second,
		pingInterval: 5 * time.Second,
		pongTimeout:  60 * time.Second,
		authn:        &NopAuthenticator{},
		logger:       nil,
	}
}
