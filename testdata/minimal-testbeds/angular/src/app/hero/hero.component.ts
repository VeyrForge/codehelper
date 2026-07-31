import { Component } from "@angular/core";
import { HeroService } from "./hero.service";

@Component({
  selector: "app-hero",
  standalone: true,
  template: "<p>{{ name }}</p>",
  providers: [HeroService],
})
export class HeroComponent {
  name = "";

  constructor(private readonly heroes: HeroService) {}

  load(): string {
    this.name = this.heroes.findOne(1);
    return this.name;
  }
}
