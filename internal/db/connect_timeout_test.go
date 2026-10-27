// SPDX-License-Identifier: AGPL-3.0-only

package db

import (
	"context"
	"net"
	"testing"
	"time"
)

// A database host that accepts the connection and then says nothing (a
// frozen server, a black-holed route) fails Open within the connect
// timeout instead of hanging.
func TestOpenSilentServerTimesOut(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	held := make(chan net.Conn, 16)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			held <- c // never answers
		}
	}()
	defer func() {
		for len(held) > 0 {
			_ = (<-held).Close()
		}
	}()
	old := DefaultConnectTimeout
	DefaultConnectTimeout = 200 * time.Millisecond
	t.Cleanup(func() { DefaultConnectTimeout = old })

	start := time.Now()
	pool, err := Open(context.Background(), "postgres://ohs:ohs@"+ln.Addr().String()+"/ohs?sslmode=disable")
	if err == nil {
		pool.Close()
		t.Fatal("Open succeeded against a silent server")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("Open took %s, want about the 200 ms connect timeout", d)
	}
}
