package geolookup

import "testing"

func TestNominatimJapanAdminLevels(t *testing.T) {
	if err := loadAddressLevels(); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		level int
		rank  uint8
		label string
	}{
		{2, 4, "country"},
		{4, 8, "state"},
		{7, 16, "city"},
		{9, 20, "suburb"},
	}
	for _, c := range cases {
		if g := AdminAddressRank("jp", c.level); g != c.rank {
			t.Errorf("jp admin_level=%d rank=%d want %d", c.level, g, c.rank)
		}
		if g := adminLabelFor("jp", c.level, ""); g != c.label {
			t.Errorf("jp admin_level=%d label=%q want %q", c.level, g, c.label)
		}
	}
}

func TestNominatimGermanyAdminLevels(t *testing.T) {
	cases := []struct {
		level int
		rank  uint8
		label string
	}{
		{2, 4, "country"},
		{4, 8, "state"},
		{5, 0, "administrative"},
		{6, 12, "county"},
		{8, 16, "city"},
		{10, 20, "suburb"},
	}
	for _, c := range cases {
		if g := AdminAddressRank("de", c.level); g != c.rank {
			t.Errorf("de admin_level=%d rank=%d want %d", c.level, g, c.rank)
		}
		if g := adminLabelFor("de", c.level, ""); g != c.label {
			t.Errorf("de admin_level=%d label=%q want %d/%q", c.level, g, c.rank, c.label)
		}
	}
}

func TestNominatimPlaceOverride(t *testing.T) {
	if g := adminLabelFor("de", 8, "town"); g != "town" {
		t.Fatalf("place override: got %q", g)
	}
	if g := placeLabelFor("jp", "neighbourhood"); g != "neighbourhood" {
		t.Fatalf("place label: got %q", g)
	}
}

func TestRankArrayOrder(t *testing.T) {
	// Nominatim: [search, address]. suburb is [19, 20].
	if g := AddressRank("", "place", "suburb"); g != 20 {
		t.Fatalf("suburb address rank %d", g)
	}
	if g := AddressRank("", "place", "town"); g != 16 {
		t.Fatalf("town address rank %d (search 18)", g)
	}
}
