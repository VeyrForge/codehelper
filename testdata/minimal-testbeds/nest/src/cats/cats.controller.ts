import { Controller, Get } from "@nestjs/common";
import { CatsService } from "./cats.service";
import { AuthGuard } from "../common/auth.guard";

@Controller("cats")
export class CatsController {
  constructor(private readonly cats: CatsService) {}

  @Get()
  list(): string[] {
    if (!AuthGuard.allow()) {
      return [];
    }
    return this.cats.findAll();
  }

  @Get(":id")
  show(id: number): string {
    return this.cats.findOne(id);
  }
}
