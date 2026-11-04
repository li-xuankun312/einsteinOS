package kernel

import "testing"

// 11/04 09:35:45 and 09:46:54: GitHub answered a fully uploaded push with "[remote
// rejected] … (failure)" / "(Internal Server Error)". Those are retried; a refusal of the
// update itself is not.
func TestAServerSideRefusalIsRetriedAnUpdateRefusalIsNot(t *testing.T) {
	retry := []string{
		"To github.com:zeng-bin3123/baloon.git\n ! [remote rejected] einsteinOS/dev-c880662efd -> einsteinOS/dev-c880662efd (failure)\nerror: failed to push some refs to 'git@github.com:zeng-bin3123/baloon.git'",
		"Total 18 (delta 11), reused 0 (delta 0)\nTo github.com:zeng-bin3123/baloon.git\n ! [remote rejected] einsteinOS/dev-c880662efd -> einsteinOS/dev-c880662efd (Internal Server Error)\nerror: failed to push some refs",
		"send-pack: unexpected disconnect while reading sideband packet\nfatal: the remote end hung up unexpectedly",
	}
	for _, out := range retry {
		if ok, why := gitPushServerSide(out); !ok || why == "" {
			t.Errorf("must be retried: %q", out)
		}
	}
	final := []string{
		" ! [rejected]        main -> main (non-fast-forward)\nerror: failed to push some refs",
		" ! [rejected]        main -> main (fetch first)\nerror: failed to push some refs",
		" ! [remote rejected] main -> main (protected branch hook declined)\nerror: failed to push some refs",
		" ! [remote rejected] main -> main (pre-receive hook declined)\nerror: failed to push some refs",
	}
	for _, out := range final {
		if ok, _ := gitPushServerSide(out); ok {
			t.Errorf("the same push gets the same answer, do not retry: %q", out)
		}
	}
}
