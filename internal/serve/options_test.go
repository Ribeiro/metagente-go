package serve

import (
	"strings"
	"testing"

	"metagente/internal/diag"
)

var defaults = Defaults{Bind: "127.0.0.1", Port: 8080}

func plain() Options { return Options{Port: -1} }

func problem(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("expected a problem")
	}
	if d, ok := diag.From(err); ok {
		return d.Render()
	}
	return err.Error()
}

func TestWithNoFlagsTheServerIsOnThisComputerAndNobodyIsInFrontOfIt(t *testing.T) {
	plan, err := Resolve(plain(), defaults)
	if err != nil {
		t.Fatal(problem(t, err))
	}
	if plan.Address != "127.0.0.1:8080" || !plan.Loopback || plan.TLS || plan.NoThrottle || plan.PublicCard || len(plan.Hosts) != 0 {
		t.Errorf("plan = %+v", plan)
	}
	if got := strings.Join(plan.HostsFor(9000), " "); got != "127.0.0.1:9000 localhost:9000 [::1]:9000" {
		t.Errorf("hosts = %s", got)
	}
}

func TestThePortAndTheAddressComeFromTheFlagsBeforeTheConfiguration(t *testing.T) {
	o := plain()
	o.Port, o.Bind = 9191, "localhost"
	plan, err := Resolve(o, Defaults{Bind: "127.0.0.1", Port: 8080})
	if err != nil || plan.Address != "localhost:9191" {
		t.Fatalf("plan = %+v, err = %v", plan, err)
	}
	o.Port = 0 // the system chooses
	if plan, err := Resolve(o, defaults); err != nil || plan.Address != "localhost:0" {
		t.Errorf("plan = %+v, err = %v", plan, err)
	}
	o.Port = 70000
	if _, err := Resolve(o, defaults); err == nil {
		t.Error("a port that does not exist was accepted")
	}
}

// req: S5
func TestAServerDoesNotListenBeyondThisComputerUnlessItIsPublic(t *testing.T) {
	for _, bind := range []string{"0.0.0.0", "::", "192.0.2.10", "example.com", "[::]", ""} {
		o := plain()
		o.Bind = bind
		if bind == "" {
			o.Bind = "" // the configuration decides
		}
		d := defaults
		if bind == "" {
			d.Bind = "0.0.0.0" // a configuration that opens the server is the same thing
		}
		_, err := Resolve(o, d)
		if text := problem(t, err); !strings.Contains(text, "only listens beyond this computer when it is told to be public") {
			t.Errorf("bind %q: %s", bind, text)
		}
	}
	for _, bind := range []string{"127.0.0.1", "127.0.0.2", "::1", "[::1]", "localhost", "LOCALHOST"} {
		o := plain()
		o.Bind = bind
		if plan, err := Resolve(o, defaults); err != nil || !plan.Loopback {
			t.Errorf("bind %q: %v %+v", bind, err, plan)
		}
	}
}

// req: S5
func TestAPublicServerNeedsItsOwnCertificateAndTheNamesItAnswersTo(t *testing.T) {
	o := plain()
	o.Public = true
	_, err := Resolve(o, defaults)
	if text := problem(t, err); !strings.Contains(text, "needs TLS of its own") || !strings.Contains(text, "--behind-proxy") {
		t.Errorf("without a certificate: %s", text)
	}

	o.TLSCert, o.TLSKey = "cert.pem", "key.pem"
	_, err = Resolve(o, defaults)
	if text := problem(t, err); !strings.Contains(text, "has to know the names it answers to") {
		t.Errorf("without names: %s", text)
	}

	o.Hosts = []string{"Agents.Example.com", "agents.example.com", " agents.example.com:8443 "}
	plan, err := Resolve(o, defaults)
	if err != nil {
		t.Fatal(problem(t, err))
	}
	if plan.Address != "0.0.0.0:8080" || plan.Loopback || !plan.TLS || plan.NoThrottle {
		t.Errorf("plan = %+v", plan)
	}
	if got := strings.Join(plan.Hosts, " "); got != "agents.example.com agents.example.com:8443" {
		t.Errorf("hosts = %s", got)
	}
	// A public server never makes a token to show: nobody can be assumed to read it.
	if plan.Loopback {
		t.Error("a public server was taken for one a person is at")
	}
}

func TestPublicNeverGoesWithoutTLSEvenOnTheLoopback(t *testing.T) {
	o := plain()
	o.Public, o.Bind, o.Hosts = true, "127.0.0.1", []string{"a.example"}
	if _, err := Resolve(o, defaults); err == nil {
		t.Error("a public server without TLS was accepted")
	}
}

