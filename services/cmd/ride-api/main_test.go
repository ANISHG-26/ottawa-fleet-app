package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestHTTPServerBoundsRequestBodyRead(t *testing.T) {
	server := newHTTPServer("127.0.0.1:0", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			http.Error(w, "request body failed", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, "handled")
	}), 150*time.Millisecond, time.Second)
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		t.Fatal(err)
	}
	server.Addr = listener.Addr().String()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	defer func() {
		_ = server.Close()
		<-done
	}()

	conn, err := net.DialTimeout("tcp", server.Addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	if _, err := fmt.Fprintf(conn, "POST / HTTP/1.1\r\nHost: localhost\r\nContent-Length: 100\r\n\r\npartial"); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodPost})
	if err == nil {
		body, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr == nil && response.StatusCode < http.StatusBadRequest && strings.Contains(string(body), "handled") {
			t.Fatal("handler accepted the unfinished request body")
		}
	}
	if time.Since(started) > 700*time.Millisecond {
		t.Fatalf("unfinished request body was not terminated by the read timeout: err=%v elapsed=%s", err, time.Since(started))
	}
	client := &http.Client{Timeout: time.Second}
	response, err = client.Get("http://" + server.Addr)
	if err != nil {
		t.Fatalf("ordinary request failed: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || string(body) != "handled" {
		t.Fatalf("ordinary request got status %d, body %q, err %v", response.StatusCode, body, err)
	}
}

func TestHTTPServerBoundsResponseWrite(t *testing.T) {
	server := newHTTPServer("127.0.0.1:0", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(250 * time.Millisecond)
		_, _ = io.WriteString(w, "too late")
	}), time.Second, 100*time.Millisecond)
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		t.Fatal(err)
	}
	server.Addr = listener.Addr().String()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	defer func() {
		_ = server.Close()
		<-done
	}()

	started := time.Now()
	client := &http.Client{Timeout: time.Second}
	response, err := client.Get("http://" + server.Addr)
	if err == nil {
		body, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr == nil && strings.Contains(string(body), "too late") {
			t.Fatalf("response body escaped the write timeout: %q", body)
		}
	}
	if time.Since(started) > time.Second {
		t.Fatalf("response was not bounded: elapsed=%s", time.Since(started))
	}
}
