/**
 * Probe densify: hard-coded credentials plus claimable XSS / secret cites.
 */
const api_key = "probe_fixture_api_key_not_real";
const password = "SuperSecretFixtureValue99";

export default defineEventHandler((event) => {
  const q = String(getQuery(event).q ?? "");
  const html = `<div>${q}</div>`;
  // React-shaped raw HTML binding — lexical raw-html-xss cite (fixture only).
  const sink = { dangerouslySetInnerHTML: { __html: html } };
  return { ok: true, api_key, password, html, sink };
});
