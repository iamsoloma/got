package git

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// Format reference: https://git-scm.com/docs/pack-format
//
//	A pack is:  header | entry* | trailer
//
//	header (12 bytes):  "PACK", version (uint32 BE, 2 or 3),
//	                    number of objects (uint32 BE)
//	entry:              variable-length type/size header, then payload:
//	                      commit/tree/blob/tag -> zlib-deflated content
//	                      OBJ_OFS_DELTA        -> offset-encoded distance to
//	                                              the base entry, then
//	                                              zlib-deflated delta data
//	                      OBJ_REF_DELTA        -> 20-byte base object name,
//	                                              then zlib-deflated delta
//	                                              data
//	trailer (20 bytes): SHA-1 over everything before it
//
// Beware: the format uses three *different* variable-length encodings:
//   - entry headers: 4 size bits in the first byte, then 7 bits per byte,
//     most-significant group first;
//   - ofs-delta offsets: 7 bits per byte plus the 2^7 + 2^14 + ... bias;
//   - delta stream sizes: little-endian 7-bit groups, no bias.

// ObjectType is the object type as encoded in a pack entry header.
type ObjectType int

const (
	ObjCommit   ObjectType = 1
	ObjTree     ObjectType = 2
	ObjBlob     ObjectType = 3
	ObjTag      ObjectType = 4
	ObjReserved ObjectType = 5
	ObjOfsDelta ObjectType = 6
	ObjRefDelta ObjectType = 7
)

// PackObject is a single object as read out of a pack, already
// delta-resolved. Data holds the raw object content ONLY, i.e. WITHOUT the
// "type size\x00" prefix used for loose objects.
type PackObject struct {
	Type ObjectType
	SHA1 string
	Data []byte
}

// Pack is a parsed packfile: the header version and all contained objects,
// in the order they appear in the pack.
type Pack struct {
	Version uint32
	Objects []PackObject
}

const (
	PackSignature  = "PACK"
	PackHeaderLen  = 12 // PACK + Version + ObjectCount
	PackTrailerLen = 20 // SHA1
)

// ParsePack reads a complete packfile from r, validates the trailing SHA-1
// checksum and resolves every ofs-/ref-delta against a base found in the
// same pack. Objects are returned in pack order.
func ParsePack(r io.Reader) (*Pack, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("ParsePack: reading error: %s", err.Error())
	}

	if len(data) < PackHeaderLen+PackTrailerLen {
		return nil, errors.New("ParsePack: input to short for header and trailer")
	}

	trailer := data[len(data)-PackTrailerLen:]
	data = data[:len(data)-PackTrailerLen]

	if sha1.Sum(data) != [20]byte(trailer) {
		return nil, errors.New("ParsePack: trailer checksum mismatch")
	}

	return parsePackBody(data)
}

func parsePackBody(body []byte) (*Pack, error) {
	r := bytes.NewReader(body)

	//Check PACK
	sig := make([]byte, len(PackSignature))
	if _, err := io.ReadFull(r, sig); err != nil {
		return nil, fmt.Errorf("ParsePackBody: reading signature: %s", err.Error())
	}
	if string(sig) != PackSignature {
		return nil, fmt.Errorf("ParsePackBody: invalid signature %s, want %s", sig, PackSignature)
	}

	var versionAndCount [8]byte
	if _, err := io.ReadFull(r, versionAndCount[:]); err != nil {
		return nil, fmt.Errorf("ParsePackBody: reading version and object count: %s", err.Error())
	}
	//Check version
	version := binary.BigEndian.Uint32(versionAndCount[0:4])
	if !(version == 2 || version == 3) {
		// git accepts versions 2 and 3 and only ever generates version 2.
		return nil, fmt.Errorf("ParsePackBody: unsupported version %d", version)
	}
	//Save count of objects
	count := binary.BigEndian.Uint32(versionAndCount[4:8])

	entries := make([]packEntry, 0, count)
	for i := uint32(0); i < count; i++ {
		entry, err := parsePackEntry(r, i)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}

	if remaining := r.Len(); remaining != 0 {
		return nil, fmt.Errorf("ParsePackBody: %d unexpected bytes after the last object", remaining)
	}

	return resolvePackEntries(version, entries)

}

// packEntry is one raw pack entry before delta resolution.
type packEntry struct {
	offset int64 // position of the entry header within the pack body

	typ ObjectType
	// Object content for plain entries, the still-encoded delta instruction
	// stream for delta entries.
	data []byte

	baseOffset int64  // OBJ_OFS_DELTA: body offset of the base entry
	baseSHA    string // OBJ_REF_DELTA: hex-encoded 20-byte base object name
}

