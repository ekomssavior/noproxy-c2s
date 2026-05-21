//go:build ethereum
// +build ethereum

package contract

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

// ContractABI is the minimal ABI for the CID feed contract.
const ContractABI = `[
	{
		"anonymous": false,
		"inputs": [
			{"indexed": true, "name": "sender", "type": "address"},
			{"indexed": false, "name": "cid", "type": "string"},
			{"indexed": false, "name": "timestamp", "type": "uint256"}
		],
		"name": "NewCID",
		"type": "event"
	},
	{
		"inputs": [],
		"name": "latestCID",
		"outputs": [{"name": "", "type": "string"}],
		"stateMutability": "view",
		"type": "function"
	},
	{
		"inputs": [{"name": "_cid", "type": "string"}],
		"name": "publishCID",
		"outputs": [],
		"stateMutability": "nonpayable",
		"type": "function"
	}
]`

// EthWatcher implements Watcher using go-ethereum.
type EthWatcher struct {
	client        *ethclient.Client
	contractAddr  common.Address
	contractABI   abi.ABI
	lastBlock     uint64
	ctx           context.Context
	cancel        context.CancelFunc
}

// NewEthWatcher creates a new Ethereum contract watcher.
func init() {
	NewWatcher = func(rpcURL, contractAddr string) (Watcher, error) {
		return NewEthWatcher(rpcURL, contractAddr)
	}
}

// NewEthWatcher creates a new Ethereum contract watcher.
func NewEthWatcher(rpcURL, contractAddr string) (*EthWatcher, error) {
	client, err := ethclient.Dial(rpcURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Ethereum node: %w", err)
	}

	parsedABI, err := abi.JSON(strings.NewReader(ContractABI))
	if err != nil {
		return nil, fmt.Errorf("failed to parse contract ABI: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &EthWatcher{
		client:       client,
		contractAddr: common.HexToAddress(contractAddr),
		contractABI:  parsedABI,
		ctx:          ctx,
		cancel:       cancel,
	}, nil
}

// Watch watches the contract for NewCID events.
func (w *EthWatcher) Watch(callback func(CIDEvent)) (<-chan error, error) {
	errCh := make(chan error, 1)

	go func() {
		pollInterval := 15 * time.Second
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-w.ctx.Done():
				return
			case <-ticker.C:
				if err := w.pollEvents(callback); err != nil {
					log.Printf("Event poll error: %v", err)
				}
			}
		}
	}()

	return errCh, nil
}

// pollEvents checks for new events since the last seen block.
func (w *EthWatcher) pollEvents(callback func(CIDEvent)) error {
	header, err := w.client.HeaderByNumber(w.ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to get block header: %w", err)
	}

	currentBlock := header.Number.Uint64()
	if currentBlock <= w.lastBlock {
		return nil
	}

	fromBlock := w.lastBlock
	if fromBlock == 0 {
		fromBlock = currentBlock - 100 // Look back 100 blocks on first poll
	}

	// Build query
	eventSig := crypto.Keccak256Hash([]byte("NewCID(address,string,uint256)"))
	query := ethereum.FilterQuery{
		FromBlock: big.NewInt(int64(fromBlock)),
		ToBlock:   big.NewInt(int64(currentBlock)),
		Addresses: []common.Address{w.contractAddr},
		Topics:    [][]common.Hash{{eventSig}},
	}

	logs, err := w.client.FilterLogs(w.ctx, query)
	if err != nil {
		return fmt.Errorf("failed to filter logs: %w", err)
	}

	for _, vLog := range logs {
		event, err := w.parseEvent(vLog)
		if err != nil {
			log.Printf("Failed to parse event: %v", err)
			continue
		}
		callback(event)
	}

	w.lastBlock = currentBlock
	return nil
}

// parseEvent parses a NewCID event from a raw log.
func (w *EthWatcher) parseEvent(vLog types.Log) (CIDEvent, error) {
	var event struct {
		Sender    common.Address
		CID       string
		Timestamp *big.Int
	}

	err := w.contractABI.UnpackIntoInterface(&event, "NewCID", vLog.Data)
	if err != nil {
		return CIDEvent{}, fmt.Errorf("failed to unpack event data: %w", err)
	}

	return CIDEvent{
		CID:       event.CID,
		Sender:    event.Sender.Hex(),
		Timestamp: event.Timestamp.Uint64(),
		BlockNum:  vLog.BlockNumber,
		TxHash:    vLog.TxHash.Hex(),
	}, nil
}

// GetLatestCID fetches the current CID from the contract.
func (w *EthWatcher) GetLatestCID() (string, error) {
	result, err := w.contractABI.Pack("latestCID")
	if err != nil {
		return "", fmt.Errorf("failed to pack call: %w", err)
	}

	msg := ethereum.CallMsg{
		To:   &w.contractAddr,
		Data: result,
	}

	output, err := w.client.CallContract(w.ctx, msg, nil)
	if err != nil {
		return "", fmt.Errorf("contract call failed: %w", err)
	}

	var cid string
	if err := w.contractABI.UnpackIntoInterface(&cid, "latestCID", output); err != nil {
		return "", fmt.Errorf("failed to unpack response: %w", err)
	}

	return cid, nil
}

// SendCID submits a new CID to the contract.
func (w *EthWatcher) SendCID(cid string, privateKeyHex string) (string, error) {
	privateKey, err := crypto.HexToECDSA(privateKeyHex)
	if err != nil {
		return "", fmt.Errorf("invalid private key: %w", err)
	}

	publicKey := privateKey.Public()
	publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
	if !ok {
		return "", fmt.Errorf("failed to get public key")
	}

	fromAddress := crypto.PubkeyToAddress(*publicKeyECDSA)

	nonce, err := w.client.PendingNonceAt(w.ctx, fromAddress)
	if err != nil {
		return "", fmt.Errorf("failed to get nonce: %w", err)
	}

	gasPrice, err := w.client.SuggestGasPrice(w.ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get gas price: %w", err)
	}

	// Pack the function call
	data, err := w.contractABI.Pack("publishCID", cid)
	if err != nil {
		return "", fmt.Errorf("failed to pack function call: %w", err)
	}

	gasLimit := uint64(200000) // reasonable estimate

	tx := types.NewTransaction(nonce, w.contractAddr, big.NewInt(0), gasLimit, gasPrice, data)

	chainID, err := w.client.NetworkID(w.ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get chain ID: %w", err)
	}

	signedTx, err := types.SignTx(tx, types.NewEIP155Signer(chainID), privateKey)
	if err != nil {
		return "", fmt.Errorf("failed to sign transaction: %w", err)
	}

	if err := w.client.SendTransaction(w.ctx, signedTx); err != nil {
		return "", fmt.Errorf("failed to send transaction: %w", err)
	}

	return signedTx.Hash().Hex(), nil
}

// Close cleans up the watcher.
func (w *EthWatcher) Close() {
	w.cancel()
	if w.client != nil {
		w.client.Close()
	}
}
