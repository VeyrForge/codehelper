#include "common.glsl"

vec3 tonemap(vec3 c) {
  return c * 1.2;
}

void main() {
  vec3 c = SampleAlbedo(vec2(0.5));
  gl_FragColor = vec4(tonemap(c), 1.0);
}
