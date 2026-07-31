import { listPostsByUser, listUsers } from "./users";

/** API/controller layer: getUsers → listUsers (paired locate surface). */
export async function getUsers() {
  return listUsers();
}

export async function getUserPosts(authorId: number) {
  return listPostsByUser(authorId);
}
