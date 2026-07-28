package recipe

import (
	"testing"

	"github.com/pietervanleuven/rehost/internal/detect"
)

func TestRequirementsFor(t *testing.T) {
	cases := []struct {
		framework string
		version   string
		minPHP    string
		needsDB   bool
	}{
		{framework: "wordpress", version: "6.5.2", minPHP: "7.2", needsDB: true},
		{framework: "drupal", version: "7.98", minPHP: "5.6", needsDB: true},
		{framework: "drupal", version: "8.9.20", minPHP: "7.3", needsDB: true},
		{framework: "drupal", version: "9.5.11", minPHP: "7.3", needsDB: true},
		{framework: "drupal", version: "10.2.6", minPHP: "8.1", needsDB: true},
		{framework: "drupal", version: "11.0.1", minPHP: "8.3", needsDB: true},
		{framework: "drupal", version: "", minPHP: "7.3", needsDB: true}, // unknown → D8 floor
		{framework: "static", version: "", minPHP: "", needsDB: false},
		{framework: "somethingelse", version: "", minPHP: "", needsDB: false},
	}
	for _, c := range cases {
		req := RequirementsFor(detect.Install{Framework: c.framework, Version: c.version})
		if req.MinPHP != c.minPHP || req.NeedsDB != c.needsDB {
			t.Errorf("RequirementsFor(%s %s) = {MinPHP:%q NeedsDB:%v}, want {%q %v}",
				c.framework, c.version, req.MinPHP, req.NeedsDB, c.minPHP, c.needsDB)
		}
	}
}

func TestRequirementsExtensionsOnlyForDBFrameworks(t *testing.T) {
	for _, fw := range []string{"wordpress", "drupal"} {
		req := RequirementsFor(detect.Install{Framework: fw})
		if len(req.RequiredExt) == 0 {
			t.Errorf("%s should require at least one PHP extension", fw)
		}
	}
	if req := RequirementsFor(detect.Install{Framework: "static"}); len(req.RequiredExt) != 0 || len(req.RecommendedExt) != 0 {
		t.Error("static sites must not require PHP extensions")
	}
}
