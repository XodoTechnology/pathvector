package templating

import (
	"embed"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/natesales/pathvector/pkg/config"
	"github.com/natesales/pathvector/pkg/util"
)

var (
	protocolNames       []string
	protocolNameMap     = map[string]*Protocol{} // bird name:protocol
	protocolNameMapLock = sync.Mutex{}
)

// Wrapper is passed to the peer template
type Wrapper struct {
	Name   string
	Peer   config.Peer
	Config config.Config
}

type Protocol struct {
	Name string
	Tags []string
}

// ProtocolNames gets a map of protocol names to user defined names
func ProtocolNames() map[string]*Protocol {
	return protocolNameMap
}

// Reset clears per-render protocol name state. The API server renders many
// times in one process — without this, UniqueProtocolName would see names
// from previous renders as duplicates and fail every subsequent generate.
func Reset() {
	protocolNameMapLock.Lock()
	defer protocolNameMapLock.Unlock()
	protocolNames = nil
	protocolNameMap = map[string]*Protocol{}
}

// Template functions
var funcMap = template.FuncMap{
	"Contains": strings.Contains,

	// SliceContains checks if a string slice contains a string
	"SliceContains": func(slice []string, s string) bool {
		return util.Contains(slice, s)
	},

	"Iterate": func(count *int) []int {
		// Create array with `count` entries
		var i int
		var items []int
		for i = 0; i < (*count); i++ {
			items = append(items, i)
		}
		return items
	},

	"IterateInt": func(count int) []int {
		var items []int
		for i := 0; i < count; i++ {
			items = append(items, i)
		}
		return items
	},

	"BirdSet": func(prefixes []string) string {
		// Build a formatted BIRD prefix list
		output := ""
		for i, prefix := range prefixes {
			output += "  " + prefix
			if i != len(prefixes)-1 {
				output += ",\n"
			}
		}

		return output
	},

	"BirdASSet": func(asns []uint32) string {
		// Build a formatted BIRD AS set
		output := ""
		for i, prefix := range asns {
			output += fmt.Sprintf("  %d", prefix)
			if i != len(asns)-1 {
				output += ",\n"
			}
		}

		return output
	},

	"Empty": func(arr interface{}) bool {
		// Is `arr` empty? Accepts nil or a pointer to any slice/map/string
		if arr == nil {
			return true
		}
		v := reflect.ValueOf(arr)
		for v.Kind() == reflect.Ptr {
			if v.IsNil() {
				return true
			}
			v = v.Elem()
		}
		return v.Len() == 0
	},

	"Timestamp": func(format string) string {
		// Get current timestamp
		if format == "unix" {
			return strconv.Itoa(int(time.Now().Unix()))
		}
		return time.Now().String()
	},

	"MakeSlice": func(args ...interface{}) []interface{} {
		return args
	},

	"IntCmp": func(i *int, j int) bool {
		return *i == j
	},

	"StringSliceIter": func(slice *[]string) []string {
		if slice != nil {
			return *slice
		}
		return []string{}
	},

	"Uint32SliceDeref": func(slice *[]uint32) []uint32 {
		if slice != nil {
			return *slice
		}
		return []uint32{}
	},

	"StrDeref": func(i *string) string {
		if i != nil {
			return *i
		}
		return ""
	},

	"BoolDeref": func(i *bool) bool {
		if i != nil {
			return *i
		}
		return false
	},

	"IntDeref": func(i *int) int {
		if i != nil {
			return *i
		}
		return 0
	},

	"UintDeref": func(i *uint) uint {
		if i != nil {
			return *i
		}
		return 0
	},

	"MapDeref": func(m *map[string]string) map[string]string {
		if m != nil {
			return *m
		}
		return map[string]string{}
	},

	"Uint32MapDeref": func(m *map[uint32]uint32) map[uint32]uint32 {
		if m != nil {
			return *m
		}
		return map[uint32]uint32{}
	},

	"StrSliceMapDeref": func(m *map[string][]string) map[string][]string {
		if m != nil {
			return *m
		}
		return map[string][]string{}
	},

	"Uint32SliceMapDeref": func(m *map[uint32][]uint32) map[uint32][]uint32 {
		if m != nil {
			return *m
		}
		return map[uint32][]uint32{}
	},

	"StringUint32MapDeref": func(m *map[string]uint32) map[string]uint32 {
		if m != nil {
			return *m
		}
		return map[string]uint32{}
	},

	"StrSliceDeref": func(s *[]string) []string {
		if s != nil {
			return *s
		}
		return []string{}
	},

	"StrSliceJoin": func(s *[]string) string {
		if s != nil {
			return strings.Join(*s, ", ")
		}
		return ""
	},

	// UniqueProtocolName takes a protocol-safe string and address family and returns a protocol name.
	// Duplicate name+ASN+AF combinations are a config error — fail the render rather than
	// silently renaming the protocol (a renamed protocol tears the session down on reconfigure).
	"UniqueProtocolName": func(s, userSuppliedName *string, af string, asn *int, tags *[]string) (string, error) {
		protoName := fmt.Sprintf("%s_AS%d_v%s", *s, *asn, af)
		// Peers are rendered concurrently, so protocolNames and protocolNameMap must be accessed under the lock
		protocolNameMapLock.Lock()
		defer protocolNameMapLock.Unlock()
		if existing, ok := protocolNameMap[protoName]; ok {
			// The same peer rendering the same name again (e.g. a re-render within one
			// process) is not a collision — only a different peer name claiming the
			// same protocol name is a config error.
			if existing.Name == *userSuppliedName {
				return protoName, nil
			}
			return "", fmt.Errorf("duplicate protocol name %s — peer names must be unique per ASN+address family", protoName)
		}
		protocolNames = append(protocolNames, protoName)
		var t []string
		if tags != nil {
			t = *tags
		}
		protocolNameMap[protoName] = &Protocol{
			Name: *userSuppliedName,
			Tags: t,
		}
		return protoName, nil
	},

	"SplitFirst": func(s string, delim string) string {
		return strings.Split(s, delim)[0]
	},

	"Add": func(a, b int) int {
		return a + b
	},

	"Last": func(index, len int) bool {
		return index+1 == len
	},

	"U32MapContains": func(i int, m map[uint32][]uint32) bool {
		_, ok := m[uint32(i)]
		return ok
	},

	"ASPAFilter": func(asn int, aspa map[uint32][]uint32) string {
		if providers, ok := aspa[uint32(asn)]; ok {
			var out string
			for i, provider := range providers {
				out += fmt.Sprintf("bgp_path ~ [= * %d %d * =]", provider, asn)
				if i != len(providers)-1 {
					out += " || "
				}
			}
			return fmt.Sprintf(`if !((bgp_path ~ [= %d+ =]) || (%s)) then _reject("not in authorized providers list");`, asn, out)
		}
		return "# CODE ERROR: ASN not in ASPA map. This should never happen."
	},

	"ASSet": func(asns []uint32) string {
		out := "["
		for i, asn := range asns {
			out += fmt.Sprintf("%d", asn)
			if i != len(asns)-1 {
				out += ", "
			}
		}
		return out + "]"
	},
}

