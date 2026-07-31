import { db } from "./db/client";
import { users, posts } from "./db/schema";

/** listUsers → db.query.users.findMany → User (+ posts/profile with). */
export async function listUsers() {
  return db.query.users.findMany({
    with: { posts: true, profile: true },
  });
}

export async function createPost(authorId: number, title: string) {
  return db.insert(posts).values({ title, authorId });
}

/** listUsersSQL → db.select().from(users) → users/User. */
export async function listUsersSQL() {
  return db.select().from(users);
}
