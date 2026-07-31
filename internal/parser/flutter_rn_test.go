package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestDetectFrameworkPacks_FlutterAndReactNative(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path string
		body string
		want string
	}{
		{"lib/screens/home_screen.dart", "import 'package:flutter/material.dart';\nclass HomeScreen extends StatelessWidget {}", "flutter"},
		{"pubspec.yaml", "name: probe\nflutter:\n  uses-material-design: true\n", "flutter"},
		{"src/screens/HomeScreen.tsx", "import { View } from 'react-native';\nexport function HomeScreen(){ return <View/> }", "react_native"},
		{"src/navigation/RootNavigator.tsx", "import { createNativeStackNavigator } from '@react-navigation/native-stack';\nconst Stack = createNativeStackNavigator();", "react_native"},
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

func TestParseDart_FlutterWidgetScreenRoutes(t *testing.T) {
	t.Parallel()
	src := []byte(`
import 'package:flutter/material.dart';
import 'package:go_router/go_router.dart';

class MyApp extends StatelessWidget {
  Widget build(BuildContext context) {
    return MaterialApp.router(routerConfig: appRouter);
  }
}

class HomeScreen extends StatelessWidget {
  Widget build(BuildContext context) {
    return GreetingCard(title: 'hi');
  }
}

class GreetingCard extends StatelessWidget {
  Widget build(BuildContext context) {
    return Text('x');
  }
}

final GoRouter appRouter = GoRouter(
  routes: [
    GoRoute(
      path: '/',
      name: 'home',
      builder: (context, state) => const HomeScreen(),
    ),
    GoRoute(
      path: '/detail',
      builder: (context, state) => const DetailScreen(),
    ),
  ],
);

void openDetail(BuildContext context) {
  Navigator.push(
    context,
    MaterialPageRoute(builder: (_) => const DetailScreen()),
  );
}

class DetailScreen extends StatelessWidget {
  Widget build(BuildContext context) {
    return Text('detail');
  }
}

void main() {
  runApp(const MyApp());
}
`)
	res, err := parseDartLite(context.Background(), "repo", "lib/main.dart", src)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]types.Symbol{}
	for _, s := range res.Symbols {
		byName[s.Name] = s
	}
	for _, name := range []string{"HomeScreen", "GreetingCard", "MyApp", "DetailScreen", "appRouter", "route:home", "route:/detail", "main"} {
		if _, ok := byName[name]; !ok {
			t.Errorf("missing symbol %q; have %#v", name, keysOf(byName))
		}
	}
	if !strings.Contains(byName["HomeScreen"].Signature, "flutter") || !strings.Contains(byName["HomeScreen"].Signature, "role=screen") {
		t.Errorf("HomeScreen signature=%q want flutter+screen", byName["HomeScreen"].Signature)
	}
	if !strings.Contains(byName["GreetingCard"].Signature, "role=widget") {
		t.Errorf("GreetingCard signature=%q want widget", byName["GreetingCard"].Signature)
	}
	if !strings.Contains(byName["MyApp"].Signature, "role=entrypoint") {
		t.Errorf("MyApp signature=%q want entrypoint", byName["MyApp"].Signature)
	}
	if !strings.Contains(byName["appRouter"].Signature, "role=navigator") {
		t.Errorf("appRouter signature=%q want navigator", byName["appRouter"].Signature)
	}
	if !strings.Contains(byName["route:home"].Signature, "role=route") {
		t.Errorf("route:home signature=%q want route", byName["route:home"].Signature)
	}
	if !strings.Contains(byName["main"].Signature, "role=entrypoint") {
		t.Errorf("main signature=%q want entrypoint", byName["main"].Signature)
	}

	inherits := false
	routeCallsHome := false
	for _, e := range res.Edges {
		if e.Kind == types.RefKindInherits && strings.Contains(e.TargetID, "StatelessWidget") {
			inherits = true
		}
		if e.Kind == types.RefKindCalls && e.SourceID == byName["route:home"].ID && strings.HasSuffix(e.TargetID, ":HomeScreen") {
			routeCallsHome = true
		}
	}
	if !inherits {
		t.Error("expected inherits→StatelessWidget edges")
	}
	if !routeCallsHome {
		t.Error("expected route:home→HomeScreen call edge")
	}
}

