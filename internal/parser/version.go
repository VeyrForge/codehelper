package parser

// Version is bumped when extraction rules change; indexer triggers full reindex on mismatch.
// v3: Go doc comments captured into Signature for natural-language search.
// v4: GDScript (.gd) symbols indexed via line-based lite extractor.
// v5: Shader/material languages (HLSL/GLSL/ShaderLab/GDShader/Metal/WGSL) indexed.
// v6: Kotlin name siblings + Elixir alias args + GDScript call/import edges.
// v7: Svelte SFC <script> symbols/calls + Rust type-use/implements + Axum route handlers.
// v8: CJS/Express prototype APIs indexed under dotted aliases (app.use, res.send).
// v9: CJS require imports + Express top-level app.* entrypoints; relative-import
//   - same_subtree symref; Laravel bootstrap/FormRequest; Svelte events/runes;
//     Sinatra DSL; GDScript extends/emit; Nest @Catch.
//
// v10: provenance confidence bands; JS/TS instances; Nest/Laravel bindings.
const Version = 10
