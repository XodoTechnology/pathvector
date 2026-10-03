package process

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/creasty/defaults"
	"github.com/go-playground/validator/v10"
	log "github.com/sirupsen/logrus"

	"github.com/natesales/pathvector/pkg/bird"
	"github.com/natesales/pathvector/pkg/block"
	"github.com/natesales/pathvector/pkg/config"
	"github.com/natesales/pathvector/pkg/embed"
	"github.com/natesales/pathvector/pkg/irr"
	"github.com/natesales/pathvector/pkg/peeringdb"
	"github.com/natesales/pathvector/pkg/plugin"
	"github.com/natesales/pathvector/pkg/templating"
	"github.com/natesales/pathvector/pkg/util"
)

// categorizeCommunity checks if the community is in standard or large form, or an empty string if invalid
func categorizeCommunity(input string) string {
	// Test if it fits the criteria for a standard community
	input = strings.ReplaceAll(input, ",", ":")
	split := strings.Split(input, ":")
	if len(split) == 2 {
		firstPart, err := strconv.Atoi(split[0])
		if err != nil {
			return ""
		}
		secondPart, err := strconv.Atoi(split[1])
		if err != nil {
			return ""
		}

		if firstPart < 0 || firstPart > 65535 {
			return ""
		}
		if secondPart < 0 || secondPart > 65535 {
			return ""
		}
		return "standard"
	} else if len(split) == 3 {
		firstPart, err := strconv.Atoi(split[0])
		if err != nil {
			return ""
		}
		secondPart, err := strconv.Atoi(split[1])
		if err != nil {
			return ""
		}
		thirdPart, err := strconv.Atoi(split[2])
		if err != nil {
			return ""
		}

		if firstPart < 0 || firstPart > 4294967295 {
			return ""
		}
		if secondPart < 0 || secondPart > 4294967295 {
			return ""
		}
		if thirdPart < 0 || thirdPart > 4294967295 {
			return ""
		}
		return "large"
	}

	return ""
}

// sortCommunities sorts a mixed list of standard and large communities into two existing community  lists
func sortCommunities(communities []string) (standard []string, large []string, err error) {
	for _, community := range communities {
		community = strings.ReplaceAll(community, ":", ",")
		switch categorizeCommunity(community) {
		case "standard":
			standard = append(standard, community)
		case "large":
			if large == nil {
				large = []string{}
			}
			large = append(large, community)
		default:
			return nil, nil, errors.New("Invalid import community: " + community)
		}
	}

	return standard, large, nil
}

func sortCommunitiesPtr(communities *[]string) (*[]string, *[]string, error) {
	if communities == nil {
		return &[]string{}, &[]string{}, nil
	}

	standard, large, err := sortCommunities(*communities)
	if err != nil {
		return nil, nil, err
	}

	return &standard, &large, nil
}

func templateReplacements(in string, peer *config.Peer) string {
	v := reflect.ValueOf(peer)
	for v.Kind() == reflect.Ptr { // Dereference pointer types
		v = v.Elem()
	}
	vType := v.Type()
	for i := 0; i < v.NumField(); i++ {
		key := vType.Field(i).Tag.Get("yaml")
		if key != "-" {
			field := v.Field(i)
			key = "<pathvector." + key + ">"
			if !field.IsZero() {
				val := fmt.Sprintf("%v", field.Elem().Interface())
				log.Tracef("Replacing %s with %s\n", key, val)
				in = strings.ReplaceAll(in, key, val)
			}
		}
	}
	return in
}

// Load loads a configuration file from a YAML blob. include patterns are
// resolved relative to the current working directory.
func Load(configBlob []byte) (*config.Config, error) {
	return load(configBlob, ".")
}

// LoadFile reads a configuration file from disk and resolves include globs
// relative to the config file's directory.
func LoadFile(filename string) (*config.Config, error) {
	configBlob, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %s", err)
	}
	return load(configBlob, filepath.Dir(filename))
}

// fragment is the subset of the config schema allowed in included files
type fragment struct {
	Peers         map[string]*config.Peer         `yaml:"peers"`
	Templates     map[string]*config.Peer         `yaml:"templates"`
	VRRPInstances map[string]*config.VRRPInstance `yaml:"vrrp"`
	BFDInstances  map[string]*config.BFDInstance  `yaml:"bfd"`
	MRTInstances  map[string]*config.MRTInstance  `yaml:"mrt"`
	PrefixRules   []*config.PrefixRule            `yaml:"prefix-rules"`
}