// req: S5
func TestBehindAProxyTheServerStaysOnThisComputer(t *testing.T) {
	o := plain()
	o.BehindProxy, o.Hosts, o.PublicURL = true, []string{"agents.example.com"}, "https://agents.example.com/"
	plan, err := Resolve(o, defaults)
	if err != nil {
		t.Fatal(problem(t, err))
	}
	if plan.Address != "127.0.0.1:8080" || !plan.NoThrottle || plan.Loopback || plan.PublicURL != "https://agents.example.com" {
		t.Errorf("plan = %+v", plan)
	}

	for _, bind := range []string{"0.0.0.0", "192.0.2.10"} {
		o.Bind = bind
		_, err := Resolve(o, defaults)
		if text := problem(t, err); !strings.Contains(text, "only counts when the server listens on this same computer") {
			t.Errorf("bind %s: %s", bind, text)
		}
	}
	// ...and a configuration that opens the address does not slip it through.
	o.Bind = ""
	if _, err := Resolve(o, Defaults{Bind: "0.0.0.0", Port: 8080}); err == nil {
		t.Error("--behind-proxy accepted with a configuration that listens on every address")
	}
}

func TestBehindAProxyTheServerNeedsToKnowHowItIsReached(t *testing.T) {
	base := plain()
	base.BehindProxy = true
	_, err := Resolve(base, defaults)
	if text := problem(t, err); !strings.Contains(text, "names it answers to") {
		t.Errorf("without names: %s", text)
	}
	base.Hosts = []string{"agents.example.com"}
	_, err = Resolve(base, defaults)
	if text := problem(t, err); !strings.Contains(text, "--public-url https://agents.example.com") {
		t.Errorf("without a public address: %s", text)
	}
}

func TestPublicAndBehindProxyAreDifferentThingsAndTLSFilesGoInPairs(t *testing.T) {
	o := plain()
	o.Public, o.BehindProxy = true, true
	if text := problem(t, func() error { _, err := Resolve(o, defaults); return err }()); !strings.Contains(text, "cannot be used together") {
		t.Errorf("both: %s", text)
	}
	for _, pair := range [][2]string{{"cert.pem", ""}, {"", "key.pem"}} {
		o := plain()
		o.TLSCert, o.TLSKey = pair[0], pair[1]
		_, err := Resolve(o, defaults)
		if text := problem(t, err); !strings.Contains(text, "go together") {
			t.Errorf("%v: %s", pair, text)
		}
	}
	// TLS on the loopback is allowed: a person may want it there too.
	o = plain()
	o.TLSCert, o.TLSKey = "cert.pem", "key.pem"
	if plan, err := Resolve(o, defaults); err != nil || !plan.TLS || !plan.Loopback {
		t.Errorf("plan = %+v, err = %v", plan, err)
	}
}

func TestTheNamesTheServerAnswersToAreChecked(t *testing.T) {
	for _, bad := range []string{"https://a.example", "a.example/path", "user@a.example", "a b.example", "a.example:99999x", "-a.example", "a.example.", "", "a_b.example", "[::1", "a.example:", "http://[::1]:80"} {
		o := plain()
		o.Public, o.TLSCert, o.TLSKey, o.Hosts = true, "c", "k", []string{bad}
		if _, err := Resolve(o, defaults); err == nil {
			t.Errorf("%q was accepted as a host", bad)
		}
	}
	for _, good := range []string{"a.example", "A.Example:8443", "localhost:9000", "[::1]:8080", "10.0.0.5", "a-b.example"} {
		o := plain()
		o.Public, o.TLSCert, o.TLSKey, o.Hosts = true, "c", "k", []string{good}
		if _, err := Resolve(o, defaults); err != nil {
			t.Errorf("%q was refused: %s", good, problem(t, err))
		}
	}
}

