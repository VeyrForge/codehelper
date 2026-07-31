import { Injectable } from "@angular/core";

@Injectable({ providedIn: "root" })
export class HeroService {
  findAll(): string[] {
    return ["hercules"];
  }

  findOne(id: number): string {
    return id === 1 ? "hercules" : "unknown";
  }
}
