// Package xmodem sends data with XMODEM-CRC: 128-byte blocks, each with
// a CRC-16, acknowledged one by one. It knows the protocol only: no
// address, no port, no machine. Who may open which connection is decided
// by the caller.
//
// The protocol has a known weakness the sender does not hide: a reply
// that arrives after its timeout is taken for the reply to the resend.
// A transfer that Send reports as done must therefore still be verified
// by reading the data back.
package xmodem

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// BlockSize is the payload of one block; Pad fills the last block (the
// CP/M end-of-file byte, as terminal programs send it).
const (
	BlockSize = 128
	Pad       = 0x1A
)

const (
	soh = 0x01
	eot = 0x04
	ack = 0x06
	nak = 0x15
	can = 0x18
	crc = 'C' // the receiver's request for CRC mode

	// maxNoise is how many bytes that mean nothing are skipped while
	// waiting for one reply before the try counts as failed.
	maxNoise = 64
	// maxStartNoise bounds the bytes skipped before the receiver asks.
	maxStartNoise = 4096
)

// Defaults of a zero Sender.
const (
	DefaultStartTimeout = 10 * time.Second
	DefaultStartTries   = 6
	DefaultAckTimeout   = 10 * time.Second
	DefaultRetries      = 10
)

var (
	// ErrTimeout is what Port.Recv returns when no byte came in time.
	ErrTimeout = errors.New("xmodem: timeout")
	// ErrNoStart means the receiver never asked for the transfer.
	ErrNoStart = errors.New("xmodem: the receiver did not ask for the transfer")
	// ErrChecksumMode means the receiver asked for checksum mode, not CRC.
	ErrChecksumMode = errors.New("xmodem: the receiver asked for checksum mode, not CRC")
	// ErrRetries means a block or the end of transfer was not acknowledged.
	ErrRetries = errors.New("xmodem: not acknowledged, retries used up")
	// ErrCancelled means the receiver cancelled the transfer.
	ErrCancelled = errors.New("xmodem: cancelled by the receiver")
)

// Port is the line to the receiver. Recv waits at most timeout for
// one byte and returns ErrTimeout when none came; any other error ends
// the transfer. Write sends a whole frame.
type Port interface {
	io.Writer
	Recv(timeout time.Duration) (byte, error)
}

// TransferError says where a transfer stopped. Block is the block that
// was being sent (from 1), 0 before the first block and at the end of
// transfer; Acked is how many of the Blocks the receiver acknowledged.
// With Acked > 0 the receiver may have stored part of the data.
type TransferError struct {
	Block, Blocks, Acked int
	Err                  error
}

func (e *TransferError) Error() string {
	where := "before the first block"
	switch {
	case e.Block > 0:
		where = fmt.Sprintf("at block %d of %d", e.Block, e.Blocks)
	case e.Acked > 0:
		where = "at the end of transfer"
	}
	return fmt.Sprintf("%v (%s, %d acknowledged)", e.Err, where, e.Acked)
}

func (e *TransferError) Unwrap() error { return e.Err }

// Sender holds the limits of a transfer; the zero value uses the
// defaults.
type Sender struct {
	// StartTimeout is one wait for the receiver's request, StartTries
	// how many such waits pass before ErrNoStart.
	StartTimeout time.Duration
	StartTries   int
	// AckTimeout is the wait for the reply to a block or to the end of
	// transfer. Retries is how often one of them is sent again after a
	// NAK, a timeout or noise; 0 means DefaultRetries.
	AckTimeout time.Duration
	Retries    int
}

