package serve

import (
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"metagente/internal/clip"
	"metagente/internal/diag"
)

// Options is what a person asked for with the flags of `metagente serve`.
type Options struct {
	Bind string
	// Port is the port to listen on; a negative one means the configured one.
	Port        int
	Public      bool
	BehindProxy bool
	PublicCard  bool
	TLSCert     string
	TLSKey      string
	Hosts       []string
	PublicURL   string
}

// Defaults is what the configuration says, for what the flags left out.
type Defaults struct {
	Bind      string
	Port      int
	Hosts     []string
	PublicURL string
}

// Plan is what the server is going to be, once the flags and the configuration are
// put together and found to make sense.
type Plan struct {
	// Address is where to listen, as host:port.
	Address string
	// Host is the address the server listens on, without the port.
	Host string
	// Loopback is true when the server listens only on this computer and nothing
	// stands in front of it: the one case where a person is surely at the other end
	// of the terminal, and a token may be made and shown.
	Loopback bool
	TLS      bool
	CertFile string
	KeyFile  string
	// Hosts are the values of the Host header the server answers to. Empty means
	// the ones of this computer, once the port is known.
	Hosts []string
	// PublicURL is how the outside reaches the server, when a proxy stands in front.
	PublicURL string
	// NoThrottle is for a server behind a proxy; see Guard.NoThrottle.
	NoThrottle bool
	PublicCard bool
	// PerAddress is true when the server faces the network by itself, the one case
	// where a limit of connections for each address tells clients apart.
	PerAddress bool
}

// Resolve puts the flags and the configuration together. The rules are the ones
// that keep a token from ever travelling where anyone can read it:
//
//   - A server only listens beyond this computer with --public, and then it needs a
//     certificate of its own and the names it answers to.
//   - Behind a proxy it listens only on this computer, and the proxy has to reach it
//     there: --behind-proxy is a declaration, and a declaration that could be made
//     about a server open to the network would be one that could be false.
func Resolve(o Options, d Defaults) (*Plan, error) {
	if err := checkMode(o); err != nil {
		return nil, err
	}
	bind := strings.Trim(chooseBind(o, d), "[]") // JoinHostPort puts the brackets back where they belong
	port, err := choosePort(o, d)
	if err != nil {
		return nil, err
	}
	onThisComputer := isLoopbackBind(bind)
	if err := checkReach(o, onThisComputer); err != nil {
		return nil, err
	}
	hosts, err := normalizeHosts(pickHosts(o, d))
	if err != nil {
		return nil, err
	}
	if (o.Public || o.BehindProxy) && len(hosts) == 0 {
		return nil, diag.New("a server that others reach has to know the names it answers to").
			Fix("add --host NAME for each one, for example: --host agents.example.com")
	}
	publicURL, err := checkPublicURL(firstText(o.PublicURL, d.PublicURL), hosts)
	if err != nil {
		return nil, err
	}
	if o.BehindProxy && publicURL == "" {
		return nil, diag.New("behind a proxy the server cannot know how it is reached from outside").
			Fix("add --public-url https://agents.example.com")
	}
	return &Plan{
		Address:    net.JoinHostPort(bind, strconv.Itoa(port)),
		Host:       bind,
		Loopback:   onThisComputer && !o.Public && !o.BehindProxy,
		TLS:        o.TLSCert != "",
		CertFile:   o.TLSCert,
		KeyFile:    o.TLSKey,
		Hosts:      hosts,
		PublicURL:  publicURL,
		NoThrottle: o.BehindProxy,
		PublicCard: o.PublicCard,
		PerAddress: o.Public,
	}, nil
}

func checkMode(o Options) error {
	if o.Public && o.BehindProxy {
		return diag.New("--public and --behind-proxy cannot be used together").
			Fix("--public is a server that faces the network by itself; --behind-proxy is one that a proxy on this computer faces for it")
	}
	if (o.TLSCert == "") != (o.TLSKey == "") {
		return diag.New("--tls-cert and --tls-key go together").
			Fix("give both, or neither")
	}
	return nil
}

