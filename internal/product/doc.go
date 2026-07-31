// Package product scaffolds installable Codehelper product modules (4.0 direction).
//
// Default builds (no ch_modules tag) ship the full bundle: core + edit + check +
// browser + ops. Team stays opt-in. Selective builds set -tags ch_modules plus
// one or more of ch_edit, ch_check, ch_browser, ch_ops, ch_team.
//
// Browser automation still needs the existing rod tag for the headless tier
// (see internal/web). The build scripts add rod whenever the browser module is
// included. See docs/MODULES.md.
package product
