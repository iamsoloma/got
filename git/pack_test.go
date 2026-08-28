package git

// ============================================================================
// Pack file tests (TDD)
//
// These tests specify the expected behaviour of a .pack reader/writer that
// is meant to live in git/pack.go (not yet implemented). Write pack.go until
// every test below is green, one step at a time.
//
// Format reference, git-scm.com only:
//   - https://git-scm.com/docs/pack-format        pack-*.pack byte layout
//   - https://git-scm.com/docs/git-pack-objects    how real git builds packs
//   - https://git-scm.com/docs/git-verify-pack     how to inspect a pack
//   - https://git-scm.com/docs/git-index-pack      how to validate a pack
//
// Expected API (git/pack.go):
//
//	type ObjectType int
//
//	const (
//	    ObjCommit   ObjectType = 1
//	    ObjTree     ObjectType = 2
//	    ObjBlob     ObjectType = 3
//	    ObjTag      ObjectType = 4
//	    ObjOfsDelta ObjectType = 6
//	    ObjRefDelta ObjectType = 7
//	)
//
//	// PackObject is a single object as read out of a pack, already
//	// delta-resolved. Data holds the raw object content ONLY, i.e. WITHOUT
//	// the "type size\x00" prefix used for loose objects.
//	type PackObject struct {
//	    Type ObjectType
//	    SHA1 string
//	    Data []byte
//	}
//
//	type Pack struct {
//	    Version uint32
//	    Objects []PackObject
//	}
//
//	func ParsePack(r io.Reader) (*Pack, error)
//	func WritePack(w io.Writer, objects []PackObject) error
//
// Steps covered below, in order:
//   1. Header: signature "PACK", version, object count, malformed headers.
//   2. Trailer: pack checksum validation.
//   3. Undeltified entries: a single blob, checked against real git.
//   4. Undeltified entries: mixed commit/tree/blob objects in one pack.
//   5. Variable-length size encoding for objects spanning multiple bytes.
//   6. Deltified entries: OBJ_OFS_DELTA resolution.
//   7. Deltified entries: OBJ_REF_DELTA resolution.
//   8. Writer: produces a pack real git accepts (index-pack / verify-pack).
//   9. Writer + parser round trip.
//  10. End-to-end cross-check against a pack built by real `git pack-objects`.
// ============================================================================

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"got/storage/filesystem"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ----------------------------------------------------------------------------
// helpers
// ----------------------------------------------------------------------------

// buildRawPack hand-assembles a pack byte stream from a header and a list of
// already-encoded entries, appending a correct trailing SHA-1 checksum. Used
// to test header/trailer parsing without depending on our own writer.
func buildRawPack(version uint32, objectCount uint32, entries [][]byte) []byte {
	var buf bytes.Buffer
	buf.WriteString("PACK")
	writeUint32BE(&buf, version)
	writeUint32BE(&buf, objectCount)

	for _, e := range entries {
		buf.Write(e)
	}

	sum := sha1.Sum(buf.Bytes())
	buf.Write(sum[:])

	return buf.Bytes()
}

func writeUint32BE(buf *bytes.Buffer, v uint32) {
	buf.WriteByte(byte(v >> 24))
	buf.WriteByte(byte(v >> 16))
	buf.WriteByte(byte(v >> 8))
	buf.WriteByte(byte(v))
}

// packObjectsToBytes runs the real `git pack-objects` on the given object
// names and returns the resulting .pack bytes, used as a golden fixture.
func packObjectsToBytes(t *testing.T, extraArgs []string, shas []string) []byte {
	t.Helper()

	args := append([]string{"pack-objects"}, extraArgs...)
	args = append(args, "--stdout")

	cmd := exec.Command("git", args...)
	cmd.Stdin = strings.NewReader(strings.Join(shas, "\n") + "\n")

	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut

	if err := cmd.Run(); err != nil {
		t.Fatalf("git pack-objects failed: %v, stderr: %s", err, errOut.String())
	}

	return out.Bytes()
}