func mergeFragment[V any](what string, filename string, dst map[string]V, src map[string]V) error {
	for k, v := range src {
		if _, exists := dst[k]; exists {
			return fmt.Errorf("include %s: duplicate %s key %q", filename, what, k)
		}
		dst[k] = v
	}
	return nil
}

// mergeIncludes merges config fragments matched by the config's include globs
func mergeIncludes(c *config.Config, baseDir string) error {
	for _, pattern := range c.Include {
		if !filepath.IsAbs(pattern) {
			pattern = filepath.Join(baseDir, pattern)
		}
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return fmt.Errorf("include glob %s: %s", pattern, err)
		}
		sort.Strings(matches)
		for _, filename := range matches {
			blob, err := os.ReadFile(filename)
			if err != nil {
				return fmt.Errorf("reading include %s: %s", filename, err)
			}
			var f fragment
			if err := util.YAMLUnmarshalStrict(blob, &f); err != nil {
				return fmt.Errorf("include %s: %s", filename, err)
			}
			if err := mergeFragment("peers", filename, c.Peers, f.Peers); err != nil {
				return err
			}
			if err := mergeFragment("templates", filename, c.Templates, f.Templates); err != nil {
				return err
			}
			if err := mergeFragment("vrrp", filename, c.VRRPInstances, f.VRRPInstances); err != nil {
				return err
			}
			if err := mergeFragment("bfd", filename, c.BFDInstances, f.BFDInstances); err != nil {
				return err
			}
			if err := mergeFragment("mrt", filename, c.MRTInstances, f.MRTInstances); err != nil {
				return err
			}
			c.PrefixRules = append(c.PrefixRules, f.PrefixRules...)
		}
	}
	return nil
}

// defaultRuleTargets maps panel-style rule actions to the template names they
// apply to when the rule doesn't set targets explicitly
var defaultRuleTargets = map[string][]string{
	"no-transit": {"upstream"},
	"no-peers":   {"peer", "routeserver"},
	"no-export":  {"upstream", "peer", "routeserver"},
}

// resolvePrefixRules normalizes prefix rules and attaches them to the peers
// whose template matches the rule's target list
func resolvePrefixRules(c *config.Config) error {
	for _, rule := range c.PrefixRules {
		if _, _, err := net.ParseCIDR(rule.Prefix); err != nil {
			return fmt.Errorf("prefix-rule: invalid prefix %q", rule.Prefix)
		}
		if rule.Session != "" {
			peer, ok := c.Peers[rule.Session]
			if !ok {
				return fmt.Errorf("prefix-rule %s: source session %q not found", rule.Prefix, rule.Session)
			}
			rule.ProtoGlob = *util.Sanitize(rule.Session) + "*"
			if rule.ASN == 0 && peer.ASN != nil {
				rule.ASN = uint32(*peer.ASN)
			}
		}
		if rule.ProtoGlob == "" {
			rule.ProtoGlob = "*"
		}

		switch rule.Action {
		case "blackhole":
			rule.Kind = "blackhole"
		case "reject", "no-transit", "no-peers", "no-export":
			rule.Kind = "reject"
		case "prepend":
			rule.Kind = "prepend"
		case "prepend1", "prepend2", "prepend3":
			rule.Kind = "prepend"
			rule.Count, _ = strconv.Atoi(strings.TrimPrefix(rule.Action, "prepend"))
		default:
			return fmt.Errorf("prefix-rule %s: unknown action %q", rule.Prefix, rule.Action)
		}
		if rule.Kind == "prepend" {
			if rule.ASN == 0 {
				return fmt.Errorf("prefix-rule %s: prepend requires an asn or a source session", rule.Prefix)
			}
			if rule.Count < 1 || rule.Count > 10 {
				rule.Count = 1
			}
		}
		if len(rule.Targets) == 0 {
			rule.Targets = defaultRuleTargets[rule.Action]
		}

		for _, peerData := range c.Peers {
			// Empty target list matches every peer; otherwise the peer's
			// template name must be in the list
			if len(rule.Targets) == 0 || (peerData.Template != nil && util.Contains(rule.Targets, *peerData.Template)) {
				peerData.ResolvedPrefixRules = append(peerData.ResolvedPrefixRules, rule)
			}
		}
	}
	return nil
}

