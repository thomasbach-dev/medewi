package xmodem

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// receiver is an in-process XMODEM-CRC receiver behind the Port
// interface. It keeps no clock: a Recv with nothing queued is a
// timeout, and the wait asked for is recorded. Faults are functions
// keyed by the count of frames received so far (1-based, resends count).
type receiver struct {
	t *testing.T

	start   []byte // bytes offered before any block arrives, one per read
	startAt int

	// drop: the frame is lost, no reply. corrupt: one data byte flips on
	// the way in. loseReply: the frame arrives, the reply is lost.
	drop, corrupt, loseReply map[int]bool
	// reply overrides the reply to a frame (after the receiver handled it).
	reply map[int][]byte
	// nakEOT is how many EOTs get a NAK before the ACK.
	nakEOT int

	out      []byte // queued bytes for the sender
	frames   int    // frames received, EOT and CAN excluded
	expect   byte   // next block number
	data     []byte // accepted payload
	eots     int
	done     bool
	cancels  int // CAN bytes received
	waits    []time.Duration
	timeouts int
	writes   [][]byte
}

func newReceiver(t *testing.T) *receiver {
	return &receiver{t: t, start: []byte{'C'}, expect: 1}
}

func (r *receiver) Recv(timeout time.Duration) (byte, error) {
	r.waits = append(r.waits, timeout)
	if len(r.out) == 0 && r.frames == 0 && r.eots == 0 && r.startAt < len(r.start) {
		b := r.start[r.startAt]
		r.startAt++
		if b == 0 { // 0 in the start script is a timeout
			r.timeouts++
			return 0, ErrTimeout
		}
		return b, nil
	}
	if len(r.out) == 0 {
		r.timeouts++
		return 0, ErrTimeout
	}
	b := r.out[0]
	r.out = r.out[1:]
	return b, nil
}

func (r *receiver) Write(p []byte) (int, error) {
	r.writes = append(r.writes, append([]byte(nil), p...))
	switch {
	case len(p) == 1 && p[0] == eot:
		r.eots++
		if r.eots <= r.nakEOT {
			r.out = append(r.out, nak)
		} else {
			r.done = true
			r.out = append(r.out, ack)
		}
	case len(p) > 0 && p[0] == can:
		r.cancels += len(p)
	default:
		r.frame(p)
	}
	return len(p), nil
}

func (r *receiver) frame(p []byte) {
	r.frames++
	n := r.frames
	if r.drop[n] {
		return
	}
	p = append([]byte(nil), p...)
	if r.corrupt[n] {
		p[10] ^= 0x40
	}
	resp := byte(nak)
	if len(p) == 3+BlockSize+2 && p[0] == soh && p[1] == ^p[2] {
		c := crc16(p[3 : 3+BlockSize])
		if byte(c>>8) == p[3+BlockSize] && byte(c) == p[4+BlockSize] {
			switch p[1] {
			case r.expect:
				r.data = append(r.data, p[3:3+BlockSize]...)
				r.expect++
				resp = ack
			case r.expect - 1: // a resend of the block just taken
				resp = ack
			default:
				r.t.Errorf("frame %d: block number %d, expected %d", n, p[1], r.expect)
			}
		}
	} else if len(p) != 3+BlockSize+2 {
		r.t.Errorf("frame %d: %d bytes in one write, want %d", n, len(p), 3+BlockSize+2)
	}
	if r.loseReply[n] {
		return
	}
	if o, ok := r.reply[n]; ok {
		r.out = append(r.out, o...)
		return
	}
	r.out = append(r.out, resp)
}

// payload returns n bytes that differ from block to block.
func payload(n int) []byte {
	p := make([]byte, n)
	for i := range p {
		p[i] = byte(i*7 + i/128)
	}
	return p
}

func padded(p []byte) []byte {
	out := append([]byte(nil), p...)
	for len(out)%BlockSize != 0 {
		out = append(out, Pad)
	}
	return out
}

func TestCRC16(t *testing.T) {
	if got := crc16([]byte("123456789")); got != 0x31C3 {
		t.Errorf("crc16 = %#04x, want 0x31c3", got)
	}
}

