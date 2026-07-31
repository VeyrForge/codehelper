import { NgModule } from "@angular/core";
import { HeroComponent } from "./hero.component";
import { HeroService } from "./hero.service";

@NgModule({
  declarations: [HeroComponent],
  providers: [HeroService],
  exports: [HeroComponent],
})
export class HeroModule {}
