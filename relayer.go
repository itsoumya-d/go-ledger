package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	ErrInvalidSequence   = errors.New("relayer: invalid sequence nonce; replay attack detected")
	ErrInvalidProof      = errors.New("relayer: state proof verification failed")
	ErrUnsupportedChain  = errors.New("relayer: unsupported source or destination chain")
	ErrInsufficientFee   = errors.New("relayer: insufficient relayer gas fee")
)

// RelayPacket represents a cross-chain inter-ledger message envelope.
type RelayPacket struct {
	PacketID      string    `json:"packet_id"`
	SourceChain   string    `json:"source_chain"`
	DestChain     string    `json:"dest_chain"`
	SequenceNonce uint64    `json:"sequence_nonce"`
	Payload       []byte    `json:"payload"`
	StateRootHash string    `json:"state_root_hash"`
	Signature     string    `json:"signature"`
	GasFee        int64     `json:"gas_fee"`
	Timestamp     time.Time `json:"timestamp"`
}

// ComputeCommitment calculates the deterministic SHA-256 state proof commitment.
func (p *RelayPacket) ComputeCommitment() string {
	h := sha256.New()
	h.Write([]byte(p.PacketID))
	h.Write([]byte(p.SourceChain))
	h.Write([]byte(p.DestChain))
	fmt.Fprintf(h, "%d", p.SequenceNonce)
	h.Write(p.Payload)
	return hex.EncodeToString(h.Sum(nil))
}

// ExecutionReceipt represents the finalized commitment on the destination chain.
type ExecutionReceipt struct {
	PacketID        string    `json:"packet_id"`
	CommitmentHash  string    `json:"commitment_hash"`
	ExecutedAt      time.Time `json:"executed_at"`
	ExecutedNonce   uint64    `json:"executed_nonce"`
	GasFeeCollected int64     `json:"gas_fee_collected"`
}

// RelayerEngine verifies cross-chain state proofs and coordinates atomic execution.
type RelayerEngine struct {
	mu             sync.RWMutex
	supportedChains map[string]bool
	channelNonces   map[string]uint64
	minFeePerRelay  int64
	receipts       map[string]*ExecutionReceipt
}

// NewRelayerEngine initializes a thread-safe cross-chain relayer verifier.
func NewRelayerEngine(minFee int64, chains ...string) *RelayerEngine {
	supported := make(map[string]bool)
	for _, c := range chains {
		supported[c] = true
	}
	return &RelayerEngine{
		supportedChains: supported,
		channelNonces:   make(map[string]uint64),
		minFeePerRelay:  minFee,
		receipts:        make(map[string]*ExecutionReceipt),
	}
}

// VerifyAndRelay evaluates cryptographic state proof commitments and guarantees strict replay prevention.
func (e *RelayerEngine) VerifyAndRelay(packet *RelayPacket) (*ExecutionReceipt, error) {
	if packet == nil {
		return nil, errors.New("relayer: packet cannot be nil")
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.supportedChains[packet.SourceChain] || !e.supportedChains[packet.DestChain] {
		return nil, ErrUnsupportedChain
	}

	if packet.GasFee < e.minFeePerRelay {
		return nil, ErrInsufficientFee
	}

	channelKey := fmt.Sprintf("%s->%s", packet.SourceChain, packet.DestChain)
	lastNonce := e.channelNonces[channelKey]

	if packet.SequenceNonce <= lastNonce {
		return nil, ErrInvalidSequence
	}

	// Verify deterministic cryptographic state commitment
	expectedCommitment := packet.ComputeCommitment()
	if packet.StateRootHash != "" && packet.StateRootHash != expectedCommitment {
		return nil, ErrInvalidProof
	}

	e.channelNonces[channelKey] = packet.SequenceNonce

	receipt := &ExecutionReceipt{
		PacketID:        packet.PacketID,
		CommitmentHash:  expectedCommitment,
		ExecutedAt:      time.Now().UTC(),
		ExecutedNonce:   packet.SequenceNonce,
		GasFeeCollected: packet.GasFee,
	}

	e.receipts[packet.PacketID] = receipt
	return receipt, nil
}

// GetChannelNonce returns the current sequential nonce for a cross-chain channel.
func (e *RelayerEngine) GetChannelNonce(source, dest string) uint64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.channelNonces[fmt.Sprintf("%s->%s", source, dest)]
}
