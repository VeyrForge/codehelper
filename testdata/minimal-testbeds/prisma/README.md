# Prisma stub bed

Paired probe: `listUsers` / `User` / `Post` / `Profile` / `findMany` / `getUsers`.

- `prisma/schema.prisma` — User↔Post↔Comment + Profile + Role enum
- `src/users.ts` — `prisma.user.findMany({ include })` client densify
- `src/users.controller.ts` — getUsers → listUsers
