#include "Lighting.hlsli"

float3 ApplyFog(float3 color) {
  return color * 0.9;
}

float4 frag(float2 uv : TEXCOORD0) : SV_Target {
  float3 lit = SampleLight(uv);
  return float4(ApplyFog(lit), 1);
}
