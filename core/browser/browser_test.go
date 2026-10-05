package browser

import (
	"testing"

	"screencaster/core/script"
)

// The script's storage state must reach Playwright field for field.
func TestPlaywrightState_mapsEveryField(t *testing.T) {
	got, err := playwrightState(&script.StorageState{
		Cookies: []script.Cookie{
			{Name: "s", Value: "v", Domain: "app.test", Path: "/", Expires: -1, HTTPOnly: true, Secure: true, SameSite: "Lax"},
			{Name: "u", Value: "w", URL: "http://app.test"},
		},
		Origins: []script.Origin{{Origin: "http://app.test", LocalStorage: []script.NameValue{{Name: "token", Value: "t"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Cookies) != 2 || len(got.Origins) != 1 {
		t.Fatalf("state = %+v, want 2 cookies and 1 origin", got)
	}
	c := got.Cookies[0]
	if c.Name != "s" || c.Value != "v" || *c.Domain != "app.test" || *c.Path != "/" || *c.Expires != -1 ||
		!*c.HttpOnly || !*c.Secure || string(*c.SameSite) != "Lax" {
		t.Errorf("cookie 0 = %+v", c)
	}
	if u := got.Cookies[1]; *u.URL != "http://app.test" || u.Domain != nil || u.Path != nil {
		t.Errorf("cookie 1 = %+v, want only the url set", u)
	}
	if o := got.Origins[0]; o.Origin != "http://app.test" || len(o.LocalStorage) != 1 || o.LocalStorage[0].Name != "token" {
		t.Errorf("origin = %+v", o)
	}
}
