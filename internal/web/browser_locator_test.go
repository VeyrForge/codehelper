package web

import "testing"

func TestResolveLocatorPrefixes(t *testing.T) {
	cases := []struct {
		name string
		a    Action
		want Locator
	}{
		{"css", Action{Selector: "#go"}, Locator{CSS: "#go"}},
		{"testid prefix", Action{Selector: "testid:submit"}, Locator{TestID: "submit"}},
		{"role name", Action{Selector: "role:button:Place order"}, Locator{Role: "button", Name: "Place order"}},
		{"text", Action{Selector: "text:Log in"}, Locator{Text: "Log in"}},
		{"name", Action{Selector: "name:Email"}, Locator{Name: "Email"}},
		{"css prefix", Action{Selector: "css:.x > y"}, Locator{CSS: ".x > y"}},
		{"ref prefix", Action{Selector: "ref:e3"}, Locator{Ref: "e3"}},
		{"ref equals", Action{Selector: "ref=e12"}, Locator{Ref: "e12"}},
		{"ref field", Action{Ref: "3"}, Locator{Ref: "e3"}},
		{"fields win for role", Action{Role: "textbox", Name: "Email", Selector: "#ignored-for-role-fields"}, Locator{Role: "textbox", Name: "Email", CSS: "#ignored-for-role-fields"}},
		{"testid field", Action{TestID: "email"}, Locator{TestID: "email"}},
	}
	for _, c := range cases {
		got := ResolveLocator(c.a)
		if got.CSS != c.want.CSS || got.Role != c.want.Role || got.Name != c.want.Name || got.TestID != c.want.TestID || got.Text != c.want.Text || got.Ref != c.want.Ref {
			t.Errorf("%s: got %+v want %+v", c.name, got, c.want)
		}
	}
}

func TestLocatorTestIDCSS(t *testing.T) {
	got := Locator{TestID: `a"b`}.TestIDCSS()
	want := `[data-testid="a\\"b"], [data-test="a\\"b"]`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestLocatorDescribeEmpty(t *testing.T) {
	if !(Locator{}).Empty() {
		t.Fatal("empty locator should be Empty")
	}
	if (Locator{CSS: "#x"}).Describe() != "#x" {
		t.Fatal((Locator{CSS: "#x"}).Describe())
	}
}