func TestParseTypeScript_ReactNativeScreensAndNavigator(t *testing.T) {
	t.Parallel()
	nav := []byte(`
import { NavigationContainer } from '@react-navigation/native';
import { createNativeStackNavigator } from '@react-navigation/native-stack';
import { HomeScreen } from '../screens/HomeScreen';
import { DetailScreen } from '../screens/DetailScreen';

const Stack = createNativeStackNavigator();

export function RootNavigator() {
  return (
    <NavigationContainer>
      <Stack.Navigator>
        <Stack.Screen name="Home" component={HomeScreen} />
        <Stack.Screen name="Detail" component={DetailScreen} />
      </Stack.Navigator>
    </NavigationContainer>
  );
}
`)
	res, err := ParseTypeScript(context.Background(), "repo", "src/navigation/RootNavigator.tsx", nav)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]types.Symbol{}
	for _, s := range res.Symbols {
		byName[s.Name] = s
	}
	if _, ok := byName["Stack"]; !ok {
		t.Fatalf("missing Stack navigator; symbols=%#v", byName)
	}
	if !strings.Contains(byName["Stack"].Signature, "react_native") || !strings.Contains(byName["Stack"].Signature, "role=navigator") {
		t.Errorf("Stack signature=%q want react_native+navigator", byName["Stack"].Signature)
	}
	if _, ok := byName["RootNavigator"]; !ok {
		t.Fatal("missing RootNavigator")
	}
	if !strings.Contains(byName["RootNavigator"].Signature, "role=navigator") {
		t.Errorf("RootNavigator signature=%q want navigator", byName["RootNavigator"].Signature)
	}
	navID := byName["RootNavigator"].ID
	screens := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls && e.SourceID == navID {
			screens[symrefName(e.TargetID)] = true
		}
	}
	for _, want := range []string{"HomeScreen", "DetailScreen"} {
		if !screens[want] {
			t.Errorf("RootNavigator missing Screen→%s edge; got %#v", want, screens)
		}
	}

	screen := []byte(`
import { View, Text, Button } from 'react-native';
import { Greeting } from '../components/Greeting';

export function HomeScreen({ navigation }: { navigation: { navigate: (s: string) => void } }) {
  return (
    <View>
      <Greeting title="hi" />
      <Button title="Go" onPress={() => navigation.navigate('Detail')} />
    </View>
  );
}
`)
	sres, err := ParseTypeScript(context.Background(), "repo", "src/screens/HomeScreen.tsx", screen)
	if err != nil {
		t.Fatal(err)
	}
	var home *types.Symbol
	for i := range sres.Symbols {
		if sres.Symbols[i].Name == "HomeScreen" {
			home = &sres.Symbols[i]
			break
		}
	}
	if home == nil {
		t.Fatal("missing HomeScreen")
	}
	if !strings.Contains(home.Signature, "react_native") || !strings.Contains(home.Signature, "role=screen") {
		t.Errorf("HomeScreen signature=%q want react_native+screen", home.Signature)
	}

	comp := []byte(`
import { Text } from 'react-native';

export function Greeting({ title }: { title: string }) {
  return <Text>{title}</Text>;
}
`)
	cres, err := ParseTypeScript(context.Background(), "repo", "src/components/Greeting.tsx", comp)
	if err != nil {
		t.Fatal(err)
	}
	var greet *types.Symbol
	for i := range cres.Symbols {
		if cres.Symbols[i].Name == "Greeting" {
			greet = &cres.Symbols[i]
			break
		}
	}
	if greet == nil {
		t.Fatal("missing Greeting")
	}
	if !strings.Contains(greet.Signature, "role=component") {
		t.Errorf("Greeting signature=%q want component", greet.Signature)
	}
}

func keysOf(m map[string]types.Symbol) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