func parsePackEntry(br *bytes.Reader, index uint32) (packEntry, error) {
	start := br.Size() - int64(br.Len())

	typ, size, err := readPackEntryHeader(br)
	if err != nil {
		return packEntry{}, fmt.Errorf("parsePackEntry: object %d: reading entry header: %w", index, err)
	}

	entry := packEntry{offset: start, typ: typ}

	switch typ {
	case ObjCommit, ObjTree, ObjBlob, ObjTag:
		// Plain entry: zlib-deflated object content follows.

	case ObjOfsDelta:
		back, err := readPackOfsOffset(br)
		if err != nil {
			return packEntry{}, fmt.Errorf("parsePackEntry: object %d: reading ofs-delta offset: %w", index, err)
		}
		entry.baseOffset = start - back
		if entry.baseOffset < 0 {
			return packEntry{}, fmt.Errorf("parsePackEntry: object %d: ofs-delta base lies before the start of the pack", index)
		}

	case ObjRefDelta:
		var base [20]byte
		if _, err := io.ReadFull(br, base[:]); err != nil {
			return packEntry{}, fmt.Errorf("parsePackEntry: object %d: reading ref-delta base name: %w", index, err)
		}
		entry.baseSHA = hex.EncodeToString(base[:])

	default:
		return packEntry{}, fmt.Errorf("parsePackEntry: object %d: invalid entry type %d", index, typ)
	}

	entry.data, err = inflatePackData(br, size)
	if err != nil {
		return packEntry{}, fmt.Errorf("parsePackEntry: object %d: %w", index, err)
	}
	return entry, nil
}

// readPackEntryHeader decodes an entry's variable-length "type size" header.
// The first byte carries the type in bits 6-4 and the lowest 4 size bits in
// bits 3-0; as long as the MSB is set, further bytes each contribute the
// next 7 size bits. size is the payload length once inflated.
func readPackEntryHeader(r io.ByteReader) (ObjectType, int64, error) {
	b, err := r.ReadByte()
	if err != nil {
		return 0, 0, err
	}

	typ := ObjectType((b >> 4) & 0x07)
	size := int64(b & 0x0F)

	for shift := uint(4); b&0x80 != 0; shift += 7 {
		if shift+7 > 64 {
			return 0, 0, errors.New("readPackEntryHeader: object size too large")
		}
		b, err = r.ReadByte()
		if err != nil {
			return 0, 0, err
		}
		size |= int64(b&0x7F) << shift
	}
	return typ, size, nil
}

// readPackOfsOffset decodes the ofs-delta offset encoding:
//
//	n bytes with MSB set in all but the last one. The offset is then the
//	number constructed by concatenating the lower 7 bit of each byte, and
//	for n >= 2 adding 2^7 + 2^14 + ... + 2^(7*(n-1)) to the result.
//
// which is equivalent to the biased accumulation below. The result is the
// distance from the delta entry back to its base.
func readPackOfsOffset(r io.ByteReader) (int64, error) {
	b, err := r.ReadByte()
	if err != nil {
		return 0, err
	}

	offset := int64(b & 0x7F)
	for b&0x80 != 0 {
		if offset >= 1<<57 {
			return 0, errors.New("readPackOfsOffset: ofs-delta offset too large")
		}
		b, err = r.ReadByte()
		if err != nil {
			return 0, err
		}
		offset = (offset+1)<<7 | int64(b&0x7F)
	}
	return offset, nil
}

// inflatePackData inflates exactly size bytes of zlib payload from br and
// leaves br positioned on the first byte after the zlib stream, so the next
// entry can be parsed. bytes.Reader implements io.ByteReader, so zlib/flate
// read from it without internal buffering and consume exactly the stream —
// which is what makes position tracking via br.Len() exact.
func inflatePackData(br *bytes.Reader, size int64) ([]byte, error) {
	if size < 0 {
		return nil, errors.New("negative object size")
	}

	zr, err := zlib.NewReader(br)
	if err != nil {
		return nil, fmt.Errorf("inflatePackData: opening zlib stream: %w", err)
	}
	defer zr.Close()

	data := make([]byte, size)
	if _, err := io.ReadFull(zr, data); err != nil {
		return nil, fmt.Errorf("inflatePackData: inflating %d bytes: %w", size, err)
	}

	// Drain the rest of the stream (the trailing Adler-32) so br lands on
	// the next entry, and reject streams larger than the header claims.
	if extra, err := io.Copy(io.Discard, zr); err != nil {
		return nil, fmt.Errorf("inflatePackData: finishing zlib stream: %w", err)
	} else if extra != 0 {
		return nil, fmt.Errorf("inflatePackData: stream inflates to more than the declared %d bytes", size)
	}
	return data, nil
}

