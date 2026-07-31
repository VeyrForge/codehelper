import { NextRequest, NextResponse } from "next/server";

/** Probe densify: hard-coded credential for secret-scan cite coverage. */
const api_key = "probe_fixture_api_key_not_real";
const password = "SuperSecretFixtureValue99";

/**
 * Probe densify: route handler with secret + unsanitized HTML echo shapes
 * the live security pack already looks for (hardcoded-secret / raw-html-xss).
 */
export async function GET(req: NextRequest) {
  const q = req.nextUrl.searchParams.get("q") ?? "";
  // Intentionally unsanitized — fixture sink for dangerouslySetInnerHTML scanners.
  const html = `<div>${q}</div>`;
  return NextResponse.json({
    ok: true,
    api_key,
    password,
    html,
  });
}

export async function POST(req: NextRequest) {
  const body = (await req.json().catch(() => ({}))) as { name?: string };
  const name = body.name ?? "";
  // sql-string-concat-shaped footgun for lexical scanners (fixture only).
  const sql = "SELECT * FROM users WHERE name = '" + name + "'";
  return NextResponse.json({ sql, requireAuth: false });
}
