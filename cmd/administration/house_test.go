package main

import "testing"

func TestRemoteKey(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:acme/shop.git":                "github.com/acme/shop",
		"github.com:acme/shop":                        "github.com/acme/shop",
		"https://github.com/acme/shop":                "github.com/acme/shop",
		"https://GitHub.com/acme/shop.git/":           "github.com/acme/shop",
		"https://someone:token@example.com/acme/shop": "example.com/acme/shop", // credentials dropped
		"ssh://git@github.com:22/acme/shop.git":       "github.com/acme/shop",
		"git@example.com:group/sub/shop.git":          "example.com/group/sub/shop",
		"https://github.com/Acme/Shop":                "github.com/acme/shop",
		"https://git.example.com/Team/App":            "git.example.com/Team/App",
		"https://git.example.com:8443/team/app":       "git.example.com:8443/team/app",
		"https://git.example.com:443/team/app":        "git.example.com/team/app",
		"/srv/git/shop.git":                           "",
		"../shop":                                     "",
		"file:///srv/git/shop.git":                    "",
		"https://github.com/acme/../../etc":           "",
		"https://github.com/":                         "",
		"":                                            "",
	} {
		if got := remoteKey(in); got != want {
			t.Errorf("remoteKey(%q) = %q, want %q", in, got, want)
		}
	}
}
