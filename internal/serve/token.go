package serve

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
)

// The token that opens the server (requirement S1). Whoever has it can do
// everything the served agents can do, so it is long, random, and never repeated
// in a message.

const (
	tokenEnv       = "METAGENTE_TOKEN"
	minTokenLength = 32
)

// GenerateToken makes a token of 32 random bytes, written so it fits in a header.
func GenerateToken() string {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		// Without randomness nothing safe can be made; stopping is better than
		// serving with a token that can be guessed.
		panic("the system could not give random bytes")
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// CheckToken says whether a text can be used as the token. The reason never
// repeats the text: it may be the real thing.
func CheckToken(token string) error {
	if len(token) < minTokenLength {
		return errors.New("the token has to be at least 32 characters long")
	}
	for _, c := range token {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-._~+/=", c)
		if !ok {
			return errors.New("the token may only use letters, digits and the characters - . _ ~ + / =")
		}
	}
	return nil
}

// ResolveToken works out the token of a server. It comes from the environment;
// if it is not there, one is made only for a person at a terminal on this same
// computer, because showing a secret anywhere else (a CI log, a container)
// leaves it in a place that outlives the server. Otherwise the server does not
// start.
func ResolveToken(getenv func(string) string, terminal, loopback bool) (token string, generated bool, err error) {
	if value := strings.TrimSpace(getenv(tokenEnv)); value != "" {
		if err := CheckToken(value); err != nil {
			return "", false, errors.New(tokenEnv + ": " + err.Error() + ". `metagente token` makes a good one")
		}
		return value, false, nil
	}
	if terminal && loopback {
		return GenerateToken(), true, nil
	}
	return "", false, errors.New(tokenEnv + " is not set. A server that is not on this computer, or has no terminal to show a token in, " +
		"needs the token to be given: run `metagente token`, keep the result, and set " + tokenEnv + " to it")
}
