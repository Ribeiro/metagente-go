package secret

import "testing"

func TestRedactHidesSecretsAndLeavesShortWordsAlone(t *testing.T) {
	if got := Redact("bad token tkn-123456 here", "tkn-123456"); got != "bad token [hidden] here" {
		t.Errorf("got %q", got)
	}
	if got := Redact("a key, ok", "key"); got != "a key, ok" {
		t.Errorf("a short word was hidden: %q", got)
	}
	if got := Redact("nothing", "", "also-not-there-1"); got != "nothing" {
		t.Errorf("got %q", got)
	}
}

func TestOnlyANameIsAcceptedAsTheNameOfAVariable(t *testing.T) {
	for _, good := range []string{"BOB_TOKEN", "my_key2", "_x"} {
		if !ValidEnvName(good) {
			t.Errorf("%q was refused", good)
		}
	}
	for _, bad := range []string{"", "1KEY", "MY-KEY", "MY KEY", "key=value", "sk-ant-usr-abc", "tok.en"} {
		if ValidEnvName(bad) {
			t.Errorf("%q was accepted", bad)
		}
	}
}
