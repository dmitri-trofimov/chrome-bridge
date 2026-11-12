package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

func main() {
	log.SetOutput(os.Stderr)
	log.SetFlags(log.LstdFlags)
	if err := run(os.Args[1:]); err != nil {
		log.Printf("chrome-bridge: %v", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// Default mode (no arguments): autodetect and start bridge
	if len(args) == 0 || args[0] == "listen" {
		var subArgs []string
		if len(args) > 0 && args[0] == "listen" {
			subArgs = args[1:]
		}
		fs := flag.NewFlagSet("chrome-bridge", flag.ContinueOnError)
		address := fs.String("listen", "127.0.0.1:9222", "local TCP listening address (loopback only)")
		portFile := fs.String("port-file", "", "path to Windows Chrome DevToolsActivePort (autodetected if empty)")
		helper := fs.String("helper", "", "path to Windows chrome-bridge.exe (autodetected if empty)")
		timeout := fs.Duration("dial-timeout", 10*time.Second, "Windows destination connection timeout")
		if err := fs.Parse(subArgs); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}

		resolvedPortFile := *portFile
		if resolvedPortFile == "" {
			if f, err := findWindowsDevToolsActivePort(); err == nil {
				resolvedPortFile = f
			}
		}

		resolvedHelper := *helper
		if resolvedHelper == "" {
			var err error
			resolvedHelper, err = findHelper()
			if err != nil {
				return fmt.Errorf("find Windows helper: %w", err)
			}
		}

		return listen(ctx, *address, resolvedPortFile, resolvedHelper, *timeout)
	}

	switch args[0] {
	case "connect":
		fs := flag.NewFlagSet("connect", flag.ContinueOnError)
		target := fs.String("target", "127.0.0.1:9222", "TCP destination")
		timeout := fs.Duration("dial-timeout", 10*time.Second, "destination connection timeout")
		if err := fs.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		dest := *target
		if fs.NArg() > 0 {
			dest = fs.Arg(0)
		}
		dialer := net.Dialer{Timeout: *timeout}
		conn, err := dialer.DialContext(ctx, "tcp", dest)
		if err != nil {
			return fmt.Errorf("connect to %s: %w", dest, err)
		}
		return relay(ctx, &pipeStream{reader: os.Stdin, writer: os.Stdout}, conn)

	case "help", "--help", "-h":
		fmt.Fprintln(os.Stdout, "Usage: chrome-bridge [options]\n"+
			"WSL-to-Windows bridge for Chrome DevTools.\n\n"+
			"Options:\n"+
			"  -listen string\n"+
			"    \tlocal TCP listening address (default \"127.0.0.1:9222\")\n"+
			"  -port-file string\n"+
			"    \tpath to Windows DevToolsActivePort (autodetected by default)\n"+
			"  -helper string\n"+
			"    \tpath to Windows chrome-bridge.exe (autodetected by default)\n"+
			"  -dial-timeout duration\n"+
			"    \tconnection timeout (default 10s)")
		return nil

	default:
		return fmt.Errorf("unknown command %q (run 'chrome-bridge --help' for usage)", args[0])
	}
}

func validateListen(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("listen address must use a literal loopback IP, such as 127.0.0.1 or ::1")
	}
	return nil
}

func listen(ctx context.Context, address, portFile, helper string, timeout time.Duration) error {
	if err := validateListen(address); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer ln.Close()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	log.Printf("chrome-bridge: listening on %s (helper: %s)", ln.Addr(), helper)

	var (
		mu         sync.Mutex
		lastTarget string
		lastPath   string
	)

	// Probe and sync immediately on startup if DevToolsActivePort is already present
	if portFile != "" {
		if t, err := readDevToolsActivePort(portFile); err == nil {
			lastTarget = t.Target
			lastPath = t.Path
			log.Printf("chrome-bridge: Windows Chrome detected on port %s", t.Port)
			if err := syncLinuxDevToolsActivePort(address, t.Path); err != nil {
				log.Printf("chrome-bridge: sync Linux DevToolsActivePort: %v", err)
			}
		} else {
			log.Printf("chrome-bridge: waiting for Windows Chrome remote debugging (%s)", portFile)
		}
	} else {
		log.Printf("chrome-bridge: waiting for Windows Chrome DevToolsActivePort...")
	}

	var connections sync.WaitGroup
	defer connections.Wait()

	for {
		conn, err := ln.Accept()
		if err != nil {
			cancel()
			if ctx.Err() != nil && errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}

		connections.Add(1)
		go func(c net.Conn) {
			defer connections.Done()

			// Resolve active Chrome port
			activePortFile := portFile
			if activePortFile == "" {
				if f, findErr := findWindowsDevToolsActivePort(); findErr == nil {
					activePortFile = f
				}
			}

			if activePortFile == "" {
				c.Close()
				log.Printf("chrome-bridge: connection rejected: Windows Chrome DevToolsActivePort not found")
				return
			}

			t, err := readDevToolsActivePort(activePortFile)
			if err != nil {
				c.Close()
				log.Printf("chrome-bridge: connection rejected: %v", err)
				return
			}

			mu.Lock()
			if t.Target != lastTarget || t.Path != lastPath {
				log.Printf("chrome-bridge: targeting Windows Chrome at %s (%s)", t.Target, t.Path)
				lastTarget = t.Target
				lastPath = t.Path
				_ = syncLinuxDevToolsActivePort(address, t.Path)
			}
			mu.Unlock()

			if err := forward(ctx, c, t.Target, helper, timeout); err != nil && ctx.Err() == nil {
				log.Printf("chrome-bridge: connection: %v", err)
			}
		}(conn)
	}
}

