package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// The test executable doubles as a native helper, exercising real subprocess
// pipes without requiring Windows interop on the test machine.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "connect" {
		if err := run(os.Args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestValidateListen(t *testing.T) {
	for _, address := range []string{"127.0.0.1:9222", "[::1]:8080", "127.0.0.2:0"} {
		if err := validateListen(address); err != nil {
			t.Errorf("%s: %v", address, err)
		}
	}
	for _, address := range []string{":9222", "0.0.0.0:9222", "[::]:9222", "192.168.1.1:22", "localhost:9222", "invalid"} {
		if err := validateListen(address); err == nil {
			t.Errorf("accepted unsafe or invalid address %s", address)
		}
	}
}

func tcpPair(t *testing.T) (*net.TCPConn, net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	server, err := ln.Accept()
	if err != nil {
		client.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close(); server.Close() })
	client.SetDeadline(time.Now().Add(15 * time.Second))
	return client.(*net.TCPConn), server
}

func TestForwardBinaryConcurrentAndHalfClose(t *testing.T) {
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	const count = 8
	serverErrors := make(chan error, count)
	go func() {
		for i := 0; i < count; i++ {
			conn, err := target.Accept()
			if err != nil {
				serverErrors <- err
				continue
			}
			go func() {
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(15 * time.Second))
				// Respond only after EOF: this requires half-close propagation
				// through both subprocess pipes and the helper's TCP socket.
				data, err := io.ReadAll(conn)
				if err == nil {
					_, err = conn.Write(data)
				}
				serverErrors <- err
			}()
		}
	}()
	var clients sync.WaitGroup
	for i := 0; i < count; i++ {
		client, bridge := tcpPair(t)
		clients.Add(1)
		go func(seed byte) {
			defer clients.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			result := make(chan error, 1)
			go func() {
				result <- forward(ctx, bridge, target.Addr().String(), helper, 5*time.Second)
			}()
			payload := bytes.Repeat([]byte{seed, seed + 1, seed + 2}, 65536)
			if _, err := client.Write(payload); err != nil {
				t.Errorf("write payload: %v", err)
				return
			}
			if err := client.CloseWrite(); err != nil {
				t.Errorf("half-close: %v", err)
				return
			}
			response, err := io.ReadAll(client)
			if err != nil {
				t.Errorf("read response: %v", err)
				return
			}
			if !bytes.Equal(payload, response) {
				t.Errorf("mismatch: got %d bytes, want %d", len(response), len(payload))
				return
			}
			if err := <-result; err != nil {
				t.Errorf("forward: %v", err)
			}
		}(byte(i * 17))
	}
	clients.Wait()
	for i := 0; i < count; i++ {
		if err := <-serverErrors; err != nil {
			t.Errorf("server: %v", err)
		}
	}
}

func TestForwardConnectionRefused(t *testing.T) {
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := ln.Addr().String()
	ln.Close()
	_, bridge := tcpPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := forward(ctx, bridge, address, helper, time.Second); err == nil {
		t.Fatal("connection refusal was not reported")
	}
	if ctx.Err() != nil {
		t.Fatal("helper failure hung until context timeout")
	}
}

func TestReadDevToolsActivePort(t *testing.T) {
	tmpDir := t.TempDir()
	portFile := filepath.Join(tmpDir, "DevToolsActivePort")

	// Missing file
	if _, err := readDevToolsActivePort(portFile); err == nil {
		t.Fatal("expected error on missing file")
	}

	// Malformed file
	if err := os.WriteFile(portFile, []byte("50000\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readDevToolsActivePort(portFile); err == nil {
		t.Fatal("expected error on single-line file")
	}

	// Valid file
	content := "49752\n/devtools/browser/abc-123-def\n"
	if err := os.WriteFile(portFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	target, err := readDevToolsActivePort(portFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if target.Port != "49752" {
		t.Errorf("got port %q, want 49752", target.Port)
	}
	if target.Path != "/devtools/browser/abc-123-def" {
		t.Errorf("got path %q, want /devtools/browser/abc-123-def", target.Path)
	}
	if target.Target != "127.0.0.1:49752" {
		t.Errorf("got target %q, want 127.0.0.1:49752", target.Target)
	}
}

func TestSyncLinuxDevToolsActivePort(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	wsPath := "/devtools/browser/test-token"
	if err := syncLinuxDevToolsActivePort("127.0.0.1:9222", wsPath); err != nil {
		t.Fatalf("sync error: %v", err)
	}

	syncedFile := filepath.Join(tmpDir, ".config/google-chrome/DevToolsActivePort")
	data, err := os.ReadFile(syncedFile)
	if err != nil {
		t.Fatalf("read synced file: %v", err)
	}

	expected := "9222\n/devtools/browser/test-token\n"
	if string(data) != expected {
		t.Errorf("got content %q, want %q", string(data), expected)
	}
}