func load(configBlob []byte, baseDir string) (*config.Config, error) {
	var c config.Config
	c.Init()
	defaults.MustSet(&c)

	if err := util.YAMLUnmarshalStrict(configBlob, &c); err != nil {
		return nil, fmt.Errorf("YAML unmarshal: %s", err)
	}

	validate := validator.New()
	if err := validate.Struct(&c); err != nil {
		return nil, fmt.Errorf("validation: %s", err)
	}

	// Merge included config fragments before peers are processed
	if err := mergeIncludes(&c, baseDir); err != nil {
		return nil, err
	}

	// Check for invalid templates
	for templateName, templateData := range c.Templates {
		if templateData.Template != nil && *templateData.Template != "" {
			return nil, fmt.Errorf("templates must not have a template field set, but %s does", templateName)
		}
	}

	// Set PeeringDB URL
	peeringdb.Endpoint = c.PeeringDBURL
	log.Debugf("Setting PeeringDB endpoint to %s", peeringdb.Endpoint)

	// Set hostname if empty
	if c.Hostname == "" {
		hostname, err := os.Hostname()
		if err != nil {
			return nil, fmt.Errorf("hostname is not defined and unable to get system hostname: %s", err)
		}
		c.Hostname = hostname
	}

	if c.Stun {
		c.NoAnnounce = true
		c.NoAccept = true
	}
	if c.NoAnnounce {
		log.Warn("DANGER: no-announce is set, no routes will be announced to any peer")
	}
	if c.NoAccept {
		log.Warn("DANGER: no-accept is set, no routes will be accepted from any peer")
	}

	for peerName, peerData := range c.Peers {
		// Set sanitized peer name
		peerData.ProtocolName = util.Sanitize(peerName)

		// If any peer has NVRS filtering enabled, mark it for querying.
		if peerData.FilterNeverViaRouteServers != nil {
			c.QueryNVRS = true
		}

		if peerData.NeighborIPs == nil || len(*peerData.NeighborIPs) < 1 {
			return nil, fmt.Errorf("[%s] has no neighbors defined", peerName)
		}

		peerData.BooleanOptions = &[]string{}

		// Assign values from template
		if peerData.Template != nil && *peerData.Template != "" {
			template := c.Templates[*peerData.Template]
			if template == nil {
				return nil, fmt.Errorf("template %s not found", *peerData.Template)
			} else {
				templateValue := reflect.ValueOf(*template)
				peerValue := reflect.ValueOf(c.Peers[peerName]).Elem()

				templateValueType := templateValue.Type()
				for i := 0; i < templateValueType.NumField(); i++ {
					fieldName := templateValueType.Field(i).Name
					peerFieldValue := peerValue.FieldByName(fieldName)
					if fieldName != "Template" { // Ignore the template field
						pVal := reflect.Indirect(peerFieldValue)
						peerHasValueConfigured := pVal.IsValid()
						tValue := templateValue.Field(i)
						templateHasValueConfigured := !tValue.IsNil()
						if templateHasValueConfigured && !peerHasValueConfigured {
							// Use the template's value
							peerFieldValue.Set(templateValue.Field(i))
						}

						log.Tracef("[%s] field: %s template's value: %+v kind: %T templateHasValueConfigured: %v", peerName, fieldName, reflect.Indirect(tValue), tValue.Kind().String(), templateHasValueConfigured)
					}
				}
			}
		} // end peer template processor

		// Set default values
		peerValue := reflect.ValueOf(c.Peers[peerName]).Elem()
		templateValueType := peerValue.Type()
		for i := 0; i < templateValueType.NumField(); i++ {
			fieldName := templateValueType.Field(i).Name
			fieldValue := peerValue.FieldByName(fieldName)
			defaultString := templateValueType.Field(i).Tag.Get("default")
			if defaultString == "" {
				return nil, fmt.Errorf("code error: field %s has no default value", fieldName)
			} else if defaultString != "-" {
				log.Tracef("[%s] (before defaulting, after templating) field %s value %+v", peerName, fieldName, reflect.Indirect(fieldValue))
				if fieldValue.IsNil() {
					elemToSwitch := templateValueType.Field(i).Type.Elem().Kind()
					switch elemToSwitch {
					case reflect.String:
						log.Tracef("[%s] setting field %s to value %+v", peerName, fieldName, defaultString)
						fieldValue.Set(reflect.ValueOf(&defaultString))
					case reflect.Int:
						defaultValueInt, err := strconv.Atoi(defaultString)
						if err != nil {
							return nil, fmt.Errorf("can't convert '%s' to int", defaultString)
						}
						log.Tracef("[%s] setting field %s to value %+v", peerName, fieldName, defaultValueInt)
						fieldValue.Set(reflect.ValueOf(&defaultValueInt))
					case reflect.Bool:
						var err error // explicit declaration used to avoid scope issues of defaultValue
						defaultBool, err := strconv.ParseBool(defaultString)
						if err != nil {
							return nil, fmt.Errorf("can't parse bool %s", defaultString)
						}
						log.Tracef("[%s] setting field %s to value %+v", peerName, fieldName, defaultBool)
						fieldValue.Set(reflect.ValueOf(&defaultBool))
					case reflect.Struct, reflect.Slice:
						// Ignore structs and slices
					default:
						return nil, fmt.Errorf("unknown kind %+v for field %s", elemToSwitch, fieldName)
					}
				} else {
					// Add boolean values to the peer's config
					if templateValueType.Field(i).Type.Elem().Kind() == reflect.Bool {
						*peerData.BooleanOptions = append(*peerData.BooleanOptions, templateValueType.Field(i).Tag.Get("yaml"))
					}
				}
			} else {
				log.Tracef("[%s] skipping field %s with ignored default (-)", peerName, fieldName)
			}
		}

		if peerData.PreImportFilter != nil {
			peerData.PreImportFilter = util.Ptr(templateReplacements(*peerData.PreImportFilter, peerData))
		}
		if peerData.PostImportFilter != nil {
			peerData.PostImportFilter = util.Ptr(templateReplacements(*peerData.PostImportFilter, peerData))
		}
		if peerData.PreImportAccept != nil {
			peerData.PreImportAccept = util.Ptr(templateReplacements(*peerData.PreImportAccept, peerData))
		}
		if peerData.PreExport != nil {
			peerData.PreExport = util.Ptr(templateReplacements(*peerData.PreExport, peerData))
		}
		if peerData.PreExportFinal != nil {
			peerData.PreExportFinal = util.Ptr(templateReplacements(*peerData.PreExportFinal, peerData))
		}

		if peerData.DefaultLocalPref != nil && util.Deref(peerData.OptimizeInbound) {
			return nil, fmt.Errorf("[%s] both default-local-pref and optimize-inbound set, Pathvector cannot optimize this peer", peerName)
		}

		if peerData.OnlyAnnounce != nil && util.Deref(peerData.AnnounceAll) {
			return nil, fmt.Errorf("[%s] only-announce and announce-all cannot both be true", peerName)
		}

		// Categorize prefix-communities
		if peerData.PrefixCommunities != nil {
			// Initialize community maps
			if peerData.PrefixStandardCommunities == nil {
				peerData.PrefixStandardCommunities = &map[string][]string{}
			}
			if peerData.PrefixLargeCommunities == nil {
				peerData.PrefixLargeCommunities = &map[string][]string{}
			}

			for prefix, communities := range *peerData.PrefixCommunities {
				for _, community := range communities {
					community = strings.ReplaceAll(community, ":", ",")
					communityType := categorizeCommunity(community)
					if communityType == "standard" {
						if _, ok := (*peerData.PrefixStandardCommunities)[prefix]; !ok {
							(*peerData.PrefixStandardCommunities)[prefix] = []string{}
						}
						(*peerData.PrefixStandardCommunities)[prefix] = append((*peerData.PrefixStandardCommunities)[prefix], community)
					} else if communityType == "large" {
						if _, ok := (*peerData.PrefixLargeCommunities)[prefix]; !ok {
							(*peerData.PrefixLargeCommunities)[prefix] = []string{}
						}
						(*peerData.PrefixLargeCommunities)[prefix] = append((*peerData.PrefixLargeCommunities)[prefix], community)
					} else {
						return nil, errors.New("Invalid prefix community: " + community)
					}
				}
			}
		}

		// Categorize community-prefs
		if peerData.CommunityPrefs != nil {
			// Initialize community maps
			if peerData.StandardCommunityPrefs == nil {
				peerData.StandardCommunityPrefs = &map[string]uint32{}
			}
			if peerData.LargeCommunityPrefs == nil {
				peerData.LargeCommunityPrefs = &map[string]uint32{}
			}

			for community, pref := range *peerData.CommunityPrefs {
				community = strings.ReplaceAll(community, ":", ",")
				communityType := categorizeCommunity(community)
				if communityType == "standard" {
					(*peerData.StandardCommunityPrefs)[community] = pref
				} else if communityType == "large" {
					(*peerData.LargeCommunityPrefs)[community] = pref
				} else {
					return nil, errors.New("Invalid community pref: " + community)
				}
			}
		}

		// Validate RFC 9234 BGP role
		if peerData.Role != nil {
			peerData.Role = util.Ptr(strings.ReplaceAll(*peerData.Role, "-", "_"))
			if *peerData.Role != "provider" && *peerData.Role != "rs_server" && *peerData.Role != "rs_client" && *peerData.Role != "customer" && *peerData.Role != "peer" {
				return nil, fmt.Errorf("[%s] Invalid BGP role: %s (must be one of provider, rs-server, rs-client, customer, peer)", *peerData.Role, peerName)
			}
		}
		requireRoles := peerData.RequireRoles != nil && *peerData.RequireRoles
		if requireRoles && peerData.Role == nil {
			return nil, fmt.Errorf("[%s] require-roles set but no role specified", peerName)
		}

	} // end peer list

	// Parse origin routes by assembling OriginIPv{4,6} lists by address family
	for _, prefix := range c.Prefixes {
		pfx, _, err := net.ParseCIDR(prefix)
		if err != nil {
			return nil, errors.New("Invalid origin prefix: " + prefix)
		}

		if pfx.To4() == nil { // If IPv6
			c.Prefixes6 = append(c.Prefixes6, prefix)
		} else { // If IPv4
			c.Prefixes4 = append(c.Prefixes4, prefix)
		}
	}

	// Initialize static maps
	c.Kernel.Statics4 = map[string]string{}
	c.Kernel.Statics6 = map[string]string{}

	// Categorize communities
	var err error
	c.Kernel.SRDStandardCommunities, c.Kernel.SRDLargeCommunities, err = sortCommunities(c.Kernel.SRDCommunities)
	if err != nil {
		return nil, fmt.Errorf("invalid SRD community: %v", err)
	}
	c.OriginStandardCommunities, c.OriginLargeCommunities, err = sortCommunities(c.OriginCommunities)
	if err != nil {
		return nil, fmt.Errorf("invalid origin community: %v", err)
	}
	c.ImportStandardCommunities, c.ImportLargeCommunities, err = sortCommunities(c.ImportCommunities)
	if err != nil {
		return nil, fmt.Errorf("invalid import community: %v", err)
	}
	c.ExportStandardCommunities, c.ExportLargeCommunities, err = sortCommunities(c.ExportCommunities)
	if err != nil {
		return nil, fmt.Errorf("invalid export community: %v", err)
	}
	c.LocalStandardCommunities, c.LocalLargeCommunities, err = sortCommunities(c.LocalCommunities)
	if err != nil {
		return nil, fmt.Errorf("invalid local community: %v", err)
	}

	// Parse static routes
	for prefix, nexthop := range c.Kernel.Statics {
		// Handle interface suffix
		var rawNexthop string
		if strings.Contains(nexthop, "%") {
			rawNexthop = strings.Split(nexthop, "%")[0]
		} else {
			rawNexthop = nexthop
		}

		pfx, _, err := net.ParseCIDR(prefix)
		if err != nil {
			return nil, errors.New("Invalid static prefix: " + prefix)
		}
		if net.ParseIP(rawNexthop) == nil {
			return nil, errors.New("Invalid static nexthop: " + rawNexthop)
		}

		if pfx.To4() == nil { // If IPv6
			c.Kernel.Statics6[prefix] = nexthop
		} else { // If IPv4
			c.Kernel.Statics4[prefix] = nexthop
		}
	}

	// Parse BFD configs
	for instanceName, bfdInstance := range c.BFDInstances {
		if net.ParseIP(*bfdInstance.Neighbor) == nil {
			return nil, fmt.Errorf("invalid BFD neighbor %s", *bfdInstance.Neighbor)
		}
		bfdInstance.ProtocolName = util.Sanitize(instanceName)
	}

	// Parse VRRP configs
	for _, vrrpInstance := range c.VRRPInstances {
		// Sort VIPs by address family
		for _, vip := range vrrpInstance.VIPs {
			ip, _, err := net.ParseCIDR(vip)
			if err != nil {
				return nil, errors.New("Invalid VIP: " + vip)
			}

			if ip.To4() == nil { // If IPv6
				vrrpInstance.VIPs6 = append(vrrpInstance.VIPs6, vip)
			} else { // If IPv4
				vrrpInstance.VIPs4 = append(vrrpInstance.VIPs4, vip)
			}
		}

		// Validate vrrpInstance
		if vrrpInstance.State == "primary" {
			vrrpInstance.State = "MASTER"
		} else if vrrpInstance.State == "backup" {
			vrrpInstance.State = "BACKUP"
		} else {
			return nil, errors.New("VRRP state must be 'primary' or 'backup', unexpected " + vrrpInstance.State)
		}
	}

	// Parse RTR server
	if c.RTRServer != "" {
		rtrServerParts := strings.Split(c.RTRServer, ":")
		if len(rtrServerParts) != 2 {
			return nil, fmt.Errorf("invalid rtr-server '%s' format should be host:port", rtrServerParts)
		}
		c.RTRServerHost = rtrServerParts[0]
		rtrServerPort, err := strconv.Atoi(rtrServerParts[1])
		if err != nil {
			return nil, fmt.Errorf("invalid RTR server port %s", rtrServerParts[1])
		}
		c.RTRServerPort = rtrServerPort
	}

	for _, peerData := range c.Peers {
		// Build static prefix filters
		if peerData.Prefixes != nil {
			for _, prefix := range *peerData.Prefixes {
				pfx, _, err := net.ParseCIDR(prefix)
				if err != nil {
					return nil, errors.New("Invalid prefix: " + prefix)
				}

				if pfx.To4() == nil { // If IPv6
					if peerData.PrefixSet6 == nil {
						peerData.PrefixSet6 = &[]string{}
					}
					pfxSet6 := append(*peerData.PrefixSet6, prefix)
					peerData.PrefixSet6 = &pfxSet6
				} else { // If IPv4
					if peerData.PrefixSet4 == nil {
						peerData.PrefixSet4 = &[]string{}
					}
					pfxSet4 := append(*peerData.PrefixSet4, prefix)
					peerData.PrefixSet4 = &pfxSet4
				}
			}
		}

		// Categorize communities
		peerData.ImportStandardCommunities, peerData.ImportLargeCommunities, err = sortCommunitiesPtr(peerData.ImportCommunities)
		if err != nil {
			return nil, fmt.Errorf("invalid import community: %v", err)
		}
		peerData.ExportStandardCommunities, peerData.ExportLargeCommunities, err = sortCommunitiesPtr(peerData.ExportCommunities)
		if err != nil {
			return nil, fmt.Errorf("invalid export community: %v", err)
		}
		peerData.AnnounceStandardCommunities, peerData.AnnounceLargeCommunities, err = sortCommunitiesPtr(peerData.AnnounceCommunities)
		if err != nil {
			return nil, fmt.Errorf("invalid announce community: %v", err)
		}
		peerData.RemoveStandardCommunities, peerData.RemoveLargeCommunities, err = sortCommunitiesPtr(peerData.RemoveCommunities)
		if err != nil {
			return nil, fmt.Errorf("invalid remove community: %v", err)
		}

		if peerData.Gateway != nil {
			peerData.Gateway = util.Ptr(strings.ReplaceAll(*peerData.Gateway, "-", "_"))
			if *peerData.Gateway != "direct" && *peerData.Gateway != "recursive" {
				return nil, fmt.Errorf("invalid gateway type %s (must be direct or recursive)", *peerData.Gateway)
			}
		}

		// Check for no originated prefixes but announce-originated enabled
		if len(c.Prefixes) < 1 && *peerData.AnnounceOriginated {
			// No locally originated prefixes are defined, so there's nothing to originate
			*peerData.AnnounceOriginated = false
		}
	} // end peer loop

	// Resolve per-prefix rules onto matching peers
	if err := resolvePrefixRules(&c); err != nil {
		return nil, err
	}

	// Blocklist
	blocklist := block.Combine(c.Blocklist, c.BlocklistURLs, c.BlocklistFiles)
	bASNs, bPrefixes, err := block.Parse(blocklist)
	if err != nil {
		return nil, err
	}
	c.BlocklistASNs = bASNs
	c.BlocklistPrefixes = bPrefixes
	log.Debugf("Loaded %d ASNs and %d prefixes into global blocklist", len(c.BlocklistASNs), len(c.BlocklistPrefixes))

	// Run plugins
	if err := plugin.ModifyAll(&c); err != nil {
		return nil, err
	}

	return &c, nil // nil error
}

