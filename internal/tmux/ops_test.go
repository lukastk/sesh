package tmux

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestNextSendBufferNameConcurrentUnique(t *testing.T) {
	const count = 512
	prefix := fmt.Sprintf("sesh-send-%d-", os.Getpid())
	names := make(chan string, count)
	var group sync.WaitGroup
	for range count {
		group.Add(1)
		go func() {
			defer group.Done()
			names <- nextSendBufferName()
		}()
	}
	group.Wait()
	close(names)

	seen := make(map[string]struct{}, count)
	for name := range names {
		if !strings.HasPrefix(name, prefix) {
			t.Errorf("buffer name %q does not have process prefix %q", name, prefix)
		}
		if _, exists := seen[name]; exists {
			t.Errorf("duplicate buffer name %q", name)
		}
		seen[name] = struct{}{}
	}
	if len(seen) != count {
		t.Fatalf("generated %d unique buffer names, want %d", len(seen), count)
	}
}
