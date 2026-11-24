package main

import (
	"sync"
	"testing"
	"time"
)

func TestRelayerEngine_ValidRelay(t *testing.T) {
	engine := NewRelayerEngine(100, "chain-a", "chain-b")

	packet := &RelayPacket{
		PacketID:      "pkt-001",
		SourceChain:   "chain-a",
		DestChain:     "chain-b",
		SequenceNonce: 1,
		Payload:       []byte(`{"transfer":{"amount":500,"recipient":"0xaddr"}}`),
		GasFee:        150,
		Timestamp:     time.Now().UTC(),
	}
	packet.StateRootHash = packet.ComputeCommitment()

	receipt, err := engine.VerifyAndRelay(packet)
	if err != nil {
		t.Fatalf("expected valid relay, got error: %v", err)
	}

	if receipt.ExecutedNonce != 1 {
		t.Errorf("expected executed nonce 1, got %d", receipt.ExecutedNonce)
	}
	if receipt.CommitmentHash != packet.StateRootHash {
		t.Errorf("mismatched commitment hash")
	}

	nonce := engine.GetChannelNonce("chain-a", "chain-b")
	if nonce != 1 {
		t.Errorf("expected channel nonce 1, got %d", nonce)
	}
}

func TestRelayerEngine_ReplayAttackPrevention(t *testing.T) {
	engine := NewRelayerEngine(50, "chain-a", "chain-b")

	p1 := &RelayPacket{
		PacketID:      "pkt-001",
		SourceChain:   "chain-a",
		DestChain:     "chain-b",
		SequenceNonce: 10,
		Payload:       []byte("initial message"),
		GasFee:        100,
	}
	p1.StateRootHash = p1.ComputeCommitment()

	if _, err := engine.VerifyAndRelay(p1); err != nil {
		t.Fatalf("first packet failed: %v", err)
	}

	// Attempt replaying same or lower nonce
	pReplay := &RelayPacket{
		PacketID:      "pkt-002",
		SourceChain:   "chain-a",
		DestChain:     "chain-b",
		SequenceNonce: 10,
		Payload:       []byte("replayed message"),
		GasFee:        100,
	}
	pReplay.StateRootHash = pReplay.ComputeCommitment()

	_, err := engine.VerifyAndRelay(pReplay)
	if err != ErrInvalidSequence {
		t.Fatalf("expected ErrInvalidSequence on replay, got: %v", err)
	}
}

func TestRelayerEngine_ProofTamperingDetection(t *testing.T) {
	engine := NewRelayerEngine(50, "chain-a", "chain-b")

	p := &RelayPacket{
		PacketID:      "pkt-003",
		SourceChain:   "chain-a",
		DestChain:     "chain-b",
		SequenceNonce: 1,
		Payload:       []byte("authentic content"),
		StateRootHash: "forged_commitment_hash",
		GasFee:        100,
	}

	_, err := engine.VerifyAndRelay(p)
	if err != ErrInvalidProof {
		t.Fatalf("expected ErrInvalidProof for forged root hash, got: %v", err)
	}
}

func TestRelayerEngine_ConcurrentRelay(t *testing.T) {
	engine := NewRelayerEngine(10, "chain-1", "chain-2", "chain-3", "chain-4")

	var wg sync.WaitGroup
	channels := [][2]string{
		{"chain-1", "chain-2"},
		{"chain-2", "chain-3"},
		{"chain-3", "chain-4"},
	}

	for _, ch := range channels {
		wg.Add(1)
		go func(src, dst string) {
			defer wg.Done()
			for i := uint64(1); i <= 5; i++ {
				pkt := &RelayPacket{
					PacketID:      src + "-" + dst,
					SourceChain:   src,
					DestChain:     dst,
					SequenceNonce: i,
					Payload:       []byte("msg"),
					GasFee:        50,
				}
				pkt.StateRootHash = pkt.ComputeCommitment()
				_, err := engine.VerifyAndRelay(pkt)
				if err != nil {
					t.Errorf("channel %s->%s failed on nonce %d: %v", src, dst, i, err)
				}
			}
		}(ch[0], ch[1])
	}

	wg.Wait()
}
