package config

import (
	"errors"
	"net/url"
	"strings"

	"github.com/Ribeiro/metagente-go/internal/diag"
	"github.com/Ribeiro/metagente-go/internal/secret"
)

// setProxy reads `http_proxy` of [network]. A user and password in the address
// are refused, since they would be a secret in a file: they go in the variable
// that `http_proxy_auth_env` names.
func setProxy(c *Config, v any) error {
	text, ok := v.(string)
	if !ok {
		return errors.New("a text in quotes")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		c.Network.HTTPProxy = ""
		return nil
	}
	parsed, err := url.Parse(text)
	switch {
	case err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "":
		return errors.New("the address of a web proxy, such as \"http://proxy.example.com:3128\"")
	case parsed.User != nil:
		return errors.New("the address of the proxy without a user or password; put them in the variable that http_proxy_auth_env names")
	case (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "":
		return errors.New("only the address of the proxy, such as \"http://proxy.example.com:3128\", with nothing after the port")
	}
	c.Network.HTTPProxy = parsed.Scheme + "://" + parsed.Host
	return nil
}

func setEnvName(field func(*Config) *string) setter {
	return func(c *Config, v any) error {
		name, ok := v.(string)
		if !ok || !secret.ValidEnvName(name) {
			return errors.New("the NAME of an environment variable, such as PROXY_AUTH, and not its value")
		}
		*field(c) = name
		return nil
	}
}

// ProxyURL is the proxy `tool http` goes through, with the user and password
// read from the variable the configuration names, or nil when there is none.
func (n Network) ProxyURL(getenv func(string) string) (*url.URL, error) {
	if n.HTTPProxy == "" {
		if n.HTTPProxyAuthEnv != "" {
			return nil, diag.New("[network] of " + FileName + " names http_proxy_auth_env, but no http_proxy").
				Fix("add the address of the proxy, for example: http_proxy = \"http://proxy.example.com:3128\"")
		}
		return nil, nil
	}
	proxy, err := url.Parse(n.HTTPProxy)
	if err != nil {
		return nil, diag.Newf("http_proxy in [network] of %s is not an address", FileName).
			Fix("write it as http://proxy.example.com:3128")
	}
	if n.HTTPProxyAuthEnv == "" {
		return proxy, nil
	}
	auth := strings.TrimSpace(getenv(n.HTTPProxyAuthEnv))
	user, password, ok := strings.Cut(auth, ":")
	if !ok || user == "" {
		why := "is not set"
		if auth != "" {
			why = "is not in the form user:password"
		}
		return nil, diag.Newf("the variable %s, which holds the user and password of the proxy, %s", n.HTTPProxyAuthEnv, why).
			Fixf("set it before you start Metagente: export %s='user:password'", n.HTTPProxyAuthEnv)
	}
	proxy.User = url.UserPassword(user, password)
	return proxy, nil
}
