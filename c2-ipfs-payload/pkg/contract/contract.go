// Package contract handles smart contract interaction for the fully
// decentralized CID feed mode (MODE B).
//
// This file only contains the interface. The actual implementation requires
// go-ethereum and is in contract_eth.go (build tag: ethereum).
package contract

import "errors"

// CIDEvent represents a NewCID event emitted by the smart contract.
type CIDEvent struct {
	CID       string
	Sender    string
	Timestamp uint64
	BlockNum  uint64
	TxHash    string
}

// Watcher watches a smart contract for NewCID events.
type Watcher interface {
	// Watch starts watching for new CID events. The callback is called
	// for each event. Returns a channel that receives an error on failure.
	Watch(callback func(CIDEvent)) (<-chan error, error)

	// GetLatestCID fetches the current CID from the contract.
	GetLatestCID() (string, error)

	// SendCID submits a new CID to the contract (requires wallet).
	SendCID(cid string, privateKeyHex string) (string, error)

	// Close cleans up the watcher.
	Close()
}

// ErrNotCompiledWithEthereum is returned when the binary is not built with
// the ethereum build tag.
var ErrNotCompiledWithEthereum = errors.New("not compiled with -tags ethereum; rebuild with: go build -tags ethereum")

// PlaceholderWatcher returns errors requiring go-ethereum.
type PlaceholderWatcher struct{}

func (p *PlaceholderWatcher) Watch(callback func(CIDEvent)) (<-chan error, error) {
	return nil, ErrNotCompiledWithEthereum
}

func (p *PlaceholderWatcher) GetLatestCID() (string, error) {
	return "", ErrNotCompiledWithEthereum
}

func (p *PlaceholderWatcher) SendCID(cid, privateKeyHex string) (string, error) {
	return "", ErrNotCompiledWithEthereum
}

func (p *PlaceholderWatcher) Close() {}

// NewWatcher creates a contract watcher. If go-ethereum is not available,
// returns a placeholder that errors.
var NewWatcher func(rpcURL, contractAddr string) (Watcher, error) = func(rpcURL, contractAddr string) (Watcher, error) {
	return &PlaceholderWatcher{}, ErrNotCompiledWithEthereum
}
