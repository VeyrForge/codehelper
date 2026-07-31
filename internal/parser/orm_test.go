package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestParsePrisma_ModelsAndRelations(t *testing.T) {
	t.Parallel()
	src := []byte(`
generator client {
  provider = "prisma-client-js"
}

datasource db {
  provider = "sqlite"
  url      = env("DATABASE_URL")
}

model User {
  id    Int    @id @default(autoincrement())
  email String @unique
  posts Post[]
  role  Role
}

model Post {
  id       Int  @id @default(autoincrement())
  title    String
  author   User @relation(fields: [authorId], references: [id])
  authorId Int
}

enum Role {
  USER
  ADMIN
}
`)
	res, err := ParsePrisma(context.Background(), "repo", "prisma/schema.prisma", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	names := map[string]string{}
	for _, s := range res.Symbols {
		names[s.Name] = s.Signature
	}
	for _, want := range []string{"User", "Post", "Role"} {
		if _, ok := names[want]; !ok {
			t.Fatalf("missing symbol %q; got %#v", want, names)
		}
	}
	if !strings.Contains(names["User"], "prisma") || !strings.Contains(names["User"], "model") {
		t.Errorf("User signature=%q want prisma model", names["User"])
	}
	if !strings.Contains(names["Role"], "enum") {
		t.Errorf("Role signature=%q want enum", names["Role"])
	}
	var userID string
	for _, s := range res.Symbols {
		if s.Name == "User" {
			userID = s.ID
		}
	}
	targets := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind != types.RefKindCalls || e.SourceID != userID {
			continue
		}
		targets[symrefName(e.TargetID)] = true
	}
	for _, want := range []string{"Post", "Role"} {
		if !targets[want] {
			t.Errorf("User missing relation edge to %q; got %#v", want, targets)
		}
	}
}

func TestParseTypeScript_PrismaFindMany(t *testing.T) {
	t.Parallel()
	src := []byte(`
import { PrismaClient } from '@prisma/client';

const prisma = new PrismaClient();

export async function listUsers() {
  return prisma.user.findMany({ include: { posts: true, profile: true } });
}
`)
	res, err := ParseTypeScript(context.Background(), "repo", "src/users.ts", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var fnID string
	for _, s := range res.Symbols {
		if s.Name == "listUsers" {
			fnID = s.ID
		}
	}
	if fnID == "" {
		t.Fatal("missing listUsers")
	}
	targets := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind != types.RefKindCalls || e.SourceID != fnID {
			continue
		}
		targets[symrefName(e.TargetID)] = true
	}
	for _, want := range []string{"User", "findMany", "Post", "Profile"} {
		if !targets[want] {
			t.Errorf("listUsers missing call to %q; got %#v", want, targets)
		}
	}
}

func TestParseTypeScript_TypeORMEntityRelations(t *testing.T) {
	t.Parallel()
	src := []byte(`
import { Entity, PrimaryGeneratedColumn, Column, ManyToOne, OneToMany } from 'typeorm';
import { Post } from './Post';
import { Account } from './Account';

@Entity()
export class User {
  @PrimaryGeneratedColumn()
  id: number;

  @Column()
  email: string;

  @OneToMany(() => Post, (post) => post.author)
  posts: Post[];

  @ManyToOne(() => Account)
  account: Account;
}
`)
	res, err := ParseTypeScript(context.Background(), "repo", "src/entities/User.ts", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var userID string
	for _, s := range res.Symbols {
		if strings.HasPrefix(s.Name, "orm_call_") {
			t.Errorf("unexpected synthetic %q (entity imports must not mint orm_call sites)", s.Name)
		}
		if s.Name == "User" {
			userID = s.ID
		}
	}
	if userID == "" {
		t.Fatal("missing User")
	}
	targets := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind != types.RefKindCalls || e.SourceID != userID {
			continue
		}
		targets[symrefName(e.TargetID)] = true
	}
	for _, want := range []string{"Post", "Account"} {
		if !targets[want] {
			t.Errorf("User missing relation edge to %q; got %#v", want, targets)
		}
	}
}

func TestParseTypeScript_TypeORMGetRepository(t *testing.T) {
	t.Parallel()
	src := []byte(`
import { getRepository } from 'typeorm';
import { User } from './entities/User';

export async function listUsers() {
  return getRepository(User).find({ relations: ['posts', 'profile'] });
}
`)
	res, err := ParseTypeScript(context.Background(), "repo", "src/users.service.ts", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var fnID string
	for _, s := range res.Symbols {
		if s.Name == "listUsers" {
			fnID = s.ID
		}
	}
	if fnID == "" {
		t.Fatal("missing listUsers")
	}
	targets := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind != types.RefKindCalls || e.SourceID != fnID {
			continue
		}
		targets[symrefName(e.TargetID)] = true
	}
	for _, want := range []string{"User", "Post", "Profile"} {
		if !targets[want] {
			t.Errorf("listUsers missing call to %q; got %#v", want, targets)
		}
	}
}