// packObjectsToFile is like packObjectsToBytes but writes a real .pack/.idx
// pair to disk (basename-prefixed), so it can be inspected by other git
// plumbing commands such as verify-pack.
func packObjectsToFile(t *testing.T, dir string, extraArgs []string, shas []string) (packPath string) {
	t.Helper()

	base := filepath.Join(dir, "testpack")
	args := append([]string{"pack-objects"}, extraArgs...)
	args = append(args, base)

	cmd := exec.Command("git", args...)
	cmd.Stdin = strings.NewReader(strings.Join(shas, "\n") + "\n")

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git pack-objects failed: %v, output: %s", err, out)
	}

	matches, err := filepath.Glob(base + "-*.pack")
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected exactly one generated pack file, got %v (err=%v)", matches, err)
	}
	return matches[0]
}

// gitVerifyPack runs `git verify-pack -v` against a pack file on disk.
func gitVerifyPack(t *testing.T, packPath string) string {
	t.Helper()
	out, err := exec.Command("git", "verify-pack", "-v", packPath).CombinedOutput()
	if err != nil {
		t.Fatalf("git verify-pack failed: %v, output: %s", err, out)
	}
	return string(out)
}

// rawCatFile returns the exact, untrimmed bytes of an object's content via
// `git cat-file <type> <sha>`, unlike the runGit helper which trims
// whitespace and would corrupt a commit/tree object's trailing newline.
func rawCatFile(t *testing.T, objType, sha string) []byte {
	t.Helper()
	out, err := exec.Command("git", "cat-file", objType, sha).Output()
	if err != nil {
		t.Fatalf("git cat-file %s %s failed: %v", objType, sha, err)
	}
	return out
}

// gitIndexPack runs `git index-pack` against a raw .pack file. It validates
// the trailer checksum and builds a matching .idx file, failing if git
// itself considers the pack malformed.
func gitIndexPack(t *testing.T, packPath string) string {
	t.Helper()
	out, err := exec.Command("git", "index-pack", packPath).CombinedOutput()
	if err != nil {
		t.Fatalf("git index-pack failed: %v, output: %s", err, out)
	}
	return string(out)
}

// looseObjectSHA1 computes the canonical object name the same way
// `git hash-object` / Repository.WriteObject do: sha1("type size\x00content").
func looseObjectSHA1(objType string, content []byte) string {
	header := fmt.Sprintf("%s %d\x00", objType, len(content))
	sum := sha1.Sum(append([]byte(header), content...))
	return hex.EncodeToString(sum[:])
}

