package irr

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/natesales/pathvector/pkg/config"
)

// bgpq4Command is the bgpq4 executable to run. It is a variable so tests can substitute a fake implementation.
var bgpq4Command = "bgpq4"

// bgpq4ArgFlags is the set of bgpq4 single-letter flags that take an argument
const bgpq4ArgFlags = "FfGHhLlMmRrSW"

// hasSourcesFlag returns true if the user-supplied bgpq4 arguments already contain a -S (sources) flag
func hasSourcesFlag(args []string) bool {
	for _, arg := range args {
		if len(arg) < 2 || arg[0] != '-' || arg[1] == '-' {
			continue
		}
		// Walk combined short flags (e.g. -AS RIPE or -SRIPE), stopping at the first flag that takes an argument
		for _, flag := range arg[1:] {
			if flag == 'S' {
				return true
			}
			if strings.ContainsRune(bgpq4ArgFlags, flag) {
				break
			}
		}
	}
	return false
}

// sourceFilter converts an AS set with an optional IRR source prefix into bgpq4 arguments.
// If userArgs already specify IRR sources (-S), the as-set's source prefix is stripped and not added,
// since a second -S flag would override the user's source list.
//
//	AS34553 -> [AS34553]
//	RIPE::AS34553 -> [-S RIPE AS34553]
//	RIPE::AS34553 (with -S in userArgs) -> [AS34553]
func sourceFilter(asSet string, userArgs []string) []string {
	if strings.Contains(asSet, "::") {
		tokens := strings.SplitN(asSet, "::", 2)
		if hasSourcesFlag(userArgs) {
			log.Debugf("Ignoring IRRDB source %s from AS set %s because bgpq-args already contains -S", tokens[0], asSet)
			return []string{tokens[1]}
		}
		log.Debugf("Using IRRDB source from AS set %s", asSet)
		return []string{"-S", tokens[0], tokens[1]}
	}
	return []string{asSet}
}

// buildArgs builds a bgpq4 argument list from user arguments, query specific arguments and an as-set
func buildArgs(bgpqArgs string, asSet string, queryArgs ...string) []string {
	userArgs := strings.Fields(bgpqArgs)
	args := append([]string{}, userArgs...)
	args = append(args, queryArgs...)
	return append(args, sourceFilter(asSet, userArgs)...)
}

// runBGPQ4 runs bgpq4 with the given arguments and returns stdout
func runBGPQ4(args []string, queryTimeout uint) ([]byte, error) {
	log.Debugf("Running %s %s", bgpq4Command, strings.Join(args, " "))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*time.Duration(queryTimeout))
	defer cancel()
	//nolint:golint,gosec
	return exec.CommandContext(ctx, bgpq4Command, args...).Output()
}

// PrefixSet uses bgpq4 to generate a prefix filter and return only the filter lines
func PrefixSet(macro string, family uint8, irrServer string, queryTimeout uint, bgpqArgs string) ([]string, error) {
	var prefixes []string

	for _, asSet := range strings.Fields(macro) {
		// Run bgpq4 for BIRD format with aggregation enabled
		stdout, err := runBGPQ4(buildArgs(bgpqArgs, asSet, "-h", irrServer, fmt.Sprintf("-Ab%d", family)), queryTimeout)
		if err != nil {
			return nil, err
		}

		for i, line := range strings.Split(string(stdout), "\n") {
			if i == 0 { // Skip first line, as it is the definition line
				continue
			}
			if strings.Contains(line, "];") { // Skip last line and return
				break
			}
			// Trim whitespace and remove the comma, then append to the prefixes slice
			prefixes = append(prefixes, strings.TrimSpace(strings.TrimRight(line, ",")))
		}
	}

	return prefixes, nil
}

// ASMembers uses bgpq4 to generate an AS member set
func ASMembers(asSet string, irrServer string, queryTimeout uint, bgpqArgs string) ([]uint32, error) {
	stdout, err := runBGPQ4(buildArgs(bgpqArgs, asSet, "-h", irrServer, "-tj"), queryTimeout)
	if err != nil {
		return nil, err
	}

	var r map[string][]uint32
	if err := json.Unmarshal(stdout, &r); err != nil {
		return nil, fmt.Errorf("bgpq4 JSON Unmarshal: %s", err)
	}

	return r["NN"], nil
}

// Update updates a peer's IRR prefix set
func Update(peerData *config.Peer, irrServer string, queryTimeout uint, bgpqArgs string) error {
	// Check for empty as-set
	if peerData.ASSet == nil || *peerData.ASSet == "" {
		return fmt.Errorf("peer has filter-irr enabled and no as-set defined")
	}

	// Does the peer have any IPv4 or IPv6 neighbors?
	var hasNeighbor4, hasNeighbor6 bool
	if peerData.NeighborIPs != nil {
		for _, n := range *peerData.NeighborIPs {
			if strings.Contains(n, ".") {
				hasNeighbor4 = true
			} else if strings.Contains(n, ":") {
				hasNeighbor6 = true
			} else {
				log.Fatalf("Invalid neighbor IP %s", n)
			}
		}
	}

	// Handle acceptChildPrefixes
	bgpqArgs4 := bgpqArgs
	bgpqArgs6 := bgpqArgs
	if peerData.IRRAcceptChildPrefixes != nil && *peerData.IRRAcceptChildPrefixes {
		if bgpqArgs4 != "" {
			bgpqArgs4 += " "
		}
		bgpqArgs4 += "-R 24"

		if bgpqArgs6 != "" {
			bgpqArgs6 += " "
		}
		bgpqArgs6 += "-R 48"
	}

	prefixesFromIRR4, err := PrefixSet(*peerData.ASSet, 4, irrServer, queryTimeout, bgpqArgs4)
	if err != nil {
		return fmt.Errorf("unable to get IPv4 IRR prefix list from %s: %s", *peerData.ASSet, err)
	}
	if peerData.PrefixSet4 == nil {
		peerData.PrefixSet4 = &[]string{}
	}
	pfx4 := append(*peerData.PrefixSet4, prefixesFromIRR4...)
	peerData.PrefixSet4 = &pfx4
	if len(pfx4) == 0 && hasNeighbor4 {
		log.Warnf("peer has IPv4 session(s) but no IPv4 prefixes")
	}

	prefixesFromIRR6, err := PrefixSet(*peerData.ASSet, 6, irrServer, queryTimeout, bgpqArgs6)
	if err != nil {
		return fmt.Errorf("unable to get IPv6 IRR prefix list from %s: %s", *peerData.ASSet, err)
	}
	if peerData.PrefixSet6 == nil {
		peerData.PrefixSet6 = &[]string{}
	}
	pfx6 := append(*peerData.PrefixSet6, prefixesFromIRR6...)
	peerData.PrefixSet6 = &pfx6
	if len(pfx6) == 0 && hasNeighbor6 {
		log.Warnf("peer has IPv6 session(s) but no IPv6 prefixes")
	}

	return nil // nil error
}