func TestParseTypeScript_SequelizeFindAll(t *testing.T) {
	t.Parallel()
	src := []byte(`
import { User } from './models/User';
import { Post } from './models/Post';

export async function listUsers() {
  User.hasMany(Post);
  return User.findAll({ include: [Post] });
}
`)
	res, err := ParseTypeScript(context.Background(), "repo", "src/models/users.ts", src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var fnID string
	for _, s := range res.Symbols {
		if s.Name == "listUsers" {
			fnID = s.ID
		}
	}
	if fnID == "" {
		t.Fatal("missing listUsers")
	}
	targets := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind != types.RefKindCalls || e.SourceID != fnID {
			continue
		}
		targets[symrefName(e.TargetID)] = true
	}
	for _, want := range []string{"User", "Post", "findAll", "hasMany"} {
		if !targets[want] {
			t.Errorf("listUsers missing call to %q; got %#v", want, targets)
		}
	}
}

func TestParseTypeScript_DrizzleQueryAndSchema(t *testing.T) {
	t.Parallel()
	schema := []byte(`
import { pgTable, serial, text, integer } from "drizzle-orm/pg-core";
import { relations } from "drizzle-orm";

export const users = pgTable("users", {
  id: serial("id").primaryKey(),
  email: text("email").notNull(),
});

export const posts = pgTable("posts", {
  id: serial("id").primaryKey(),
  authorId: integer("author_id"),
});

export const usersRelations = relations(users, ({ many }) => ({
  posts: many(posts),
}));
`)
	sres, err := ParseTypeScript(context.Background(), "repo", "src/db/schema.ts", schema)
	if err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	byName := map[string]types.Symbol{}
	for _, s := range sres.Symbols {
		byName[s.Name] = s
	}
	for _, want := range []string{"users", "posts"} {
		if _, ok := byName[want]; !ok {
			t.Fatalf("missing table %q; got %#v", want, byName)
		}
		if !strings.Contains(byName[want].Signature, "drizzle") || !strings.Contains(byName[want].Signature, "role=table") {
			t.Errorf("%s signature=%q want drizzle+table", want, byName[want].Signature)
		}
	}
	userTargets := map[string]bool{}
	for _, e := range sres.Edges {
		if e.Kind != types.RefKindCalls || e.SourceID != byName["users"].ID {
			continue
		}
		userTargets[symrefName(e.TargetID)] = true
	}
	for _, want := range []string{"posts", "Post"} {
		if !userTargets[want] {
			t.Errorf("users missing relation edge to %q; got %#v", want, userTargets)
		}
	}

	svc := []byte(`
import { db } from "./client";
import { users } from "./schema";

export async function listUsers() {
  return db.query.users.findMany({ with: { posts: true, profile: true } });
}

export async function listUsersSQL() {
  return db.select().from(users);
}
`)
	res, err := ParseTypeScript(context.Background(), "repo", "src/users.ts", svc)
	if err != nil {
		t.Fatalf("parse svc: %v", err)
	}
	var fnID string
	for _, s := range res.Symbols {
		if s.Name == "listUsers" {
			fnID = s.ID
		}
	}
	if fnID == "" {
		t.Fatal("missing listUsers")
	}
	targets := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind != types.RefKindCalls || e.SourceID != fnID {
			continue
		}
		targets[symrefName(e.TargetID)] = true
	}
	for _, want := range []string{"users", "User", "findMany", "Post", "Profile"} {
		if !targets[want] {
			t.Errorf("listUsers missing call to %q; got %#v", want, targets)
		}
	}
	var sqlID string
	for _, s := range res.Symbols {
		if s.Name == "listUsersSQL" {
			sqlID = s.ID
		}
	}
	if sqlID == "" {
		t.Fatal("missing listUsersSQL")
	}
	sqlTargets := map[string]bool{}
	for _, e := range res.Edges {
		if e.Kind != types.RefKindCalls || e.SourceID != sqlID {
			continue
		}
		sqlTargets[symrefName(e.TargetID)] = true
	}
	for _, want := range []string{"users", "User"} {
		if !sqlTargets[want] {
			t.Errorf("listUsersSQL missing call to %q; got %#v", want, sqlTargets)
		}
	}
}

func TestDetectFrameworkPacks_Drizzle(t *testing.T) {
	t.Parallel()
	got := DetectFrameworkPacks("src/db/schema.ts", nil, "import { pgTable } from 'drizzle-orm/pg-core';\nexport const users = pgTable('users', {});")
	found := false
	for _, g := range got {
		if g == "drizzle" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected drizzle, got %v", got)
	}
}