func TestThePublicAddressIsHttpsWithNothingSecretAndAHostTheServerAnswersTo(t *testing.T) {
	for name, tt := range map[string]struct{ url, want string }{
		"http":     {"http://agents.example.com", "has to be like https://"},
		"user":     {"https://me:secret@agents.example.com", "has to be like https://"},
		"query":    {"https://agents.example.com/?token=abc", "has to be like https://"},
		"fragment": {"https://agents.example.com/#x", "has to be like https://"},
		"no host":  {"https://", "has to be like https://"},
		"other":    {"https://other.example.com", "not one the server answers to"},
	} {
		o := plain()
		o.BehindProxy, o.Hosts, o.PublicURL = true, []string{"agents.example.com"}, tt.url
		_, err := Resolve(o, defaults)
		text := problem(t, err)
		if !strings.Contains(text, tt.want) {
			t.Errorf("%s: %s", name, text)
		}
		if strings.Contains(text, "secret") || strings.Contains(text, "token=abc") {
			t.Errorf("%s: the address with a secret was repeated:\n%s", name, text)
		}
	}
	// A path is allowed: a proxy may mount the server under one.
	o := plain()
	o.BehindProxy, o.Hosts, o.PublicURL = true, []string{"agents.example.com"}, "https://Agents.Example.com/team/a/"
	if plan, err := Resolve(o, defaults); err != nil || plan.PublicURL != "https://Agents.Example.com/team/a" {
		t.Errorf("plan = %+v, err = %v", plan, err)
	}
}

func TestTheConfigurationFillsInWhatTheFlagsLeftOut(t *testing.T) {
	o := plain()
	o.BehindProxy = true
	plan, err := Resolve(o, Defaults{Bind: "127.0.0.1", Port: 9000, Hosts: []string{"agents.example.com"}, PublicURL: "https://agents.example.com"})
	if err != nil || plan.Address != "127.0.0.1:9000" || plan.PublicURL != "https://agents.example.com" {
		t.Errorf("plan = %+v, err = %v", plan, err)
	}
}

// req: S8
func TestTheAddressOfTheCardComesFromTheSetupAndNeverFromARequest(t *testing.T) {
	for name, tt := range map[string]struct {
		plan *Plan
		port int
		want string
	}{
		"the address it listens on":    {&Plan{Host: "127.0.0.1"}, 8080, "http://127.0.0.1:8080"},
		"an address with colons":       {&Plan{Host: "::1"}, 9000, "http://[::1]:9000"},
		"a name":                       {&Plan{Host: "localhost", TLS: true}, 8443, "https://localhost:8443"},
		"the first name it answers to": {&Plan{Host: "0.0.0.0", TLS: true, Hosts: []string{"agents.example.com", "other.example.com"}}, 8443, "https://agents.example.com"},
		"a name with a port":           {&Plan{Host: "0.0.0.0", TLS: true, Hosts: []string{"agents.example.com:8443"}}, 8443, "https://agents.example.com:8443"},
		"the public address, if any":   {&Plan{Host: "127.0.0.1", PublicURL: "https://agents.example.com/team"}, 1, "https://agents.example.com/team"},
	} {
		if got := tt.plan.BaseURL(tt.port); got != tt.want {
			t.Errorf("%s: %q, want %q", name, got, tt.want)
		}
	}
}

func TestThePlanKeepsTheAddressItListensOnWithoutBrackets(t *testing.T) {
	o := plain()
	o.Bind = "[::1]"
	plan, err := Resolve(o, defaults)
	if err != nil || plan.Host != "::1" || plan.BaseURL(8080) != "http://[::1]:8080" {
		t.Errorf("plan = %+v, err = %v", plan, err)
	}
}

func TestAnAddressWrittenWithBracketsDoesNotGetThemTwice(t *testing.T) {
	for bind, want := range map[string]string{"[::1]": "[::1]:8080", "::1": "[::1]:8080", "127.0.0.1": "127.0.0.1:8080"} {
		o := plain()
		o.Bind = bind
		plan, err := Resolve(o, defaults)
		if err != nil {
			t.Errorf("bind %q: %s", bind, problem(t, err))
			continue
		}
		if plan.Address != want {
			t.Errorf("bind %q: address %q, want %q", bind, plan.Address, want)
		}
	}
}

// req: S6
func TestOnlyAPublicServerLimitsTheConnectionsOfEachPlace(t *testing.T) {
	public, err := Resolve(Options{Port: -1, Public: true, TLSCert: "c", TLSKey: "k", Hosts: []string{"a.example"}}, Defaults{Port: 8443})
	if err != nil {
		t.Fatal(err)
	}
	local, err := Resolve(Options{Port: -1}, Defaults{Port: 8080})
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := Resolve(Options{Port: -1, BehindProxy: true, Hosts: []string{"a.example"}, PublicURL: "https://a.example"}, Defaults{Port: 8080})
	if err != nil {
		t.Fatal(err)
	}
	if !public.PerAddress || local.PerAddress || proxy.PerAddress {
		t.Errorf("public %v, local %v, behind a proxy %v", public.PerAddress, local.PerAddress, proxy.PerAddress)
	}
}
