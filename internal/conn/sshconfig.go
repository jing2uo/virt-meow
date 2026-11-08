package conn

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// Hop is one SSH server on the way to a host: the host itself, or a jump
// host before it (ProxyJump), with what the SSH configuration says about
// logging in to it.
type Hop struct {
	Name           string   `json:"name"` // as written: an alias or an address
	User           string   `json:"user"`
	Host           string   `json:"host"` // the HostName it resolves to
	Port           string   `json:"port"`
	IdentityFiles  []string `json:"identity_files,omitempty"` // the keys to try; the default ones when the config names none
	IdentitiesOnly bool     `json:"identities_only,omitempty"`
	// Agent is "none" for no agent, else the agent's socket ("" for
	// $SSH_AUTH_SOCK).
	Agent string `json:"agent,omitempty"`
	// Configured says the SSH configuration said something about it.
	Configured bool `json:"configured,omitempty"`
}

// Addr is the hop's host:port.
func (h Hop) Addr() string { return net.JoinHostPort(h.Host, h.Port) }

func (h Hop) String() string { return h.User + "@" + h.Addr() }

// maxHops bounds a chain of jump hosts, which a configuration could make
// endless (each one jumping through the next).
const maxHops = 8

// Route is how t is reached over SSH, read from the user's ~/.ssh/config
// and the system's ssh_config as OpenSSH reads them: its jump hosts in the
// order they are dialled, then the host. A user or port in the URI wins
// over the configuration's, as on ssh's command line. Not an SSH target:
// nil.
func Route(t Target) ([]Hop, error) {
	if t.Transport != SSH {
		return nil, nil
	}
	return sshConfigFiles().route(t.Host, t.User, t.Port, 0)
}

// sshConfig is the SSH client configuration files, user's first: for each
// setting the first value found counts (IdentityFile adds up).
type sshConfig struct {
	files []configFile
}

type configFile struct {
	path string
	base string // where an Include's relative path starts
}

func sshConfigFiles() sshConfig {
	var c sshConfig
	if home, err := os.UserHomeDir(); err == nil {
		dir := filepath.Join(home, ".ssh")
		c.files = append(c.files, configFile{filepath.Join(dir, "config"), dir})
	}
	sys := "/etc/ssh"
	if runtime.GOOS == "windows" {
		sys = filepath.Join(os.Getenv("ProgramData"), "ssh")
	}
	c.files = append(c.files, configFile{filepath.Join(sys, "ssh_config"), sys})
	return c
}

func (c sshConfig) route(name, user, port string, depth int) ([]Hop, error) {
	if depth >= maxHops {
		return nil, fmt.Errorf("ssh config: more than %d jump hosts on the way to %s (a loop?)", maxHops, name)
	}
	s, err := c.lookup(name, user)
	if err != nil {
		return nil, err
	}
	h := s.hop(name, user, port)
	jumps := s.jumps
	if s.proxyCommand != "" {
		if jumps, err = proxyCommandJump(s.proxyCommand); err != nil {
			return nil, err
		}
	}
	var route []Hop
	for i, j := range jumps {
		jn, ju, jp, err := parseJump(j)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			// The first jump host is reached as ssh would reach it, through
			// its own jump hosts if it has any.
			r, err := c.route(jn, ju, jp, depth+1)
			if err != nil {
				return nil, err
			}
			route = append(route, r...)
			continue
		}
		js, err := c.lookup(jn, ju)
		if err != nil {
			return nil, err
		}
		route = append(route, js.hop(jn, ju, jp))
	}
	if len(route)+1 > maxHops {
		return nil, fmt.Errorf("ssh config: more than %d jump hosts on the way to %s", maxHops, name)
	}
	return append(route, h), nil
}

// settings are what the configuration says about one host.
type settings struct {
	hostName, user, port string
	identityFiles        []string
	identitiesOnly       string
	identityAgent        string
	proxySet             bool // ProxyJump or ProxyCommand seen: the first one wins
	jumps                []string
	proxyCommand         string
	configured           bool
}

func (s *settings) hop(name, user, port string) Hop {
	h := Hop{Name: name, User: user, Port: port, Configured: s.configured}
	if h.User == "" {
		h.User = s.user
	}
	if h.User == "" {
		h.User = currentUser()
	}
	if h.Port == "" {
		h.Port = s.port
	}
	if h.Port == "" {
		h.Port = "22"
	}
	h.Host = name
	if s.hostName != "" {
		h.Host = expandTokens(s.hostName, map[byte]string{'h': name})
	}
	home, _ := os.UserHomeDir()
	tokens := map[byte]string{'h': h.Host, 'n': name, 'p': h.Port, 'r': h.User, 'u': currentUser(), 'd': home}
	files := s.identityFiles
	if len(files) == 0 {
		for _, f := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
			files = append(files, filepath.Join("~", ".ssh", f))
		}
	}
	for _, f := range files {
		f = expandPath(expandTokens(f, tokens), home)
		if !slices.Contains(h.IdentityFiles, f) {
			h.IdentityFiles = append(h.IdentityFiles, f)
		}
	}
	h.IdentitiesOnly = strings.EqualFold(s.identitiesOnly, "yes")
	switch a := s.identityAgent; {
	case a == "", a == "SSH_AUTH_SOCK":
	case strings.EqualFold(a, "none"):
		h.Agent = "none"
	case strings.HasPrefix(a, "$"):
		h.Agent = os.Getenv(strings.Trim(a[1:], "{}"))
	default:
		h.Agent = expandPath(expandTokens(a, tokens), home)
	}
	return h
}

