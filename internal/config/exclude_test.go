package config

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestReadV2Exclusions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fields  string
		want    []string
		wantErr string
	}{
		{name: "omitted"},
		{name: "empty", fields: `,"exclude":[]`, want: []string{}},
		{name: "null array", fields: `,"exclude":null`},
		{name: "patterns", fields: `,"exclude":["benchmark-runs","scratch-*","test-?","run-[0-9]","run-[^0-9]","literal\\*"]`,
			want: []string{"benchmark-runs", "scratch-*", "test-?", "run-[0-9]", "run-[^0-9]", `literal\*`}},
		{name: "unclosed class", fields: `,"exclude":["*","bad["]`, wantErr: `target "t1": exclude[1] pattern "bad["`},
		{name: "empty class", fields: `,"exclude":["[]"]`, wantErr: `exclude[0]`},
		{name: "trailing escape", fields: `,"exclude":["bad\\"]`, wantErr: `exclude[0]`},
		{name: "empty pattern", fields: `,"exclude":[""]`, wantErr: "pattern must not be empty"},
		{name: "whitespace pattern", fields: `,"exclude":["  "]`, wantErr: "pattern must not be empty"},
		{name: "path", fields: `,"exclude":["t1/repo"]`, wantErr: "not a path"},
		{name: "exception", fields: `,"exclude":["!repo"]`, wantErr: "exception patterns are not supported"},
		{name: "string", fields: `,"exclude":"repo"`, wantErr: "cannot unmarshal"},
		{name: "object", fields: `,"exclude":{}`, wantErr: "cannot unmarshal"},
		{name: "nonstring entry", fields: `,"exclude":[42]`, wantErr: "cannot unmarshal"},
		{name: "null entry", fields: `,"exclude":[null]`, wantErr: "pattern must not be empty"},
		{name: "repo target", fields: `,"repo":"app","exclude":["scratch-*"]`, wantErr: "only supported on organization targets"},
		{name: "repo empty exclusions", fields: `,"repo":"app","exclude":[]`, want: []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte(fmt.Sprintf(`{
				"providers":{"github":{"type":"github","token":"test-token"}},
				"targets":[{"provider":"github","org":"t1","path":"/tmp/t1"%s}]
			}`, tc.fields))
			cfg, err := LoadFromBytes(data)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("LoadFromBytes() error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cfg.Targets[0].Exclude, tc.want) {
				t.Fatalf("Exclude = %#v, want %#v", cfg.Targets[0].Exclude, tc.want)
			}
			encoded, err := cfg.ToJSON()
			if err != nil {
				t.Fatal(err)
			}
			if len(tc.want) == 0 && strings.Contains(string(encoded), `"exclude"`) {
				t.Fatalf("empty exclusions should be omitted: %s", encoded)
			}
			roundTrip, err := LoadFromBytes(encoded)
			if err != nil {
				t.Fatal(err)
			}
			if len(tc.want) > 0 && !reflect.DeepEqual(roundTrip.Targets[0].Exclude, tc.want) {
				t.Fatalf("round-trip Exclude = %#v, want %#v", roundTrip.Targets[0].Exclude, tc.want)
			}
		})
	}
}
