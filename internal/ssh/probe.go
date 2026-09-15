package ssh

import (
	"context"

	"github.com/pietervanleuven/rehost/internal/ssh/remote"
)

// Tool is one probed remote binary. It lives in the remote subpackage; this
// alias keeps the root API complete for callers that already dial here.
type Tool = remote.Tool

// Capabilities is what a remote host offers. See remote.Capabilities.
type Capabilities = remote.Capabilities

// ProbedTools returns the canonical display order of probed tools.
func ProbedTools() []string { return remote.ProbedTools() }

// Probe detects the connected host's capabilities — see remote.Probe, which
// this wraps with the client's own host and user identity.
func Probe(ctx context.Context, client *Client) (*Capabilities, error) {
	caps, err := remote.Probe(ctx, client, client.Config.Host)
	if err != nil {
		return nil, err
	}
	caps.User = client.Config.User
	return caps, nil
}
