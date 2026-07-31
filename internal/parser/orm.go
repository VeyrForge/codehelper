package parser

import (
	"fmt"
	"regexp"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// TS/JS ORM densify: TypeORM entity relations, Prisma client delegates
// (prisma.user.findMany), Sequelize model calls, and Drizzle schema/query
// densify — so context/impact reach model leaves from services without
// grepping schema/entity files.

var (
	typeormRelationPattern = regexp.MustCompile(
		`@(ManyToOne|OneToMany|OneToOne|ManyToMany)\s*\(\s*(?:\(\)\s*=>\s*|type\s*=>\s*)([A-Z][A-Za-z0-9_]*)`)
	typeormEntityPattern  = regexp.MustCompile(`@Entity\s*\(`)
	sequelizeAssocPattern = regexp.MustCompile(
		`\b([A-Z][A-Za-z0-9_]*)\.(hasMany|belongsTo|hasOne|belongsToMany)\s*\(\s*([A-Z][A-Za-z0-9_]*)`)
	prismaClientCallPattern = regexp.MustCompile(
		`(?i)\b(?:prisma|tx|db|client)\.([a-z][A-Za-z0-9_]*)\.(findMany|findFirst|findUnique|findUniqueOrThrow|findFirstOrThrow|create|createMany|update|updateMany|upsert|delete|deleteMany|count|aggregate|groupBy)\b`)
	typeormRepoCallPattern = regexp.MustCompile(
		`(?i)\b(?:getRepository|getTreeRepository)\s*\(\s*([A-Z][A-Za-z0-9_]*)\s*\)`)
	typeormManagerFindPattern = regexp.MustCompile(
		`(?i)\.(?:find|findOne|findOneBy|findBy|findAndCount|save|remove|delete|insert|update)\s*\(\s*([A-Z][A-Za-z0-9_]*)\b`)
	sequelizeModelCallPattern = regexp.MustCompile(
		`\b([A-Z][A-Za-z0-9_]*)\.(findAll|findOne|findByPk|findAndCountAll|create|bulkCreate|update|destroy)\s*\(`)
	// include: { posts: true, profile: true } / relations: ["posts", "profile"]
	ormIncludeKeyPattern = regexp.MustCompile(
		`(?i)(?:include|select)\s*:\s*\{([^}]*)\}`)
	ormRelationsArrayPattern = regexp.MustCompile(
		`(?i)relations\s*:\s*\[([^\]]*)\]`)
	ormRelationNameToken = regexp.MustCompile(`["']?([A-Za-z_][A-Za-z0-9_]*)["']?`)
	// Drizzle: db.query.users.findMany / db.insert(users) / .from(users)
	drizzleQueryCallPattern = regexp.MustCompile(
		`(?i)\b(?:db|tx|client)\.query\.([a-z][A-Za-z0-9_]*)\.(findMany|findFirst|findUnique|create|update|delete|deleteMany|updateMany|createMany)\b`)
	drizzleMutatePattern = regexp.MustCompile(
		`(?i)\b(?:db|tx|client)\.(insert|update|delete)\s*\(\s*([a-z][A-Za-z0-9_]*)\s*\)`)
	drizzleFromPattern = regexp.MustCompile(
		`(?i)\.from\s*\(\s*([a-z][A-Za-z0-9_]*)\s*\)`)
	drizzleRelationsPattern = regexp.MustCompile(
		`(?i)\brelations\s*\(\s*([a-z][A-Za-z0-9_]*)\s*,`)
	drizzleRelTargetPattern = regexp.MustCompile(
		`(?i)\b(?:many|one)\s*\(\s*([a-z][A-Za-z0-9_]*)\s*\)`)
	drizzleWithKeyPattern = regexp.MustCompile(
		`(?i)with\s*:\s*\{([^}]*)\}`)
	drizzleTableBuilderPattern = regexp.MustCompile(
		`(?i)\b(?:pgTable|sqliteTable|mysqlTable|singlestoreTable|cockroachTable)\s*\(`)
)

var prismaDelegateMethods = map[string]bool{
	"findMany": true, "findFirst": true, "findUnique": true,
	"findUniqueOrThrow": true, "findFirstOrThrow": true,
	"create": true, "createMany": true, "update": true, "updateMany": true,
	"upsert": true, "delete": true, "deleteMany": true,
	"count": true, "aggregate": true, "groupBy": true,
}

var drizzleQueryMethods = map[string]bool{
	"findMany": true, "findFirst": true, "findUnique": true,
	"create": true, "createMany": true, "update": true, "updateMany": true,
	"delete": true, "deleteMany": true,
}

// looksLikeTypeORMFile reports TypeORM entity / repository markers.
func looksLikeTypeORMFile(relPath string, buf []byte) bool {
	body := string(buf)
	lower := strings.ToLower(body)
	p := strings.ToLower(filepathSlash(relPath))
	return strings.Contains(lower, "typeorm") ||
		strings.Contains(body, "@Entity(") || strings.Contains(body, "@Entity()") ||
		strings.Contains(body, "@ManyToOne") || strings.Contains(body, "@OneToMany") ||
		strings.Contains(body, "@OneToOne") || strings.Contains(body, "@ManyToMany") ||
		strings.Contains(body, "getRepository(") ||
		strings.Contains(p, "/entities/") || strings.Contains(p, "/entity/")
}

func looksLikePrismaClientFile(buf []byte) bool {
	body := string(buf)
	lower := strings.ToLower(body)
	return strings.Contains(lower, "@prisma/client") ||
		strings.Contains(lower, "prismaclient") ||
		prismaClientCallPattern.MatchString(body)
}

func looksLikeSequelizeFile(buf []byte) bool {
	body := string(buf)
	lower := strings.ToLower(body)
	return strings.Contains(lower, "sequelize") ||
		strings.Contains(body, ".hasMany(") || strings.Contains(body, ".belongsTo(") ||
		sequelizeModelCallPattern.MatchString(body)
}

func looksLikeDrizzleFile(relPath string, buf []byte) bool {
	body := string(buf)
	lower := strings.ToLower(body)
	p := strings.ToLower(filepathSlash(relPath))
	return strings.Contains(lower, "drizzle-orm") || strings.Contains(lower, "drizzle-kit") ||
		drizzleTableBuilderPattern.MatchString(body) ||
		drizzleQueryCallPattern.MatchString(body) ||
		(strings.Contains(lower, "relations(") && (strings.Contains(lower, "drizzle") ||
			strings.Contains(p, "/schema") || strings.Contains(p, "/db/"))) ||
		(strings.Contains(lower, "db.query.") && (strings.Contains(lower, "findmany") ||
			strings.Contains(lower, "findfirst") || strings.Contains(lower, "with:")))
}

func isDrizzleTableBuilder(val *sitter.Node, buf []byte) bool {
	if val == nil || val.Type() != "call_expression" {
		return false
	}
	return drizzleTableBuilderPattern.MatchString(val.Content(buf))
}

func filepathSlash(p string) string {
	return strings.ReplaceAll(p, "\\", "/")
}

// extractTypeORMEntity densifies @Entity classes: relation decorator → target
// model call edges (Nest-style symref).
func extractTypeORMEntity(classNode *sitter.Node, buf []byte, repoID, relPath, classSym string, out *ParseResult) {
	if classNode == nil || classSym == "" || !looksLikeTypeORMFile(relPath, buf) {
		return
	}
	text := classNode.Content(buf)
	if !typeormEntityPattern.MatchString(text) &&
		!strings.Contains(text, "@ManyToOne") && !strings.Contains(text, "@OneToMany") &&
		!strings.Contains(text, "@OneToOne") && !strings.Contains(text, "@ManyToMany") {
		return
	}
	seen := map[string]bool{}
	for _, m := range typeormRelationPattern.FindAllStringSubmatch(text, -1) {
		if len(m) < 3 {
			continue
		}
		kind, target := strings.ToLower(m[1]), m[2]
		if target == "" || seen[target] {
			continue
		}
		seen[target] = true
		emitNestCall(repoID, relPath, classSym, target, 0.86, out)
		_ = kind
	}
}

// extractORMClientUsage scans TS/JS source for Prisma/TypeORM/Sequelize/Drizzle
// client calls and wires them to enclosing function/method symbols + model leaves.
func extractORMClientUsage(repoID, relPath string, buf []byte, out *ParseResult) {
	if out == nil {
		return
	}
	src := string(buf)
	needPrisma := looksLikePrismaClientFile(buf)
	needTypeORM := looksLikeTypeORMFile(relPath, buf)
	needSequelize := looksLikeSequelizeFile(buf)
	needDrizzle := looksLikeDrizzleFile(relPath, buf)
	if !needPrisma && !needTypeORM && !needSequelize && !needDrizzle {
		return
	}
	lines := strings.Split(src, "\n")
	drizzleRelFrom := ""
	for i, line := range lines {
		lineNo := i + 1
		from := enclosingSymbolAtLine(out, lineNo)
		ensureFrom := func() string {
			if from != "" {
				return from
			}
			// File-level call sites (rare) — synthesize a thin site so edges exist.
			site := fmt.Sprintf("orm_call_%d", lineNo)
			sym := symbol(repoID, relPath, site, types.SymbolKindFunction, lineNo, lineNo, "typescript",
				"frameworks=orm;role=orm_call", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			from = sym.ID
			return from
		}
		if needPrisma {
			for _, m := range prismaClientCallPattern.FindAllStringSubmatch(line, -1) {
				if len(m) < 3 {
					continue
				}
				delegate, method := m[1], m[2]
				model := ormPascalCase(delegate)
				if model == "" || !prismaDelegateMethods[method] {
					continue
				}
				srcID := ensureFrom()
				emitNestCall(repoID, relPath, srcID, model, 0.9, out)
				emitNestCall(repoID, relPath, srcID, method, 0.75, out)
			}
			if prismaClientCallPattern.MatchString(line) ||
				ormIncludeKeyPattern.MatchString(line) || ormRelationsArrayPattern.MatchString(line) {
				emitORMIncludeRelationEdges(repoID, relPath, ensureFrom(), line, out)
			}
		}
		if needTypeORM {
			matched := false
			for _, m := range typeormRepoCallPattern.FindAllStringSubmatch(line, -1) {
				if len(m) > 1 {
					emitNestCall(repoID, relPath, ensureFrom(), m[1], 0.88, out)
					matched = true
				}
			}
			for _, m := range typeormManagerFindPattern.FindAllStringSubmatch(line, -1) {
				if len(m) > 1 {
					emitNestCall(repoID, relPath, ensureFrom(), m[1], 0.85, out)
					matched = true
				}
			}
			if matched || ormIncludeKeyPattern.MatchString(line) || ormRelationsArrayPattern.MatchString(line) {
				emitORMIncludeRelationEdges(repoID, relPath, ensureFrom(), line, out)
			}
		}
		if needSequelize {
			for _, m := range sequelizeAssocPattern.FindAllStringSubmatch(line, -1) {
				if len(m) > 3 {
					srcID := ensureFrom()
					emitNestCall(repoID, relPath, srcID, m[1], 0.8, out)
					emitNestCall(repoID, relPath, srcID, m[3], 0.88, out)
					emitNestCall(repoID, relPath, srcID, m[2], 0.7, out)
				}
			}
			for _, m := range sequelizeModelCallPattern.FindAllStringSubmatch(line, -1) {
				if len(m) > 2 {
					srcID := ensureFrom()
					emitNestCall(repoID, relPath, srcID, m[1], 0.88, out)
					emitNestCall(repoID, relPath, srcID, m[2], 0.72, out)
				}
			}
		}
		if needDrizzle {
			matched := false
			for _, m := range drizzleQueryCallPattern.FindAllStringSubmatch(line, -1) {
				if len(m) < 3 {
					continue
				}
				table, method := m[1], m[2]
				if !drizzleQueryMethods[method] {
					continue
				}
				srcID := ensureFrom()
				emitDrizzleTableLeaves(repoID, relPath, srcID, table, out)
				emitNestCall(repoID, relPath, srcID, method, 0.75, out)
				matched = true
			}
			for _, m := range drizzleMutatePattern.FindAllStringSubmatch(line, -1) {
				if len(m) > 2 {
					srcID := ensureFrom()
					emitDrizzleTableLeaves(repoID, relPath, srcID, m[2], out)
					emitNestCall(repoID, relPath, srcID, m[1], 0.72, out)
					matched = true
				}
			}
			for _, m := range drizzleFromPattern.FindAllStringSubmatch(line, -1) {
				if len(m) > 1 {
					emitDrizzleTableLeaves(repoID, relPath, ensureFrom(), m[1], out)
					matched = true
				}
			}
			for _, m := range drizzleRelationsPattern.FindAllStringSubmatch(line, -1) {
				if len(m) > 1 {
					srcID := drizzleTableSymbolID(out, m[1])
					if srcID == "" {
						srcID = ensureFrom()
					} else {
						from = srcID
					}
					drizzleRelFrom = srcID
					matched = true
				}
			}
			for _, m := range drizzleRelTargetPattern.FindAllStringSubmatch(line, -1) {
				if len(m) > 1 {
					srcID := drizzleRelFrom
					if srcID == "" {
						srcID = from
					}
					if srcID == "" {
						srcID = ensureFrom()
					}
					emitDrizzleTableLeaves(repoID, relPath, srcID, m[1], out)
					matched = true
				}
			}
			if matched || drizzleWithKeyPattern.MatchString(line) {
				srcID := from
				if srcID == "" {
					srcID = drizzleRelFrom
				}
				if srcID == "" {
					srcID = ensureFrom()
				}
				emitORMIncludeRelationEdges(repoID, relPath, srcID, line, out)
				emitDrizzleWithRelationEdges(repoID, relPath, srcID, line, out)
			}
		}
	}
}

// emitDrizzleTableLeaves wires both the table const (users) and Pascal singular (User).
func emitDrizzleTableLeaves(repoID, relPath, from, table string, out *ParseResult) {
	table = strings.TrimSpace(table)
	if table == "" || from == "" {
		return
	}
	emitNestCall(repoID, relPath, from, table, 0.88, out)
	model := ormPascalCase(table)
	if strings.HasSuffix(model, "s") && len(model) > 1 && !strings.HasSuffix(model, "ss") {
		singular := model[:len(model)-1]
		if singular != "" {
			emitNestCall(repoID, relPath, from, singular, 0.9, out)
		}
	} else if model != "" && model != table {
		emitNestCall(repoID, relPath, from, model, 0.85, out)
	}
}

func drizzleTableSymbolID(out *ParseResult, table string) string {
	if out == nil || table == "" {
		return ""
	}
	for _, s := range out.Symbols {
		if s.Name == table && strings.Contains(s.Signature, "role=table") {
			return s.ID
		}
	}
	for _, s := range out.Symbols {
		if s.Name == table {
			return s.ID
		}
	}
	return ""
}

func emitDrizzleWithRelationEdges(repoID, relPath, from, line string, out *ParseResult) {
	if from == "" || out == nil {
		return
	}
	for _, m := range drizzleWithKeyPattern.FindAllStringSubmatch(line, -1) {
		if len(m) < 2 {
			continue
		}
		for _, tok := range ormRelationNameToken.FindAllStringSubmatch(m[1], -1) {
			if len(tok) < 2 {
				continue
			}
			key := strings.TrimSpace(tok[1])
			if key == "" || key == "true" || key == "false" {
				continue
			}
			switch strings.ToLower(key) {
			case "where", "orderby", "limit", "offset", "columns", "with", "extras":
				continue
			}
			emitDrizzleTableLeaves(repoID, relPath, from, key, out)
		}
	}
}

func enclosingSymbolAtLine(out *ParseResult, line int) string {
	if out == nil {
		return ""
	}
	bestID := ""
	bestSpan := int(^uint(0) >> 1)
	for _, s := range out.Symbols {
		if s.LineStart <= line && line <= s.LineEnd {
			span := s.LineEnd - s.LineStart
			if span < bestSpan {
				bestSpan = span
				bestID = s.ID
			}
		}
	}
	return bestID
}

func ormPascalCase(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// emitORMIncludeRelationEdges turns include/select/relations keys into model leaves
// (posts → Post, profile → Profile) so listUsers reach related models without grepping.
func emitORMIncludeRelationEdges(repoID, relPath, from, line string, out *ParseResult) {
	if from == "" || out == nil {
		return
	}
	seen := map[string]bool{}
	emitKey := func(key string) {
		key = strings.TrimSpace(key)
		if key == "" || key == "true" || key == "false" || seen[key] {
			return
		}
		// Skip nested option keys that are not relation names.
		switch strings.ToLower(key) {
		case "where", "orderby", "take", "skip", "cursor", "distinct", "include", "select":
			return
		}
		seen[key] = true
		model := ormPascalCase(key)
		// Plural relation fields: posts → Post, comments → Comment
		if strings.HasSuffix(model, "s") && len(model) > 1 && !strings.HasSuffix(model, "ss") {
			singular := model[:len(model)-1]
			if singular != "" {
				emitNestCall(repoID, relPath, from, singular, 0.82, out)
			}
		}
		emitNestCall(repoID, relPath, from, model, 0.8, out)
	}
	for _, m := range ormIncludeKeyPattern.FindAllStringSubmatch(line, -1) {
		if len(m) < 2 {
			continue
		}
		for _, tok := range ormRelationNameToken.FindAllStringSubmatch(m[1], -1) {
			if len(tok) > 1 {
				emitKey(tok[1])
			}
		}
	}
	for _, m := range ormRelationsArrayPattern.FindAllStringSubmatch(line, -1) {
		if len(m) < 2 {
			continue
		}
		for _, tok := range ormRelationNameToken.FindAllStringSubmatch(m[1], -1) {
			if len(tok) > 1 {
				emitKey(tok[1])
			}
		}
	}
}
