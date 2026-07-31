function greet(name: string): string {
  return "hello " + name;
}

export default {
  async fetch(_req: Request): Promise<Response> {
    return new Response(greet("workers"));
  },
};
