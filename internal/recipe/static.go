package recipe

import (
	"context"
	"path"

	"github.com/placeholder/rehost/internal/detect"
)

// Static is the generic fallback: a directory served as a plain static site.
//
// It is deliberately conservative — it matches only when a classic entry
// document (index.html/index.htm) is present, so a bare account home is not
// mistaken for a site. PHP-without-framework is a later "generic PHP" recipe,
// not static. Static is registered last so real frameworks win first.
type Static struct{}

func (Static) Name() string { return "static" }

var staticIndexFiles = []string{"index.html", "index.htm"}

func (s Static) Detect(ctx context.Context, fs detect.FS, dir string) (*detect.Install, error) {
	for _, name := range staticIndexFiles {
		ok, err := fs.Exists(ctx, path.Join(dir, name))
		if err != nil {
			return nil, err
		}
		if ok {
			return &detect.Install{Framework: s.Name(), Root: dir}, nil
		}
	}
	return nil, nil
}
