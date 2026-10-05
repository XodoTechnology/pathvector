package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/natesales/pathvector/pkg/bird"
	"github.com/natesales/pathvector/pkg/config"
	"github.com/natesales/pathvector/pkg/process"
	"github.com/natesales/pathvector/pkg/templating"
)

// SessionReport is the per-session payload XodoPanel's /api/v1/bgp/report expects
type SessionReport struct {
	Name          string            `json:"name"`
	State         string            `json:"state"`
	Uptime        int64             `json:"uptime,omitempty"`
	Accepted      int               `json:"accepted"`
	FilteredCount int               `json:"filtered_count"`
	Sent          int               `json:"sent"`
	Received      []string          `json:"received,omitempty"`
	Filtered      []string          `json:"filtered,omitempty"`
	Exporting     []string          `json:"exporting,omitempty"`
	ASPaths       map[string]string `json:"as_paths,omitempty"`
	// per-address-family breakdown — v4 and v6 are separate BGP protocols in
	// BIRD, and one can be down while the other is established
	Families map[string]*FamilyReport `json:"families,omitempty"`
}

// FamilyReport is the per-AF (4/6) view of a session
type FamilyReport struct {
	State    string `json:"state"`
	Uptime   int64  `json:"uptime,omitempty"`
	Accepted int    `json:"accepted"`
	Filtered int    `json:"filtered_count"`
	Sent     int    `json:"sent"`
}

// Report is the full payload posted to the panel
type Report struct {
	Router   string          `json:"router"`
	Sessions []SessionReport `json:"sessions"`
}

var (
	routePrefixRegex = regexp.MustCompile(`^([0-9a-fA-F:.]+/\d+)\s`)
	routeASPathRegex = regexp.MustCompile(`\[AS([0-9 ]+)[a-z?]*\]`)
	protoAFRegex     = regexp.MustCompile(`_v([46])(_\d+)?$`)
)

// protoAF returns "4" or "6" for a protocol. BIRD 3 shows "---" in the table
// column for BGP protos, so the pathvector _v4/_v6 name suffix is primary;
// table and neighbor address are fallbacks.
func protoAF(ps *bird.ProtocolState) string {
	if m := protoAFRegex.FindStringSubmatch(ps.Name); m != nil {
		return m[1]
	}
	switch ps.Table {
	case "master4":
		return "4"
	case "master6":
		return "6"
	}
	if ps.BGP != nil && strings.Contains(ps.BGP.NeighborAddress, ":") {
		return "6"
	}
	return ""
}

// sinceFormats are the timestamp formats BIRD prints in `show protocols`
var sinceFormats = []string{
	"2006-01-02 15:04:05",
	"2006-01-02",
	"15:04:05",
}

