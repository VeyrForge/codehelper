package parser

import (
	"context"
	"strings"
	"testing"
)

func TestParseTypeScript_AngularSkipsThisAssign(t *testing.T) {
	t.Parallel()
	src := []byte(`
import { Component } from '@angular/core';
@Component({ selector: 'app-heroes', templateUrl: './x.html' })
export class HeroesComponent {
  heroes: any;
  error: any;
  selectedHero: any;
  getHeroes(): void {
    this.heroService.getHeroes().subscribe(
      heroes => (this.heroes = heroes),
      error => (this.error = error)
    );
  }
  onSelect(hero: any): void {
    this.selectedHero = hero;
  }
}
`)
	res, err := ParseTypeScript(context.Background(), "repo", "src/app/heroes.component.ts", src)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range res.Symbols {
		if strings.Contains(s.Signature, "cjs_export") {
			t.Fatalf("unexpected cjs_export symbol %#v", s)
		}
	}
}