func TestSendClean(t *testing.T) {
	for _, n := range []int{1, 127, 128, 129, 4096, 300 * BlockSize} {
		r := newReceiver(t)
		data := payload(n)
		if err := (Sender{}).Send(context.Background(), r, data); err != nil {
			t.Fatalf("%d bytes: %v", n, err)
		}
		blocks := (n + BlockSize - 1) / BlockSize
		if !r.done || r.frames != blocks || r.eots != 1 || r.timeouts != 0 || r.cancels != 0 {
			t.Errorf("%d bytes: done %v, frames %d (want %d), eots %d, timeouts %d, cancels %d",
				n, r.done, r.frames, blocks, r.eots, r.timeouts, r.cancels)
		}
		if !bytes.Equal(r.data, padded(data)) {
			t.Errorf("%d bytes: received data differs", n)
		}
	}
}

func TestSendFrame(t *testing.T) {
	r := newReceiver(t)
	if err := (Sender{}).Send(context.Background(), r, []byte("ab")); err != nil {
		t.Fatal(err)
	}
	f := r.writes[0]
	want := append([]byte{soh, 1, 0xFE, 'a', 'b'}, bytes.Repeat([]byte{0x1A}, 126)...)
	c := crc16(want[3:])
	want = append(want, byte(c>>8), byte(c))
	if !bytes.Equal(f, want) {
		t.Errorf("frame\n%x\nwant\n%x", f, want)
	}
	if len(r.writes) != 2 || !bytes.Equal(r.writes[1], []byte{eot}) {
		t.Errorf("writes after the frame: %x", r.writes[1:])
	}
}

func TestSendWaits(t *testing.T) {
	r := newReceiver(t)
	r.start = []byte{0, 0, 'C'} // two timeouts before the receiver asks
	s := Sender{StartTimeout: 3 * time.Second, AckTimeout: 7 * time.Second}
	if err := s.Send(context.Background(), r, payload(200)); err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{
		3 * time.Second, 3 * time.Second, 3 * time.Second, // start
		7 * time.Second, 7 * time.Second, 7 * time.Second, // two blocks, EOT
	}
	if len(r.waits) != len(want) {
		t.Fatalf("waits %v, want %v", r.waits, want)
	}
	for i := range want {
		if r.waits[i] != want[i] {
			t.Fatalf("waits %v, want %v", r.waits, want)
		}
	}
}

func TestSendDefaults(t *testing.T) {
	r := newReceiver(t)
	if err := (Sender{}).Send(context.Background(), r, payload(1)); err != nil {
		t.Fatal(err)
	}
	if r.waits[0] != 10*time.Second || r.waits[1] != 10*time.Second {
		t.Errorf("default waits %v", r.waits)
	}
}

// Each fault costs exactly one resend, and the data arrives once.
func TestSendFaults(t *testing.T) {
	for name, c := range map[string]struct {
		set      func(r *receiver)
		frames   int // of a 3-block transfer
		timeouts int
		eots     int
	}{
		"block lost":            {func(r *receiver) { r.drop = map[int]bool{2: true} }, 4, 1, 1},
		"first block lost":      {func(r *receiver) { r.drop = map[int]bool{1: true} }, 4, 1, 1},
		"block corrupted":       {func(r *receiver) { r.corrupt = map[int]bool{2: true} }, 4, 0, 1},
		"last block corrupted":  {func(r *receiver) { r.corrupt = map[int]bool{3: true} }, 4, 0, 1},
		"ack lost":              {func(r *receiver) { r.loseReply = map[int]bool{2: true} }, 4, 1, 1},
		"lost twice, corrupted": {func(r *receiver) { r.drop = map[int]bool{1: true, 2: true}; r.corrupt = map[int]bool{3: true} }, 6, 2, 1},
		"eot nak once":          {func(r *receiver) { r.nakEOT = 1 }, 3, 0, 2},
		"eot nak twice":         {func(r *receiver) { r.nakEOT = 2 }, 3, 0, 3},
		"noise before the ack":  {func(r *receiver) { r.reply = map[int][]byte{2: {'x', '\r', '\n', ack}} }, 3, 0, 1},
		"one CAN, then ack":     {func(r *receiver) { r.reply = map[int][]byte{2: {can, ack}} }, 3, 0, 1},
		"C in place of an ack":  {func(r *receiver) { r.reply = map[int][]byte{1: {'C'}} }, 4, 0, 1},
	} {
		r := newReceiver(t)
		c.set(r)
		data := payload(3 * BlockSize)
		if err := (Sender{}).Send(context.Background(), r, data); err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !r.done || r.frames != c.frames || r.timeouts != c.timeouts || r.eots != c.eots || r.cancels != 0 {
			t.Errorf("%s: done %v, frames %d (want %d), timeouts %d (want %d), eots %d (want %d), cancels %d",
				name, r.done, r.frames, c.frames, r.timeouts, c.timeouts, r.eots, c.eots, r.cancels)
		}
		if !bytes.Equal(r.data, data) {
			t.Errorf("%s: received data differs", name)
		}
	}
}