func parseSince(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, f := range sinceFormats {
		if t, err := time.ParseInLocation(f, s, time.Local); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// protocolNames reads the bird-directory protocols.json name/tag map
func protocolNames(c *config.Config) map[string]*templating.Protocol {
	names := map[string]*templating.Protocol{}
	contents, err := os.ReadFile(path.Join(c.BIRDDirectory, "protocols.json"))
	if err != nil {
		log.Debugf("report: reading protocol names: %s", err)
		return names
	}
	if err := json.Unmarshal(contents, &names); err != nil {
		log.Debugf("report: unmarshalling protocol names: %s", err)
	}
	return names
}

// sessionRoutes lists prefixes for a BIRD protocol. filtered selects between
// `show route protocol` and `show route filtered protocol`; mode "export"
// uses `show route export` to list what we'd announce to the peer — the AS
// paths then reflect prepends applied by export rules.
func sessionRoutes(socket, protoName, mode string, timeout time.Duration) ([]string, map[string]string, error) {
	command := fmt.Sprintf("show route protocol %s", protoName)
	switch mode {
	case "filtered":
		command = fmt.Sprintf("show route filtered protocol %s", protoName)
	case "export":
		command = fmt.Sprintf("show route export %s", protoName)
	}
	resp, _, err := bird.RunCommand(command, socket, timeout)
	if err != nil {
		return nil, nil, err
	}

	var prefixes []string
	asPaths := map[string]string{}
	for _, line := range strings.Split(resp, "\n") {
		line = strings.TrimSpace(line)
		m := routePrefixRegex.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		pfx := m[1]
		prefixes = append(prefixes, pfx)
		if pm := routeASPathRegex.FindStringSubmatch(line); pm != nil {
			// "AS65001 AS65002 i" -> "65001 65002"
			var asnParts []string
			for _, tok := range strings.Fields(pm[1]) {
				asnParts = append(asnParts, strings.TrimRight(tok, "i?"))
			}
			if len(asnParts) > 0 {
				asPaths[pfx] = strings.Join(asnParts, " ")
			}
		}
	}
	return prefixes, asPaths, nil
}

// Collect builds a Report from live BIRD state. Prefix lists are only
// enumerated for sessions with at most prefixCap imported routes.
func Collect(c *config.Config, router string, prefixCap int) (*Report, error) {
	resp, _, err := bird.RunCommand("show protocols all", c.BIRDSocket, time.Duration(c.BIRDTimeout)*time.Second)
	if err != nil {
		return nil, fmt.Errorf("show protocols all: %s", err)
	}
	protocolStates, err := bird.ParseProtocols(resp)
	if err != nil {
		return nil, fmt.Errorf("parsing protocols: %s", err)
	}

	names := protocolNames(c)
	sessions := map[string]*SessionReport{}
	var order []string

	getSession := func(protoName string) *SessionReport {
		name := protoName
		if p, ok := names[protoName]; ok && p.Name != "" {
			name = p.Name
		}
		if s, ok := sessions[name]; ok {
			return s
		}
		s := &SessionReport{Name: name, ASPaths: map[string]string{}, Families: map[string]*FamilyReport{}}
		sessions[name] = s
		order = append(order, name)
		return s
	}

	// Aggregate per-peer across v4/v6 protocols
	for _, ps := range protocolStates {
		if ps.Proto != "BGP" {
			continue
		}
		s := getSession(ps.Name)
		state := ""
		if ps.BGP != nil {
			state = ps.BGP.State
		}
		if state == "" {
			state = ps.Info
		}
		if state == "" {
			state = ps.State // down/start — protos without a BGP detail section
		}
		if state != "" && (s.State == "" || state == "Established") {
			s.State = state
		}
		if af := protoAF(ps); af != "" {
			fam := s.Families[af]
			if fam == nil {
				fam = &FamilyReport{}
				s.Families[af] = fam
			}
			fam.State = state
			if ps.Routes != nil {
				if ps.Routes.Imported >= 0 {
					fam.Accepted += ps.Routes.Imported
				}
				if ps.Routes.Filtered >= 0 {
					fam.Filtered += ps.Routes.Filtered
				}
				if ps.Routes.Exported >= 0 {
					fam.Sent += ps.Routes.Exported
				}
			}
			if t, ok := parseSince(ps.Since); ok && state == "Established" {
				if u := int64(time.Since(t).Seconds()); u > fam.Uptime {
					fam.Uptime = u
				}
			}
		}
		if t, ok := parseSince(ps.Since); ok && s.State == "Established" {
			uptime := int64(time.Since(t).Seconds())
			if uptime > s.Uptime {
				s.Uptime = uptime
			}
		}
		if ps.Routes != nil {
			if ps.Routes.Imported >= 0 {
				s.Accepted += ps.Routes.Imported
			}
			if ps.Routes.Filtered >= 0 {
				s.FilteredCount += ps.Routes.Filtered
			}
			if ps.Routes.Exported >= 0 {
				s.Sent += ps.Routes.Exported
			}
		}

		// Enumerate prefixes when the session is small enough
		if ps.Routes != nil && ps.Routes.Imported >= 0 && ps.Routes.Imported <= prefixCap {
			cmdTimeout := time.Duration(c.BIRDTimeout) * time.Second
			if recv, paths, err := sessionRoutes(c.BIRDSocket, ps.Name, "received", cmdTimeout); err == nil {
				s.Received = append(s.Received, recv...)
				for pfx, p := range paths {
					s.ASPaths[pfx] = p
				}
			} else {
				log.Debugf("report: session routes %s: %s", ps.Name, err)
			}
			if filt, paths, err := sessionRoutes(c.BIRDSocket, ps.Name, "filtered", cmdTimeout); err == nil {
				s.Filtered = append(s.Filtered, filt...)
				for pfx, p := range paths {
					s.ASPaths[pfx] = p
				}
			}
		}
		// Export side — same cap applies; a downstream receiving a full table
		// stays count-only rather than enumerating ~1M routes every cycle.
		if ps.Routes != nil && ps.Routes.Exported >= 0 && ps.Routes.Exported <= prefixCap {
			cmdTimeout := time.Duration(c.BIRDTimeout) * time.Second
			if exp, paths, err := sessionRoutes(c.BIRDSocket, ps.Name, "export", cmdTimeout); err == nil {
				s.Exporting = append(s.Exporting, exp...)
				for pfx, p := range paths {
					s.ASPaths[pfx] = p
				}
			} else {
				log.Debugf("report: export routes %s: %s", ps.Name, err)
			}
		}
	}

	// Report configured peers that BIRD doesn't know about as down
	for peerName, peerData := range c.Peers {
		if _, ok := sessions[peerName]; ok {
			continue
		}
		s := &SessionReport{Name: peerName, State: "down", Accepted: -1, FilteredCount: -1, Sent: -1, Families: map[string]*FamilyReport{}}
		if peerData.Disabled != nil && *peerData.Disabled {
			s.State = "disabled"
		}
		// Synthesise per-family state from configured neighbor IPs so a
		// never-generated session still reports v4/v6 independently
		if peerData.NeighborIPs != nil {
			for _, n := range *peerData.NeighborIPs {
				af := "4"
				if strings.Contains(n, ":") {
					af = "6"
				}
				s.Families[af] = &FamilyReport{State: s.State}
			}
		}
		sessions[peerName] = s
		order = append(order, peerName)
	}

	// Sessions must serialize as [] not null — the panel rejects null
	r := &Report{Router: router, Sessions: []SessionReport{}}
	for _, name := range order {
		r.Sessions = append(r.Sessions, *sessions[name])
	}
	return r, nil
}

// Post sends a report to the panel endpoint (XodoPanel /api/v1/bgp/report)
func Post(r *Report, url, key string) error {
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("X-API-Key", key)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("report POST %s: %s", url, err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("report POST %s: %s", url, res.Status)
	}
	return nil
}

// Loop collects and posts reports on an interval until done is closed. The
// config file is reloaded each cycle so panel-driven session changes are
// reflected without restarting the daemon.
func Loop(configFile string, done <-chan struct{}) {
	for {
		interval := time.Minute
		if c, err := process.LoadFile(configFile); err != nil {
			log.Warnf("report: loading config: %s", err)
		} else if c.ReportURL == "" {
			return // reporting disabled
		} else {
			if c.ReportInterval > 0 {
				interval = time.Duration(c.ReportInterval) * time.Second
			}
			router := c.ReportRouter
			if router == "" {
				router = c.Hostname
			}
			r, err := Collect(c, router, c.ReportPrefixCap)
			if err != nil {
				log.Warnf("report: %s", err)
			} else if err := Post(r, c.ReportURL, c.ReportKey); err != nil {
				log.Warnf("report: %s", err)
			}
		}

		select {
		case <-done:
			return
		case <-time.After(interval):
		}
	}
}
