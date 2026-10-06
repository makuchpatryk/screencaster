package voices

import (
	"reflect"
	"strings"
	"testing"
)

// catalog builds a Catalog from voice names; the language is the part before
// the first "_", empty when there is none (the provider decides).
func catalog(defaults map[string]string, names ...string) Catalog {
	c := Catalog{Defaults: defaults}
	for _, n := range names {
		lang, _, found := strings.Cut(n, "_")
		if !found {
			lang = ""
		}
		c.Voices = append(c.Voices, Voice{Name: n, Lang: lang})
	}
	return c
}

var defaults = map[string]string{"en": "en_US-ryan-high", "pl": "pl_PL-darkman-medium"}

func TestResolve(t *testing.T) {
	all := catalog(defaults, "en_US-ryan-high", "pl_PL-darkman-medium", "pl_PL-gosia-medium", "de_DE-thorsten")
	tests := []struct {
		name    string
		langs   []string
		script  map[string]string
		cat     Catalog
		want    map[string]string
		wantErr string
	}{
		{
			name:  "provider defaults (FR-001 AC2)",
			langs: []string{"en", "pl"},
			cat:   all,
			want:  map[string]string{"en": "en_US-ryan-high", "pl": "pl_PL-darkman-medium"},
		},
		{
			name:   "script beats default",
			langs:  []string{"pl"},
			script: map[string]string{"pl": "pl_PL-gosia-medium"},
			cat:    all,
			want:   map[string]string{"pl": "pl_PL-gosia-medium"},
		},
		{
			name:   "language without default works when the script names a voice",
			langs:  []string{"de"},
			script: map[string]string{"de": "de_DE-thorsten"},
			cat:    all,
			want:   map[string]string{"de": "de_DE-thorsten"},
		},
		{
			name:    "language without any voice fails",
			langs:   []string{"de"},
			cat:     all,
			wantErr: "no voice for language: de",
		},
		{
			name:    "uninstalled voice fails (BR-011)",
			langs:   []string{"en"},
			cat:     catalog(defaults),
			wantErr: "voice not installed: en_US-ryan-high",
		},
		{
			name:   "unselected languages are not checked",
			langs:  []string{"en"},
			script: map[string]string{"pl": "pl_PL-missing"},
			cat:    catalog(defaults, "en_US-ryan-high"),
			want:   map[string]string{"en": "en_US-ryan-high"},
		},
		{
			name:    "reports every failing language",
			langs:   []string{"en", "pl"},
			cat:     catalog(defaults),
			wantErr: "voice not installed: en_US-ryan-high\nvoice not installed: pl_PL-darkman-medium",
		},
		{
			name:  "voice names are opaque to the rules",
			langs: []string{"en"},
			cat:   catalog(map[string]string{"en": "alloy"}, "alloy"),
			want:  map[string]string{"en": "alloy"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, errs := Resolve(tt.langs, tt.script, tt.cat)
			if tt.wantErr != "" {
				if len(errs) == 0 || errs.Error() != tt.wantErr {
					t.Fatalf("Resolve() errors = %q, want %q", errs.Error(), tt.wantErr)
				}
				return
			}
			if len(errs) != 0 {
				t.Fatalf("Resolve() errors = %v", errs)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Resolve() = %v, want %v", got, tt.want)
			}
		})
	}
}

// FR-018 acceptance criteria.
func TestOptions(t *testing.T) {
	tests := []struct {
		name string
		cat  Catalog
		want []Option
	}{
		{"stock image", catalog(defaults, "en_US-ryan-high", "pl_PL-darkman-medium"), []Option{
			{"en", true, "en_US-ryan-high", []string{"en_US-ryan-high"}},
			{"pl", false, "pl_PL-darkman-medium", []string{"pl_PL-darkman-medium"}},
		}},
		{"extra pl voice and a language without default", catalog(defaults,
			"en_US-ryan-high", "pl_PL-darkman-medium", "pl_PL-gosia-medium", "de_DE-thorsten-medium", "README"), []Option{
			{"de", false, "", []string{"de_DE-thorsten-medium"}},
			{"en", true, "en_US-ryan-high", []string{"en_US-ryan-high"}},
			{"pl", false, "pl_PL-darkman-medium", []string{"pl_PL-darkman-medium", "pl_PL-gosia-medium"}},
		}},
		{"default languages always listed, default needs an installed voice", catalog(defaults), []Option{
			{"en", true, "", []string{}},
			{"pl", false, "", []string{}},
		}},
		{"a provider without defaults lists only installed languages", catalog(nil, "de_DE-thorsten-medium"), []Option{
			{"de", false, "", []string{"de_DE-thorsten-medium"}},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Options(tt.cat); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Options() = %+v\nwant %+v", got, tt.want)
			}
		})
	}
}
