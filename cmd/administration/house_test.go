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
		"git+ssh://git@github.com:22/acme/shop":       "github.com/acme/shop",
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

func TestAnSSHAliasKeysTheHouseOfItsHost(t *testing.T) {
	alias := map[string]string{"work": "github.com", "gl": "gitlab.com"}
	resolve := func(h string) string {
		if r, ok := alias[h]; ok {
			return r
		}
		return h
	}
	for in, want := range map[string]string{
		"git@work:Acme/Shop.git":                "github.com/acme/shop",
		"ssh://git@gl/team/app":                 "gitlab.com/team/app",
		"git@github.com:acme/shop.git":          "github.com/acme/shop",
		"git@example.com:team/app":              "example.com/team/app",
		"https://work/acme/shop":                "work/acme/shop",            // not ssh: not ssh's alias
		"ssh://git@work:2222/acme/shop":         "github.com:2222/acme/shop", // another port, another server
		"git+ssh://git@work:22/acme/shop":       "github.com/acme/shop",
		"git+ssh://git@github.com:22/acme/shop": "github.com/acme/shop",
	} {
		if got := remoteKeyVia(in, resolve); got != want {
			t.Errorf("remoteKeyVia(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSSHIsAskedOnlyAboutAHostName(t *testing.T) {
	// The remote's text reaches ssh -G, whose config can run it (Match exec
	// with %h): anything but a host name's letters is never asked about.
	for _, bad := range []string{"", "-oProxyCommand=x", "a;b", "a$(b)", "a b", "a`b`", "a/b", "a%h"} {
		if sshAlias.MatchString(bad) {
			t.Errorf("%q would be passed to ssh -G", bad)
		}
	}
	for _, ok := range []string{"work", "github.com", "gh-work_2", "10.0.0.1"} {
		if !sshAlias.MatchString(ok) {
			t.Errorf("%q isn't asked about", ok)
		}
	}
}