// Send transfers data to the receiver behind p and returns nil once the
// receiver has acknowledged every block and the end of transfer. Any
// other outcome is a *TransferError. Send waits for the receiver's
// request first and sends nothing before it. When it gives up on a
// receiver that is still there (retries used up, ctx done, checksum
// mode), it sends two CAN bytes so the receiver stops waiting.
func (s Sender) Send(ctx context.Context, p Port, data []byte) error {
	if len(data) == 0 {
		return errors.New("xmodem: no data to send")
	}
	if s.StartTimeout <= 0 {
		s.StartTimeout = DefaultStartTimeout
	}
	if s.StartTries <= 0 {
		s.StartTries = DefaultStartTries
	}
	if s.AckTimeout <= 0 {
		s.AckTimeout = DefaultAckTimeout
	}
	if s.Retries <= 0 {
		s.Retries = DefaultRetries
	}
	t := &transfer{Sender: s, ctx: ctx, p: p, blocks: (len(data) + BlockSize - 1) / BlockSize}
	fail := func(block int, cancel bool, err error) error {
		if cancel {
			_, _ = p.Write([]byte{can, can})
		}
		return &TransferError{Block: block, Blocks: t.blocks, Acked: t.acked, Err: err}
	}
	if err := ctx.Err(); err != nil {
		return fail(0, false, err)
	}
	if cancel, err := t.awaitStart(); err != nil {
		return fail(0, cancel, err)
	}
	frame := make([]byte, 3+BlockSize+2)
	for n := 1; n <= t.blocks; n++ {
		frame[0], frame[1], frame[2] = soh, byte(n), ^byte(n)
		body := frame[3 : 3+BlockSize]
		k := copy(body, data[(n-1)*BlockSize:])
		for i := k; i < BlockSize; i++ {
			body[i] = Pad
		}
		c := crc16(body)
		frame[3+BlockSize], frame[4+BlockSize] = byte(c>>8), byte(c)
		if cancel, err := t.deliver(frame); err != nil {
			return fail(n, cancel, err)
		}
		t.acked++
	}
	if cancel, err := t.deliver([]byte{eot}); err != nil {
		return fail(0, cancel, err)
	}
	return nil
}

type transfer struct {
	Sender
	ctx    context.Context
	p      Port
	blocks int
	acked  int
}

// awaitStart waits for the receiver's 'C'. cancel says whether the
// receiver should be told that the sender gives up.
func (t *transfer) awaitStart() (cancel bool, err error) {
	noise := 0
	for tries := 0; tries < t.StartTries; {
		if err := t.ctx.Err(); err != nil {
			return false, err
		}
		b, err := t.p.Recv(t.StartTimeout)
		switch {
		case errors.Is(err, ErrTimeout):
			tries++
		case err != nil:
			return false, err
		case b == crc:
			return false, nil
		case b == nak:
			return true, ErrChecksumMode
		default: // a prompt, line ends
			if noise++; noise > maxStartNoise {
				return false, ErrNoStart
			}
		}
	}
	return false, ErrNoStart
}

// deliver sends frame until the receiver acknowledges it.
func (t *transfer) deliver(frame []byte) (cancel bool, err error) {
	for try := 0; try <= t.Retries; try++ {
		if err := t.ctx.Err(); err != nil {
			return true, err
		}
		if _, err := t.p.Write(frame); err != nil {
			return false, err
		}
		ok, err := t.reply()
		if err != nil {
			return false, err
		}
		if ok {
			return false, nil
		}
	}
	return true, ErrRetries
}

// reply reads the answer to one frame: true for ACK, false for a NAK, a
// timeout, a repeated 'C' or too much noise (send again). Two CAN bytes
// in a row are the receiver's cancel.
func (t *transfer) reply() (ok bool, err error) {
	cans := 0
	for noise := 0; noise <= maxNoise; noise++ {
		b, err := t.p.Recv(t.AckTimeout)
		if errors.Is(err, ErrTimeout) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		switch b {
		case ack:
			return true, nil
		case nak, crc:
			return false, nil
		case can:
			if cans++; cans == 2 {
				return false, ErrCancelled
			}
			continue
		}
		cans = 0
	}
	return false, nil
}

// crc16 is the CRC-16 of XMODEM: polynomial 0x1021, start value 0, most
// significant bit first.
func crc16(data []byte) uint16 {
	var c uint16
	for _, b := range data {
		c ^= uint16(b) << 8
		for i := 0; i < 8; i++ {
			if c&0x8000 != 0 {
				c = c<<1 ^ 0x1021
			} else {
				c <<= 1
			}
		}
	}
	return c
}

// Conn is what NewPort needs of a connection; net.Conn has it.
type Conn interface {
	io.ReadWriter
	SetReadDeadline(time.Time) error
}

// NewPort makes a Port of a connection: a read that hits its deadline
// is ErrTimeout.
func NewPort(c Conn) Port { return &connPort{c: c} }

type connPort struct {
	c   Conn
	buf [1]byte
}

func (p *connPort) Write(b []byte) (int, error) { return p.c.Write(b) }

func (p *connPort) Recv(timeout time.Duration) (byte, error) {
	if err := p.c.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return 0, err
	}
	n, err := p.c.Read(p.buf[:])
	if n == 1 {
		return p.buf[0], nil
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return 0, ErrTimeout
	}
	if err == nil {
		err = io.ErrNoProgress
	}
	return 0, err
}
