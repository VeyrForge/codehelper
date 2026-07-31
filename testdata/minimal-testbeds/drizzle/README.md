# Drizzle ORM stub bed

Paired probe: `listUsers` / `users` / `User` / `Post` / `findMany` / `getUsers`.

- `src/db/schema.ts` — `pgTable` users/posts + `relations(users → posts)`
- `src/users.ts` — `db.query.users.findMany({ with })` + `db.select().from(users)`
- `src/users.controller.ts` — getUsers → listUsers
