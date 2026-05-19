package protocol

import (
	"encoding/json"
	"fmt"
)

// Message types
const (
	TypeExec      = "exec"
	TypeUpload    = "upload"
	TypeDownload  = "download"
	TypeBeacon    = "beacon"
	TypePing      = "ping"
	TypePong      = "pong"
	TypeHeartbeat = "heartbeat"
	TypeResult    = "result"
	TypeCmd       = "cmd"
	TypeRegister  = "register"
	TypeExit      = "exit"
)

// Message is the shared JSON frame sent over WebSocket.
type Message struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
	Data string `json:"data,omitempty"`
	Err  string `json:"error,omitempty"`
}

// Marshal serializes a Message to JSON bytes.
func (m *Message) Marshal() ([]byte, error) {
	return json.Marshal(m)
}

// UnmarshalMessage deserializes JSON bytes into a Message.
func UnmarshalMessage(data []byte) (*Message, error) {
	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("unmarshal message: %w", err)
	}
	return &msg, nil
}

// NewMessage is a convenience constructor.
func NewMessage(msgType, id, data string) *Message {
	return &Message{Type: msgType, ID: id, Data: data}
}

// NewErrorMessage creates an error result message.
func NewErrorMessage(id, err string) *Message {
	return &Message{Type: TypeResult, ID: id, Err: err}
}

// NewResult creates a success result message.
func NewResult(id, data string) *Message {
	return &Message{Type: TypeResult, ID: id, Data: data}
}
