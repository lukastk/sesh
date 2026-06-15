package conformance

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lukastk/sesh/internal/matrix"
)

func init() {
	for _, loc := range matrix.AllLocalities {
		loc := loc
		matrix.RegisterTest("blob.store", matrix.AgentAgnostic, loc,
			func(t *testing.T) { testBlobStore(t, loc) })
		matrix.RegisterTest("blob.expand", matrix.AgentAgnostic, loc,
			func(t *testing.T) { testBlobExpand(t, loc) })
	}
}

// blobAdd stores bytes via `blob add --stdin` and returns the full hash (parsed from
// the JSON), exercising the real store on the targeted daemon.
func (sb *Sandbox) blobAdd(t *testing.T, name, content string) string {
	t.Helper()
	stdout, stderr, err := sb.Runner.RunStdin(t, content, "blob", "add", "--stdin", "--name", name, "--json")
	if err != nil {
		t.Fatalf("blob add: %v\n%s", err, stderr)
	}
	var got struct {
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decode blob add: %v\nraw: %s", err, stdout)
	}
	if got.Hash == "" {
		t.Fatalf("blob add returned no hash: %s", stdout)
	}
	return got.Hash
}

// testBlobStore round-trips the content-addressed store: add → ls → get → rm, plus
// dedup and prefix resolution and the loud missing-blob error.
func testBlobStore(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	hash := sb.blobAdd(t, "shot.png", "PNGBYTES-unique-content")
	// Dedup: identical bytes, different name → same hash.
	hash2 := sb.blobAdd(t, "copy.png", "PNGBYTES-unique-content")
	if hash != hash2 {
		t.Fatalf("identical bytes hashed differently: %s vs %s", hash, hash2)
	}
	// ls shows it.
	stdout, stderr, err := sb.Runner.Run(t, "blob", "ls", "--json")
	if err != nil {
		t.Fatalf("blob ls: %v\n%s", err, stderr)
	}
	if !strings.Contains(stdout, hash) || !strings.Contains(stdout, "shot.png") {
		t.Errorf("ls missing the blob: %s", stdout)
	}
	// get by a short prefix returns the exact bytes.
	prefix := hash[:12]
	got, stderr, err := sb.Runner.Run(t, "blob", "get", prefix)
	if err != nil {
		t.Fatalf("blob get: %v\n%s", err, stderr)
	}
	if got != "PNGBYTES-unique-content" {
		t.Errorf("get bytes = %q, want the stored content", got)
	}
	// A non-existent prefix is loud.
	if _, _, err := sb.Runner.Run(t, "blob", "get", "ffffffffffff"); err == nil {
		t.Errorf("get of a missing blob was accepted")
	}
	// rm removes it.
	if _, stderr, err := sb.Runner.Run(t, "blob", "rm", prefix); err != nil {
		t.Fatalf("blob rm: %v\n%s", err, stderr)
	}
	stdout, _, _ = sb.Runner.Run(t, "blob", "ls", "--json")
	if strings.Contains(stdout, hash) {
		t.Errorf("blob still listed after rm: %s", stdout)
	}
}

// TestSendExpandsBlobReferences proves blob expansion is WIRED INTO the send path
// (not just the standalone `blob expand`): a headless turn whose prompt references a
// NON-EXISTENT blob is refused LOUDLY by the daemon before the turn runs — which can
// only happen if send-headless expands first. (A real-blob positive path is driven
// live in self-test, where an agent reads the expanded file.) Outside the matrix.
func TestSendExpandsBlobReferences(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, matrix.Local)
	sb.startDaemon(t)

	th := sb.newThread(t, "pi", "be", "/tmp")
	if _, _, err := sb.Runner.Run(t, "thread", "send-headless", "--id", th.ID, "--text", "look at @blob(deadbeefdead)"); err == nil {
		t.Errorf("send-headless with an unknown @blob token was accepted (expansion not wired into send?)")
	}
}

// testBlobExpand asserts token→path substitution, the @@ escape, and the loud error
// on a token referencing no blob.
func testBlobExpand(t *testing.T, loc matrix.Locality) {
	if testing.Short() {
		t.Skip("short mode")
	}
	sb := newSandbox(t, loc)
	sb.startDaemon(t)

	hash := sb.blobAdd(t, "data.txt", "the file body")
	prefix := hash[:12]

	// Token expands to an absolute path (containing the hash + filename).
	out, stderr, err := sb.Runner.RunStdin(t, "see @blob("+prefix+") ok", "blob", "expand")
	if err != nil {
		t.Fatalf("blob expand: %v\n%s", err, stderr)
	}
	if !strings.Contains(out, hash) || !strings.Contains(out, "data.txt") || strings.Contains(out, "@blob(") {
		t.Errorf("expand = %q, want the @blob token replaced by the blob path", out)
	}
	// The escape @@blob(...) is emitted literally, NOT expanded.
	out, _, err = sb.Runner.RunStdin(t, "literal @@blob("+prefix+") here", "blob", "expand")
	if err != nil {
		t.Fatalf("blob expand escape: %v", err)
	}
	if out != "literal @blob("+prefix+") here" {
		t.Errorf("escape = %q, want a literal @blob(...)", out)
	}
	// A token referencing no blob is a LOUD error (never a silent passthrough).
	if _, _, err := sb.Runner.RunStdin(t, "ghost @blob(aaaaaaaaaaaa)", "blob", "expand"); err == nil {
		t.Errorf("expand of an unknown blob token was accepted")
	}
}
