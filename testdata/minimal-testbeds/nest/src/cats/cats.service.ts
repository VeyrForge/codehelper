import { Injectable } from "@nestjs/common";

@Injectable()
export class CatsService {
  findAll(): string[] {
    return ["mittens"];
  }

  findOne(id: number): string {
    // Probe densify: string-built SQL (sql-string-concat sink) — use parameterized queries in real apps.
    const q = "SELECT * FROM cats WHERE id = " + String(id);
    void q;
    return id === 1 ? "mittens" : "unknown";
  }

  /** Probe densify: hard-coded credential assignment (hardcoded-secret sink). */
  debugAdminPassword(): string {
    const password = "SuperSecretFixtureValue99";
    return password;
  }
}