func TestSendStartNoise(t *testing.T) {
	r := newReceiver(t)
	r.start = []byte("\r\nsend now\r\n\x00C")
	if err := (Sender{}).Send(context.Background(), r, payload(5)); err != nil || !r.done {
		t.Fatalf("done %v, err %v", r.done, err)
	}
}

func TestSendNeverStarts(t *testing.T) {
	r := newReceiver(t)
	r.start = nil
	err := Sender{StartTries: 4}.Send(context.Background(), r, payload(5))
	if !errors.Is(err, ErrNoStart) {
		t.Fatalf("err %v, want ErrNoStart", err)
	}
	// Nothing was sent, not even a cancel: the receiver is not listening.
	if r.timeouts != 4 || len(r.writes) != 0 {
		t.Errorf("timeouts %d (want 4), writes %x", r.timeouts, r.writes)
	}
}

func TestSendStartNoiseIsBounded(t *testing.T) {
	r := newReceiver(t)
	r.start = bytes.Repeat([]byte{'x'}, 100000)
	if err := (Sender{}).Send(context.Background(), r, payload(5)); !errors.Is(err, ErrNoStart) {
		t.Fatalf("err %v, want ErrNoStart", err)
	}
	if len(r.writes) != 0 {
		t.Errorf("writes %x", r.writes)
	}
}

func TestSendChecksumReceiver(t *testing.T) {
	r := newReceiver(t)
	r.start = []byte{nak}
	err := (Sender{}).Send(context.Background(), r, payload(5))
	if !errors.Is(err, ErrChecksumMode) {
		t.Fatalf("err %v, want ErrChecksumMode", err)
	}
	if r.frames != 0 || r.cancels != 2 {
		t.Errorf("frames %d, cancels %d (want 2)", r.frames, r.cancels)
	}
}

func TestSendGivesUp(t *testing.T) {
	r := newReceiver(t)
	r.drop = map[int]bool{}
	for i := 2; i < 100; i++ {
		r.drop[i] = true
	}
	err := Sender{Retries: 3}.Send(context.Background(), r, payload(3*BlockSize))
	if !errors.Is(err, ErrRetries) {
		t.Fatalf("err %v, want ErrRetries", err)
	}
	var te *TransferError
	if !errors.As(err, &te) || te.Block != 2 || te.Blocks != 3 || te.Acked != 1 {
		t.Errorf("error %+v", te)
	}
	// Block 1 once, block 2 once plus 3 resends, then CAN CAN, no EOT.
	if r.frames != 5 || r.timeouts != 4 || r.cancels != 2 || r.eots != 0 {
		t.Errorf("frames %d (want 5), timeouts %d (want 4), cancels %d (want 2), eots %d",
			r.frames, r.timeouts, r.cancels, r.eots)
	}
}

func TestSendGivesUpOnEOT(t *testing.T) {
	r := newReceiver(t)
	r.nakEOT = 100
	err := Sender{Retries: 2}.Send(context.Background(), r, payload(5))
	var te *TransferError
	if !errors.Is(err, ErrRetries) || !errors.As(err, &te) || te.Acked != 1 || te.Block != 0 {
		t.Fatalf("err %v (%+v)", err, te)
	}
	if r.eots != 3 || r.cancels != 2 {
		t.Errorf("eots %d (want 3), cancels %d (want 2)", r.eots, r.cancels)
	}
}

func TestSendReceiverCancels(t *testing.T) {
	r := newReceiver(t)
	r.reply = map[int][]byte{2: {can, can}}
	err := (Sender{}).Send(context.Background(), r, payload(3*BlockSize))
	var te *TransferError
	if !errors.Is(err, ErrCancelled) || !errors.As(err, &te) || te.Block != 2 || te.Acked != 1 {
		t.Fatalf("err %v (%+v)", err, te)
	}
	// The receiver has left: no resend, no cancel, no EOT.
	if r.frames != 2 || r.cancels != 0 || r.eots != 0 {
		t.Errorf("frames %d, cancels %d, eots %d", r.frames, r.cancels, r.eots)
	}
}