// lookup reads the settings for host name as ssh would for `ssh
// [user@]name`.
func (c sshConfig) lookup(name, user string) (*settings, error) {
	s := &settings{}
	p := &configParser{s: s, host: strings.ToLower(name), user: user}
	for _, f := range c.files {
		if err := p.file(f.path, f.base, 0, true); err != nil {
			return nil, err
		}
	}
	return s, nil
}

type configParser struct {
	s    *settings
	host string // the host as given, lowercased (ssh does)
	user string // the user given in the URI, or ""
}

// file reads one configuration file; a missing one is no error, as for ssh.
func (p *configParser) file(path, base string, depth int, top bool) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || !top {
			return nil
		}
		return fmt.Errorf("ssh config: %w", err)
	}
	defer f.Close()
	active := true
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		n++
		key, args := configLine(sc.Text())
		if key == "" {
			continue
		}
		switch key {
		case "host":
			active = p.hostMatches(args)
			continue
		case "match":
			active = p.matchMatches(args)
			continue
		}
		if !active {
			continue
		}
		if key == "include" {
			if depth >= 16 {
				return fmt.Errorf("ssh config: %s:%d: Include nested too deep", path, n)
			}
			for _, a := range args {
				a = expandPath(a, "")
				if !filepath.IsAbs(a) {
					a = filepath.Join(base, a)
				}
				matches, _ := filepath.Glob(a)
				slices.Sort(matches)
				for _, m := range matches {
					if err := p.file(m, base, depth+1, false); err != nil {
						return err
					}
				}
			}
			continue
		}
		p.set(key, args)
	}
	return sc.Err()
}

func (p *configParser) set(key string, args []string) {
	s := p.s
	if len(args) == 0 {
		return
	}
	first := func(v *string) {
		if *v == "" {
			*v = args[0]
			s.configured = true
		}
	}
	switch key {
	case "hostname":
		first(&s.hostName)
	case "user":
		first(&s.user)
	case "port":
		first(&s.port)
	case "identitiesonly":
		first(&s.identitiesOnly)
	case "identityagent":
		first(&s.identityAgent)
	case "identityfile":
		s.identityFiles = append(s.identityFiles, args[0])
		s.configured = true
	case "proxyjump":
		if !s.proxySet {
			s.proxySet, s.configured = true, true
			if !strings.EqualFold(args[0], "none") {
				s.jumps = strings.Split(strings.Join(args, ""), ",")
			}
		}
	case "proxycommand":
		if !s.proxySet {
			s.proxySet, s.configured = true, true
			if !strings.EqualFold(args[0], "none") {
				s.proxyCommand = strings.Join(args, " ")
			}
		}
	}
}

// hostMatches is a Host line: any pattern matches and no negated one does.
func (p *configParser) hostMatches(pats []string) bool {
	return matchList(pats, p.host)
}

// matchMatches is a Match line. The criteria a client without a shell can
// judge (all, host, originalhost, user, localuser) are; a block with any
// other (exec, canonical, final, localnetwork, tagged) is skipped, as if it
// didn't match.
func (p *configParser) matchMatches(args []string) bool {
	for i := 0; i < len(args); i++ {
		crit := strings.ToLower(args[i])
		neg := strings.HasPrefix(crit, "!")
		crit = strings.TrimPrefix(crit, "!")
		var ok bool
		switch crit {
		case "all":
			ok = true
		case "host", "originalhost", "user", "localuser":
			if i+1 >= len(args) {
				return false
			}
			i++
			pats := strings.Split(args[i], ",")
			switch crit {
			case "host":
				h := p.host
				if p.s.hostName != "" {
					h = strings.ToLower(expandTokens(p.s.hostName, map[byte]string{'h': p.host}))
				}
				ok = matchList(pats, h)
			case "originalhost":
				ok = matchList(pats, p.host)
			case "user":
				u := p.user
				if u == "" {
					u = p.s.user
				}
				if u == "" {
					u = currentUser()
				}
				ok = matchList(pats, u)
			case "localuser":
				ok = matchList(pats, currentUser())
			}
		default:
			return false
		}
		if ok == neg {
			return false
		}
	}
	return true
}

// matchList is ssh's pattern list: some pattern matches s and no negated
// one does. Patterns may be split over several arguments or by commas.
func matchList(pats []string, s string) bool {
	matched := false
	for _, arg := range pats {
		for _, pat := range strings.Split(arg, ",") {
			if pat == "" {
				continue
			}
			neg := strings.HasPrefix(pat, "!")
			pat = strings.ToLower(strings.TrimPrefix(pat, "!"))
			if wildcard(pat, s) {
				if neg {
					return false
				}
				matched = true
			}
		}
	}
	return matched
}

