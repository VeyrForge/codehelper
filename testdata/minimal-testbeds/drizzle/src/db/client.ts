/** Stub drizzle client — densify probes only. */
export const db = {
  query: {
    users: {
      findMany: async (_opts?: unknown) => [],
    },
  },
  select: () => ({ from: async (_t: unknown) => [] }),
  insert: (_t: unknown) => ({ values: async (_v: unknown) => ({}) }),
};
