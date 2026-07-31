import { PrismaClient } from "@prisma/client";

const prisma = new PrismaClient();

/** listUsers → prisma.user.findMany → User (+ posts/profile include). */
export async function listUsers() {
  return prisma.user.findMany({
    include: { posts: true, profile: true },
  });
}

export async function createPost(authorId: number, title: string) {
  return prisma.post.create({
    data: { title, authorId },
  });
}

/** listPostsByUser → prisma.post.findMany → Post (+ comments). */
export async function listPostsByUser(authorId: number) {
  return prisma.post.findMany({
    where: { authorId },
    include: { comments: true },
  });
}
