package infra

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrTargetNotFound is returned by ResolveTarget when nothing in the config
// matches the target.
var ErrTargetNotFound = errors.New("target not found in infrastructure config")

// AmbiguousTargetError is returned by ResolveTarget when a target matches more
// than one registered server. Resolution refuses to guess: a wrong match would
// act on, or record against, the wrong database.
type AmbiguousTargetError struct {
	Target  string
	By      string   // what matched more than once: "host+port" or "name"
	Matches []string // server keys, sorted
}

func (e *AmbiguousTargetError) Error() string {
	return fmt.Sprintf("target %q matches multiple registered servers by %s (%s) — refusing to guess which one; use the exact server ID instead",
		e.Target, e.By, strings.Join(e.Matches, ", "))
}

// ResolveTarget finds the db_servers entry a target names and returns its key.
// It is the one place that decides which database a target means: the
// sysadmin tools, the recovery objectives and the incident ledger all use it,
// so they cannot disagree. A target may be, in order:
//
//  1. a server key;
//  2. a connection string (or "host:port"), matched on host and port together,
//     never on the port alone unless the target is a bare port number;
//  3. a container name;
//  4. a server's display name;
//  5. a server's full connection string, for one that carries no port.
//
// A target that matches several servers at one step is an error
// (*AmbiguousTargetError), never a pick. No match is ErrTargetNotFound.
// Credentials in a connection string play no part in the match.
func (c *Config) ResolveTarget(target string) (string, error) {
	target = strings.TrimSpace(target)
	if c == nil || target == "" {
		return "", ErrTargetNotFound
	}
	if _, ok := c.DBServers[target]; ok {
		return target, nil
	}

	// Host+port. A bare port number carries no host, so it matches on port
	// alone, still refusing when that is ambiguous.
	if port := ConnStrPort(target); port != "" {
		host := ConnStrHost(target)
		var matches []string
		for key, db := range c.DBServers {
			if ConnStrPort(db.ConnectionString) != port {
				continue
			}
			if host != "" && ConnStrHost(db.ConnectionString) != host {
				continue
			}
			matches = append(matches, key)
		}
		switch len(matches) {
		case 0:
		case 1:
			return matches[0], nil
		default:
			sort.Strings(matches)
			return "", &AmbiguousTargetError{Target: target, By: "host+port", Matches: matches}
		}
	}

	if key, err := c.uniqueMatch(target, "name", func(db DBServer) bool { return db.ContainerName == target }); key != "" || err != nil {
		return key, err
	}
	if key, err := c.uniqueMatch(target, "name", func(db DBServer) bool { return db.Name == target }); key != "" || err != nil {
		return key, err
	}
	if key, err := c.uniqueMatch(target, "connection string", func(db DBServer) bool { return db.ConnectionString == target }); key != "" || err != nil {
		return key, err
	}
	return "", ErrTargetNotFound
}

// uniqueMatch returns the key of the one server for which match is true, "" and
// nil for none, and an ambiguity error for several.
func (c *Config) uniqueMatch(target, by string, match func(DBServer) bool) (string, error) {
	var matches []string
	for key, db := range c.DBServers {
		if match(db) {
			matches = append(matches, key)
		}
	}
	switch len(matches) {
	case 0:
		return "", nil
	case 1:
		return matches[0], nil
	}
	sort.Strings(matches)
	return "", &AmbiguousTargetError{Target: target, By: by, Matches: matches}
}

// ConnStrHost extracts the host from a libpq key=value connection string, a
// postgres:// URI or a "host:port" address. Returns "" when there is none, and
// for a bare port number, so a caller can tell "no host known" from a host
// that parses oddly.
func ConnStrHost(s string) string {
	if strings.TrimFunc(s, func(r rune) bool { return r >= '0' && r <= '9' }) == "" && s != "" {
		return ""
	}
	for _, field := range strings.Fields(s) {
		if strings.HasPrefix(field, "host=") {
			if h := strings.TrimPrefix(field, "host="); h != "" {
				return h
			}
		}
	}
	s = strings.TrimPrefix(s, "postgresql://")
	s = strings.TrimPrefix(s, "postgres://")
	if idx := strings.LastIndex(s, "@"); idx >= 0 {
		s = s[idx+1:]
	}
	if idx := strings.LastIndex(s, ":"); idx >= 0 {
		return s[:idx]
	}
	if sl := strings.IndexAny(s, "/?"); sl >= 0 {
		s = s[:sl]
	}
	return s
}

// SharedServers lists the groups of db_servers entries whose connection strings
// name the same host and port. An objective, an incident, a restore, the downtime
// ledger and the recovery-time estimate are all kept per entry, and an entry is a
// whole PostgreSQL server (the cluster), not a database inside it. Two entries
// that reach one server therefore have separate budgets and histories for the
// same machine, and an outage can be charged to one and not the other. It is a
// warning and not an error: two entries behind one tunnel or proxy can be
// deliberate. Entries with no host are skipped, an absent port is 5432, and the
// order is stable.
func (c *Config) SharedServers() []string {
	if c == nil {
		return nil
	}
	byAddr := map[string][]string{}
	for _, id := range sortedKeys(c.DBServers) {
		s := c.DBServers[id].ConnectionString
		host := strings.ToLower(ConnStrHost(s))
		if host == "" {
			continue
		}
		port := ConnStrPort(s)
		if port == "" {
			port = "5432"
		}
		addr := host + ":" + port
		byAddr[addr] = append(byAddr[addr], id)
	}
	var out []string
	for _, addr := range sortedKeys(byAddr) {
		if ids := byAddr[addr]; len(ids) > 1 {
			out = append(out, fmt.Sprintf("db_servers %s all reach %s: each is kept as its own server, with its own objectives, budget and restore history, so an outage on that machine is charged to only one of them", strings.Join(ids, ", "), addr))
		}
	}
	return out
}

// ConnStrPort extracts the port from a libpq key=value connection string, a
// postgres:// URI or a "host:port" address, or a bare port number. Returns ""
// when there is none.
func ConnStrPort(s string) string {
	for _, field := range strings.Fields(s) {
		if strings.HasPrefix(field, "port=") {
			if p := strings.TrimPrefix(field, "port="); p != "" {
				return p
			}
		}
	}
	s = strings.TrimPrefix(s, "postgresql://")
	s = strings.TrimPrefix(s, "postgres://")
	if idx := strings.LastIndex(s, ":"); idx >= 0 {
		p := s[idx+1:]
		if sl := strings.IndexAny(p, "/?"); sl >= 0 {
			p = p[:sl]
		}
		if p != "" && p != s {
			return p
		}
	}
	if strings.TrimFunc(s, func(r rune) bool { return r >= '0' && r <= '9' }) == "" && s != "" {
		return s
	}
	return ""
}
