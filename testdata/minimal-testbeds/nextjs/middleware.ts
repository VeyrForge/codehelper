import type { NextRequest } from "next/server";
import { NextResponse } from "next/server";

/**
 * Probe densify: auth-gated middleware so security packs can claim
 * app-auth-middleware / NextResponse.redirect with file:line (not a CVE).
 */
export function middleware(req: NextRequest) {
  const token = req.cookies.get("session")?.value;
  if (!token) {
    return NextResponse.redirect(new URL("/login", req.url));
  }
  return NextResponse.next();
}

export const config = {
  matcher: ["/dashboard/:path*", "/api/probe/:path*"],
};
