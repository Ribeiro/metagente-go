package serve

// places is a number of places that can be taken and given back, never more than it
// has. The A2A server and the MCP server behind the same door share theirs, so a
// limit of the configuration is the limit of the whole server, whichever protocol
// a client speaks.
type places chan struct{}

func newPlaces(n int) places { return make(places, n) }

// take takes a place, and says false when none is left. It never waits.
func (p places) take() bool {
	select {
	case p <- struct{}{}:
		return true
	default:
		return false
	}
}

// give gives back a place that was taken.
func (p places) give() { <-p }
