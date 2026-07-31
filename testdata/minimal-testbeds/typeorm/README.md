# TypeORM stub bed

Paired probe: `listUsers` / `User` / `Post` / `Profile` / `getRepository` / `getUsers`.

- `src/entities/` — User↔Post↔Comment + Profile `@Entity` relations
- `src/users.service.ts` — `getRepository(User).find({ relations })`
- `src/users.controller.ts` — getUsers → listUsers