// peer processes a single peer
func peer(peerName string, peerData *config.Peer, c *config.Config, wg *sync.WaitGroup, errCh chan<- error) {
	defer wg.Done()

	log.Debugf("Processing AS%d %s", *peerData.ASN, peerName)

	// If a PeeringDB query is required
	if (*peerData.AutoImportLimits || *peerData.AutoASSet) && !c.SkipPeeringDB {
		log.Debugf("[%s] has auto-import-limits or auto-as-set, querying PeeringDB", peerName)

		if err := peeringdb.Update(peerData, c.PeeringDBQueryTimeout, c.PeeringDBAPIKey, true); err != nil {
			errCh <- fmt.Errorf("[%s] %s", peerName, err)
			return
		}
	} // end peeringdb query enabled

	// Build IRR prefix sets
	if *peerData.FilterIRR && !c.SkipIRR {
		if err := irr.Update(peerData, c.IRRServer, c.IRRQueryTimeout, c.BGPQArgs); err != nil {
			errCh <- fmt.Errorf("[%s] %s", peerName, err)
			return
		}
	}
	if *peerData.AutoASSetMembers && !c.SkipIRR {
		membersFromIRR, err := irr.ASMembers(*peerData.ASSet, c.IRRServer, c.IRRQueryTimeout, c.BGPQArgs)
		if err != nil {
			errCh <- fmt.Errorf("[%s] %s", peerName, err)
			return
		}
		if peerData.ASSetMembers == nil {
			peerData.ASSetMembers = &membersFromIRR
		} else {
			newASSetMembers := *peerData.ASSetMembers
			newASSetMembers = append(newASSetMembers, membersFromIRR...)
			peerData.ASSetMembers = &newASSetMembers
		}
	}
	// When IRR queries are skipped, members can only come from an explicit
	// as-set-members list - an empty result just means no as-set filter
	if *peerData.FilterASSet && !c.SkipIRR && (peerData.ASSetMembers == nil || len(*peerData.ASSetMembers) < 1) {
		errCh <- fmt.Errorf("[%s] has filter-as-set enabled but no members in its as-set", peerName)
		return
	}

	util.PrintStructInfo(peerName, peerData)

	// Create peer file
	peerFileName := path.Join(c.CacheDirectory, fmt.Sprintf("AS%d_%s.conf", *peerData.ASN, *util.Sanitize(peerName)))
	peerSpecificFile, err := os.Create(peerFileName)
	if err != nil {
		errCh <- fmt.Errorf("create peer specific output file: %v", err)
		return
	}

	// Render the template and write to buffer
	var b bytes.Buffer
	log.Debugf("[%s] Writing config", peerName)
	if err := templating.Template.ExecuteTemplate(&b, "peer.tmpl", &templating.Wrapper{
		Name:   peerName,
		Peer:   *peerData,
		Config: *c,
	}); err != nil {
		errCh <- fmt.Errorf("execute template: %v", err)
		return
	}

	// Reformat config and write template to file
	if _, err := peerSpecificFile.Write([]byte(bird.Reformat(b.String()))); err != nil {
		errCh <- fmt.Errorf("write template to file: %v", err)
		return
	}

	log.Debugf("[%s] Wrote config", peerName)
}

