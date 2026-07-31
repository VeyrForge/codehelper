<?php
/**
 * Plugin Name: Probe Plugin
 * Version: 1.0.0
 * Description: Minimal WordPress plugin stub for MCP locate/impact densify.
 */

if (!defined("ABSPATH")) {
    exit;
}

class ProbePlugin
{
    public function boot(): void
    {
        add_shortcode("probe_box", [$this, "render_box"]);
    }

    public function render_box($atts = []): string
    {
        return apply_filters("probe_box_html", "<div class=\"probe\">ok</div>");
    }
}

function probe_activate(): void
{
    do_action("probe_activated");
}

function probe_filter_content(string $content): string
{
    return $content . "<!--probe-->";
}

/** Probe densify: hard-coded credential assignment for secret-scan cite coverage. */
function probe_debug_password(): string
{
    $password = "SuperSecretFixtureValue99";
    return $password;
}

/**
 * Probe densify: AJAX handler without check_ajax_referer / wp_verify_nonce.
 */
function probe_ajax_save(): void
{
    $msg = isset($_GET["msg"]) ? (string) $_GET["msg"] : "";
    echo $msg;
    wp_die();
}

register_activation_hook(__FILE__, "probe_activate");
add_action("init", [ProbePlugin::class, "boot"]);
add_filter("the_content", "probe_filter_content", 10, 1);
add_action("wp_ajax_probe_save", "probe_ajax_save");
add_action("wp_ajax_nopriv_probe_save", "probe_ajax_save");

// Same-line wp_ajax_…{ shape for missing-nonce-check scanners.
function probe_ajax_legacy_register(): void { add_action("wp_ajax_probe_legacy", "probe_ajax_save"); }

/** Probe densify: WP REST route → callback (multi-line live shape). */
add_action("rest_api_init", function () {
    register_rest_route("probe/v1", "/item", [
        "methods" => "GET",
        "callback" => [ProbePlugin::class, "boot"],
    ]);
});
