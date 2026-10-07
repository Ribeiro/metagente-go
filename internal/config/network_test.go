package config

import (
	"slices"
	"strings"
	"testing"
)

func TestTheProxyOfTheNetworkSectionIsReadAndItsPasswordComesFromAVariable(t *testing.T) {
	cfg := Default()
	text := "[network]\nhttp_proxy = \"http://proxy.example.com:3128/\"\nhttp_proxy_auth_env = \"PROXY_AUTH\"\n"
	if err := cfg.apply("metagente.toml", text); err != nil {
		t.Fatal(problemText(t, err))
	}
	if len(cfg.Warnings) != 0 {
		t.Errorf("warnings: %v", cfg.Warnings)
	}
	if cfg.Network.HTTPProxy != "http://proxy.example.com:3128" || cfg.Network.HTTPProxyAuthEnv != "PROXY_AUTH" {
		t.Errorf("network = %+v", cfg.Network)
	}
	proxy, err := cfg.Network.ProxyURL(getenvFrom(map[string]string{"PROXY_AUTH": " ana:s3cr:et "}))
	if err != nil {
		t.Fatal(problemText(t, err))
	}
	password, _ := proxy.User.Password()
	if proxy.Host != "proxy.example.com:3128" || proxy.User.Username() != "ana" || password != "s3cr:et" {
		t.Errorf("proxy = %v", proxy.Redacted())
	}
	// The password is a secret like the others: agents and tool servers never read it.
	if !slices.Contains(cfg.HiddenEnv(), "PROXY_AUTH") {
		t.Errorf("hidden = %v", cfg.HiddenEnv())
	}
}

func TestWithoutAProxyThereIsNone(t *testing.T) {
	proxy, err := Default().Network.ProxyURL(getenvFrom(map[string]string{"HTTPS_PROXY": "http://elsewhere:8080"}))
	if err != nil || proxy != nil {
		t.Errorf("proxy = %v, %v", proxy, err)
	}
	cfg := Default()
	if err := cfg.apply("metagente.toml", "[network]\nhttp_proxy = \"\"\n"); err != nil || cfg.Network.HTTPProxy != "" {
		t.Errorf("an empty address = %q, %v", cfg.Network.HTTPProxy, err)
	}
	cfg = Default()
	cfg.Network.HTTPProxy = "https://proxy.example.com"
	if proxy, err := cfg.Network.ProxyURL(getenvFrom(nil)); err != nil || proxy.String() != "https://proxy.example.com" {
		t.Errorf("a proxy without a password = %v, %v", proxy, err)
	}
}

func TestAProxyThatIsNotAnAddressIsExplained(t *testing.T) {
	for value, want := range map[string]string{
		`"proxy.example.com:3128"`:          "the address of a web proxy",
		`"socks5://proxy.example.com:1080"`: "the address of a web proxy",
		`"http://"`:                         "the address of a web proxy",
		`"http://proxy.example.com/path"`:   "with nothing after the port",
		`"http://proxy.example.com/?a=b"`:   "with nothing after the port",
		`3128`:                              "a text in quotes",
	} {
		err := Default().apply("metagente.toml", "[network]\nhttp_proxy = "+value+"\n")
		if shown := problemText(t, err); !strings.Contains(shown, want) || !strings.Contains(shown, "line 2") {
			t.Errorf("%s: %s", value, shown)
		}
	}
	err := Default().apply("metagente.toml", "[network]\nhttp_proxy_auth_env = \"ana:secret\"\n")
	if shown := problemText(t, err); !strings.Contains(shown, "the NAME of an environment variable") {
		t.Errorf("a password where the name goes: %s", shown)
	}
}

func TestThePasswordOfAProxyIsNeverShownInAnError(t *testing.T) {
	const password = "pw-live-0123456789"
	for name, text := range map[string]string{
		"in the address":            "[network]\nhttp_proxy = \"http://ana:" + password + "@proxy.example.com:3128\"\n",
		"in an address that is bad": "[network]\nhttp_proxy = \"ftp://ana:" + password + "@proxy.example.com\"\n",
		"without quotes":            "[network]\nhttp_proxy = http://ana:" + password + "@proxy.example.com\n",
	} {
		shown := problemText(t, Default().apply("metagente.toml", text))
		if strings.Contains(shown, password) {
			t.Errorf("%s: the password is in the message:\n%s", name, shown)
		}
		if !strings.Contains(shown, "line 2") {
			t.Errorf("%s: the message does not say where the problem is:\n%s", name, shown)
		}
	}
	shown := problemText(t, Default().apply("metagente.toml", "[network]\nhttp_proxy = \"http://ana:"+password+"@proxy.example.com\"\n"))
	if !strings.Contains(shown, "http_proxy_auth_env") {
		t.Errorf("the message does not say where the password goes:\n%s", shown)
	}
}

func TestAMissingOrMalformedPasswordOfTheProxyIsExplained(t *testing.T) {
	network := Network{HTTPProxy: "http://proxy.example.com:3128", HTTPProxyAuthEnv: "PROXY_AUTH"}
	for value, want := range map[string]string{
		"":           "PROXY_AUTH, which holds the user and password of the proxy, is not set",
		"pw-nocolon": "is not in the form user:password",
		":pw-noname": "is not in the form user:password",
	} {
		_, err := network.ProxyURL(getenvFrom(map[string]string{"PROXY_AUTH": value}))
		shown := problemText(t, err)
		if !strings.Contains(shown, want) || !strings.Contains(shown, "export PROXY_AUTH=") {
			t.Errorf("%q: %s", value, shown)
		}
		if value != "" && strings.Contains(shown, value) {
			t.Errorf("%q: the value is in the message: %s", value, shown)
		}
	}
	_, err := Network{HTTPProxyAuthEnv: "PROXY_AUTH"}.ProxyURL(getenvFrom(nil))
	if shown := problemText(t, err); !strings.Contains(shown, "no http_proxy") {
		t.Errorf("a password without a proxy: %s", shown)
	}
}
