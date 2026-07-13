package parser

import (
	"context"
	"testing"
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
					t.Errorf("%s: expected symbol %q; got %v", tc.path, w, keysOf(got))
				}
			}
		})
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
