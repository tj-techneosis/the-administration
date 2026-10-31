package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEquipmentGround(t *testing.T) {
	refused := []string{"https://staging.example.com/v1", "staging.example.com:443", "//db.example.com/app",
		"192.168.1.5:8080", "192.168.1.5", "jdbc:postgresql://db.example.com:5432/app", "postgres://app:secret@db.staging.test/app",
		"host=db.example.com port=5432 user=app", `"https://staging.example.com"`, "hostaddr=10.1.2.3 port=5432",
		"Server=db.example.com;Database=app", "Data Source=db.example.com;Initial Catalog=app", "host='db.example.com'"}
	allowed := []string{"http://127.0.0.1:8080", "http://LOCALHOST:8080", "http://127.0.0.2:9", "0.0.0.0:3000",
		"http://[::1]:80", "http://[::ffff:127.0.0.1]:80", "http://10.0.2.2:8080", "localhost:5432", "emulator-5554",
		"10:30:00", "1.2.3", "postgres://app@localhost/app", "host=127.0.0.1 port=5432", "app.localhost:3000", "hello world",
		"owner@example.com", "Pass@word1", "host.docker.internal:5432", "http://host.lima.internal:8080"}
	for _, v := range refused {
		if _, err := equipmentGround("x", "API", v, nil); err == nil {
			t.Errorf("%q should be refused", v)
		}
	}
	for _, v := range allowed {
		if _, err := equipmentGround("x", "API", v, nil); err != nil {
			t.Errorf("%q should be allowed: %v", v, err)
		}
	}
}

func TestASharedServerIsAllowedWhenDeclared(t *testing.T) {
	shared := []string{"staging.example.com"}
	for v, want := range map[string]string{"https://staging.example.com/v1": "staging.example.com", "api.staging.example.com:443": "staging.example.com", "http://127.0.0.1:9": ""} {
		if hit, err := equipmentGround("x", "API", v, shared); err != nil || hit != want {
			t.Errorf("%q: got %q, %v; want %q", v, hit, err, want)
		}
	}
	if _, err := equipmentGround("x", "API", "https://other.example.com", shared); err == nil {
		t.Error("a server it didn't declare passed")
	}
	if _, err := equipmentGround("x", "DATA", "shared", shared); err != nil {
		t.Errorf("DATA=shared with a shared server: %v", err)
	}
	if _, err := equipmentGround("x", "DATA", "shared", nil); err == nil {
		t.Error("DATA=shared with no shared server passed")
	}
	for _, s := range []string{"prod.example.com", "api-prod.example.com", "production", "live.example.com", "https://app.live.example.com"} {
		if _, err := (&equipment{Shared: []string{s}}).sharedHosts(); err == nil {
			t.Errorf("%q passed as a shared server", s)
		}
	}
	// A name is a hint: once the person confirms it isn't production, it's usable.
	if hs, err := (&equipment{Shared: []string{"prod-replica.example.com"}, NotProduction: []string{"prod-replica.example.com"}}).sharedHosts(); err != nil || hs[0] != "prod-replica.example.com" {
		t.Errorf("a confirmed prod-replica was refused: %v %v", hs, err)
	}
	if _, err := (&equipment{Shared: []string{"prodreplica.example.com"}}).sharedHosts(); err != nil {
		t.Errorf("prodreplica (no separate prod part) was refused: %v", err)
	}
	if hs, err := (&equipment{Shared: []string{"https://Staging.Example.com/api", "products.example.com"}}).sharedHosts(); err != nil || hs[0] != "staging.example.com" {
		t.Errorf("got %v, %v", hs, err)
	}
}

func TestAPlanWithASharedServerMustSaySoForThePerson(t *testing.T) {
	block := "```toml\n[equipment.phone]\nabout = \"A phone against staging.\"\nserver = \"screen\"\ndriver = \"drv\"\nshared = [\"%s\"]\n```\n"
	write := func(body string) string {
		p := filepath.Join(t.TempDir(), "equipment.md")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	has := func(ps []string, s string) bool {
		for _, p := range ps {
			if strings.Contains(p, s) {
				return true
			}
		}
		return false
	}
	bare := checkEquipmentPlan(write("## phone\n"+fmt.Sprintf(block, "staging.example.com")), nil)
	if !has(bare, "no \"Shared server\" part") {
		t.Errorf("a shared server with nothing said passed: %v", bare)
	}
	said := checkEquipmentPlan(write("## phone\n"+fmt.Sprintf(block, "staging.example.com")+
		"\n**Shared server**: staging.example.com, the team's staging API. It must not be production; the session confirms that with the person before adding it.\n"), nil)
	if has(said, "Shared server") {
		t.Errorf("a plan that says so was sent back: %v", said)
	}
	// A production-looking name is the person's to settle, not a repair for the Director.
	if prod := checkEquipmentPlan(write(fmt.Sprintf(block, "api.prod.example.com")+"Shared server: not production.\n"), nil); len(prod) != 0 {
		t.Errorf("a production-looking name sent the plan back: %v", prod)
	}
}

func TestTheGroundRuleSeesEveryAddressInAValue(t *testing.T) {
	for _, v := range []string{"http://localhost:3000,https://app.example.com", "host=localhost,db.example.com port=5432",
		"http://localhost:3000 https://app.example.com"} {
		if _, err := equipmentGround("x", "ORIGINS", v, nil); err == nil {
			t.Errorf("%q: the second address passed", v)
		}
	}
	shared := []string{"example.com"}
	if _, err := equipmentGround("x", "API", "https://db.prod.example.com", shared); err == nil {
		t.Error("a production-looking host under a declared one passed")
	}
	if hit, err := equipmentGround("x", "API", "https://staging.example.com", shared); err != nil || hit != "example.com" {
		t.Errorf("a plain host under a declared one: %q %v", hit, err)
	}
	if hs, err := (&equipment{Shared: []string{"staging.example.com:443"}}).sharedHosts(); err != nil || hs[0] != "staging.example.com" {
		t.Errorf("a shared entry with a port: %v %v", hs, err)
	}
}

func TestProductionAbbreviationsNeedConfirming(t *testing.T) {
	for _, h := range []string{"prd.example.com", "api.prod1.example.com", "live2.example.com", "production-2.example.com"} {
		if _, err := (&equipment{Shared: []string{h}}).sharedHosts(); err == nil {
			t.Errorf("%q passed unconfirmed", h)
		}
	}
	for _, h := range []string{"products.example.com", "staging.example.com", "prodeu-staging.example.com"} {
		if _, err := (&equipment{Shared: []string{h}}).sharedHosts(); err != nil {
			t.Errorf("%q was refused: %v", h, err)
		}
	}
}
