package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func symNames(res *ParseResult) map[string]bool {
	m := map[string]bool{}
	for _, s := range res.Symbols {
		m[s.Name] = true
	}
	return m
}

func TestParseShaderLite(t *testing.T) {
	cases := []struct {
		name string
		path string
		src  string
		want []string
	}{
		{
			name: "unity_shaderlab_hlsl",
			path: "Assets/Shaders/Water.shader",
			src: `Shader "Custom/Water" {
  Properties { _MainTex ("Tex", 2D) = "white" {} }
  SubShader {
    Pass {
      HLSLPROGRAM
      #define WAVES 4
      struct Attributes { float4 pos : POSITION; };
      cbuffer PerFrame { float _Time; }
      float4 frag (Attributes i) : SV_Target {
        return float4(1,1,1,1);
      }
      ENDHLSL
    }
  }
}`,
			want: []string{"Custom/Water", "WAVES", "Attributes", "PerFrame", "frag"},
		},
		{
			name: "unreal_usf",
			path: "Shaders/Private/MyCompute.usf",
			src: `#include "/Engine/Public/Platform.ush"
RWStructuredBuffer<float> OutBuffer;
void MainCS(uint3 ThreadId) {
  OutBuffer[0] = 1.0;
}`,
			want: []string{"MainCS"},
		},
		{
			name: "godot_gdshader",
			path: "materials/water.gdshader",
			src: `shader_type spatial;
uniform sampler2D albedo_tex;
varying vec3 world_pos;
void vertex() {
  world_pos = VERTEX;
}
void fragment() {
  ALBEDO = vec3(1.0);
}`,
			want: []string{"albedo_tex", "world_pos", "vertex", "fragment"},
		},
		{
			name: "glsl",
			path: "shaders/post.frag",
			src: `#version 450
uniform float u_time;
vec3 tonemap(vec3 c) { return c; }
void main() {
  gl_FragColor = vec4(tonemap(vec3(1.0)), 1.0);
}`,
			want: []string{"u_time", "tonemap", "main"},
		},
		{
			name: "wgsl",
			path: "shaders/pipeline.wgsl",
			src: `struct VertexOut { @builtin(position) pos: vec4<f32> };
@group(0) @binding(0) var<uniform> viewProj: mat4x4<f32>;
@vertex
fn vs_main() -> @builtin(position) vec4<f32> {
  return vec4<f32>(0.0);
}`,
			want: []string{"VertexOut", "viewProj", "vs_main"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := parseShaderLite(context.Background(), "repo", tc.path, []byte(tc.src))
			if err != nil {
				t.Fatal(err)
			}
			got := symNames(res)
			for _, w := range tc.want {
				if !got[w] {
					t.Errorf("%s: expected symbol %q; got %v", tc.path, w, shaderKeys(got))
				}
			}
		})
	}
}

func TestParseShaderLite_IncludeAndCalls(t *testing.T) {
	src := `#include "common.glsl"
#include <Lighting.hlsli>

vec3 tonemap(vec3 c) {
  return c * 1.2;
}

void main() {
  vec3 c = SampleAlbedo(uv);
  gl_FragColor = vec4(tonemap(c), 1.0);
}
`
	res, err := parseShaderLite(context.Background(), "repo", "shaders/post.frag", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	imports := map[string]bool{}
	for _, imp := range res.Imports {
		imports[imp] = true
	}
	for _, want := range []string{"common.glsl", "Lighting.hlsli"} {
		if !imports[want] {
			t.Errorf("missing import %q; got %v", want, res.Imports)
		}
	}
	var sawIncludeEdge bool
	for _, e := range res.Edges {
		if e.Kind == types.RefKindImports && strings.Contains(e.TargetID, "common.glsl") {
			sawIncludeEdge = true
			break
		}
	}
	if !sawIncludeEdge {
		t.Fatalf("expected imports edge for common.glsl; edges=%d", len(res.Edges))
	}

	mainID := ""
	for _, s := range res.Symbols {
		if s.Name == "main" {
			mainID = s.ID
			break
		}
	}
	if mainID == "" {
		t.Fatal("expected main symbol")
	}
	callees := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind != types.RefKindCalls || e.SourceID != mainID {
			continue
		}
		if i := strings.LastIndex(e.TargetID, ":"); i >= 0 {
			callees[e.TargetID[i+1:]] = true
		}
	}
	for _, want := range []string{"tonemap", "SampleAlbedo"} {
		if !callees[want] {
			t.Errorf("main missing call to %q; got %v", want, shaderKeys(callees))
		}
	}
	if callees["vec4"] || callees["vec3"] {
		t.Errorf("builtin constructors should be skipped; got %v", shaderKeys(callees))
	}
}

func TestParseShaderLite_HLSLIncludeCalls(t *testing.T) {
	src := `#include "Lighting.hlsli"

float3 ApplyFog(float3 color) {
  return color;
}

float4 frag(float2 uv : TEXCOORD0) : SV_Target {
  float3 lit = SampleLight(uv);
  return float4(ApplyFog(lit), 1);
}
`
	res, err := parseShaderLite(context.Background(), "repo", "Assets/Shaders/Water.hlsl", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Imports) != 1 || res.Imports[0] != "Lighting.hlsli" {
		t.Fatalf("imports=%v", res.Imports)
	}
	fragID := ""
	for _, s := range res.Symbols {
		if s.Name == "frag" {
			fragID = s.ID
			break
		}
	}
	if fragID == "" {
		t.Fatal("expected frag")
	}
	callees := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls && e.SourceID == fragID {
			if i := strings.LastIndex(e.TargetID, ":"); i >= 0 {
				callees[e.TargetID[i+1:]] = true
			}
		}
	}
	for _, want := range []string{"ApplyFog", "SampleLight"} {
		if !callees[want] {
			t.Errorf("frag missing call to %q; got %v", want, shaderKeys(callees))
		}
	}
}

func shaderKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
