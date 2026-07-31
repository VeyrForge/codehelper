package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestDetectFrameworkPacks_SwiftUIAndIonic(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path string
		body string
		want string
	}{
		{"Views/HomeView.swift", "import SwiftUI\nstruct HomeView: View { var body: some View { Text(\"hi\") } }", "swiftui"},
		{"src/pages/HomePage.tsx", "import { IonPage, IonContent } from '@ionic/react';\nexport function HomePage(){ return <IonPage/> }", "ionic"},
		{"src/plugins/device.ts", "import { registerPlugin } from '@capacitor/core';\nconst Device = registerPlugin('Device');", "capacitor"},
	}
	for _, tc := range cases {
		got := DetectFrameworkPacks(tc.path, nil, tc.body)
		found := false
		for _, g := range got {
			if g == tc.want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("path %q expected framework %q, got %v", tc.path, tc.want, got)
		}
	}
}

func TestParseSwift_SwiftUIViewsAndNav(t *testing.T) {
	t.Parallel()
	src := []byte(`
import SwiftUI

struct HomeView: View {
    var body: some View {
        NavigationStack {
            NavigationLink("Detail") {
                DetailView()
            }
            GreetingView()
        }
    }
}

struct DetailView: View {
    var body: some View {
        Text("detail")
    }
}

struct GreetingView: View {
    var body: some View {
        Text("hi")
    }
}

struct DetailScreen: View {
    var body: some View {
        Text("screen")
    }
}
`)
	res, err := ParseSwift(context.Background(), "repo", "Views/HomeView.swift", src)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]types.Symbol{}
	for _, s := range res.Symbols {
		byName[s.Name] = s
	}
	for _, name := range []string{"HomeView", "DetailView", "GreetingView", "DetailScreen"} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("missing %q; have %#v", name, byName)
		}
	}
	if !strings.Contains(byName["HomeView"].Signature, "swiftui") || !strings.Contains(byName["HomeView"].Signature, "role=view") {
		t.Errorf("HomeView signature=%q want swiftui+view", byName["HomeView"].Signature)
	}
	if !strings.Contains(byName["DetailScreen"].Signature, "role=screen") {
		t.Errorf("DetailScreen signature=%q want screen", byName["DetailScreen"].Signature)
	}
	targets := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind != types.RefKindCalls || e.SourceID != byName["HomeView"].ID {
			continue
		}
		targets[symrefName(e.TargetID)] = true
	}
	for _, want := range []string{"DetailView", "GreetingView"} {
		if !targets[want] {
			t.Errorf("HomeView missing nav/call to %q; got %#v", want, targets)
		}
	}
}

func TestParseTypeScript_IonicPagesAndRoutes(t *testing.T) {
	t.Parallel()
	routes := []byte(`
import { IonRouterOutlet } from '@ionic/react';
import { Route } from 'react-router-dom';
import { HomePage } from '../pages/HomePage';
import { SettingsPage } from '../pages/SettingsPage';

export function AppRoutes() {
  return (
    <IonRouterOutlet>
      <Route exact path="/home" component={HomePage} />
      <Route path="/settings" component={SettingsPage} />
    </IonRouterOutlet>
  );
}
`)
	res, err := ParseTypeScript(context.Background(), "repo", "src/routes/AppRoutes.tsx", routes)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]types.Symbol{}
	for _, s := range res.Symbols {
		byName[s.Name] = s
	}
	if _, ok := byName["AppRoutes"]; !ok {
		t.Fatalf("missing AppRoutes; symbols=%#v", byName)
	}
	if !strings.Contains(byName["AppRoutes"].Signature, "ionic") || !strings.Contains(byName["AppRoutes"].Signature, "role=router") {
		t.Errorf("AppRoutes signature=%q want ionic+router", byName["AppRoutes"].Signature)
	}
	pages := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls && e.SourceID == byName["AppRoutes"].ID {
			pages[symrefName(e.TargetID)] = true
		}
	}
	for _, want := range []string{"HomePage", "SettingsPage"} {
		if !pages[want] {
			t.Errorf("AppRoutes missing Route→%s; got %#v", want, pages)
		}
	}

	page := []byte(`
import { IonPage, IonContent, IonButton } from '@ionic/react';
import { registerPlugin } from '@capacitor/core';

const DevicePlugin = registerPlugin('Device');

export function HomePage() {
  return (
    <IonPage>
      <IonContent>
        <IonButton onClick={() => openSettings()}>Go</IonButton>
      </IonContent>
    </IonPage>
  );
}

function openSettings() {}
`)
	pres, err := ParseTypeScript(context.Background(), "repo", "src/pages/HomePage.tsx", page)
	if err != nil {
		t.Fatal(err)
	}
	var home *types.Symbol
	var plugin *types.Symbol
	for i := range pres.Symbols {
		switch pres.Symbols[i].Name {
		case "HomePage":
			home = &pres.Symbols[i]
		case "DevicePlugin":
			plugin = &pres.Symbols[i]
		}
	}
	if home == nil {
		t.Fatal("missing HomePage")
	}
	if !strings.Contains(home.Signature, "ionic") || !strings.Contains(home.Signature, "role=page") {
		t.Errorf("HomePage signature=%q want ionic+page", home.Signature)
	}
	if plugin == nil {
		t.Fatal("missing DevicePlugin")
	}
	if !strings.Contains(plugin.Signature, "capacitor") || !strings.Contains(plugin.Signature, "role=plugin") {
		t.Errorf("DevicePlugin signature=%q want capacitor+plugin", plugin.Signature)
	}
}
