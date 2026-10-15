package loadbalancer

import "net/http"

// Strategy picks the instance for one request. The request is passed so a
// strategy can key off it (ConsistentHash reads the document id);
// RoundRobin ignores it.
//
// A non-nil error means the request cannot be served right now, and the
// caller answers 503 rather than trying another instance - see
// ConsistentHash for why silently rerouting would be worse than failing.
type Strategy interface {
	Name() string
	Pick(r *http.Request) (*Backend, error)
}
