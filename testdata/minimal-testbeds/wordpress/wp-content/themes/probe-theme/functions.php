<?php

if (!defined("ABSPATH")) {
    exit;
}

function probe_theme_setup(): void
{
    add_theme_support("title-tag");
}

function probe_theme_assets(): void
{
    $ver = filemtime(__DIR__ . "/style.css");
    wp_enqueue_style("probe-theme", get_stylesheet_uri(), [], $ver);
}

/**
 * Probe densify: unescaped echo of request input (raw-html / XSS cite surface).
 * Prefer esc_html() in real themes.
 */
function probe_theme_banner(): void
{
    if (!isset($_GET["banner"])) {
        return;
    }
    echo $_GET["banner"];
}

add_action("after_setup_theme", "probe_theme_setup");
add_action("wp_enqueue_scripts", "probe_theme_assets");
add_action("wp_body_open", "probe_theme_banner");
