package recipe

import (
	"context"

	"github.com/pietervanleuven/go-ssh/remote"
	"github.com/pietervanleuven/rehost/internal/db"
	"github.com/pietervanleuven/rehost/internal/detect"
)

// Host bundles what a recipe capability may use on a host. Caps may be nil,
// in which case layers try their tool and let the failure speak.
type Host struct {
	Run  remote.Runner
	FS   detect.FS
	Caps *remote.Capabilities
}

// HasTool reports whether a tool is known to exist on the host; with no
// capability info it optimistically returns true so the layer still tries.
func (h Host) HasTool(name string) bool {
	if h.Caps == nil {
		return true
	}
	return h.Caps.Has(name)
}

// Extractor is the credential-extraction capability a recipe may implement
// alongside detection. (nil, nil) means "not found" — an honest absence;
// an error is a transport failure, never absence.
type Extractor interface {
	ExtractCredentials(ctx context.Context, h Host, in detect.Install) (*db.Credentials, error)
}
