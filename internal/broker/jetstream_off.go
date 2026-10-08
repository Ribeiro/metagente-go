//go:build nojetstream

package broker

// JetStreamAvailable says whether this build has the JetStream broker; a build made with -tags nojetstream
// leaves it out. The releases are not built with this tag.
const JetStreamAvailable = false

// JetStreamConfig is here so that the code that asks for a broker builds in both cases.
type JetStreamConfig struct {
	URL, User, Secret, TLS, CAFile, Stream string
}

// DialJetStream says that this build cannot do it.
func DialJetStream(JetStreamConfig) (Broker, error) {
	return nil, ErrNotInBuild
}
