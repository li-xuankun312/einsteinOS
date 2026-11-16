package claudeweb

import (
	"fmt"
	"strings"
	"testing"
)

// What every conversation is told about the machine does not offer it the machine's
// credentials. 11/14 09:03 that line ended "credentials in the user's message are for that
// machine", and the dev conversations spent two days looking for tokens.
func TestTheMachinesFactsDoNotOfferItsCredentials(t *testing.T) {
	facts := fmt.Sprintf(localExecFactsFmt, "Iluvatar (4x)")
	if strings.Contains(facts, "credentials in the user's message are for that machine") {
		t.Fatal("the conversation is told the machine's credentials are its to use")
	}
	for _, want := range []string{
		"the kernel holds that machine's credentials and pushes your work itself",
		"not yours to find or use",
		"commands that read them are not run there",
	} {
		if !strings.Contains(facts, want) {
			t.Errorf("missing: %q", want)
		}
	}
}