func forward(ctx context.Context, conn net.Conn, target, helper string, timeout time.Duration) error {
	defer conn.Close()
	cmd := exec.CommandContext(ctx, helper, "connect", "--target", target, "--dial-timeout", timeout.String())
	cmd.Stderr = os.Stderr
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	defer output.Close()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start Windows helper: %w", err)
	}
	relayErr := relay(ctx, conn, &pipeStream{reader: output, writer: input})
	waitErr := cmd.Wait()
	return errors.Join(relayErr, waitErr)
}

type pipeStream struct {
	reader io.ReadCloser
	writer io.Writer
}

func (p *pipeStream) Read(b []byte) (int, error)  { return p.reader.Read(b) }
func (p *pipeStream) Write(b []byte) (int, error) { return p.writer.Write(b) }
func (p *pipeStream) CloseWrite() error {
	if closer, ok := p.writer.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}
func (p *pipeStream) Close() error {
	return errors.Join(p.reader.Close(), p.CloseWrite())
}

func relay(ctx context.Context, client io.ReadWriteCloser, destination io.ReadWriteCloser) error {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			client.Close()
			destination.Close()
		case <-done:
		}
	}()
	defer close(done)
	upload := make(chan error, 1)
	go func() {
		_, err := io.Copy(destination, client)
		if half, ok := destination.(interface{ CloseWrite() error }); ok {
			err = errors.Join(err, half.CloseWrite())
		}
		if err != nil {
			destination.Close()
			client.Close()
		}
		upload <- err
	}()
	_, downloadErr := io.Copy(client, destination)
	client.Close()
	destination.Close()
	uploadErr := <-upload
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.Join(ignoreClosed(downloadErr), ignoreClosed(uploadErr))
}

func ignoreClosed(err error) error {
	if errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrClosed) {
		return nil
	}
	return err
}

type DevToolsTarget struct {
	Port   string
	Path   string
	Target string
}

func readDevToolsActivePort(filePath string) (DevToolsTarget, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return DevToolsTarget{}, err
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 2 {
		return DevToolsTarget{}, fmt.Errorf("invalid DevToolsActivePort content in %s", filePath)
	}
	port := strings.TrimSpace(lines[0])
	wsPath := strings.TrimSpace(lines[1])
	if port == "" || wsPath == "" {
		return DevToolsTarget{}, fmt.Errorf("empty port or path in %s", filePath)
	}
	return DevToolsTarget{
		Port:   port,
		Path:   wsPath,
		Target: net.JoinHostPort("127.0.0.1", port),
	}, nil
}

func syncLinuxDevToolsActivePort(listenAddr, wsPath string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".config/google-chrome")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	_, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		port = "9222"
	}
	content := fmt.Sprintf("%s\n%s\n", port, wsPath)
	filePath := filepath.Join(dir, "DevToolsActivePort")
	return os.WriteFile(filePath, []byte(content), 0644)
}

func findWindowsDevToolsActivePort() (string, error) {
	if username := os.Getenv("USER"); username != "" {
		direct := filepath.Join("/mnt/c/Users", username, "AppData/Local/Google/Chrome/User Data/DevToolsActivePort")
		if _, err := os.Stat(direct); err == nil {
			return direct, nil
		}
	}

	matches, err := filepath.Glob("/mnt/c/Users/*/AppData/Local/Google/Chrome/User Data/DevToolsActivePort")
	if err == nil && len(matches) > 0 {
		var newest string
		var newestTime time.Time
		for _, m := range matches {
			if fi, err := os.Stat(m); err == nil {
				if fi.ModTime().After(newestTime) {
					newest = m
					newestTime = fi.ModTime()
				}
			}
		}
		if newest != "" {
			return newest, nil
		}
	}
	return "", errors.New("DevToolsActivePort not found")
}

func findHelper() (string, error) {
	if self, err := os.Executable(); err == nil {
		dir := filepath.Dir(self)
		candidate := filepath.Join(dir, "chrome-bridge.exe")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}

	if home, err := os.UserHomeDir(); err == nil {
		candidates := []string{
			filepath.Join(home, ".local/lib/chrome-bridge/chrome-bridge.exe"),
			filepath.Join(home, ".local/bin/chrome-bridge.exe"),
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				return c, nil
			}
		}
	}
	return "", errors.New("chrome-bridge.exe not found in ~/.local/lib/chrome-bridge, ~/.local/bin, or beside executable")
}