// RunOptions controls how Run executes the data generation procedure
type RunOptions struct {
	NoConfigure bool // render and validate but don't reconfigure BIRD
	DryRun      bool // full pipeline including bird -p, but don't apply
	Withdraw    bool // withdraw all routes by commenting out peer files
	SkipPDB     bool // skip PeeringDB queries for this run (auto-import-limits, auto-as-set, NVRS)
	SkipIRR     bool // skip bgpq4/IRR queries for this run (filter-irr, auto-as-set-members)
}

// Run runs the full data generation procedure
func Run(configFilename, lockFile, version string, opts RunOptions) error {
	// Check lockfile
	if lockFile != "" {
		if _, err := os.Stat(lockFile); err == nil {
			return errors.New("lockfile exists, exiting")
		} else if os.IsNotExist(err) {
			// If the lockfile doesn't exist, create it
			log.Debug("Lockfile doesn't exist, creating one")
			//nolint:golint,gosec
			if err := os.WriteFile(lockFile, []byte(""), 0644); err != nil {
				return fmt.Errorf("writing lockfile: %v", err)
			}
		} else {
			return fmt.Errorf("accessing lockfile: %v", err)
		}
	}

	log.Infof("Starting Pathvector %s", version)
	startTime := time.Now()

	// Load the config file from config file
	log.Debugf("Loading config from %s", configFilename)
	c, err := LoadFile(configFilename)
	if err != nil {
		return err
	}
	log.Debug("Finished loading config")

	// Per-run skip flags override the config - once set on c, peer() sees them
	if opts.SkipPDB && !c.SkipPeeringDB {
		c.SkipPeeringDB = true
	}
	if opts.SkipIRR && !c.SkipIRR {
		c.SkipIRR = true
	}
	if c.SkipPeeringDB {
		log.Warn("Skipping PeeringDB queries - auto-import-limits and auto-as-set peers will render with defaults")
	}
	if c.SkipIRR {
		log.Warn("Skipping bgpq4/IRR queries - filter-irr and auto-as-set-members will render without IRR data")
	}

	// Run NVRS query. When skipping PeeringDB, leave QueryNVRS false so the
	// template omits the (empty) ASN set entirely.
	if c.QueryNVRS && c.SkipPeeringDB {
		log.Warn("Skipping NVRS query - never-via-route-servers filter will not be rendered")
		c.QueryNVRS = false
	}
	if c.QueryNVRS {
		var err error
		c.NVRSASNs, err = peeringdb.NeverViaRouteServers(c.PeeringDBQueryTimeout, c.PeeringDBAPIKey)
		if err != nil {
			return fmt.Errorf("PeeringDB NVRS query: %s", err)
		}
	}

	// Load templates from embedded filesystem
	log.Debug("Loading templates from embedded filesystem")
	if err := templating.Load(embed.FS); err != nil {
		return err
	}
	log.Debug("Finished loading templates")

	// Create cache directory
	log.Debugf("Making cache directory %s", c.CacheDirectory)
	if err := os.MkdirAll(c.CacheDirectory, os.FileMode(0755)); err != nil {
		return err
	}

	// Create the global output file
	log.Debug("Creating global config")
	globalFile, err := os.Create(path.Join(c.CacheDirectory, "bird.conf"))
	if err != nil {
		return fmt.Errorf("create global BIRD output file: %v", err)
	}
	log.Debug("Finished creating global config file")

	// Render the global template and write to buffer
	log.Debug("Writing global config file")
	if err := templating.Template.ExecuteTemplate(globalFile, "global.tmpl", c); err != nil {
		return fmt.Errorf("execute global template: %v", err)
	}
	log.Debug("Finished writing global config file")

	// Remove old manual configs
	if err := util.RemoveFileGlob(path.Join(c.CacheDirectory, "manual*.conf")); err != nil {
		return fmt.Errorf("removing old manual config files: %v", err)
	}

	// Copying manual configs
	if err := util.CopyFileToGlob(path.Join(c.BIRDDirectory, "manual*.conf"), c.CacheDirectory); err != nil {
		return fmt.Errorf("copying manual config files: %v", err)
	}

	// Remove old peer-specific configs
	if err := util.RemoveFileGlob(path.Join(c.CacheDirectory, "AS*.conf")); err != nil {
		return fmt.Errorf("removing old peer config files: %v", err)
	}

	// Print global config
	util.PrintStructInfo("pathvector.global", c)

	if opts.Withdraw {
		log.Warn("DANGER: withdraw flag is set, withdrawing all routes")
		c.NoAnnounce = true
	}

	// Iterate over peers
	log.Debug("Processing peers")
	wg := new(sync.WaitGroup)
	errCh := make(chan error, len(c.Peers))
	for peerName, peerData := range c.Peers {
		wg.Add(1)
		go peer(peerName, peerData, c, wg, errCh)
	} // end peer loop
	wg.Wait()
	close(errCh)
	for peerErr := range errCh {
		return peerErr // return the first peer processing error
	}

	// Run BIRD config validation
	if err := bird.Validate(c.BIRDBinary, c.CacheDirectory); err != nil {
		return err
	}

	// Copy config file
	log.Debug("Copying Pathvector config file to cache directory")
	if err := util.CopyFile(configFilename, path.Join(c.CacheDirectory, "pathvector.yml")); err != nil {
		return fmt.Errorf("copying Pathvector config file to cache directory: %v", err)
	}

	if !opts.DryRun {
		// Write protocol name map
		names := templating.ProtocolNames()
		j, err := json.Marshal(names)
		if err != nil {
			return fmt.Errorf("marshalling protocol names: %v", err)
		}
		file := path.Join(c.BIRDDirectory, "protocols.json")
		log.Debugf("Writing protocol names to %s", file)
		//nolint:golint,gosec
		if err := os.WriteFile(file, j, 0644); err != nil {
			return fmt.Errorf("writing protocol names: %v", err)
		}

		// Write VRRP config
		if err := templating.WriteVRRPConfig(c); err != nil {
			return err
		}

		if c.WebUIFile != "" {
			log.Info("Writing web UI")
			if err := templating.WriteUIFile(c); err != nil {
				return err
			}
		}

		if err := bird.MoveCacheAndReconfigure(c.BIRDDirectory, c.CacheDirectory, c.BIRDSocket, opts.NoConfigure, time.Duration(c.BIRDTimeout)*time.Second); err != nil {
			return err
		}
	} // end dry run check

	// Delete lockfile
	if lockFile != "" {
		if err := os.Remove(lockFile); err != nil {
			return fmt.Errorf("removing lockfile: %v", err)
		}
	}

	log.Infof("Processed %d sessions over %d peers in %s", countSessions(c.Peers), len(c.Peers), time.Since(startTime).Round(time.Second))
	return nil
}

func countSessions(peers map[string]*config.Peer) int {
	var count int
	for _, p := range peers {
		count += len(*p.NeighborIPs)
	}
	return count
}