func chooseBind(o Options, d Defaults) string {
	switch {
	case o.Bind != "":
		return o.Bind
	case o.Public:
		return "0.0.0.0"
	case d.Bind != "":
		return d.Bind
	}
	return "127.0.0.1"
}

func choosePort(o Options, d Defaults) (int, error) {
	port := o.Port
	if port < 0 {
		port = d.Port
	}
	if port < 0 || port > 65535 {
		return 0, diag.Newf("%d is not a port", port).Fix("use a number from 1 to 65535")
	}
	return port, nil
}

// checkReach refuses to listen where the mode does not allow.
func checkReach(o Options, onThisComputer bool) error {
	switch {
	case o.Public && o.TLSCert == "":
		return diag.New("a server open to the network needs TLS of its own").
			Fix("add --tls-cert FILE and --tls-key FILE, or run it on this computer behind a proxy with --behind-proxy")
	case o.BehindProxy && !onThisComputer:
		return diag.New("--behind-proxy only counts when the server listens on this same computer").
			Fix("remove --bind, or use an address of this computer such as 127.0.0.1")
	case !o.Public && !o.BehindProxy && !onThisComputer:
		return diag.New("a server only listens beyond this computer when it is told to be public").
			Fix("add --public (it needs --tls-cert, --tls-key and --host), or listen on 127.0.0.1")
	}
	return nil
}

// isLoopbackBind is true for an address that only this computer can reach. An empty
// address, 0.0.0.0 and :: mean every address, which is not it.
func isLoopbackBind(bind string) bool {
	if strings.EqualFold(bind, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(bind, "[]"))
	return ip != nil && ip.IsLoopback()
}

func pickHosts(o Options, d Defaults) []string {
	if len(o.Hosts) > 0 {
		return o.Hosts
	}
	return d.Hosts
}

func firstText(texts ...string) string {
	for _, t := range texts {
		if t != "" {
			return t
		}
	}
	return ""
}

var hostPattern = regexp.MustCompile(`^(\[[0-9a-f:]+\]|[a-z0-9]([a-z0-9.-]*[a-z0-9])?)(:[0-9]{1,5})?$`)

// normalizeHosts checks the names the server answers to: a name, with a port if the
// clients write one, and nothing else (no scheme, no path, no user, no spaces).
func normalizeHosts(hosts []string) ([]string, error) {
	var clean []string
	seen := map[string]bool{}
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if !hostPattern.MatchString(h) {
			return nil, diag.Newf("`%s` is not a host name", clip.Collapse(h, 40)).
				Fix("write only the name and, if clients use one, the port: agents.example.com or agents.example.com:8443")
		}
		if !seen[h] {
			seen[h] = true
			clean = append(clean, h)
		}
	}
	return clean, nil
}

// checkPublicURL checks how the outside reaches the server: https, a host the server
// answers to, and nothing that could carry a secret.
func checkPublicURL(raw string, hosts []string) (string, error) {
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", diag.New("the public address has to be like https://agents.example.com").
			Fix("it is https, a host, and maybe a path; no user, no query")
	}
	host := strings.ToLower(u.Host)
	if len(hosts) > 0 && !containsText(hosts, host) {
		return "", diag.Newf("the host of the public address (%s) is not one the server answers to", host).
			Fix("add --host " + host)
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func containsText(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// BaseURL is how a client reaches this server, as an Agent Card must write it. It comes
// from the setup, never from a request: the public address when there is one, else the
// first of the names the server answers to, else the address it listens on.
func (p *Plan) BaseURL(port int) string {
	if p.PublicURL != "" {
		return p.PublicURL
	}
	scheme := "http"
	if p.TLS {
		scheme = "https"
	}
	if len(p.Hosts) > 0 {
		return scheme + "://" + p.Hosts[0]
	}
	return scheme + "://" + net.JoinHostPort(p.Host, strconv.Itoa(port))
}

// HostsFor are the names the server answers to once it listens on the port.
func (p *Plan) HostsFor(port int) []string {
	if len(p.Hosts) > 0 {
		return p.Hosts
	}
	return LoopbackHosts(port)
}
