import { getRepository } from "typeorm";
import { User } from "./entities/User";
import { Post } from "./entities/Post";

/** listUsers → getRepository(User).find — TypeORM client densify. */
export async function listUsers() {
  return getRepository(User).find({ relations: ["posts", "profile"] });
}

/** createPost → getRepository(Post).save. */
export async function createPost(authorId: number, title: string) {
  const repo = getRepository(Post);
  return repo.save({ title, author: { id: authorId } as User });
}
