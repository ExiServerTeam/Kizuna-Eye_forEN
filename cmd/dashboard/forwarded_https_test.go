package main

import (
	"crypto/tls"
	"net/http"
	"testing"
)

func TestForwardedHTTPS(t *testing.T) {
	cases := []struct {
		name       string
		remoteAddr string
		proto      string
		tls        bool
		want       bool
	}{
		{"direct TLS", "203.0.113.5:5000", "", true, true},
		{"loopback proxy https", "127.0.0.1:4000", "https", false, true},
		{"loopback proxy http", "127.0.0.1:4000", "http", false, false},
		{"remote client cannot spoof", "203.0.113.5:5000", "https", false, false},
		{"no header, plain http", "192.168.0.5:5000", "", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &http.Request{RemoteAddr: c.remoteAddr, Header: http.Header{}}
			if c.proto != "" {
				r.Header.Set("X-Forwarded-Proto", c.proto)
			}
			if c.tls {
				r.TLS = &tls.ConnectionState{}
			}
			if got := forwardedHTTPS(r); got != c.want {
				t.Fatalf("forwardedHTTPS(%s) = %v, want %v", c.name, got, c.want)
			}
		})
	}
}
