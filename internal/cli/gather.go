package cli

import (
	"context"
	"fmt"

	"golang.org/x/sync/errgroup"

	"github.com/pietervanleuven/rehost/internal/check"
	"github.com/pietervanleuven/rehost/internal/db"
	"github.com/pietervanleuven/rehost/internal/detect"
	"github.com/pietervanleuven/rehost/internal/dns"
	"github.com/pietervanleuven/rehost/internal/project"
	"github.com/pietervanleuven/rehost/internal/recipe"
	"github.com/pietervanleuven/rehost/internal/ssh"
)

// sourceGather is everything the pre-flight learns from the source host. The
// client is returned open so callers that need it afterwards (migrate stats
// and history) can use it; the owner closes it via hosts.close.
type sourceGather struct {
	client   *ssh.Client
	caps     *ssh.Capabilities
	installs []detect.Install
	creds    map[string]*db.Credentials // install root → credentials (nil value = extraction failed)
	dbs      map[string]*db.Inspection  // install root → inspection; nil when no mysql client
	ip       string
	sitesKB  int64
}

// destGather is everything the pre-flight learns from the destination host.
type destGather struct {
	client        *ssh.Client
	caps          *ssh.Capabilities
	phpExtensions []string
	freeKB        int64
}

// hosts bundles the live connections and the check.Input the compatibility
// gate consumes. Both clients stay open until close releases them.
type hosts struct {
	source *sourceGather
	dest   *destGather
	input  check.Input
}

// close releases both connections; safe to call with either gather missing.
func (h *hosts) close() {
	if h.source != nil && h.source.client != nil {
		h.source.client.Close()
	}
	if h.dest != nil && h.dest.client != nil {
		h.dest.client.Close()
	}
}

// gatherHosts connects to source and destination, gathers what the check gate
// needs from each, and assembles check.Input. Both clients stay open (close
// releases them). The two gathers run sequentially when interactive — so
// password and host-key prompts from two hosts never interleave — and
// concurrently otherwise. The DNS snapshot is source-side and needs no SSH, so
// it is taken first and a resolver hiccup never fails the gather (the rule
// reports the absence). The caller must have verified f.Destination is set.
func gatherHosts(ctx context.Context, u ui, f *project.File, docroots []string) (*hosts, error) {
	h := &hosts{input: check.Input{Domain: f.Domain}}

	if f.Domain != "" {
		u.progress("dns: looking up %s…", f.Domain)
		snap, err := dns.NewClient().Snapshot(ctx, f.Domain)
		if err != nil {
			u.progress("dns: %v", err)
		} else {
			h.input.DNS = snap
		}
	}

	srcCfg := f.Source.SSHConfig()
	dstCfg := f.Destination.SSHConfig()

	if u.interactive {
		var err error
		if h.source, err = gatherSource(ctx, srcCfg, u, docroots); err != nil {
			return nil, err
		}
		if h.dest, err = gatherDest(ctx, dstCfg, u); err != nil {
			h.close()
			return nil, err
		}
	} else {
		g, gctx := errgroup.WithContext(ctx)
		g.Go(func() error {
			s, err := gatherSource(gctx, srcCfg, u, docroots)
			h.source = s // distinct field from the dest goroutine — no shared write
			return err
		})
		g.Go(func() error {
			d, err := gatherDest(gctx, dstCfg, u)
			h.dest = d
			return err
		})
		if err := g.Wait(); err != nil {
			h.close()
			return nil, err
		}
	}

	h.assembleInput()
	return h, nil
}

// assembleInput folds both gathers into the check.Input the rules read. It
// runs after both gathers complete, so it observes their fully-written state.
func (h *hosts) assembleInput() {
	if h.source != nil {
		h.input.Source = h.source.caps
		h.input.Installs = h.source.installs
		h.input.SourceIPs = []string{h.source.ip}
		h.input.SourceSitesKB = h.source.sitesKB
		if len(h.source.creds) > 0 {
			h.input.SourceCreds = h.source.creds
		}
		if len(h.source.dbs) > 0 {
			h.input.SourceDBs = h.source.dbs
		}
	}
	if h.dest != nil {
		h.input.Destination = h.dest.caps
		h.input.DestPHPExtensions = h.dest.phpExtensions
		h.input.DestFreeKB = h.dest.freeKB
	}
}

