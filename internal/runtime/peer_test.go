package runtime

import "testing"

// TestCheckPeerSameUID runs on every supported OS: the accepted end of a
// socket dialled by this process must pass the peer check.
func TestCheckPeerSameUID(t *testing.T) {
	_, accepted := unixPair(t)
	if e := CheckPeer(accepted); e != nil {
		t.Fatalf("CheckPeer on a same-UID connection = %v, want nil", e)
	}
}