// resolvePackEntries turns raw entries into final objects. Deltas are
// applied until a fixed point is reached, because a delta's base may itself
// be stored as a delta. Ref-deltas whose base is not part of the pack
// (thin packs) cannot be resolved here and are rejected.
func resolvePackEntries(version uint32, entries []packEntry) (*Pack, error) {
	pack := &Pack{
		Version: version,
		Objects: make([]PackObject, len(entries)),
	}

	byOffset := make(map[int64]int, len(entries))
	for i, e := range entries {
		byOffset[e.offset] = i
	}
	bySHA := make(map[string]int)

	resolved := make([]bool, len(entries))
	var pending []int

	for i, e := range entries {
		switch e.typ {
		case ObjCommit, ObjTree, ObjBlob, ObjTag:
			pack.Objects[i] = PackObject{
				Type: e.typ,
				SHA1: packObjectSHA1(e.typ, e.data),
				Data: e.data,
			}
			resolved[i] = true
			bySHA[pack.Objects[i].SHA1] = i
		default:
			pending = append(pending, i)
		}
	}

	for len(pending) > 0 {
		var stillPending []int
		progressed := false

		for _, i := range pending {
			e := entries[i]

			var baseIndex int
			switch e.typ {
			case ObjOfsDelta:
				j, ok := byOffset[e.baseOffset]
				if !ok {
					return nil, fmt.Errorf("resolvePackEntries: object %d: ofs-delta base at offset %d not found", i, e.baseOffset)
				}
				baseIndex = j
			case ObjRefDelta:
				j, ok := bySHA[e.baseSHA]
				if !ok {
					stillPending = append(stillPending, i) // base not seen (yet)
					continue
				}
				baseIndex = j
			default:
				return nil, fmt.Errorf("resolvePackEntries: object %d: unexpected entry type %d", i, e.typ)
			}

			if !resolved[baseIndex] {
				// Base is itself a delta that is not resolved yet; retry
				// in the next round.
				stillPending = append(stillPending, i)
				continue
			}

			base := pack.Objects[baseIndex]
			data, err := applyPackDelta(base.Data, e.data)
			if err != nil {
				return nil, fmt.Errorf("resolvePackEntries: object %d: applying delta: %w", i, err)
			}
			pack.Objects[i] = PackObject{
				Type: base.Type,
				SHA1: packObjectSHA1(base.Type, data),
				Data: data,
			}
			resolved[i] = true
			bySHA[pack.Objects[i].SHA1] = i
			progressed = true
		}

		if !progressed {
			return nil, errors.New("resolvePackEntries: cannot resolve delta: base object not found in pack")
		}
		pending = stillPending
	}

	return pack, nil
}

// packObjectSHA1 computes the object's canonical name the same way
// `git hash-object` does: sha1("<type> <size>\x00" + content).
func packObjectSHA1(typ ObjectType, data []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "%s %d\x00", packTypeName(typ), len(data))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// packTypeName is the canonical name of an object type as used in the loose
// object header.
func packTypeName(typ ObjectType) string {
	switch typ {
	case ObjCommit:
		return "commit"
	case ObjTree:
		return "tree"
	case ObjBlob:
		return "blob"
	case ObjTag:
		return "tag"
	default:
		return "unknown"
	}
}

