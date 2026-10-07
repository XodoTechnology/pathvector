package process

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/natesales/pathvector/pkg/bird"
	"github.com/natesales/pathvector/pkg/embed"
	"github.com/natesales/pathvector/pkg/templating"
)

// renderConfig loads a YAML config, renders the global and peer templates into a temporary directory and,
// if a BIRD binary is available, validates the result with `bird -p`. It returns the rendered files keyed by peer name ("" for the global config).
func renderConfig(t *testing.T, configYAML string) map[string]string {
	t.Helper()
	c, err := Load([]byte(configYAML))
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	if err := templating.Load(embed.FS); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	out := map[string]string{}

	var global bytes.Buffer
	if err := templating.Template.ExecuteTemplate(&global, "global.tmpl", c); err != nil {
		t.Fatalf("global template: %v", err)
	}
	out[""] = global.String()
	if err := os.WriteFile(path.Join(dir, "bird.conf"), global.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}

	for name, p := range c.Peers {
		var b bytes.Buffer
		if err := templating.Template.ExecuteTemplate(&b, "peer.tmpl", &templating.Wrapper{Name: name, Peer: *p, Config: *c}); err != nil {
			t.Fatalf("peer template: %v", err)
		}
		out[name] = bird.Reformat(b.String())
		if err := os.WriteFile(path.Join(dir, fmt.Sprintf("AS%d_%s.conf", *p.ASN, *p.ProtocolName)), []byte(out[name]), 0600); err != nil {
			t.Fatal(err)
		}
	}

	if birdBin, err := exec.LookPath("bird"); err == nil {
		cmd := exec.Command(birdBin, "-p", "-c", "bird.conf")
		cmd.Dir = dir
		if o, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("BIRD validation failed: %v\n%s", err, o)
		}
	} else {
		t.Log("bird binary not found, skipping BIRD validation")
	}

	return out
}

// protocolLevel returns the part of a rendered peer config outside of channel ({ipv4,ipv6} { ... }) blocks
func protocolLevel(conf string) string {
	var out strings.Builder
	depth := 0
	for _, line := range strings.Split(conf, "\n") {
		trimmed := strings.TrimSpace(line)
		if depth == 1 && (strings.HasPrefix(trimmed, "ipv4 {") || strings.HasPrefix(trimmed, "ipv6 {")) {
			depth = 2
			continue
		}
		if depth == 0 && strings.HasPrefix(trimmed, "protocol bgp") {
			depth = 1
			continue
		}
		if depth == 1 {
			if trimmed == "}" {
				depth = 0
				continue
			}
			out.WriteString(trimmed + "\n")
			continue
		}
		if depth >= 2 {
			depth += strings.Count(trimmed, "{") - strings.Count(trimmed, "}")
			if depth < 2 {
				depth = 1
			}
		}
	}
	return out.String()
}

func TestRenderAdvertiseHostname(t *testing.T) {
	out := renderConfig(t, `
asn: 65530
router-id: 192.0.2.1
hostname: router.example.com
rpki-enable: false
peers:
  AS112:
    asn: 112
    advertise-hostname: true
    neighbors: [192.0.2.112, 2001:db8::112]
`)
	// advertise hostname is a protocol option, it must not be rendered inside a channel block
	if !strings.Contains(protocolLevel(out["AS112"]), "advertise hostname on;") {
		t.Errorf("advertise hostname not found at protocol level:\n%s", out["AS112"])
	}
}

func TestRenderDefaultImportLimits(t *testing.T) {
	out := renderConfig(t, `
asn: 65530
router-id: 192.0.2.1
hostname: r1
rpki-enable: false
peers:
  Transit:
    asn: 6939
    neighbors: [192.0.2.1, 2001:db8::1]
`)
	// Defaults must match the documented values (import-limit4: 1000000, import-limit6: 300000)
	assert.Contains(t, out["Transit"], "define AS6939_TRANSIT_IMPORT_v4 = 1000000;")
	assert.Contains(t, out["Transit"], "define AS6939_TRANSIT_IMPORT_v6 = 300000;")
}

