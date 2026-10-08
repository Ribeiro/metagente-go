package config

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/Ribeiro/metagente-go/internal/diag"
	"github.com/Ribeiro/metagente-go/internal/lang"
)

// BrokerDrivers are the brokers that a [broker.NAME] section may name. "memory" lives in the memory of the
// process: it serves the tests, and the trying of agents that publish and consume in one process.
var BrokerDrivers = []string{"jetstream", "memory"}

// BrokerConn is a [broker.NAME] section: a message broker that `tool x from broker "NAME"` publishes to,
// and that `metagente consume` reads from.
type BrokerConn struct {
	Name   string
	Driver string
	// URL is the address of the server, like nats://host:4222 or tls://host:4222. It has no password: the
	// secret comes from [credentials].
	URL string
	// User is the user of the connection; with no user, the secret of [credentials] is a token.
	User string
	// TLS is one of the TLS modes (TLSVerify, TLSRequire, TLSDisable) and CAFile the certificates to trust
	// with TLSVerify, relative to the folder of the project.
	TLS    string
	CAFile string
	// Stream is the stream that has to take what is published. It is optional.
	Stream string
}

// addBroker reads one [broker.NAME] section. The line is the one of its header.
func (cfg *Config) addBroker(file, text string, e entry) error {
	fail := func(format string, args ...any) *diag.Diagnostic {
		return diag.New(fmt.Sprintf(format, args...)).At(file, e.line, 1).WithSource(text)
	}
	if !lang.ValidConnectionName(e.key) {
		return fail("`%s` is not a name for a connection: use letters, digits, `_` and `-`", e.key).
			Fix("write the section like: [broker.main]")
	}
	table, ok := e.value.(map[string]any)
	if !ok {
		return fail("`%s` in [broker] must be a section of its own", e.key).
			Fix("write one section for each broker, for example [broker.main], and put driver and url under it")
	}
	conn := &BrokerConn{Name: e.key}
	where := "[broker." + e.key + "]"
	keys := make([]string, 0, len(table))
	for key := range table {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		var err error
		switch key {
		case "driver":
			conn.Driver, err = sqlText(table[key])
		case "url":
			conn.URL, err = sqlText(table[key])
		case "user":
			conn.User, err = sqlText(table[key])
		case "tls":
			conn.TLS, err = sqlText(table[key])
		case "ca_file":
			conn.CAFile, err = sqlText(table[key])
		case "stream":
			conn.Stream, err = sqlText(table[key])
		default:
			return fail("I do not know the setting `%s` in %s", key, where).
				Fix("a broker has: driver, url, user, tls, ca_file and stream")
		}
		if err != nil {
			return fail("`%s` in %s %s", key, where, err.Error()).Fix("change it in " + FileName)
		}
	}
	if err := conn.check(fail, where); err != nil {
		return err
	}
	if _, twice := cfg.Broker[e.key]; twice {
		return fail("%s is written twice", where).Fix("keep one")
	}
	cfg.Broker[e.key] = conn
	return nil
}

// check looks at the settings of a broker and fills in the TLS mode that was left out.
func (c *BrokerConn) check(fail func(string, ...any) *diag.Diagnostic, where string) error {
	known := false
	for _, d := range BrokerDrivers {
		known = known || d == c.Driver
	}
	switch {
	case c.Driver == "":
		return fail("%s needs a driver", where).Fixf("write, for example: driver = %q", BrokerDrivers[0])
	case !known:
		return fail("%s names the driver `%s`, which this version does not have", where, c.Driver).
			Fixf("the drivers are: %s", strings.Join(BrokerDrivers, ", "))
	case c.Driver == "memory":
		if c.URL != "" || c.User != "" || c.TLS != "" || c.CAFile != "" {
			return fail("%s is a broker in memory, and it has no url, user, tls or ca_file", where).
				Fix("write only the driver")
		}
		return nil
	case c.URL == "":
		return fail("%s needs a url", where).Fix(`write, for example: url = "nats://broker.example.com:4222"`)
	}
	u, err := url.Parse(c.URL)
	switch {
	case err != nil || u.Host == "" || (u.Scheme != "nats" && u.Scheme != "tls"):
		return fail("url in %s is not an address of a NATS server", where).
			Fix(`write it like: nats://broker.example.com:4222 (or tls://...)`)
	case u.User != nil:
		return fail("url in %s has a user or a password in it", where).
			Fix("write the user in `user`, and put the password in [credentials] (a variable that holds it)")
	case u.Path != "" && u.Path != "/" || u.RawQuery != "":
		return fail("url in %s has more than the address of the server", where).Fix("write only scheme, host and port")
	case strings.ContainsAny(c.Stream, " .*>\t") || (c.Stream != "" && !lang.ValidConnectionName(c.Stream)):
		return fail("stream in %s is not the name of a stream: use letters, digits, `_` and `-`", where).
			Fix("write the name the stream has on the server")
	}
	switch c.TLS {
	case "":
		c.TLS = TLSVerify
	case TLSVerify, TLSRequire, TLSDisable:
	default:
		return fail("tls in %s is `%s`, and it has to be %s, %s or %s", where, c.TLS, TLSVerify, TLSRequire, TLSDisable).
			Fix("remove it to have the default, " + TLSVerify)
	}
	if c.CAFile != "" && c.TLS != TLSVerify {
		return fail("ca_file in %s only means something with tls = \"%s\"", where, TLSVerify).
			Fix("remove it, or remove tls")
	}
	return nil
}