// wildcard matches s against a pattern of * and ?.
func wildcard(pat, s string) bool {
	if pat == "" {
		return s == ""
	}
	switch pat[0] {
	case '*':
		for i := 0; i <= len(s); i++ {
			if wildcard(pat[1:], s[i:]) {
				return true
			}
		}
		return false
	case '?':
		return s != "" && wildcard(pat[1:], s[1:])
	}
	return s != "" && s[0] == pat[0] && wildcard(pat[1:], s[1:])
}

// configLine splits a line into its keyword, lowercased, and arguments:
// "Key value", "Key=value", with double quotes around an argument holding
// spaces. A comment or an empty line gives "".
func configLine(line string) (string, []string) {
	line = strings.TrimSpace(line)
	if line == "" || line[0] == '#' {
		return "", nil
	}
	i := strings.IndexAny(line, " \t=")
	if i < 0 {
		return strings.ToLower(line), nil
	}
	key := strings.ToLower(line[:i])
	rest := strings.TrimLeft(line[i:], " \t")
	rest = strings.TrimLeft(strings.TrimPrefix(rest, "="), " \t")
	var args []string
	for rest != "" {
		if rest[0] == '#' {
			break
		}
		var a string
		if rest[0] == '"' {
			end := strings.IndexByte(rest[1:], '"')
			if end < 0 {
				a, rest = rest[1:], ""
			} else {
				a, rest = rest[1:end+1], rest[end+2:]
			}
		} else if j := strings.IndexAny(rest, " \t"); j >= 0 {
			a, rest = rest[:j], rest[j:]
		} else {
			a, rest = rest, ""
		}
		args = append(args, a)
		rest = strings.TrimLeft(rest, " \t")
	}
	return key, args
}

// expandTokens replaces ssh's %-tokens that tokens has, and %%.
func expandTokens(s string, tokens map[byte]string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '%' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		if s[i] == '%' {
			b.WriteByte('%')
		} else if v, ok := tokens[s[i]]; ok {
			b.WriteString(v)
		} else {
			b.WriteByte('%')
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// expandPath expands a leading ~ and ${VAR}s, as ssh does in file names.
func expandPath(p, home string) string {
	p = os.Expand(p, func(v string) string {
		if v == "" {
			return "$"
		}
		return os.Getenv(v)
	})
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home == "" {
			home, _ = os.UserHomeDir()
		}
		p = filepath.Join(home, p[1:])
	}
	return p
}

// parseJump reads one ProxyJump entry: [user@]host[:port] or
// ssh://[user@]host[:port].
func parseJump(s string) (host, user, port string, err error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "ssh://") {
		s = "ssh://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" || (u.Path != "" && u.Path != "/") {
		return "", "", "", fmt.Errorf("ssh config: jump host %q: want [user@]host[:port]", strings.TrimPrefix(s, "ssh://"))
	}
	if u.User != nil {
		user = u.User.Username()
	}
	return u.Hostname(), user, u.Port(), nil
}

// proxyCommandJump reads a ProxyCommand that is a jump host in other words,
// `ssh [options] -W %h:%p host`, as the jump host it names. Any other
// command would need running a program here, which virt-meow doesn't
// (docs/adr/0002).
func proxyCommandJump(cmd string) ([]string, error) {
	words := strings.Fields(cmd)
	unsupported := fmt.Errorf("ssh config: ProxyCommand %q: virt-meow runs no program to connect; only `ssh -W %%h:%%p <host>` is understood, ProxyJump is the way to say it", cmd)
	if len(words) == 0 || strings.TrimSuffix(filepath.Base(words[0]), ".exe") != "ssh" {
		return nil, unsupported
	}
	var dest, user, port, forward string
	var jumps []string
	for i := 1; i < len(words); i++ {
		w := words[i]
		if !strings.HasPrefix(w, "-") || w == "-" {
			if dest != "" {
				return nil, unsupported // a command to run on it
			}
			dest = w
			continue
		}
		// Options with a value, as -W's.
		if len(w) == 2 && strings.ContainsRune("BbcDEeFIiJLlmOoPpQRSWw", rune(w[1])) {
			if i+1 >= len(words) {
				return nil, unsupported
			}
			i++
			switch w[1] {
			case 'W':
				forward = words[i]
			case 'l':
				user = words[i]
			case 'p':
				port = words[i]
			case 'J':
				jumps = append(jumps, strings.Split(words[i], ",")...)
			case 'F', 'o', 'D', 'L', 'R':
				return nil, unsupported
			}
			continue
		}
		if strings.HasPrefix(w, "-W") {
			forward = w[2:]
		}
	}
	if dest == "" || (forward != "%h:%p" && forward != "[%h]:%p") {
		return nil, unsupported
	}
	if user != "" && !strings.Contains(dest, "@") {
		dest = user + "@" + dest
	}
	if port != "" {
		dest = "ssh://" + dest + ":" + port
	}
	return append(jumps, dest), nil
}
