package recipe

import (
	"context"
	"fmt"
	"sort"

	hostdb "github.com/pietervanleuven/go-hostdb"
)

// RewriteConfig points the synced config at the destination database by
// replacing exactly the literals the parser read — nothing else in the file
// is touched, which is the only safe contract available for code rehost has
// no model of. A config whose credentials are positional arguments to
// mysqli_connect or new PDO is left alone: those literals carry no key
// saying which is which, so a splice would be a guess.
func (g Generic) RewriteConfig(ctx context.Context, h Host, rw ConfigRewrite) (ConfigRewriteResult, error) {
	confPath, ok := destConfigPath(rw)
	if !ok {
		return ConfigRewriteResult{Supported: false,
			Note: fmt.Sprintf("%s sits outside the site root and was not synced — copy it to the destination and point it at database %s by hand", rw.SourceConfig, rw.DB.Name)}, nil
	}
	content, err := readRemoteFile(ctx, h.Run, confPath)
	if err != nil {
		return ConfigRewriteResult{}, err
	}
	rewritten, missing, err := rewriteGenericConfig(content, rw.DB)
	if err != nil {
		return ConfigRewriteResult{Supported: false,
			Note: fmt.Sprintf("%s: %v — point it at database %s by hand", confPath, err, rw.DB.Name)}, nil
	}
	if err := writeRemoteFileRandom(ctx, h.Run, confPath, rewritten); err != nil {
		return ConfigRewriteResult{}, err
	}
	res := ConfigRewriteResult{Path: confPath, Supported: true}
	if len(missing) > 0 {
		res.PostSteps = append(res.PostSteps, fmt.Sprintf(
			"set the %s in %s by hand — the config assigns no recognizable variable for it, so the site still uses the source's value",
			joinAnd(missing), confPath))
	}
	res.PostSteps = append(res.PostSteps,
		"check the site for a second copy of these credentials — hand-rolled PHP often repeats them in an installer or cron script")
	return res, nil
}

// rewriteGenericConfig replaces the credential literals in place, working
// back to front so each splice leaves the earlier offsets valid. Values are
// re-emitted as single-quoted literals, which sidesteps the interpolation a
// double-quoted password containing '$' would otherwise trigger.
func rewriteGenericConfig(content []byte, creds hostdb.Credentials) ([]byte, []string, error) {
	parsed := parseGenericConfig(content)
	if parsed == nil {
		return nil, nil, fmt.Errorf("no database credentials could be found to replace")
	}
	if len(parsed.slots) == 0 {
		return nil, nil, fmt.Errorf("the credentials are positional arguments to a connect call, which cannot be rewritten safely")
	}

	replacements := map[string]string{"name": creds.Name}
	var missing []string
	have := map[string]bool{}
	for _, s := range parsed.slots {
		have[s.field] = true
	}
	if creds.User != "" {
		if have["user"] {
			replacements["user"] = creds.User
		} else {
			missing = append(missing, "database user")
		}
	}
	if creds.Password != "" {
		if have["password"] {
			replacements["password"] = creds.Password
		} else {
			missing = append(missing, "database password")
		}
	}
	if have["host"] {
		host := creds.Host
		if host == "" {
			host = "localhost"
		}
		replacements["host"] = host
	}
	if creds.Port != 0 && have["port"] {
		replacements["port"] = fmt.Sprint(creds.Port)
	}

	slots := append([]genericSlot(nil), parsed.slots...)
	sort.Slice(slots, func(i, j int) bool { return slots[i].start > slots[j].start })
	out := append([]byte(nil), content...)
	for _, s := range slots {
		value, ok := replacements[s.field]
		if !ok {
			continue
		}
		spliced := make([]byte, 0, len(out))
		spliced = append(spliced, out[:s.start]...)
		spliced = append(spliced, phpSingleQuote(value)...)
		spliced = append(spliced, out[s.end:]...)
		out = spliced
	}
	return out, missing, nil
}

// Generic PHP implements no Maintainer: there is no framework to ask, and no
// file a hand-rolled site would honor. migrate reports that and dumps the
// live site, which is the honest outcome.