// applyPackDelta applies a delta instruction stream to base and returns the
// reconstructed target object. Delta format (pack-format, "delta format"):
//
//	source size (varint), target size (varint), then instructions:
//	  MSB set:  copy from the base. Bits 0-3 say which of the following
//	            bytes form the (little-endian) offset, bits 4-6 which form
//	            the (little-endian) length. A length of 0 stands for 0x10000.
//	  1..127:   insert the next N bytes of the delta literally.
//	  0:        reserved, invalid.
func applyPackDelta(base, delta []byte) ([]byte, error) {
	srcSize, pos, err := readPackVarint(delta, 0)
	if err != nil {
		return nil, err
	}
	tgtSize, pos, err := readPackVarint(delta, pos)
	if err != nil {
		return nil, err
	}

	if srcSize != int64(len(base)) {
		return nil, fmt.Errorf("applyPackDelta: delta expects a %d-byte base, got %d bytes", srcSize, len(base))
	}

	out := make([]byte, 0, tgtSize)

	for pos < len(delta) {
		op := delta[pos]
		pos++

		switch {
		case op&0x80 != 0: // copy from base
			var offset, length uint64
			for i := uint(0); i < 4; i++ {
				if op&(1<<i) == 0 {
					continue
				}
				if pos >= len(delta) {
					return nil, errors.New("applyPackDelta: truncated copy offset")
				}
				offset |= uint64(delta[pos]) << (8 * i)
				pos++
			}
			for i := uint(0); i < 3; i++ {
				if op&(1<<(4+i)) == 0 {
					continue
				}
				if pos >= len(delta) {
					return nil, errors.New("applyPackDelta: truncated copy length")
				}
				length |= uint64(delta[pos]) << (8 * i)
				pos++
			}
			if length == 0 {
				length = 0x10000
			}
			if offset+length > uint64(len(base)) {
				return nil, fmt.Errorf("applyPackDelta: copy range [%d, %d) exceeds base size %d", offset, offset+length, len(base))
			}
			out = append(out, base[offset:offset+length]...)

		case op != 0: // insert literal bytes
			n := int(op)
			if pos+n > len(delta) {
				return nil, errors.New("applyPackDelta: truncated insert data")
			}
			out = append(out, delta[pos:pos+n]...)
			pos += n

		default:
			return nil, errors.New("applyPackDelta: invalid delta opcode 0x00")
		}
	}

	if int64(len(out)) != tgtSize {
		return nil, fmt.Errorf("applyPackDelta: delta produced %d bytes, expected %d", len(out), tgtSize)
	}
	return out, nil
}

// readPackVarint decodes the little-endian base-128 varint used for the
// source/target sizes at the start of a delta stream (NOT the same encoding
// as entry headers or ofs-delta offsets).
func readPackVarint(b []byte, pos int) (int64, int, error) {
	var value int64
	for shift := uint(0); ; shift += 7 {
		if pos >= len(b) {
			return 0, pos, errors.New("readPackVarint: truncated delta size")
		}
		if shift+7 > 64 {
			return 0, pos, errors.New("readPackVarint: delta size too large")
		}
		c := b[pos]
		pos++
		value |= int64(c&0x7F) << shift
		if c&0x80 == 0 {
			return value, pos, nil
		}
	}
}

// WritePack encodes objects as a version 2 packfile and writes it to w.
// Every object is stored undeltified as zlib-compressed content — simple,
// and exactly what git's index-pack/verify-pack expect a valid pack to
// contain. The trailer is the SHA-1 over everything written before it.
//
// The SHA1 field of each PackObject is informational and is not serialized:
// object names live in the .idx, not in the .pack itself.
func WritePack(w io.Writer, objects []PackObject) error {
	hasher := sha1.New()
	out := io.MultiWriter(w, hasher)

	var header [PackHeaderLen]byte
	copy(header[0:4], PackSignature)
	binary.BigEndian.PutUint32(header[4:8], 2) // pack format version
	binary.BigEndian.PutUint32(header[8:12], uint32(len(objects)))
	if _, err := out.Write(header[:]); err != nil {
		return fmt.Errorf("WritePack: writing header: %w", err)
	}

	for i, obj := range objects {
		switch obj.Type {
		case ObjCommit, ObjTree, ObjBlob, ObjTag:
			// plain entries only; deltified storage is an optimization
		default:
			return fmt.Errorf("WritePack: object %d: cannot store type %d undeltified", i, obj.Type)
		}

		if _, err := out.Write(encodePackEntryHeader(obj.Type, int64(len(obj.Data)))); err != nil {
			return fmt.Errorf("WritePack: object %d: writing entry header: %w", i, err)
		}

		zw := zlib.NewWriter(out)
		if _, err := zw.Write(obj.Data); err != nil {
			return fmt.Errorf("WritePack: object %d: compressing: %w", i, err)
		}
		if err := zw.Close(); err != nil {
			return fmt.Errorf("WritePack: object %d: flushing zlib stream: %w", i, err)
		}
	}

	if _, err := w.Write(hasher.Sum(nil)); err != nil {
		return fmt.Errorf("WritePack: writing trailer: %w", err)
	}
	return nil
}

// encodePackEntryHeader builds an entry's variable-length type/size header;
// the inverse of readPackEntryHeader.
func encodePackEntryHeader(typ ObjectType, size int64) []byte {
	b := byte(typ&0x07)<<4 | byte(size&0x0F)
	size >>= 4

	header := make([]byte, 0, 10)
	for size > 0 {
		header = append(header, b|0x80)
		b = byte(size & 0x7F)
		size >>= 7
	}
	return append(header, b)
}
