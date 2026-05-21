// Package types defines shared types for the C2 IPFS payload system.
package types

import "time"

// CIDEntry represents a CID submission with metadata.
type CIDEntry struct {
	CID       string    `json:"cid"`
	Timestamp time.Time `json:"timestamp"`
	Note      string    `json:"note,omitempty"`
}

// StatusResponse is returned by the server status endpoint.
type StatusResponse struct {
	CurrentCID string     `json:"current_cid"`
	History    []CIDEntry `json:"history"`
	Mode       string     `json:"mode"` // "http" or "contract"
	Uptime     string     `json:"uptime"`
}

// ImplantReport is sent by the implant after executing a payload.
type ImplantReport struct {
	ImplantID   string `json:"implant_id"`
	CID         string `json:"cid"`
	Success     bool   `json:"success"`
	Error       string `json:"error,omitempty"`
	Platform    string `json:"platform,omitempty"`
	Hostname    string `json:"hostname,omitempty"`
	Timestamp   string `json:"timestamp"`
}

// Config holds the persistent implant configuration.
type Config struct {
	ImplantID      string `json:"implant_id"`
	LastCID        string `json:"last_cid"`
	PollInterval   int    `json:"poll_interval"`   // seconds
	DecryptionKey  string `json:"decryption_key"`
	CIDSource      string `json:"cid_source"`       // URL or contract address
	Mode           string `json:"mode"`             // "http" or "contract"
	Gateways       []string `json:"gateways"`
	ReportURL      string `json:"report_url,omitempty"`
	JWTFetch       string `json:"jwt_fetch,omitempty"` // JWT for auth on CID hub
}
