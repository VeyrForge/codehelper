import { NextRequest } from "next/server";

/**
 * Probe densify: React dangerouslySetInnerHTML on a route handler response
 * shape so raw-html-xss scanners have a claimable file:line.
 */
export async function GET(req: NextRequest) {
  const html = req.nextUrl.searchParams.get("html") ?? "";
  return (
    <div
      dangerouslySetInnerHTML={{
        __html: html,
      }}
    />
  );
}
