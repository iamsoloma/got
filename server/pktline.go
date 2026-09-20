// Package server implements Git's HTTP-based transfer protocols
// ("smart" and "dumb") as documented at:
//
//	https://git-scm.com/docs/gitprotocol-http
//	https://git-scm.com/book/en/v2/Git-on-the-Server-Smart-HTTP
//	https://git-scm.com/book/en/v2/Git-Internals-Transfer-Protocols
//
// This file contains the pkt-line framing primitives shared by both
// protocols. pkt-line is a length-prefixed line format: each line is
// prefixed by its total length (including the 4 length bytes),
// encoded as 4 lowercase hex digits.
package server

import (
	"bufio"
	"errors"
	"fmt"
	"io"
)

// FlushPkt is the special "0000" pkt-line that terminates a list.
const FlushPkt = "0000"

// MaxPktLineData is the largest payload (excluding the 4-byte length
// prefix) that a single pkt-line may carry, per gitprotocol-pack.
const MaxPktLineData = 65516

var ErrPktLineTooLong = errors.New("githttp: pkt-line payload exceeds 65516 bytes")

// EncodePktLine renders data as a single pkt-line: a 4-hex-digit
// length prefix (counting itself) followed by the raw payload.
// It does NOT append a trailing LF; callers that need one (as the
// protocol does for most text lines) must include it in data.
func EncodePktLine(data []byte) ([]byte, error) {
	if len(data) > MaxPktLineData {
		return nil, ErrPktLineTooLong
	}
	length := len(data) + 4
	return append([]byte(fmt.Sprintf("%04x", length)), data...), nil
}

// EncodePktLineString is a convenience wrapper around EncodePktLine.
func EncodePktLineString(s string) ([]byte, error) {
	return EncodePktLine([]byte(s))
}

// WritePktLine encodes data and writes it to w.
func WritePktLine(w io.Writer, data []byte) error {
	b, err := EncodePktLine(data)
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// WriteFlushPkt writes the "0000" flush-pkt to w.
func WriteFlushPkt(w io.Writer) error {
	_, err := io.WriteString(w, FlushPkt)
	return err
}

// PktLineReader reads a stream of pkt-lines from r, stopping either
// at EOF or after a flush-pkt is returned to the caller (callers
// decide whether a flush ends the whole exchange or just one
// section, so ReadPktLine surfaces flush as an explicit, non-error
// zero-length-with-flush=true result rather than swallowing it).
type PktLineReader struct {
	br *bufio.Reader
}

func NewPktLineReader(r io.Reader) *PktLineReader {
	return &PktLineReader{br: bufio.NewReader(r)}
}

// ReadPktLine reads one pkt-line. isFlush is true when the line read
// was the "0000" flush-pkt, in which case data is nil.
func (p *PktLineReader) ReadPktLine() (data []byte, isFlush bool, err error) {
	lenHex := make([]byte, 4)
	if _, err := io.ReadFull(p.br, lenHex); err != nil {
		return nil, false, err
	}

	var length int
	if _, err := fmt.Sscanf(string(lenHex), "%04x", &length); err != nil {
		return nil, false, fmt.Errorf("githttp: invalid pkt-line length %q: %w", lenHex, err)
	}

	switch {
	case length == 0:
		return nil, true, nil
	case length < 4:
		return nil, false, fmt.Errorf("githttp: invalid pkt-line length %d", length)
	}

	payload := make([]byte, length-4)
	if _, err := io.ReadFull(p.br, payload); err != nil {
		return nil, false, err
	}
	return payload, false, nil
}

// ReadPktLines reads pkt-lines until (and including consuming, but
// not returning) a flush-pkt or EOF.
func (p *PktLineReader) ReadPktLines() ([][]byte, error) {
	var lines [][]byte
	for {
		data, isFlush, err := p.ReadPktLine()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return lines, nil
			}
			return lines, err
		}
		if isFlush {
			return lines, nil
		}
		lines = append(lines, data)
	}
}