// gatherSource connects to the source, probes it, discovers the sites,
// measures their size, extracts each site's database credentials (memory
// only, never stored or printed) and inspects each reachable database. The
// client is returned open on success; any failure closes it before returning.
func gatherSource(ctx context.Context, cfg ssh.Config, u ui, docroots []string) (*sourceGather, error) {
	u.progress("source: connecting to %s…", targetLabel(cfg))
	client, err := ssh.Dial(ctx, cfg, u.prompter)
	if err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			client.Close()
		}
	}()

	caps, err := ssh.Probe(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	u.progress("source: connected to %s (%s) — scanning for websites…", caps.Target(), caps.Summary())

	startRoots := docroots
	if len(startRoots) == 0 {
		startRoots = []string{homeOrDot(caps.Home)}
	}
	fsys := detect.NewSSHFS(client)
	installs, err := detect.Discover(ctx, fsys, startRoots, recipe.All(),
		detect.FindOptions{Prune: detect.DefaultPrune})
	if err != nil {
		return nil, fmt.Errorf("source: detecting frameworks: %w", err)
	}
	u.progress("source: found %s — measuring size…", pluralizeSites(len(installs)))

	roots := make([]string, 0, len(installs))
	for _, inst := range installs {
		roots = append(roots, inst.Root)
	}
	g := &sourceGather{
		client:   client,
		caps:     caps,
		installs: installs,
		ip:       client.RemoteIP(),
		sitesKB:  check.DirsSizeKB(ctx, client, roots),
	}

	// Credentials stay in memory for this run only — never stored, never
	// printed; the check gate only reports whether they were readable.
	host := db.Host{Run: client, FS: fsys, Caps: caps}
	creds := map[string]*db.Credentials{}
	for _, inst := range installs {
		ex := recipe.ExtractorFor(inst.Framework)
		if ex == nil {
			continue
		}
		if len(creds) == 0 {
			u.progress("source: reading database credentials…")
		}
		c, err := ex.ExtractCredentials(ctx, host, inst)
		if err != nil {
			return nil, fmt.Errorf("source: extracting credentials for %s: %w", inst.Root, err)
		}
		creds[inst.Root] = c
	}
	if len(creds) > 0 {
		g.creds = creds
	}

	// Inspect each database with its credentials; sites sharing one database
	// (same name/host/user) are inspected once.
	if len(creds) > 0 && caps.Has("mysql") {
		u.progress("source: inspecting databases…")
		dbs := map[string]*db.Inspection{}
		byIdentity := map[string]*db.Inspection{}
		for root, c := range creds {
			if c == nil || c.Name == "" {
				continue
			}
			key := c.Name + "\x00" + c.Host + "\x00" + c.User
			insp, seen := byIdentity[key]
			if !seen {
				insp, err = db.Inspect(ctx, client, c)
				if err != nil {
					return nil, fmt.Errorf("source: inspecting database %s: %w", c.Name, err)
				}
				byIdentity[key] = insp
			}
			dbs[root] = insp
		}
		g.dbs = dbs
	}

	ok = true
	return g, nil
}

// gatherDest connects to the destination, probes it, and measures the two
// facts the gate compares against the source: the loaded PHP extensions and
// the free space at the account home. The client is returned open on success;
// any failure closes it before returning.
func gatherDest(ctx context.Context, cfg ssh.Config, u ui) (*destGather, error) {
	u.progress("destination: connecting to %s…", targetLabel(cfg))
	client, err := ssh.Dial(ctx, cfg, u.prompter)
	if err != nil {
		return nil, fmt.Errorf("destination: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			client.Close()
		}
	}()

	caps, err := ssh.Probe(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("destination: %w", err)
	}
	u.progress("destination: connected to %s (%s) — checking PHP and disk space…", caps.Target(), caps.Summary())

	g := &destGather{client: client, caps: caps}
	if caps.PHPVersion != "" {
		g.phpExtensions = check.PHPExtensions(ctx, client)
	}
	g.freeKB = check.FreeKB(ctx, client, homeOrDot(caps.Home))
	ok = true
	return g, nil
}
