package claudeweb

import "testing"

func TestAnAxleRequestWithoutCredentialsGetsTheKernels(t *testing.T) {
	old := axleKernelCreds
	defer func() { axleKernelCreds = old }()
	axleKernelCreds = func() (string, string) { return "SESS", "CSRF" }
	set := axleRequestSet{Payloads: []string{"{}"}}
	if !fillAxleCredentials(&set) || set.Session != "SESS" || set.CSRF != "CSRF" {
		t.Fatalf("%+v", set)
	}
	own := axleRequestSet{Payloads: []string{"{}"}, Session: "MINE", CSRF: "C"}
	if fillAxleCredentials(&own) || own.Session != "MINE" {
		t.Fatalf("a request with its own credentials is left as it is: %+v", own)
	}
}