// Templates

var Template *template.Template

// Load loads the templates from the embedded filesystem
func Load(fs embed.FS) error {
	var err error
	Template, err = template.New("").Funcs(funcMap).ParseFS(fs, "templates/*.tmpl")
	return err
}

// vrrpTemplateData is the template context for vrrp.tmpl
type vrrpTemplateData struct {
	Config    *config.Config
	Instances map[string]*config.VRRPInstance
}

// WriteVRRPConfig writes the VRRP config to a keepalived config file
func WriteVRRPConfig(c *config.Config) error {
	if len(c.VRRPInstances) < 1 {
		log.Debug("No VRRP instances are defined, not writing config")
		return nil
	}

	// Create the VRRP config file
	keepalivedFile, err := os.Create(c.KeepalivedConfig)
	if err != nil {
		return fmt.Errorf("create keepalived output file: %v", err)
	}

	// Render the template and write to disk
	if err := Template.ExecuteTemplate(keepalivedFile, "vrrp.tmpl", vrrpTemplateData{Config: c, Instances: c.VRRPInstances}); err != nil {
		return fmt.Errorf("execute template: %v", err)
	}
	return nil
}

// WriteUIFile renders and writes the web UI file
func WriteUIFile(config *config.Config) error {
	// Create the UI output file
	log.Debug("Creating UI output file")
	uiFileObj, err := os.Create(config.WebUIFile)
	if err != nil {
		return fmt.Errorf("create UI output file: %v", err)
	}
	log.Debug("Finished creating UI file")

	// Render the UI template and write to disk
	log.Debug("Writing UI file")
	if err := Template.ExecuteTemplate(uiFileObj, "ui.tmpl", config); err != nil {
		return fmt.Errorf("execute UI template: %v", err)
	}
	log.Debug("Finished writing UI file")
	return nil
}
