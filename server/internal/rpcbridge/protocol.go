package rpcbridge

// Handshake constants modelled on HashiCorp go-plugin. A plugin subprocess
// verifies the magic cookie env var at startup and refuses to run without it,
// which avoids accidental execution of plugin binaries outside the host.
//
// Handshake line format (plugin → host, first stdout line):
//
//	CORE_PROTO|APP_PROTO|NETWORK|ADDRESS|PROTOCOL
//	    1   |    1    |  tcp  | 127.0.0.1:PORT | netrpc
//
// Everything else written to stdout is treated as a log line.
const (
	CoreProtocolVersion = 1
	AppProtocolVersion  = 1

	MagicCookieKey   = "MODDLE_PLUGIN"
	MagicCookieValue = "moddle.v1"

	// RPC service name registered via net/rpc. Mirrors the Mattermost
	// convention of a single service per plugin.
	ServiceName = "Moddle"
)

// Raw is the wire payload for every hook. We push JSON bytes through a thin
// gob envelope so hook schemas can evolve without breaking the gob ABI.
type Raw struct {
	Data []byte
}

// PostDecision mirrors Mattermost's MessageWillBePosted contract.
type PostDecision struct {
	Post     []byte // possibly-modified post JSON, empty means unchanged
	Rejected bool
	Reason   string
}

// HTTPRequest / HTTPResponse are used for the ServeHTTP hook.
type HTTPRequest struct {
	Method  string
	Path    string
	Headers map[string]string
	Body    []byte
}

type HTTPResponse struct {
	Status  int
	Headers map[string]string
	Body    []byte
}
