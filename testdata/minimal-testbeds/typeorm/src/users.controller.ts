import { createPost, listUsers } from "./users.service";

/** Controller layer: getUsers → listUsers (paired locate surface). */
export async function getUsers() {
  return listUsers();
}

export async function postCreate(authorId: number, title: string) {
  return createPost(authorId, title);
}
