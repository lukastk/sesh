package daemon

import (
	"reflect"
	"testing"
)

// The matcher must select exactly the processes whose EXECUTABLE lives under the
// home's managed-daemon package dir — never a lookalike, a different home, or a
// process that merely mentions the path in its arguments. Asserted on the pure
// matcher: no process is signalled by this test.
func TestMatchExePrefix(t *testing.T) {
	const prefix = "/home/u/.codex/packages/app-server-daemon/"
	ps := `  101 /home/u/.codex/packages/app-server-daemon/releases/0.159.0/bin/codex app-server --listen unix:// --managed-daemon
  102 /home/u/.codex/packages/app-server-daemon/releases/0.159.0/bin/codex app-server daemon pid-update-loop
  103 /home/u/.codex-other/packages/app-server-daemon/releases/0.159.0/bin/codex app-server --managed-daemon
  104 /tmp/sesh-codex-home-1/packages/app-server-daemon/releases/0.159.0/bin/codex app-server --managed-daemon
  105 grep /home/u/.codex/packages/app-server-daemon/
  106 sleep 86400
  107 /home/u/.codex/packages/app-server-daemonX/bin/codex
  108 node /home/u/.local/bin/codex --dangerously-bypass-approvals-and-sandbox
`
	got := matchExePrefix(ps, prefix)
	want := []string{
		"101 /home/u/.codex/packages/app-server-daemon/releases/0.159.0/bin/codex app-server --listen unix:// --managed-daemon",
		"102 /home/u/.codex/packages/app-server-daemon/releases/0.159.0/bin/codex app-server daemon pid-update-loop",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("matchExePrefix =\n%q\nwant\n%q", got, want)
	}
}
