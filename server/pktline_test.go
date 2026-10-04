// ============================================================================
// Section 1: pkt-line primitives  (spec §4)
// ============================================================================
//
// pktline.go already implements EncodePktLine / WritePktLine /
// WriteFlushPkt / PktLineReader. These tests validate the wire format
// defined in §4.1-4.5 of the spec and serve as a regression net for the
// rest of the test suite (which depends on these primitives).

// --- §4.1 / §4.3: length-prefix encoding ---

// TestEncodePktLine_SimplePayload verifies the canonical example from spec
// §4.3: payload "a\n" must be encoded as "0006a\n".
package server

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
)

// pktLine encodes s as a single pkt-line (4-hex length prefix + payload).
// Fails the test if s exceeds the 65516-byte payload limit.
func pktLine(t *testing.T, s string) []byte {
	t.Helper()
	b, err := EncodePktLineString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// flushPkt returns the bytes "0000".
func flushPkt() []byte { return []byte("0000") }

// delimPkt returns the bytes "0001" (v2 only).
func delimPkt() []byte { return []byte("0001") }

// responseEndPkt returns the bytes "0002" (v2 over stateless only).
func responseEndPkt() []byte { return []byte("0002") }

// pktLineKind enumerates the four kinds of "pkt-line" entries a stream can
// contain, including the v2 special markers (spec §4.5, §10.1).
type pktLineKind int

const (
	pktData        pktLineKind = iota
	pktFlush                   // "0000"
	pktDelim                   // "0001" (v2 only)
	pktResponseEnd             // "0002" (v2 stateless only)
)

// parsedPktLine is a single parsed pkt-line entry.
type parsedPktLine struct {
	kind  pktLineKind
	bytes []byte // nil for non-data kinds
}

// String renders a parsed pkt-line for debug output.
func (p parsedPktLine) String() string {
	switch p.kind {
	case pktFlush:
		return "<flush>"
	case pktDelim:
		return "<delim>"
	case pktResponseEnd:
		return "<response-end>"
	default:
		return fmt.Sprintf("%q", string(p.bytes))
	}
}

// readPktLines parses an entire pkt-line stream into a slice of parsed
// entries, stopping at EOF. The special 0000/0001/0002 markers are
// surfaced as pktFlush / pktDelim / pktResponseEnd entries so callers can
// assert on the structure of the stream.
func readPktLines(t *testing.T, r io.Reader) []parsedPktLine {
	t.Helper()
	var out []parsedPktLine
	br := bufio.NewReader(r)
	for {
		lenHex := make([]byte, 4)
		if _, err := io.ReadFull(br, lenHex); err != nil {
			if err == io.EOF {
				return out
			}
			t.Fatalf("reading pkt-line length: %v", err)
		}
		var length int
		if _, err := fmt.Sscanf(string(lenHex), "%04x", &length); err != nil {
			t.Fatalf("invalid pkt-line length %q: %v", lenHex, err)
		}
		switch {
		case length == 0:
			out = append(out, parsedPktLine{kind: pktFlush})
			continue
		case length == 1:
			out = append(out, parsedPktLine{kind: pktDelim})
			continue
		case length == 2:
			out = append(out, parsedPktLine{kind: pktResponseEnd})
			continue
		case length < 4:
			t.Fatalf("invalid pkt-line length %d (prefix=%q)", length, lenHex)
		}
		payload := make([]byte, length-4)
		if _, err := io.ReadFull(br, payload); err != nil {
			t.Fatalf("reading pkt-line payload (length=%d): %v", length, err)
		}
		out = append(out, parsedPktLine{kind: pktData, bytes: payload})
	}
}

// dataLines returns only the data pkt-lines from a parsed stream, as byte slices.
func dataLines(lines []parsedPktLine) [][]byte {
	var out [][]byte
	for _, l := range lines {
		if l.kind == pktData {
			out = append(out, l.bytes)
		}
	}
	return out
}

// dataLinesStr is dataLines but returns strings (without trailing LF).
func dataLinesStr(lines []parsedPktLine) []string {
	var out []string
	for _, l := range lines {
		if l.kind == pktData {
			out = append(out, strings.TrimRight(string(l.bytes), "\n"))
		}
	}
	return out
}

func TestEncodePktLine_SimplePayload(t *testing.T) {
	got, err := EncodePktLineString("a\n")
	if err != nil {
		t.Fatalf("EncodePktLineString: %v", err)
	}
	want := []byte("0006a\n")
	if !bytes.Equal(got, want) {
		t.Errorf("EncodePktLineString(\"a\\n\") = %q, want %q", got, want)
	}
}

// TestEncodePktLine_LengthIncludesItself verifies spec §4.1: "pkt-len
// includes the 4 bytes of the hex length itself".
func TestEncodePktLine_LengthIncludesItself(t *testing.T) {
	payload := "foobar\n" // 7 bytes
	got, err := EncodePktLineString(payload)
	if err != nil {
		t.Fatalf("EncodePktLineString: %v", err)
	}
	// First 4 bytes are hex length; total = 4 (prefix) + 7 (payload) = 11 = 0x0b
	want := []byte("000bfoobar\n")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestEncodePktLine_LowercaseHex verifies spec §4.4: pkt-len uses
// lowercase hex digits, matching set_packet_header() in pkt-line.c.
func TestEncodePktLine_LowercaseHex(t *testing.T) {
	// 0x000e = 14 → "000e" (lowercase), payload length 10.
	got, err := EncodePktLineString("0123456789")
	if err != nil {
		t.Fatalf("EncodePktLineString: %v", err)
	}
	if string(got[:4]) != "000e" {
		t.Errorf("length prefix = %q, want \"000e\" (lowercase hex)", got[:4])
	}
}

// TestEncodePktLine_BinarySafe verifies spec §4.1: "pkt-line MAY contain
// binary data; implementations MUST ensure 8-bit clean parsing/serialization".
func TestEncodePktLine_BinarySafe(t *testing.T) {
	payload := []byte{0x00, 0x01, 0x02, 0xff, 0x0a, 0x0d}
	got, err := EncodePktLine(payload)
	if err != nil {
		t.Fatalf("EncodePktLine: %v", err)
	}
	if len(got) != 4+len(payload) {
		t.Fatalf("encoded length = %d, want %d", len(got), 4+len(payload))
	}
	if !bytes.Equal(got[4:], payload) {
		t.Errorf("payload round-trip failed: got %v, want %v", got[4:], payload)
	}
}

// TestEncodePktLine_TooLong verifies spec §4.1: implementations MUST NOT
// send pkt-line longer than 65520 (65516 payload + 4 length).
func TestEncodePktLine_TooLong(t *testing.T) {
	tooLong := strings.Repeat("x", specMaxPktLineData+1)
	_, err := EncodePktLineString(tooLong)
	if err == nil {
		t.Fatal("EncodePktLineString: expected ErrPktLineTooLong, got nil")
	}
	if err != ErrPktLineTooLong {
		t.Errorf("expected ErrPktLineTooLong, got %v", err)
	}
}

// TestEncodePktLine_MaxAllowed verifies that exactly 65516 bytes of
// payload is the largest legal pkt-line.
func TestEncodePktLine_MaxAllowed(t *testing.T) {
	payload := strings.Repeat("x", specMaxPktLineData)
	got, err := EncodePktLineString(payload)
	if err != nil {
		t.Fatalf("EncodePktLineString: %v", err)
	}
	if len(got) != specMaxPktLineTotal {
		t.Errorf("total length = %d, want %d", len(got), specMaxPktLineTotal)
	}
}

// TestEncodePktLine_Empty verifies the empty-payload case (length=4,
// payload=""). Spec §4.1: "Implementations SHOULD NOT send empty pkt-line
// (0004)" — but the encoder must still produce a valid framing if asked.
func TestEncodePktLine_Empty(t *testing.T) {
	got, err := EncodePktLineString("")
	if err != nil {
		t.Fatalf("EncodePktLineString: %v", err)
	}
	if string(got) != "0004" {
		t.Errorf("got %q, want \"0004\"", got)
	}
}

// --- §4.5: special markers ---

// TestWriteFlushPkt_Writes0000 verifies the flush-pkt is exactly "0000".
func TestWriteFlushPkt_Writes0000(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFlushPkt(&buf); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "0000" {
		t.Errorf("WriteFlushPkt wrote %q, want \"0000\"", buf.String())
	}
}

// --- PktLineReader ---

// TestPktLineReader_ReadSingle verifies decoding of a single data pkt-line.
func TestPktLineReader_ReadSingle(t *testing.T) {
	stream := bytes.NewReader([]byte("000bfoobar\n"))
	r := NewPktLineReader(stream)
	data, isFlush, err := r.ReadPktLine()
	if err != nil {
		t.Fatalf("ReadPktLine: %v", err)
	}
	if isFlush {
		t.Error("isFlush = true, want false")
	}
	if string(data) != "foobar\n" {
		t.Errorf("data = %q, want \"foobar\\n\"", data)
	}
}

// TestPktLineReader_ReadFlush verifies that "0000" is surfaced as
// isFlush=true with nil data (spec §4.5).
func TestPktLineReader_ReadFlush(t *testing.T) {
	stream := bytes.NewReader([]byte("0000"))
	r := NewPktLineReader(stream)
	data, isFlush, err := r.ReadPktLine()
	if err != nil {
		t.Fatalf("ReadPktLine: %v", err)
	}
	if !isFlush {
		t.Error("isFlush = false, want true")
	}
	if data != nil {
		t.Errorf("data = %v, want nil", data)
	}
}

// TestPktLineReader_ReadMultiple verifies reading several pkt-lines in
// sequence, ending with a flush.
func TestPktLineReader_ReadMultiple(t *testing.T) {
	stream := bytes.NewReader([]byte("0006a\n0006b\n0006c\n0000"))
	r := NewPktLineReader(stream)

	want := []string{"a\n", "b\n", "c\n"}
	for i, w := range want {
		data, isFlush, err := r.ReadPktLine()
		if err != nil {
			t.Fatalf("read #%d: %v", i, err)
		}
		if isFlush {
			t.Fatalf("read #%d: unexpected flush", i)
		}
		if string(data) != w {
			t.Errorf("read #%d = %q, want %q", i, data, w)
		}
	}
	// Final read must be a flush.
	_, isFlush, err := r.ReadPktLine()
	if err != nil {
		t.Fatalf("final read: %v", err)
	}
	if !isFlush {
		t.Error("final read: isFlush = false, want true")
	}
}

// TestPktLineReader_ReadPktLines_StopsOnFlush verifies that
// ReadPktLines consumes the flush-pkt and returns all preceding data.
func TestPktLineReader_ReadPktLines_StopsOnFlush(t *testing.T) {
	stream := bytes.NewReader([]byte("0006a\n0006b\n0000"))
	r := NewPktLineReader(stream)
	lines, err := r.ReadPktLines()
	if err != nil {
		t.Fatalf("ReadPktLines: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("len(lines) = %d, want 2", len(lines))
	}
	if string(lines[0]) != "a\n" || string(lines[1]) != "b\n" {
		t.Errorf("lines = %q, want [\"a\\n\" \"b\\n\"]", lines)
	}
}

// TestPktLineReader_InvalidHex verifies that a non-hex length prefix
// produces an error.
func TestPktLineReader_InvalidHex(t *testing.T) {
	stream := bytes.NewReader([]byte("zzzz"))
	r := NewPktLineReader(stream)
	_, _, err := r.ReadPktLine()
	if err == nil {
		t.Fatal("expected error for non-hex length prefix, got nil")
	}
}

// TestPktLineReader_LengthTooSmall verifies that a length of 3 (which
// would mean -1 bytes of payload) is rejected.
func TestPktLineReader_LengthTooSmall(t *testing.T) {
	stream := bytes.NewReader([]byte("0003"))
	r := NewPktLineReader(stream)
	_, _, err := r.ReadPktLine()
	if err == nil {
		t.Fatal("expected error for length=3, got nil")
	}
}

// TestPktLineReader_BinaryPayload verifies that binary payloads (with NUL,
// high-bit bytes, etc.) round-trip intact.
func TestPktLineReader_BinaryPayload(t *testing.T) {
	payload := []byte{0x00, 0xff, 0x0a, 0x42, 0x01}
	encoded, err := EncodePktLine(payload)
	if err != nil {
		t.Fatal(err)
	}
	r := NewPktLineReader(bytes.NewReader(encoded))
	got, isFlush, err := r.ReadPktLine()
	if err != nil {
		t.Fatalf("ReadPktLine: %v", err)
	}
	if isFlush {
		t.Fatal("isFlush = true, want false")
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("got %v, want %v", got, payload)
	}
}