func objTypeName(ot ObjectType) string {
	switch ot {
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

// isDeltaEntry inspects `git verify-pack -v` output and reports whether the
// object with the given SHA-1 was stored as a delta (7 fields: object-name
// type size size-in-packfile offset-in-packfile depth base-object-name)
// rather than plain (5 fields).
func isDeltaEntry(verifyOut, sha string) bool {
	for _, line := range strings.Split(verifyOut, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == sha {
			return len(fields) >= 7
		}
	}
	return false
}

// ============================================================================
// Step 1: header — signature, version, object count
// https://git-scm.com/docs/pack-format
// ============================================================================

func TestParsePack_Header_EmptyPack(t *testing.T) {
	raw := buildRawPack(2, 0, nil)

	pack, err := ParsePack(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("ParsePack: unexpected error: %v", err)
	}
	if pack.Version != 2 {
		t.Errorf("Version: expected 2, got %d", pack.Version)
	}
	if len(pack.Objects) != 0 {
		t.Errorf("Objects: expected 0, got %d", len(pack.Objects))
	}
}

func TestParsePack_Header_InvalidSignature(t *testing.T) {
	raw := buildRawPack(2, 0, nil)
	raw[0] = 'X' // corrupt "PACK" -> "XACK"

	if _, err := ParsePack(bytes.NewReader(raw)); err == nil {
		t.Fatal("ParsePack: expected error for invalid signature, got nil")
	}
}

func TestParsePack_Header_UnsupportedVersion(t *testing.T) {
	// Git currently accepts version 2 or 3 and generates version 2 only;
	// any other value must be rejected.
	raw := buildRawPack(9, 0, nil)

	if _, err := ParsePack(bytes.NewReader(raw)); err == nil {
		t.Fatal("ParsePack: expected error for unsupported version, got nil")
	}
}

func TestParsePack_Header_TruncatedHeader(t *testing.T) {
	raw := buildRawPack(2, 0, nil)
	truncated := raw[:8] // cuts off in the middle of the object count field

	if _, err := ParsePack(bytes.NewReader(truncated)); err == nil {
		t.Fatal("ParsePack: expected error for truncated header, got nil")
	}
}

// ============================================================================
// Step 2: trailer — pack checksum
// ============================================================================

func TestParsePack_Trailer_ChecksumMismatch(t *testing.T) {
	raw := buildRawPack(2, 0, nil)
	raw[len(raw)-1] ^= 0xFF // flip a bit in the trailing SHA-1

	if _, err := ParsePack(bytes.NewReader(raw)); err == nil {
		t.Fatal("ParsePack: expected checksum validation error, got nil")
	}
}

// ============================================================================
// Step 3: undeltified object entries — single blob
// ============================================================================

func TestParsePack_SingleBlob(t *testing.T) {
	_, cleanup := setupGitRepo(t)
	defer cleanup()

	content := "hello, pack file!\n"
	createFile(t, "blob.txt", content)
	sha := runGit(t, "hash-object", "-w", "blob.txt")

	raw := packObjectsToBytes(t, nil, []string{sha})

	pack, err := ParsePack(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("ParsePack: %v", err)
	}
	if len(pack.Objects) != 1 {
		t.Fatalf("expected 1 object, got %d", len(pack.Objects))
	}

	obj := pack.Objects[0]
	if obj.Type != ObjBlob {
		t.Errorf("Type: expected ObjBlob, got %v", obj.Type)
	}
	if obj.SHA1 != sha {
		t.Errorf("SHA1: expected %s, got %s", sha, obj.SHA1)
	}
	if string(obj.Data) != content {
		t.Errorf("Data: expected %q, got %q", content, string(obj.Data))
	}
}

// ============================================================================
// Step 4: undeltified object entries — mixed commit/tree/blob
// ============================================================================

func TestParsePack_MixedObjectTypes(t *testing.T) {
	_, cleanup := setupGitRepo(t)
	defer cleanup()

	createFile(t, "a.txt", "aaa\n")
	createFile(t, "b.txt", "bbb\n")
	runGit(t, "add", ".")
	treeSHA := runGit(t, "write-tree")
	commitSHA := runGit(t, "commit-tree", treeSHA, "-m", "test commit")

	blobA := runGit(t, "rev-parse", treeSHA+":a.txt")
	blobB := runGit(t, "rev-parse", treeSHA+":b.txt")

	shas := []string{commitSHA, treeSHA, blobA, blobB}
	raw := packObjectsToBytes(t, nil, shas)

	pack, err := ParsePack(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("ParsePack: %v", err)
	}
	if len(pack.Objects) != len(shas) {
		t.Fatalf("expected %d objects, got %d", len(shas), len(pack.Objects))
	}

	wantType := map[string]ObjectType{
		commitSHA: ObjCommit,
		treeSHA:   ObjTree,
		blobA:     ObjBlob,
		blobB:     ObjBlob,
	}

	seen := map[string]bool{}
	for _, obj := range pack.Objects {
		wt, ok := wantType[obj.SHA1]
		if !ok {
			t.Errorf("unexpected object in pack: %s", obj.SHA1)
			continue
		}
		if obj.Type != wt {
			t.Errorf("%s: expected type %s, got %s", obj.SHA1, objTypeName(wt), objTypeName(obj.Type))
		}

		// Recomputing the loose-object hash from (type, Data) exercises the
		// size/type framing for every entry, independent of object kind.
		recomputed := looseObjectSHA1(objTypeName(obj.Type), obj.Data)
		if recomputed != obj.SHA1 {
			t.Errorf("hash mismatch for %s: recomputed %s", obj.SHA1, recomputed)
		}
		seen[obj.SHA1] = true
	}
	for sha := range wantType {
		if !seen[sha] {
			t.Errorf("missing object %s in parsed pack", sha)
		}
	}
}

// ============================================================================
// Step 5: variable-length size encoding
// "the seven least significant bits are used to form the resulting integer;
// as long as the most significant bit is 1, this process continues"
// ============================================================================

func TestParsePack_LargeBlob_MultiByteSizeHeader(t *testing.T) {
	_, cleanup := setupGitRepo(t)
	defer cleanup()

	// The first size byte only carries 4 bits, every following byte carries
	// 7 more, so anything past a few KB forces a multi-byte size header.
	content := strings.Repeat("pack-format size encoding test line\n", 500)
	createFile(t, "big.txt", content)
	sha := runGit(t, "hash-object", "-w", "big.txt")

	raw := packObjectsToBytes(t, nil, []string{sha})

	pack, err := ParsePack(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("ParsePack: %v", err)
	}
	if len(pack.Objects) != 1 {
		t.Fatalf("expected 1 object, got %d", len(pack.Objects))
	}
	if string(pack.Objects[0].Data) != content {
		t.Errorf("Data mismatch for large blob: expected len=%d, got len=%d", len(content), len(pack.Objects[0].Data))
	}
}

// ============================================================================
// Step 6: deltified representation — OBJ_OFS_DELTA
// "If the base object is in the same pack, ofs-delta encodes the offset of
// the base object in the pack instead [of a 20-byte object name]."
// git only emits OFS_DELTA when --delta-base-offset is requested.
// ============================================================================

func TestParsePack_OfsDelta_Resolution(t *testing.T) {
	dir, cleanup := setupGitRepo(t)
	defer cleanup()

	textA := strings.Repeat("The quick brown fox jumps over the lazy dog.\n", 200)
	textB := textA + "one more trailing line that differs from the base.\n"

	createFile(t, "a.txt", textA)
	createFile(t, "b.txt", textB)
	shaA := runGit(t, "hash-object", "-w", "a.txt")
	shaB := runGit(t, "hash-object", "-w", "b.txt")
	content := map[string]string{shaA: textA, shaB: textB}

	packPath := packObjectsToFile(t, dir, []string{"--delta-base-offset", "--window=10"},
		[]string{shaA, shaB})

	verifyOut := gitVerifyPack(t, packPath)
	// git picks whichever of the two similar blobs is cheaper to express as
	// a delta of the other - not necessarily the textually "newer" one - so
	// find out which SHA it actually deltified rather than assuming.
	deltaSHA := ""
	for _, sha := range []string{shaA, shaB} {
		if isDeltaEntry(verifyOut, sha) {
			deltaSHA = sha
			break
		}
	}
	if deltaSHA == "" {
		t.Skip("git did not store either blob as a delta; nothing to resolve")
	}

	raw, err := os.ReadFile(packPath)
	if err != nil {
		t.Fatal(err)
	}

	pack, err := ParsePack(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("ParsePack: %v", err)
	}

	var got []byte
	for _, obj := range pack.Objects {
		if obj.SHA1 == deltaSHA {
			got = obj.Data
		}
	}
	if got == nil {
		t.Fatalf("deltified blob %s not found among parsed objects", deltaSHA)
	}
	if string(got) != content[deltaSHA] {
		t.Errorf("OFS_DELTA resolution mismatch for %s: expected len=%d, got len=%d",
			deltaSHA, len(content[deltaSHA]), len(got))
	}
}

// ============================================================================
// Step 7: deltified representation — OBJ_REF_DELTA
// "ref-delta directly encodes base object name [...] Ref-delta can also
// refer to an object outside the pack." Here the base stays inside the
// pack; git's default (no --delta-base-offset) still uses the 20-byte
// object-name form for compatibility.
// ============================================================================

func TestParsePack_RefDelta_Resolution(t *testing.T) {
	dir, cleanup := setupGitRepo(t)
	defer cleanup()

	textA := strings.Repeat("Lorem ipsum dolor sit amet, consectetur adipiscing.\n", 200)
	textB := textA + "an extra line appended after the shared base content.\n"

	createFile(t, "a.txt", textA)
	createFile(t, "b.txt", textB)
	shaA := runGit(t, "hash-object", "-w", "a.txt")
	shaB := runGit(t, "hash-object", "-w", "b.txt")
	content := map[string]string{shaA: textA, shaB: textB}

	// No --delta-base-offset: if git deltifies at all, it must use REF_DELTA.
	packPath := packObjectsToFile(t, dir, []string{"--window=10"}, []string{shaA, shaB})

	verifyOut := gitVerifyPack(t, packPath)
	deltaSHA := ""
	for _, sha := range []string{shaA, shaB} {
		if isDeltaEntry(verifyOut, sha) {
			deltaSHA = sha
			break
		}
	}
	if deltaSHA == "" {
		t.Skip("git did not store either blob as a delta; nothing to resolve")
	}

	raw, err := os.ReadFile(packPath)
	if err != nil {
		t.Fatal(err)
	}

	pack, err := ParsePack(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("ParsePack: %v", err)
	}

	var got []byte
	for _, obj := range pack.Objects {
		if obj.SHA1 == deltaSHA {
			got = obj.Data
		}
	}
	if got == nil {
		t.Fatalf("deltified blob %s not found among parsed objects", deltaSHA)
	}
	if string(got) != content[deltaSHA] {
		t.Errorf("REF_DELTA resolution mismatch for %s: expected len=%d, got len=%d",
			deltaSHA, len(content[deltaSHA]), len(got))
	}
}

// ============================================================================
// Step 8: WritePack — produces a pack real git accepts
// ============================================================================

func TestWritePack_AcceptedByGitIndexPack(t *testing.T) {
	dir, cleanup := setupGitRepo(t)
	defer cleanup()

	createFile(t, "a.txt", "aaa\n")
	createFile(t, "b.txt", "bbb\n")
	runGit(t, "add", ".")
	treeSHA := runGit(t, "write-tree")
	commitSHA := runGit(t, "commit-tree", treeSHA, "-m", "written by got")

	blobA := runGit(t, "rev-parse", treeSHA+":a.txt")
	blobB := runGit(t, "rev-parse", treeSHA+":b.txt")

	repo, err := Open(&filesystem.FileSystemStorage{}, dir)
	if err != nil {
		t.Fatalf("can`t open a repo: %s", err.Error())
	}

	objects := []PackObject{
		{Type: ObjCommit, SHA1: commitSHA, Data: rawCatFile(t, "commit", commitSHA)},
		{Type: ObjTree, SHA1: treeSHA, Data: rawTreeBytes(t, repo, treeSHA)},
		{Type: ObjBlob, SHA1: blobA, Data: []byte("aaa\n")},
		{Type: ObjBlob, SHA1: blobB, Data: []byte("bbb\n")},
	}

	var buf bytes.Buffer
	if err := WritePack(&buf, objects); err != nil {
		t.Fatalf("WritePack: %v", err)
	}

	packPath := filepath.Join(dir, "got-written.pack")
	if err := os.WriteFile(packPath, buf.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}

	// If our writer produced anything git considers invalid (bad header,
	// bad per-object framing, bad trailer checksum), this fails.
	gitIndexPack(t, packPath)

	verifyOut := gitVerifyPack(t, packPath)
	for _, obj := range objects {
		if !strings.Contains(verifyOut, obj.SHA1) {
			t.Errorf("git verify-pack output missing expected object %s:\n%s", obj.SHA1, verifyOut)
		}
	}
}

// rawTreeBytes reconstructs a tree object's raw on-disk payload (mode, name,
// 20-byte sha triples) directly from `got`'s own LsTree, so the test does
// not depend on WritePack already existing to build its own fixtures.
func rawTreeBytes(t *testing.T, repo *Repository, treeSHA string) []byte {
	t.Helper()

	nodes, err := repo.LsTree(treeSHA)
	if err != nil {
		t.Fatalf("LsTree: %v", err)
	}

	var buf bytes.Buffer
	for _, node := range nodes {
		buf.WriteString(strings.TrimLeft(node.Mode.String(), "0"))
		buf.WriteByte(' ')
		buf.WriteString(node.Name)
		buf.WriteByte(0)
		shaBytes, err := hex.DecodeString(node.Sha1)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(shaBytes)
	}
	return buf.Bytes()
}

// ============================================================================
// Step 9: WritePack + ParsePack round trip
// ============================================================================

func TestWritePack_ParsePack_RoundTrip(t *testing.T) {
	objects := []PackObject{
		{Type: ObjBlob, SHA1: looseObjectSHA1("blob", []byte("first\n")), Data: []byte("first\n")},
		{Type: ObjBlob, SHA1: looseObjectSHA1("blob", []byte("second\n")), Data: []byte("second\n")},
	}

	var buf bytes.Buffer
	if err := WritePack(&buf, objects); err != nil {
		t.Fatalf("WritePack: %v", err)
	}

	pack, err := ParsePack(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("ParsePack: %v", err)
	}
	if len(pack.Objects) != len(objects) {
		t.Fatalf("expected %d objects, got %d", len(objects), len(pack.Objects))
	}

	byWant := map[string]PackObject{}
	for _, o := range objects {
		byWant[o.SHA1] = o
	}
	for _, got := range pack.Objects {
		want, ok := byWant[got.SHA1]
		if !ok {
			t.Errorf("unexpected object %s after round trip", got.SHA1)
			continue
		}
		if got.Type != want.Type || string(got.Data) != string(want.Data) {
			t.Errorf("round trip mismatch for %s: want {%v,%q}, got {%v,%q}",
				got.SHA1, want.Type, want.Data, got.Type, got.Data)
		}
	}
}

// ============================================================================
// Step 10: end-to-end cross-check against a real `git pack-objects` pack
// ============================================================================

func TestPack_MatchesRealGitPackObjects(t *testing.T) {
	dir, cleanup := setupGitRepo(t)
	defer cleanup()

	createFile(t, "file1.txt", "content one\n")
	createFile(t, "file2.txt", "content two\n")
	runGit(t, "add", ".")
	treeSHA := runGit(t, "write-tree")
	commitSHA := runGit(t, "commit-tree", treeSHA, "-m", "cross-check commit")

	shas := []string{
		commitSHA,
		treeSHA,
		runGit(t, "rev-parse", treeSHA+":file1.txt"),
		runGit(t, "rev-parse", treeSHA+":file2.txt"),
	}

	// Reference pack produced entirely by real git.
	referenceRaw := packObjectsToBytes(t, nil, shas)
	reference, err := ParsePack(bytes.NewReader(referenceRaw))
	if err != nil {
		t.Fatalf("ParsePack (reference): %v", err)
	}

	// Same objects, repacked by our own writer.
	var buf bytes.Buffer
	if err := WritePack(&buf, reference.Objects); err != nil {
		t.Fatalf("WritePack: %v", err)
	}
	ours, err := ParsePack(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("ParsePack (ours): %v", err)
	}

	if len(ours.Objects) != len(reference.Objects) {
		t.Fatalf("object count mismatch: git=%d got=%d", len(reference.Objects), len(ours.Objects))
	}

	refBySHA := map[string]PackObject{}
	for _, o := range reference.Objects {
		refBySHA[o.SHA1] = o
	}
	for _, o := range ours.Objects {
		want, ok := refBySHA[o.SHA1]
		if !ok {
			t.Errorf("got produced an object git's pack doesn't have: %s", o.SHA1)
			continue
		}
		if o.Type != want.Type {
			t.Errorf("%s: type mismatch: git=%s got=%s", o.SHA1, objTypeName(want.Type), objTypeName(o.Type))
		}
		if string(o.Data) != string(want.Data) {
			t.Errorf("%s: content mismatch after round trip through got's writer", o.SHA1)
		}
	}

	_ = dir
}