func TestRenderASPrefsPerAF(t *testing.T) {
	out := renderConfig(t, `
asn: 65530
router-id: 192.0.2.1
hostname: r1
rpki-enable: false
peers:
  Peer:
    asn: 64496
    mp-unicast-46: true
    as-prefs:
      174: 90
    as-prefs4:
      6939: 80
    as-prefs6:
      6939: 120
    neighbors: [192.0.2.1]
`)
	conf := out["Peer"]
	v4 := conf[strings.Index(conf, "ipv4 {"):strings.Index(conf, "ipv6 {")]
	v6 := conf[strings.Index(conf, "ipv6 {"):]
	assert.Contains(t, v4, "if (174 ~ bgp_path) then { bgp_local_pref = 90; }")
	assert.Contains(t, v6, "if (174 ~ bgp_path) then { bgp_local_pref = 90; }")
	assert.Contains(t, v4, "if (6939 ~ bgp_path) then { bgp_local_pref = 80; }")
	assert.NotContains(t, v4, "bgp_local_pref = 120")
	assert.Contains(t, v6, "if (6939 ~ bgp_path) then { bgp_local_pref = 120; }")
	assert.NotContains(t, v6, "bgp_local_pref = 80")
	// AF-specific prefs are evaluated after as-prefs so they take precedence
	assert.Less(t, strings.Index(v4, "bgp_local_pref = 90"), strings.Index(v4, "bgp_local_pref = 80"))
}

func TestRenderLocalPrefPrecedence(t *testing.T) {
	out := renderConfig(t, `
asn: 65530
router-id: 192.0.2.1
hostname: r1
rpki-enable: false
peers:
  Peer:
    asn: 64496
    local-pref: 100
    community-prefs:
      "65530:0:200": 95
    as-prefs:
      6939: 150
    neighbors: [192.0.2.1]
`)
	conf := out["Peer"]
	// Last match wins, so as-prefs must be evaluated after community-prefs to take precedence
	localPref := strings.Index(conf, "bgp_local_pref = 100;")
	communityPref := strings.Index(conf, "if ((65530,0,200) ~ bgp_large_community) then { bgp_local_pref = 95; }")
	asPref := strings.Index(conf, "if (6939 ~ bgp_path) then { bgp_local_pref = 150; }")
	assert.True(t, localPref >= 0 && communityPref >= 0 && asPref >= 0, conf)
	assert.Less(t, localPref, communityPref)
	assert.Less(t, communityPref, asPref)
}

func TestRenderPrefixPrefs(t *testing.T) {
	out := renderConfig(t, `
asn: 65530
router-id: 192.0.2.1
hostname: r1
rpki-enable: false
peers:
  Peer:
    asn: 64496
    as-prefs:
      6939: 150
    prefix-prefs:
      "198.51.100.0/24+": 200
      "2001:db8:100::/40{40,48}": 210
    neighbors: [192.0.2.1, 2001:db8::1]
`)
	conf := out["Peer"]
	v4 := conf[:strings.Index(conf, "ipv6 {")]
	v6 := conf[strings.Index(conf, "ipv6 {"):]
	assert.Contains(t, v4, "if (net ~ [ 198.51.100.0/24+ ]) then { bgp_local_pref = 200; }")
	assert.NotContains(t, v4, "2001:db8:100::/40")
	assert.Contains(t, v6, "if (net ~ [ 2001:db8:100::/40{40,48} ]) then { bgp_local_pref = 210; }")
	assert.NotContains(t, v6, "198.51.100.0/24")
	// prefix-prefs take precedence over as-prefs (last match wins)
	assert.Less(t, strings.Index(v4, "bgp_local_pref = 150"), strings.Index(v4, "bgp_local_pref = 200"))
}