func TestSendNoisyReply(t *testing.T) {
	r := newReceiver(t)
	r.reply = map[int][]byte{1: bytes.Repeat([]byte{'x'}, 1000)}
	err := Sender{Retries: 1}.Send(context.Background(), r, payload(5))
	if !errors.Is(err, ErrRetries) {
		t.Fatalf("err %v", err)
	}
	// A reply of nothing but noise is a failed try, not an endless read.
	if r.frames != 2 || len(r.waits) != 1+2*(maxNoise+1) {
		t.Errorf("frames %d (want 2), reads %d (want %d)", r.frames, len(r.waits), 1+2*(maxNoise+1))
	}
}

func TestSendContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := newReceiver(t)
	// Cancel while block 2 is on its way.
	hook := &cancelOnFrame{receiver: r, n: 2, cancel: cancel}
	err := (Sender{}).Send(ctx, hook, payload(4*BlockSize))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if r.frames != 2 || r.cancels != 2 || r.eots != 0 {
		t.Errorf("frames %d (want 2), cancels %d (want 2), eots %d", r.frames, r.cancels, r.eots)
	}

	// A context that is already done sends nothing.
	r = newReceiver(t)
	if err := (Sender{}).Send(ctx, r, payload(5)); !errors.Is(err, context.Canceled) || len(r.writes) != 0 {
		t.Errorf("err %v, writes %x", err, r.writes)
	}
}

type cancelOnFrame struct {
	*receiver
	n      int
	cancel func()
}

func (c *cancelOnFrame) Write(p []byte) (int, error) {
	n, err := c.receiver.Write(p)
	if c.frames == c.n {
		c.cancel()
	}
	return n, err
}

func TestSendEmpty(t *testing.T) {
	r := newReceiver(t)
	if err := (Sender{}).Send(context.Background(), r, nil); err == nil || len(r.waits) != 0 {
		t.Errorf("err %v, waits %v", err, r.waits)
	}
}

type failingPort struct {
	*receiver
	failAt int
	n      int
}

var errBroken = errors.New("broken pipe")

func (f *failingPort) Write(p []byte) (int, error) {
	f.n++
	if f.n == f.failAt {
		return 0, errBroken
	}
	return f.receiver.Write(p)
}

func TestSendWriteError(t *testing.T) {
	f := &failingPort{receiver: newReceiver(t), failAt: 2}
	err := (Sender{}).Send(context.Background(), f, payload(3*BlockSize))
	var te *TransferError
	if !errors.Is(err, errBroken) || !errors.As(err, &te) || te.Block != 2 {
		t.Fatalf("err %v (%+v)", err, te)
	}
	if f.frames != 1 {
		t.Errorf("frames %d: sent on after a write error", f.frames)
	}
}

type readErrPort struct{ *receiver }

func (readErrPort) Recv(time.Duration) (byte, error) { return 0, errBroken }

func TestSendReadError(t *testing.T) {
	r := newReceiver(t)
	if err := (Sender{}).Send(context.Background(), readErrPort{r}, payload(5)); !errors.Is(err, errBroken) {
		t.Fatalf("err %v", err)
	}
	if len(r.writes) != 0 {
		t.Errorf("writes %x", r.writes)
	}
}

func TestConnPort(t *testing.T) {
	a, b := net.Pipe()
	defer func() { _ = a.Close(); _ = b.Close() }()
	p := NewPort(a)
	go func() { _, _ = b.Write([]byte{'C'}) }()
	if got, err := p.Recv(5 * time.Second); err != nil || got != 'C' {
		t.Fatalf("got %q, %v", got, err)
	}
	// Nothing comes: the deadline passes and the error is ErrTimeout.
	if _, err := p.Recv(time.Millisecond); !errors.Is(err, ErrTimeout) {
		t.Fatalf("err %v, want ErrTimeout", err)
	}
	// The port still works after a timeout.
	go func() { _, _ = b.Write([]byte{ack}) }()
	if got, err := p.Recv(5 * time.Second); err != nil || got != ack {
		t.Fatalf("after a timeout: got %q, %v", got, err)
	}
	got := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 3)
		_, _ = b.Read(buf)
		got <- buf
	}()
	if _, err := p.Write([]byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(<-got, []byte{1, 2, 3}) {
		t.Error("write did not arrive")
	}
	_ = b.Close()
	if _, err := p.Recv(5 * time.Second); err == nil || errors.Is(err, ErrTimeout) {
		t.Errorf("closed peer: err %v", err)
	}
}
