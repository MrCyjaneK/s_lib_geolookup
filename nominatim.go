package geolookup

import (
	"embed"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

//go:embed data/address-levels.json
var addressLevelsFS embed.FS

func addressLevelsJSON() []byte {
	b, err := addressLevelsFS.ReadFile("data/address-levels.json")
	if err != nil {
		panic(err)
	}
	return b
}

// Nominatim address-levels.json: a number is search=address rank;
// a two-element array is [search_rank, address_rank].
type rankPair struct{ search, address uint8 }

var (
	levelsOnce   sync.Once
	levelsErr    error
	defaultRanks map[string]rankPair            // class\x00type
	countryRanks map[string]map[string]rankPair // ISO-2 → class\x00type
)

func loadAddressLevels() error {
	levelsOnce.Do(func() {
		var entries []struct {
			Countries []string                  `json:"countries"`
			Tags      map[string]map[string]any `json:"tags"`
		}
		if err := json.Unmarshal(addressLevelsJSON(), &entries); err != nil {
			levelsErr = err
			return
		}
		defaultRanks = map[string]rankPair{}
		countryRanks = map[string]map[string]rankPair{}
		for _, e := range entries {
			parsed := map[string]rankPair{}
			for class, types := range e.Tags {
				for typ, raw := range types {
					rp, err := parseRank(raw)
					if err != nil {
						levelsErr = fmt.Errorf("%s/%s: %w", class, typ, err)
						return
					}
					parsed[class+"\x00"+typ] = rp
				}
			}
			if len(e.Countries) == 0 {
				for k, v := range parsed {
					defaultRanks[k] = v
				}
				continue
			}
			for _, cc := range e.Countries {
				cc = strings.ToLower(cc)
				m := countryRanks[cc]
				if m == nil {
					m = map[string]rankPair{}
					countryRanks[cc] = m
				}
				for k, v := range parsed {
					m[k] = v
				}
			}
		}
	})
	return levelsErr
}

func parseRank(raw any) (rankPair, error) {
	switch v := raw.(type) {
	case float64:
		r := uint8(v)
		return rankPair{r, r}, nil
	case []any:
		if len(v) == 0 {
			return rankPair{}, fmt.Errorf("empty rank array")
		}
		s, ok := v[0].(float64)
		if !ok {
			return rankPair{}, fmt.Errorf("bad rank array")
		}
		if len(v) == 1 {
			r := uint8(s)
			return rankPair{r, r}, nil
		}
		a, ok := v[1].(float64)
		if !ok {
			return rankPair{}, fmt.Errorf("bad rank array")
		}
		return rankPair{uint8(s), uint8(a)}, nil
	default:
		return rankPair{}, fmt.Errorf("unexpected rank type %T", raw)
	}
}

func lookupRanks(country, class, typ string) rankPair {
	if err := loadAddressLevels(); err != nil {
		return rankPair{30, 30}
	}
	key := class + "\x00" + typ
	empty := class + "\x00"
	cc := strings.ToLower(country)
	if cc != "" {
		if m := countryRanks[cc]; m != nil {
			if r, ok := m[key]; ok {
				return r
			}
		}
	}
	if r, ok := defaultRanks[key]; ok {
		return r
	}
	if cc != "" {
		if m := countryRanks[cc]; m != nil {
			if r, ok := m[empty]; ok {
				return r
			}
		}
	}
	if r, ok := defaultRanks[empty]; ok {
		return r
	}
	return rankPair{30, 30}
}

// AddressRank is Nominatim rank_address for an OSM class/type in a country.
func AddressRank(country, class, typ string) uint8 {
	return lookupRanks(country, class, typ).address
}

// AdminAddressRank is Nominatim rank_address for boundary=administrative at admin_level.
func AdminAddressRank(country string, adminLevel int) uint8 {
	return AddressRank(country, "boundary", "administrative"+strconv.Itoa(adminLevel))
}

// Nominatim ADMIN_LABELS from src/nominatim_api/v1/classtypes.py.
// Key is (country, rank_address//2); empty country is the default.
var adminLabels = map[[2]string]string{
	{"", "1"}:   "continent",
	{"", "2"}:   "country",
	{"", "3"}:   "region",
	{"", "4"}:   "state",
	{"", "5"}:   "state_district",
	{"", "6"}:   "county",
	{"", "7"}:   "municipality",
	{"", "8"}:   "city",
	{"", "9"}:   "city_district",
	{"", "10"}:  "suburb",
	{"", "11"}:  "neighbourhood",
	{"", "12"}:  "city_block",
	{"no", "3"}: "state",
	{"no", "4"}: "county",
	{"se", "3"}: "state",
	{"se", "4"}: "county",
}

func adminLabel(country string, rank uint8) string {
	k := strconv.Itoa(int(rank / 2))
	cc := strings.ToLower(country)
	if s, ok := adminLabels[[2]string{cc, k}]; ok {
		return s
	}
	if s, ok := adminLabels[[2]string{"", k}]; ok {
		return s
	}
	return "administrative"
}

// LabelTag is Nominatim get_label_tag for a place (addresstype / address key).
func LabelTag(country, class, typ, place string, rank uint8) string {
	if (class == "place" && typ == "postcode") || (class == "boundary" && typ == "postal_code") {
		return "postcode"
	}
	if rank < 26 && place != "" {
		return strings.ToLower(strings.ReplaceAll(place, " ", "_"))
	}
	if class == "boundary" && typ == "administrative" {
		return adminLabel(country, rank)
	}
	if rank < 26 {
		if typ != "" && typ != "yes" {
			return strings.ToLower(strings.ReplaceAll(typ, " ", "_"))
		}
		return strings.ToLower(strings.ReplaceAll(class, " ", "_"))
	}
	if rank < 28 {
		return "road"
	}
	if class == "place" && (typ == "house_number" || typ == "house_name" || typ == "country_code") {
		return typ
	}
	return strings.ToLower(strings.ReplaceAll(class, " ", "_"))
}

func adminLabelFor(country string, adminLevel int, place string) string {
	rank := AdminAddressRank(country, adminLevel)
	return LabelTag(country, "boundary", "administrative", place, rank)
}

func placeLabelFor(country, place string) string {
	rank := AddressRank(country, "place", place)
	return LabelTag(country, "place", place, "", rank)
}
