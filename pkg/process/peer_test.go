package process

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/natesales/pathvector/pkg/config"
	"github.com/natesales/pathvector/pkg/embed"
	"github.com/natesales/pathvector/pkg/templating"
)

// shimBGPQ4 puts a fake bgpq4 script running body first in PATH for the duration of the test
func shimBGPQ4(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	//nolint:gosec
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bgpq4"), []byte("#!/bin/sh\n"+body+"\n"), 0755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// fakeBGPQ4Body answers prefix queries with the given v4/v6 output and member queries with the given JSON
func fakeBGPQ4Body(prefixes4, prefixes6, members string) string {
	return `case "$*" in
  *-Ab4*) printf 'define x = [\n` + prefixes4 + `\n];\n' ;;
  *-Ab6*) printf 'define x = [\n` + prefixes6 + `\n];\n' ;;
  *-tj*) echo '` + members + `' ;;
esac`
}

// loadTestPeers loads a config with the given peers YAML and prepares templates and a temporary cache directory
func loadTestPeers(t *testing.T, peersYAML string) *config.Config {
	t.Helper()
	c, err := Load([]byte(`
asn: 34553
router-id: 192.0.2.1
prefixes:
  - 192.0.2.0/24
peers:
` + peersYAML))
	require.NoError(t, err)
	c.CacheDirectory = t.TempDir()
	require.NoError(t, templating.Load(embed.FS))
	return c
}

// runPeer runs peer() for a single peer and returns its rendered config
func runPeer(t *testing.T, c *config.Config, name string) string {
	t.Helper()
	wg := new(sync.WaitGroup)
	wg.Add(1)
	peer(name, c.Peers[name], c, wg)
	wg.Wait()
	files, err := filepath.Glob(filepath.Join(c.CacheDirectory, "AS*_"+*c.Peers[name].ProtocolName+".conf"))
	require.NoError(t, err)
	require.Len(t, files, 1)
	b, err := os.ReadFile(files[0])
	require.NoError(t, err)
	return string(b)
}

const irrTestPeers = `
  Good:
    asn: 65510
    as-set: AS-GOOD
    filter-irr: true
    auto-as-set-members: true
    filter-as-set: true
    neighbors:
      - 203.0.113.10
      - 2001:db8::10
  Bad:
    asn: 65520
    as-set: AS-BAD
    filter-irr: true
    auto-as-set-members: true
    filter-as-set: true
    neighbors:
      - 203.0.113.20
`

func TestPeerIRRFailureRejectsImports(t *testing.T) {
	shimBGPQ4(t, `case "$*" in *AS-BAD*) echo "ERROR: something went wrong" >&2; exit 1 ;; esac
`+fakeBGPQ4Body("    198.51.100.0/24", "    2001:db8:1::/48", `{"NN": [65510]}`))
	c := loadTestPeers(t, irrTestPeers)

	good := runPeer(t, c, "Good")
	assert.True(t, *c.Peers["Good"].Import)
	assert.NotContains(t, good, "reject; # import: false")
	assert.Contains(t, good, "198.51.100.0/24")
	assert.Contains(t, good, "2001:db8:1::/48")

	bad := runPeer(t, c, "Bad")
	assert.False(t, *c.Peers["Bad"].Import)
	assert.Contains(t, bad, "reject; # import: false")
	// filter-as-set with no members is disabled to keep the BIRD config valid (imports are rejected anyway)
	assert.False(t, *c.Peers["Bad"].FilterASSet)
	assert.NotContains(t, bad, "AS_SET_MEMBERS = [")
}

func TestPeerIRRNoPrefixesRejectsImports(t *testing.T) {
	shimBGPQ4(t, fakeBGPQ4Body("", "", `{"NN": [65520]}`))
	c := loadTestPeers(t, irrTestPeers)

	out := runPeer(t, c, "Bad")
	assert.False(t, *c.Peers["Bad"].Import)
	assert.Contains(t, out, "reject; # import: false")
}

func TestPeerIRRSingleFamily(t *testing.T) {
	// Only IPv6 prefixes registered: import stays enabled, the IPv4 prefix set is empty and so rejects everything
	shimBGPQ4(t, fakeBGPQ4Body("", "    2001:db8:1::/48", `{"NN": [65510]}`))
	c := loadTestPeers(t, irrTestPeers)

	out := runPeer(t, c, "Good")
	assert.True(t, *c.Peers["Good"].Import)
	assert.True(t, strings.Contains(out, "_PFX_v4 = -empty-;"))
}
