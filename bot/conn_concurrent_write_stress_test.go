package bot

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"sync"
	"testing"

	mcnet "github.com/Tnze/go-mc/net"
	pk "github.com/Tnze/go-mc/net/packet"
	"github.com/Tnze/go-mc/net/queue"
)

// TestConn_ConcurrentWritePacket_NeverCorruptsFraming is a stress test built
// while investigating docs/bugs/rare-packet-decode-disconnect.md in
// mc-agent: a rare, non-deterministic server-side
// "DecoderException: Failed to decode packet" disconnect that only ever
// showed up under sustained high-frequency packet dispatch. This test
// exercises the exact suspect described in that doc ("Conn.WritePacket and
// friends ... a missing mutex or similar concurrency bug") by hammering a
// single wrapped Conn's WritePacket from many concurrent goroutines - the
// same shape a live agent produces when, e.g., a chat-error report
// (bot/msg/chat.go's SendMessage) fires from one goroutine while movement/
// inventory packets are being dispatched from others - and verifying, on
// the other end of a real net.Conn pair, that every frame decodes cleanly,
// with the exact ID+payload some caller sent, and no frame is ever merged,
// split, or corrupted.
//
// Result of this investigation (see docs/bugs/rare-packet-decode-disconnect/
// for the full writeup): this test passes cleanly, including under
// `go test -race`, for every run attempted. That's a meaningful negative
// result, not a non-result: it confirms wrapConn's single WriteLoop
// goroutine + mutex-protected send queue (net/queue.LinkedListQueue)
// design does serialize concurrent WritePacket callers correctly, so the
// disconnect's root cause is NOT simple concurrent-caller corruption at
// this layer. See the investigation doc for where the evidence actually
// points instead.
func TestConn_ConcurrentWritePacket_NeverCorruptsFraming(t *testing.T) {
	testConnConcurrentWritePacketNeverCorruptsFraming(t, -1)
}

// TestConn_ConcurrentWritePacket_NeverCorruptsFraming_Compressed is the same
// stress test but with packet compression enabled (SetThreshold(0), so every
// packet - including short chat packets - takes the zlibPool/compressPacket
// path in go-mc's packet.Pack). Real Minecraft servers enable compression by
// default (network-compression-threshold, typically 256), so the
// uncompressed-only variant above doesn't exercise the code path a live
// server disconnect would actually go through.
func TestConn_ConcurrentWritePacket_NeverCorruptsFraming_Compressed(t *testing.T) {
	testConnConcurrentWritePacketNeverCorruptsFraming(t, 0)
}

func testConnConcurrentWritePacketNeverCorruptsFraming(t *testing.T, compressionThreshold int) {
	serverSide, clientSide := net.Pipe()
	defer serverSide.Close()
	defer clientSide.Close()

	mcConn := mcnet.WrapConn(clientSide)
	mcConn.SetThreshold(compressionThreshold)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	qr := queue.NewLinkedQueue[pk.Packet]()
	qw := queue.NewLinkedQueue[pk.Packet]()
	wrapped := wrapConn(ctx, mcConn, qr, qw, nil, nil, nil, "stress-test-bot", nil)
	defer wrapped.Close()

	const (
		numWriters        = 32
		packetsPerWriter  = 200
		totalPackets      = numWriters * packetsPerWriter
		chatLikePacketID  = int32(7) // mirrors 1.21.5 ServerboundChat's ID (packetMgr.go case 7)
		otherPacketIDBase = int32(40)
	)

	// received collects every (id, payload) pair the "server" side actually
	// decodes off the wire, guarded by mu since the reader goroutine
	// appends while writer goroutines are still running.
	type receivedPacket struct {
		id      int32
		payload string
	}
	var (
		mu       sync.Mutex
		received []receivedPacket
	)
	readerDone := make(chan error, 1)
	go func() {
		for i := 0; i < totalPackets; i++ {
			var p pk.Packet
			if err := p.UnPack(serverSide, compressionThreshold); err != nil {
				readerDone <- fmt.Errorf("frame %d: UnPack failed (this is exactly the shape of a server-side DecoderException): %w", i, err)
				return
			}
			var decoded pk.String
			if _, err := decoded.ReadFrom(bytes.NewReader(p.Data)); err != nil {
				readerDone <- fmt.Errorf("frame %d: payload did not decode as a pk.String (corrupted data): %w", i, err)
				return
			}
			mu.Lock()
			received = append(received, receivedPacket{id: p.ID, payload: string(decoded)})
			mu.Unlock()
		}
		readerDone <- nil
	}()

	// expected mirrors what every writer goroutine sends so the test can
	// verify, after the fact, that the multiset of received frames exactly
	// matches the multiset of sent frames - i.e. no frame was corrupted,
	// duplicated, dropped, or had its content swapped with a concurrent
	// caller's.
	expected := make(map[string]int, totalPackets)
	var expectedMu sync.Mutex

	var wg sync.WaitGroup
	for writer := 0; writer < numWriters; writer++ {
		wg.Add(1)
		go func(writerIdx int) {
			defer wg.Done()
			for seq := 0; seq < packetsPerWriter; seq++ {
				var (
					id      int32
					payload string
				)
				if seq%7 == 0 {
					// Every 7th packet mimics a chat/error-report send
					// (bot/msg/chat.go's SendMessage / SendCommand shape):
					// the exact kind of packet the real bug disconnected on.
					id = chatLikePacketID
					payload = fmt.Sprintf("Craft error: craft minecraft:stick: writer=%d seq=%d", writerIdx, seq)
				} else {
					id = otherPacketIDBase + int32(writerIdx%5)
					payload = fmt.Sprintf("movement-or-click-packet writer=%d seq=%d", writerIdx, seq)
				}
				key := fmt.Sprintf("%d|%s", id, payload)
				expectedMu.Lock()
				expected[key]++
				expectedMu.Unlock()

				if err := wrapped.WritePacket(pk.Marshal(id, pk.String(payload))); err != nil {
					t.Errorf("writer %d seq %d: WritePacket failed: %v", writerIdx, seq, err)
					return
				}
			}
		}(writer)
	}

	wg.Wait()
	if err := <-readerDone; err != nil {
		t.Fatalf("reader observed corrupted/undecodable framing: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) != totalPackets {
		t.Fatalf("expected %d frames on the wire, got %d", totalPackets, len(received))
	}

	actual := make(map[string]int, totalPackets)
	for _, r := range received {
		actual[fmt.Sprintf("%d|%s", r.id, r.payload)]++
	}

	expectedMu.Lock()
	defer expectedMu.Unlock()
	for key, wantCount := range expected {
		if gotCount := actual[key]; gotCount != wantCount {
			t.Errorf("frame %q: expected count %d, got %d (indicates corrupted/duplicated/dropped framing)", key, wantCount, gotCount)
		}
	}
	for key, gotCount := range actual {
		if _, ok := expected[key]; !ok {
			t.Errorf("received unexpected frame %q (count %d) not sent by any writer - indicates byte-level corruption produced a spurious-but-decodable frame", key, gotCount)
		}
	}
}
