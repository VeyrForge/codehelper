package parser

// Version is bumped when extraction rules change; indexer triggers full reindex on mismatch.
// v3: Go doc comments captured into Signature for natural-language search.
// v4: GDScript (.gd) symbols indexed via line-based lite extractor.
// v5: Shader/material languages (HLSL/GLSL/ShaderLab/GDShader/Metal/WGSL) indexed.
const Version = 5
