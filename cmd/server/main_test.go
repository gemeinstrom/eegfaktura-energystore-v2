package main

import "testing"

func TestTenantFromTopic(t *testing.T) {
	cases := map[string]string{
		"eegfaktura/vfeeg/energy/TE100200":        "vfeeg",
		"eegfaktura/vfeeg/energy/TE100200/extras": "vfeeg",
		"eegfaktura/":                             "",
		"":                                        "",
		"single":                                  "",
		"a/b":                                     "b",
	}
	for in, want := range cases {
		if got := tenantFromTopic(in); got != want {
			t.Errorf("tenantFromTopic(%q) = %q, want %q", in, got, want)
		}
	}
}
