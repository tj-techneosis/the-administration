package main

import "testing"

func TestRemoteKey(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:acme/shop.git":               "github.com/acme/shop",
		"github.com:acme/shop":                       "github.com/acme/shop",
		"https://github.com/acme/shop":               "github.com/acme/shop",
		"https://GitHub.com/acme/shop.git/":          "github.com/acme/shop",
		"https://someone:token@github.com/acme/shop": "github.com/acme/shop",
		"ssh://git@github.com:22/acme/shop.git":      "github.com/acme/shop",
		"git@gitlab.example.com:group/sub/shop.git":  "gitlab.example.com/group/sub/shop",
		"/srv/git/shop.git":                          "",
		"../shop":                                    "",
		"file:///srv/git/shop.git":                   "",
		"https://github.com/acme/../../etc":          "",
		"https://github.com/":                        "",
		"":                                           "",
	} {
		if got := remoteKey(in); got != want {
			t.Errorf("remoteKey(%q) = %q, want %q", in, got, want)
		}
	}
}
